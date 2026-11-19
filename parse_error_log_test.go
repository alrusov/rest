package rest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
