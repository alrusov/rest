package rest

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

//----------------------------------------------------------------------------------------------------------------------------//

type testLevel int8

func convertTo[T any](s string) (any, error) {
	v := reflect.New(reflect.TypeFor[T]()).Elem()
	err := convert(s, v)
	return v.Interface(), err
}

func TestConvert(t *testing.T) {
	tests := []struct {
		name     string
		conv     func(s string) (any, error)
		src      string
		expected any // nil -- ожидается ошибка
	}{
		{"int", convertTo[int], " 123 ", 123},
		{"int bad", convertTo[int], "x", nil},
		{"int8 max", convertTo[int8], "127", int8(127)},
		{"int8 min", convertTo[int8], "-128", int8(-128)},
		{"int8 overflow", convertTo[int8], "128", nil},
		{"int8 underflow", convertTo[int8], "-129", nil},
		{"int16 overflow", convertTo[int16], "40000", nil},
		{"int32 overflow", convertTo[int32], "2147483648", nil},
		{"custom int8 overflow", convertTo[testLevel], "200", nil},
		{"custom int8", convertTo[testLevel], "-5", testLevel(-5)},

		{"uint8 max", convertTo[uint8], "255", uint8(255)},
		{"uint8 overflow", convertTo[uint8], "256", nil},
		{"uint negative", convertTo[uint], "-1", nil},
		{"uint32 overflow", convertTo[uint32], "4294967296", nil},
		{"uint64", convertTo[uint64], "18446744073709551615", uint64(18446744073709551615)},

		{"float32", convertTo[float32], "1.5", float32(1.5)},
		{"float32 overflow", convertTo[float32], "1e39", nil},
		{"float64", convertTo[float64], "1e39", 1e39},
		{"float bad", convertTo[float64], "x", nil},

		{"bool empty", convertTo[bool], "", true},
		{"bool 1", convertTo[bool], "1", true},
		{"bool yes", convertTo[bool], "yes", true},
		{"bool ON", convertTo[bool], "ON", true},
		{"bool true", convertTo[bool], "True", true},
		{"bool 0", convertTo[bool], "0", false},
		{"bool no", convertTo[bool], "no", false},
		{"bool off", convertTo[bool], "off", false},
		{"bool false", convertTo[bool], "FALSE", false},
		{"bool unknown", convertTo[bool], "maybe", nil},

		{"duration", convertTo[time.Duration], "10s", 10 * time.Second},
		{"duration bad", convertTo[time.Duration], "x", nil},

		{"string", convertTo[string], " abc ", "abc"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.conv(tc.src)

			if tc.expected == nil {
				if err == nil {
					t.Fatalf("got %v (%T), expected error", got, got)
				}
				if !strings.Contains(err.Error(), "conversion error") || !strings.Contains(err.Error(), tc.src) {
					t.Errorf(`error "%s" has no context`, err)
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got != tc.expected {
				t.Errorf("got %v (%T), expected %v (%T)", got, got, tc.expected, tc.expected)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestParseQueryParamsConvertErrors(t *testing.T) {
	type params struct {
		Count int8 `json:"count"`
		Flag  bool `json:"flag"`
		Name  string
	}

	tests := []struct {
		name     string
		src      url.Values
		errParts []string
	}{
		{"ok", url.Values{"count": {"100"}, "flag": {""}}, nil},
		{"overflow", url.Values{"count": {"300"}}, []string{`"Count"`, `"300"`}},
		{"bad bool", url.Values{"flag": {"maybe"}}, []string{`"Flag"`, `"maybe"`}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proc := &ProcOptions{Info: &Info{}}
			proc.ChainLocal.Params.QueryParamsType = reflect.TypeFor[params]()

			err := proc.parseQueryParams(tc.src)

			if len(tc.errParts) == 0 {
				if err != nil {
					t.Fatal(err)
				}

				p := proc.QueryParams.(*params)
				if p.Count != 100 || !p.Flag {
					t.Errorf("got %+v, expected {Count:100 Flag:true}", *p)
				}
				return
			}

			if err == nil {
				t.Fatal("error expected")
			}

			for _, s := range tc.errParts {
				if !strings.Contains(err.Error(), s) {
					t.Errorf(`error "%s" doesn't contain %s`, err, s)
				}
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestSetCookie(t *testing.T) {
	w := httptest.NewRecorder()
	proc := &ProcOptions{W: w}

	proc.SetCookie(CookieLocale, "ru", true)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, expected 1", len(cookies))
	}

	c := cookies[0]
	if c.Name != CookieLocale || c.Value != "ru" || c.Path != "/" {
		t.Errorf("got %s=%s path %s, expected %s=ru path /", c.Name, c.Value, c.Path, CookieLocale)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("got SameSite %v, expected Lax", c.SameSite)
	}
	if c.MaxAge != cookieMaxAge || c.MaxAge > 400*24*60*60 {
		t.Errorf("got MaxAge %d, expected %d (<= 400 days)", c.MaxAge, cookieMaxAge)
	}
	if !c.HttpOnly || !c.Secure {
		t.Errorf("got HttpOnly %v Secure %v, expected both true", c.HttpOnly, c.Secure)
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
