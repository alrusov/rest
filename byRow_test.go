package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type byRowItem struct {
	ID   int    `db:"id" json:"id"`
	Name string `db:"name" json:"name"`
}

func newByRowProc(t *testing.T, withGzip bool) (proc *ProcOptions, w *httptest.ResponseRecorder) {
	sdb, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })

	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id", "name"}).
			AddRow(1, "a").
			AddRow(2, "b").
			AddRow(3, "c"),
	)

	rows, err := sqlx.NewDb(sdb, "sqlmock").Queryx("SELECT id, name FROM t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rows.Close() })

	r := httptest.NewRequest(stdhttp.MethodGET, "/", nil)
	if withGzip {
		r.Header.Set(stdhttp.HTTPheaderAcceptEncoding, stdhttp.ContentEncodingGzip)
	}

	w = httptest.NewRecorder()

	proc = &ProcOptions{
		R:            r,
		W:            w,
		ExtraHeaders: misc.StringMap{},
		DBqueryRows:  rows,
	}
	proc.ChainLocal.Params.Response.Type = reflect.TypeOf(byRowItem{})

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestByRowSuccess(t *testing.T) {
	proc, w := newByRowProc(t, false)

	br, err := NewByRow(proc, nil, true)
	if err != nil {
		t.Fatal(err)
	}

	err = br.Do()
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("got code %d, expected %d", w.Code, http.StatusOK)
	}

	var got []byRowItem
	err = json.Unmarshal(w.Body.Bytes(), &got)
	if err != nil {
		t.Fatalf("%s: %s", err, w.Body.Bytes())
	}

	expected := []byRowItem{{1, "a"}, {2, "b"}, {3, "c"}}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("got %v, expected %v", got, expected)
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestByRowFailureBeforeFlush(t *testing.T) {
	for _, withGzip := range []bool{false, true} {
		t.Run(fmt.Sprintf("gzip=%v", withGzip), func(t *testing.T) {
			proc, w := newByRowProc(t, withGzip)

			tuner := func(br *ByRow, row any) (err error) {
				if br.RowNum == 1 {
					return fmt.Errorf("tuner error")
				}

				br.Data = []RowData{{Separator: ByRowDefaultSeparator, Data: row}}
				return
			}

			br, err := NewByRow(proc, tuner, true)
			if err != nil {
				t.Fatal(err)
			}

			err = br.Do()
			if err == nil {
				t.Fatal("error expected")
			}

			if w.Body.Len() != 0 {
				t.Errorf("partial data sent: %q", w.Body.Bytes())
			}

			if ce := proc.ExtraHeaders[stdhttp.HTTPheaderContentEncoding]; ce != "" {
				t.Errorf(`Content-Encoding "%s" left in the ExtraHeaders`, ce)
			}

			// Вызывающий должен иметь возможность вернуть ошибку
			w.WriteHeader(http.StatusInternalServerError)
			if w.Code != http.StatusInternalServerError {
				t.Errorf("got code %d, expected %d", w.Code, http.StatusInternalServerError)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestByRowFailureAfterFlush(t *testing.T) {
	proc, w := newByRowProc(t, false)

	big := strings.Repeat("x", 2*blockBufferSize) // гарантированно вызовет Flush
	tuner := func(br *ByRow, row any) (err error) {
		if br.RowNum == 1 {
			return fmt.Errorf("tuner error")
		}

		br.Data = []RowData{{Separator: ByRowDefaultSeparator, Data: big}}
		return
	}

	br, err := NewByRow(proc, tuner, true)
	if err != nil {
		t.Fatal(err)
	}

	err = br.Do()
	if err == nil {
		t.Fatal("error expected")
	}

	// Данные уже ушли с кодом 200 -- изменить это нельзя, ошибка только логируется
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Errorf("got code %d body len %d, expected 200 and partial data", w.Code, w.Body.Len())
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
