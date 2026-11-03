package path

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type pathParamsPattern struct {
	Tp     string
	GID    uint64
	ID     uint64
	Status string
}

func TestPrepareIllegal(t *testing.T) {
	illegal := []*Chains{
		{
			Chains: []*Chain{
				{
					Name: "no PathParamsPattern",
					Tokens: []*Token{
						{Expr: `\d+`, VarName: `ID`},
					},
				},
			},
		},
		{
			StdParams: Params{
				PathParamsPattern: "test",
			},
			Chains: []*Chain{
				{
					Name: "illegal PathParamsPattern type",
					Tokens: []*Token{
						{Expr: `\d+`, VarName: `X`},
					},
				},
			},
		},
		{
			StdParams: Params{
				PathParamsPattern: pathParamsPattern{},
			},
			Chains: []*Chain{
				// Empty
				{},
			},
		},
		{
			StdParams: Params{
				PathParamsPattern: struct{ X float64 }{},
			},
			Chains: []*Chain{
				{
					Name: "illegal PathParamsPattern field type",
					Tokens: []*Token{
						{Expr: `\d+`, VarName: `X`},
					},
				},
			},
		},
		{
			StdParams: Params{
				PathParamsPattern: pathParamsPattern{},
			},
			Chains: []*Chain{
				{
					Name: "unknown field X",
					Tokens: []*Token{
						{Expr: `\d+`, VarName: `X`},
					},
				},
			},
		},
		{
			StdParams: Params{
				PathParamsPattern: pathParamsPattern{},
			},
			Chains: []*Chain{
				{
					Name: "tokens with empty first expression",
					Tokens: []*Token{
						{Expr: ``, VarName: `Tp`},
						{Expr: `\d+`, VarName: `ID`},
					},
				},
			},
		},
		{
			StdParams: Params{
				Request: RequestParams{
					ParamsObject: ParamsObject{
						Name: "o1",
						Pattern: struct {
							a int
							b string
						}{},
					},
				},
				PathParamsPattern: pathParamsPattern{},
			},
			Chains: []*Chain{
				{
					Name: "bad regexp",
					Tokens: []*Token{
						{Expr: `group`, VarName: `Tp`},
						{Expr: `[\d+`, VarName: `GID`},
					},
				},
			},
		},
		{
			StdParams: Params{
				Request: RequestParams{
					ParamsObject: ParamsObject{
						Name: "o1",
						Pattern: struct {
							a int
							b string
							c int
						}{},
					},
				},
				PathParamsPattern: pathParamsPattern{},
			},
			Chains: []*Chain{
				{
					Name: "bad regexp",
					Tokens: []*Token{
						{Expr: `group`, VarName: `Tp`},
						{Expr: `\d+`, VarName: `GID`},
					},
				},
			},
		},
	}

	for i, c := range illegal {
		err := c.Prepare("GET")
		if err == nil {
			t.Fatalf("[%d] error expected, but not found (%#v)", i, c)
		}
	}

}

func TestParser(t *testing.T) {
	c := Chains{
		StdParams: Params{
			PathParamsPattern: pathParamsPattern{},
		},
		// unsorted!
		Chains: []*Chain{
			// Active or blocked users in group with ID=gid
			{
				Name: "c1",
				Tokens: []*Token{
					{Expr: `group`, VarName: `Tp`},
					{Expr: `\d+`, VarName: `GID`},
					{Expr: `active|blocked`, VarName: `Status`},
				},
			},
			{
				// Active or blocked users
				Name: "c2",
				Tokens: []*Token{
					{Expr: `active|blocked`, VarName: `Status`},
				},
			},
			{
				// User by ID
				Name: "c3",
				Tokens: []*Token{
					{Expr: `\d+`, VarName: `ID`},
				},
			},
			{
				// Users in group with ID=gid
				Name: "c4",
				Tokens: []*Token{
					{Expr: `group`, VarName: `Tp`},
					{Expr: `\d+`, VarName: `GID`},
				},
			},
			{
				// All users
				Name: "c5",
				Tokens: []*Token{
					{Expr: ``, VarName: `Tp`},
				},
			},
		},
	}

	variants := []struct {
		path       []string
		pathParams *pathParamsPattern
		found      bool
		name       string
		isErr      bool
		code       int
	}{
		{
			path:       []string{},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: ""},
			found:      true,
			name:       "c5",
			isErr:      false,
		},
		{
			path:       []string{""},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: ""},
			found:      true,
			name:       "c5",
			isErr:      false,
		},
		{
			path:       []string{"24"},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 24, Status: ""},
			found:      true,
			name:       "c3",
			isErr:      false,
		},
		{
			path:       []string{"active"},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: "active"},
			found:      true,
			name:       "c2",
			isErr:      false,
		},
		{
			path:       []string{"blocked"},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: "blocked"},
			found:      true,
			name:       "c2",
			isErr:      false,
		},
		{
			path:       []string{"group", "335"},
			pathParams: &pathParamsPattern{Tp: "group", GID: 335, ID: 0, Status: ""},
			found:      true,
			name:       "c4",
			isErr:      false,
		},
		{
			path:       []string{"group", "335", "active"},
			pathParams: &pathParamsPattern{Tp: "group", GID: 335, ID: 0, Status: "active"},
			found:      true,
			name:       "c1",
			isErr:      false,
		},
		{
			path:       []string{"group", "335", "blocked"},
			pathParams: &pathParamsPattern{Tp: "group", GID: 335, ID: 0, Status: "blocked"},
			found:      true,
			name:       "c1",
			isErr:      false,
		},
		{
			path:       []string{"123", ""},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 123, Status: ""},
			found:      false,
			name:       "",
			isErr:      false,
		},
		{
			path:       []string{"123", "active"},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: ""},
			found:      false,
			name:       "",
			isErr:      false,
		},
		{
			path:       []string{"123", ""},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: ""},
			found:      false,
			name:       "",
			isErr:      false,
		},
		{
			path:       []string{"active", "123"},
			pathParams: &pathParamsPattern{Tp: "", GID: 0, ID: 0, Status: ""},
			found:      false,
			name:       "",
			isErr:      false,
		},
	}

	err := c.Prepare("GET")
	if err != nil {
		t.Fatal(err)
	}

	var prev *Chain
	prevLn := -1

	for _, chain := range c.Chains {
		ln := len(chain.Tokens)
		if ln < prevLn {
			t.Errorf(`Unsorted: %#v after %#v`, chain, prev)
		}
		prev = chain
		prevLn = len(chain.Tokens)

		for _, token := range chain.Tokens {
			if token.re == nil {
				t.Errorf(`re is nil: %#v`, token)
			}
		}
	}

	for i, df := range variants {
		matched, pathParams, code, err := c.Find(df.path)

		if !df.isErr && err != nil {
			t.Errorf("[%d] %s (%#v)", i, err, df)
			continue
		}

		if df.isErr && err == nil {
			t.Errorf("[%d] error expected (%#v)", i, df)
			continue
		}

		if err != nil {
			continue
		}

		found := matched != nil

		if found != df.found {
			t.Errorf("[%d] found is %v, expected %v (%#v)", i, found, df.found, df)
			continue
		}

		if !found {
			continue
		}

		if matched.Name != df.name {
			t.Errorf(`[%d] name is "%s", expected "%s" (%#v)`, i, matched.Name, df.name, df)
			continue
		}

		if code != df.code {
			t.Errorf(`[%d] code is "%d", expected "%d" (%#v)`, i, code, df.code, df)
			continue
		}

		if !reflect.DeepEqual(pathParams, df.pathParams) {
			t.Errorf("[%d] got pathParams\n%#v\nexpected\n%#v", i, pathParams, df.pathParams)
		}
		/*
			for name, v := range vars {
				expected, exists := df.expected[name]
				if !exists {
					t.Errorf("[%d] %s -- unknown", i, name)
					continue
				}

				v := reflect.ValueOf(v).Elem().Interface()
				if !reflect.DeepEqual(v, expected) {
					t.Errorf("[%d] %s is %T(%v), %T(%v) expected (%#v)", i, name, v, v, expected, expected, df)
					continue
				}
			}
		*/
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestFindPathParamsConvertError(t *testing.T) {
	c := &Chains{
		StdParams: Params{
			PathParamsPattern: struct{ ID uint64 }{},
		},
		Chains: []*Chain{
			{
				Name: "id",
				Tokens: []*Token{
					{Expr: `\d+`, VarName: `ID`},
				},
			},
		},
	}

	err := c.Prepare(stdhttp.MethodGET)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		path     []string
		code     int
		hasError bool
	}{
		{"ok", []string{"123"}, 0, false},
		{"overflow", []string{"99999999999999999999"}, http.StatusNotFound, true},
		{"not matched", []string{"abc"}, http.StatusNotFound, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matched, _, code, err := c.Find(tc.path)

			if code != tc.code {
				t.Errorf("got code %d, expected %d", code, tc.code)
			}

			if (err != nil) != tc.hasError {
				t.Errorf(`got error "%v", expected error: %v`, err, tc.hasError)
			}

			if (matched != nil) != (tc.code == 0) {
				t.Errorf("got matched %v, expected matched: %v", matched != nil, tc.code == 0)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
