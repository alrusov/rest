package rest

import (
	"fmt"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	"github.com/alrusov/db"
)

//----------------------------------------------------------------------------------------------------------------------------//

func TestGetRowsClose(t *testing.T) {
	type afterFunc func(calls int) (result any, code int, err error)

	tests := []struct {
		name   string
		after  afterFunc
		closed []bool // Ожидаемое состояние курсоров каждого выполненного запроса
	}{
		{
			"nothing returned",
			func(int) (any, int, error) { return nil, 0, nil },
			[]bool{true},
		},
		{
			"error",
			func(int) (any, int, error) { return nil, 0, fmt.Errorf("after error") },
			[]bool{true},
		},
		{
			"panic",
			func(int) (any, int, error) { panic("test panic") },
			[]bool{true},
		},
		{
			"result returned",
			func(int) (any, int, error) { return "x", 0, nil },
			[]bool{false},
		},
		{
			"processed",
			func(int) (any, int, error) { return nil, StatusProcessed, nil },
			[]bool{false},
		},
		{
			"retry",
			func(calls int) (any, int, error) {
				if calls == 1 {
					return nil, StatusRetry, nil
				}
				return nil, 0, nil
			},
			[]bool{true, true},
		},
		{
			"retry then processed",
			func(calls int) (any, int, error) {
				if calls == 1 {
					return nil, StatusRetry, nil
				}
				return nil, StatusProcessed, nil
			},
			[]bool{true, false},
		},
	}

	fields, err := db.MakeFieldsList(byRowItem{})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opened []*sqlx.Rows

			old := queryTx
			t.Cleanup(func() { queryTx = old })

			queryTx = func(_ *db.DB, _ *sqlx.Tx, dest any, _ string, _ []string, _ []any) (err error) {
				sdb, mock, err := sqlmock.New()
				if err != nil {
					return
				}
				t.Cleanup(func() { sdb.Close() })

				mock.ExpectQuery("SELECT").WillReturnRows(
					sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "a"),
				)

				rows, err := sqlx.NewDb(sdb, "sqlmock").Queryx("SELECT id, name FROM t")
				if err != nil {
					return
				}

				opened = append(opened, rows)
				*dest.(**sqlx.Rows) = rows
				return
			}

			calls := 0
			proc := &ProcOptions{
				handler: testAPI{},
				Info: &Info{
					After: func(proc *ProcOptions) (result any, code int, err error) {
						calls++
						return tc.after(calls)
					},
				},
				db:           &db.DB{},
				ResultAsRows: true,
			}
			proc.ChainLocal.Params.DBFields = fields

			func() {
				defer func() { recover() }()
				proc.get()
			}()

			if len(opened) != len(tc.closed) {
				t.Fatalf("got %d queries, expected %d", len(opened), len(tc.closed))
			}

			// Закрытый курсор строк не отдает, а у открытого есть непрочитанная строка
			closed := make([]bool, len(opened))
			for i, rows := range opened {
				closed[i] = !rows.Next()
			}

			if !slices.Equal(closed, tc.closed) {
				t.Errorf("got closed %v, expected %v", closed, tc.closed)
			}

			last := opened[len(opened)-1]
			if tc.closed[len(tc.closed)-1] {
				if proc.DBqueryRows != nil {
					t.Error("DBqueryRows is not reset after closing")
				}
			} else if proc.DBqueryRows != last {
				t.Error("DBqueryRows owned by After is lost")
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
