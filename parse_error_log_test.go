package rest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alrusov/jsonw"
)

//----------------------------------------------------------------------------------------------------------------------------//

// Ошибка разбора тела - в журнал ERR без заголовков и тела запроса (idc/docs#346)
func TestBodyParseErrorMessage(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/api/metainfo/collectors/lines/guid/x?token=secret-q", nil)
	r.Header.Set("Authorization", "Bearer eyJsecret-jwt")
	r.Header.Set("Content-Type", "application/json")

	proc := &ProcOptions{
		ID:      42,
		R:       r,
		RawBody: []byte(`[{"parameters":{"password":"secret-p1"},}]`),
	}

	msg := bodyParseErrorMessage(proc, errors.New("invalid character '}' looking for beginning of object key string"))

	for _, v := range []string{"eyJ", "secret-jwt", "secret-p1", "secret-q", "parameters"} {
		if strings.Contains(msg, v) {
			t.Errorf("%q in log message: %s", v, msg)
		}
	}

	for _, v := range []string{"[42]", "PUT", "/api/metainfo/collectors/lines/guid/x", "invalid character", "application/json", "42 bytes"} {
		if !strings.Contains(msg, v) {
			t.Errorf("%q expected in log message: %s", v, msg)
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

// Ошибка json-iterator несёт окна тела, обрезанные посреди ключа (…|mmunityName":"x"|…) — в журнал их не пускаем
func TestBodyParseErrorMessageJsoniterContext(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/metainfo/collectors/lines", nil)
	r.Header.Set("Content-Type", "application/json")

	body := []byte(`[{"parameters":{"communityName":"secret-cn","password":"secret-pw"]`)
	proc := &ProcOptions{ID: 7, R: r, RawBody: body}

	// api-rt и api-mdl-new разбирают через json-iterator (jsonw.UseStd(false)); по умолчанию jsonw — стандартная библиотека
	jsonw.UseStd(false)
	defer jsonw.UseStd(true)

	var v []struct {
		Parameters map[string]string `json:"parameters"`
	}
	err := jsonw.Unmarshal(body, &v)
	if err == nil {
		t.Fatal("parse error expected")
	}
	if !strings.Contains(err.Error(), jsonContextMarker) {
		t.Fatalf("json-iterator context marker not found, the check is useless: %s", err)
	}

	msg := bodyParseErrorMessage(proc, err)

	for _, v := range []string{"secret-cn", "secret-pw", "ecret", "mmunityName", "|"} {
		if strings.Contains(msg, v) {
			t.Errorf("%q in log message: %s", v, msg)
		}
	}

	for _, v := range []string{"[7]", "POST", "/api/metainfo/collectors/lines", "error found in #", "body fragment omitted", "application/json"} {
		if !strings.Contains(msg, v) {
			t.Errorf("%q expected in log message: %s", v, msg)
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
