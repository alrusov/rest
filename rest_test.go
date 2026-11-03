package rest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	"github.com/alrusov/auth"
	"github.com/alrusov/cache"
	"github.com/alrusov/config"
	"github.com/alrusov/db"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v4/path"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type testAPI struct{}

func (testAPI) Info() *Info { return nil }

func (testAPI) Prepare(proc *ProcOptions) (result any, code int, err error) { return }
func (testAPI) Before(proc *ProcOptions) (result any, code int, err error)  { return }
func (testAPI) After(proc *ProcOptions) (result any, code int, err error)   { return }

func useTestCache(t *testing.T) {
	c := cache.New()
	old := cacheGet
	cacheGet = c.Get
	t.Cleanup(func() { cacheGet = old })
}

func newCachedProc(info *Info, host string, user string) *ProcOptions {
	r := httptest.NewRequest(stdhttp.MethodGET, "/api/test?x=1", nil)
	r.Host = host

	proc := &ProcOptions{
		handler:      testAPI{},
		Info:         info,
		ID:           1,
		Path:         "/api/test",
		R:            r,
		ExtraHeaders: misc.StringMap{},
	}
	proc.ChainLocal.CacheLifetime = config.Duration(time.Minute)

	if user != "" {
		proc.AuthIdentity = &auth.Identity{User: user}
	}

	return proc
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestCacheKey(t *testing.T) {
	type req struct {
		host string
		user string
	}

	tests := []struct {
		name     string
		reqs     []req
		expected int
	}{
		{"same", []req{{"a", ""}, {"a", ""}}, 1},
		{"host", []req{{"a", ""}, {"b", ""}, {"a", ""}}, 2},
		{"user", []req{{"a", "u1"}, {"a", "u2"}, {"a", "u1"}, {"a", ""}}, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTestCache(t)

			calls := 0
			info := &Info{
				Before: func(proc *ProcOptions) (result any, code int, err error) {
					calls++
					return fmt.Sprintf("%s/%s", proc.R.Host, proc.cacheUser()), 0, nil
				},
			}

			for i, r := range tc.reqs {
				result, _, err := newCachedProc(info, r.host, r.user).Get()
				if err != nil {
					t.Fatalf("[%d] %s", i, err)
				}

				expected := fmt.Sprintf("%s/%s", r.host, r.user)
				if result != expected {
					t.Errorf(`[%d] got "%v", expected "%s"`, i, result, expected)
				}
			}

			if calls != tc.expected {
				t.Errorf("got %d calls, expected %d", calls, tc.expected)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestCacheNotCacheable(t *testing.T) {
	tests := []struct {
		name  string
		first func() (result any, code int, err error)
	}{
		{"error", func() (any, int, error) { return nil, http.StatusInternalServerError, fmt.Errorf("db error") }},
		{"error without code", func() (any, int, error) { return nil, 0, fmt.Errorf("error") }},
		{"bad code", func() (any, int, error) { return "x", http.StatusNotFound, nil }},
		{"processed", func() (any, int, error) { return nil, StatusProcessed, nil }},
		{"panic", func() (any, int, error) { panic("test panic") }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTestCache(t)

			calls := 0
			info := &Info{
				Before: func(proc *ProcOptions) (result any, code int, err error) {
					calls++
					if calls == 1 {
						return tc.first()
					}
					return "ok", 0, nil
				},
			}

			get := func() (result any, code int, err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("panic: %v", r)
					}
				}()
				return newCachedProc(info, "a", "").Get()
			}

			get() // результат первого вызова не должен попасть в кэш

			for i := range 2 {
				result, code, err := get()
				if err != nil || code != 0 || result != "ok" {
					t.Errorf(`[%d] got (%v, %d, %v), expected ("ok", 0, nil)`, i, result, code, err)
				}
			}

			if calls != 2 {
				t.Errorf("got %d calls, expected 2 (the successful result must be cached)", calls)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestCacheWaiterAfterFailure(t *testing.T) {
	useTestCache(t)

	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0

	info := &Info{
		Before: func(proc *ProcOptions) (result any, code int, err error) {
			calls++
			if calls == 1 {
				close(started)
				<-release
				return nil, http.StatusInternalServerError, fmt.Errorf("db error")
			}
			return "ok", 0, nil
		},
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		newCachedProc(info, "a", "").Get()
	}()

	<-started

	waiterDone := make(chan any)
	go func() {
		result, _, _ := newCachedProc(info, "a", "").Get() // ждет заполнения кэша первым запросом
		waiterDone <- result
	}()

	time.Sleep(50 * time.Millisecond) // даем ожидающему встать на ожидание
	close(release)
	<-firstDone

	select {
	case result := <-waiterDone:
		if result != "ok" {
			t.Errorf(`got "%v", expected "ok"`, result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter hangs")
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestIsTxSuccess(t *testing.T) {
	tests := []struct {
		name      string
		completed bool
		result    any
		code      int
		err       error
		expected  bool
	}{
		{"ok", true, nil, 0, nil, true},
		{"ok 200", true, "x", http.StatusOK, nil, true},
		{"panic", false, nil, 0, nil, false},
		{"error", true, nil, 0, fmt.Errorf("error"), false},
		{"bad code", true, nil, http.StatusConflict, nil, false},
		{"exec ok", true, &ExecResult{SuccessRows: 1}, http.StatusOK, nil, true},
		{"exec failed rows", true, &ExecResult{SuccessRows: 1, FailedRows: 1}, http.StatusMultiStatus, nil, false},
		{"processed", true, nil, StatusProcessed, nil, true},
		{"processed with error", true, nil, StatusProcessed, fmt.Errorf("error"), false},
		{"processed panic", false, nil, StatusProcessed, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTxSuccess(tc.completed, tc.result, tc.code, tc.err)
			if got != tc.expected {
				t.Errorf("got %v, expected %v", got, tc.expected)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

type testKeyObj struct {
	ID   uint64 `json:"id" db:"id" role:"primary"`
	Code string `json:"code" db:"obj_code" role:"key"`
	Ext  string `json:"ext" db:"ext" role:"key"`
	Name string `json:"name" db:"obj_name"`
}

func TestCheckFieldsKeys(t *testing.T) {
	p := path.Params{
		Request: path.RequestParams{
			ParamsObject: path.ParamsObject{
				Name:    "testKeyObj",
				Pattern: testKeyObj{},
			},
		},
	}

	msgs := misc.NewMessages()
	defer msgs.Free()

	err := p.Prepare(stdhttp.MethodPATCH, msgs)
	if err == nil {
		err = msgs.Error()
	}
	if err != nil {
		t.Fatal(err)
	}

	proc := &ProcOptions{}
	proc.ChainLocal.Params = p
	proc.Fields = []misc.InterfaceMap{
		{
			"id":       1,
			"obj_code": "c",
			"ext":      "e",
			"obj_name": "n",
		},
	}

	execResult := NewExecResult()
	ok := proc.checkFields(true, execResult)
	if !ok {
		t.Fatalf("checkFields failed: %v", execResult.Rows[0].Messages)
	}

	if len(proc.Fields[0]) != 1 || proc.Fields[0]["obj_name"] != "n" {
		t.Errorf("got fields %v, expected only obj_name", proc.Fields[0])
	}

	if len(execResult.Rows) != 1 {
		t.Fatalf("got %d rows, expected 1", len(execResult.Rows))
	}

	expected := []string{
		`primary key field "id" ignored`,
		`key field "code" ignored`,
		`key field "ext" ignored`,
	}

	if !slices.Equal(execResult.Rows[0].Messages, expected) {
		t.Errorf("got messages %q, expected %q", execResult.Rows[0].Messages, expected)
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestQueryResult(t *testing.T) {
	items := &[]byRowItem{{1, "a"}, {2, "b"}}
	empty := &[]byRowItem{}

	tests := []struct {
		name         string
		asRows       bool
		notArray     bool
		dbResult     any
		expected     any
		expectedCode int
	}{
		{"array", false, false, items, items, 0},
		{"not array", false, true, items, byRowItem{1, "a"}, 0},
		{"not array empty", false, true, empty, nil, http.StatusNoContent},
		{"as rows", true, false, nil, nil, 0},
		{"as rows not array", true, true, nil, nil, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proc := &ProcOptions{
				ResultAsRows:  tc.asRows,
				DBqueryResult: tc.dbResult,
			}
			if tc.notArray {
				proc.ChainLocal.Params.Flags |= path.FlagResponseIsNotArray
			}

			result, code := proc.queryResult()
			if !reflect.DeepEqual(result, tc.expected) || code != tc.expectedCode {
				t.Errorf("got (%v, %d), expected (%v, %d)", result, code, tc.expected, tc.expectedCode)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestDelSubstArg(t *testing.T) {
	a := db.Subst("a", 1)
	b := db.Subst("b", 2)

	tests := []struct {
		name     string
		vars     []any
		del      string
		expected []any
	}{
		{"middle", []any{1, a, 2}, "a", []any{1, 2}},
		{"first", []any{a, 1, b}, "a", []any{1, b}},
		{"last", []any{1, a, b}, "b", []any{1, a}},
		{"only", []any{a}, "a", []any{}},
		{"not found", []any{1, a, 2}, "x", []any{1, a, 2}},
		{"empty", []any{}, "a", []any{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := slices.Clone(tc.vars)

			result := DelSubstArg(tc.vars, tc.del)

			if !slices.Equal(result, tc.expected) {
				t.Errorf("got %v, expected %v", result, tc.expected)
			}

			if !slices.Equal(tc.vars, orig) {
				t.Errorf("source changed: got %v, expected %v", tc.vars, orig)
			}

			if len(result) > 0 && len(result) < len(tc.vars) {
				result[0] = "changed"
				if tc.vars[0] == "changed" {
					t.Error("result shares memory with the source")
				}
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestResolveTags(t *testing.T) {
	oldTags, oldMap := tags, tagsMap
	tags, tagsMap = Tags{}, map[string]*Tag{}
	t.Cleanup(func() { tags, tagsMap = oldTags, oldMap })

	for _, tag := range []*Tag{
		{Name: "users", Aliases: []string{"user", "accounts"}},
		{Name: "orders", Aliases: []string{"order"}},
	} {
		if err := AddTag(tag); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		src      []string
		expected []string
	}{
		{"empty", nil, []string{}},
		{"name", []string{"users"}, []string{"users"}},
		{"alias", []string{"user"}, []string{"users"}},
		{"all aliases", []string{"user", "order"}, []string{"users", "orders"}},
		{"unknown", []string{"misc", "order"}, []string{"misc", "orders"}},
		{"duplicates", []string{"user", "accounts", "users", "misc", "misc"}, []string{"users", "misc"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTags(tc.src)
			if !slices.Equal(got, tc.expected) {
				t.Errorf("got %q, expected %q", got, tc.expected)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

type testRequiredObj struct {
	ID    uint64 `json:"id" db:"id" role:"primary"`
	Name  string `json:"name" db:"obj_name" required:"true"`
	Count int    `json:"count" db:"obj_count" required:"true"`
	Note  string `json:"note" db:"obj_note"`
}

func TestCheckFieldsRequired(t *testing.T) {
	p := path.Params{
		Request: path.RequestParams{
			ParamsObject: path.ParamsObject{
				Name:    "testRequiredObj",
				Pattern: testRequiredObj{},
			},
		},
	}

	msgs := misc.NewMessages()
	defer msgs.Free()

	err := p.Prepare(stdhttp.MethodPOST, msgs)
	if err == nil {
		err = msgs.Error()
	}
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		forUpdate bool
		fields    misc.InterfaceMap
		missing   []string // ожидаемые сообщения об обязательных полях
	}{
		{"insert ok", false, misc.InterfaceMap{"obj_name": "n", "obj_count": 1}, nil},
		{"insert zero number", false, misc.InterfaceMap{"obj_name": "n", "obj_count": 0}, nil},
		{"insert absent", false, misc.InterfaceMap{"obj_note": "x"}, []string{`"name" is not found`, `"count" is not found`}},
		{"insert empty string", false, misc.InterfaceMap{"obj_name": "", "obj_count": 1}, []string{`"name" is empty`}},
		{"insert null", false, misc.InterfaceMap{"obj_name": nil, "obj_count": nil}, []string{`"name" is empty`, `"count" is empty`}},
		{"update absent", true, misc.InterfaceMap{"obj_note": "x"}, nil},
		{"update empty string", true, misc.InterfaceMap{"obj_name": ""}, []string{`"name" is empty`}},
		{"update null", true, misc.InterfaceMap{"obj_name": nil, "obj_note": "x"}, []string{`"name" is empty`}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proc := &ProcOptions{}
			proc.ChainLocal.Params = p
			proc.Fields = []misc.InterfaceMap{tc.fields}

			execResult := NewExecResult()
			ok := proc.checkFields(tc.forUpdate, execResult)

			var got []string
			for _, m := range execResult.Rows[0].Messages {
				if s, ok := strings.CutPrefix(m, "mandatory field "); ok {
					got = append(got, s)
				}
			}

			slices.Sort(got)
			expected := slices.Clone(tc.missing)
			slices.Sort(expected)

			if !slices.Equal(got, expected) {
				t.Errorf("got missing %q, expected %q (messages: %q)", got, expected, execResult.Rows[0].Messages)
			}

			if ok != (len(tc.missing) == 0) {
				t.Errorf("got success %v, expected %v", ok, len(tc.missing) == 0)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestTagsConcurrent(t *testing.T) {
	oldTags, oldMap := tags, tagsMap
	tags, tagsMap = Tags{}, map[string]*Tag{}
	t.Cleanup(func() { tags, tagsMap = oldTags, oldMap })

	const n = 50

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(2)

		go func() {
			defer wg.Done()
			if err := AddTag(&Tag{Name: fmt.Sprintf("tag%d", i), Aliases: []string{fmt.Sprintf("alias%d", i)}}); err != nil {
				t.Error(err)
			}
		}()

		go func() {
			defer wg.Done()
			GetTagName(fmt.Sprintf("alias%d", i))
			resolveTags([]string{"alias0", "tag1"})
			_ = len(GetTags())
		}()
	}
	wg.Wait()

	if got := len(GetTags()); got != n {
		t.Errorf("got %d tags, expected %d", got, n)
	}

	for i := range n {
		if name := GetTagName(fmt.Sprintf("alias%d", i)); name != fmt.Sprintf("tag%d", i) {
			t.Errorf(`alias%d: got "%s", expected "tag%d"`, i, name, i)
		}
	}

	// GetTags отдает копию
	list := GetTags()
	list[0] = &Tag{Name: "changed"}
	if GetTags()[0].Name == "changed" {
		t.Error("GetTags returns the internal slice")
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

type testQueryParams struct {
	Count int    `json:"count" default:"10"`
	Flag  bool   `json:"flag"`
	Name  string `json:"name" sample:"name=abc"`
	From  string `json:"from"`
	Skip  string `json:"-"`
}

func TestMakeParamsDescription(t *testing.T) {
	tests := []struct {
		name     string
		pattern  any
		expected string
	}{
		{"struct", testQueryParams{}, "count=10 & flag & name=abc & from=..."},
		{"pointer", &testQueryParams{}, "count=10 & flag & name=abc & from=..."},
		{"none", nil, "-"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &Info{
				Methods: &path.Set{
					Methods: path.Methods{
						stdhttp.MethodGET: &path.Chains{
							StdParams: path.Params{
								QueryParamsPattern: tc.pattern,
							},
							Chains: path.ChainsList{
								{Tokens: []*path.Token{{VarName: path.VarIgnore}}},
							},
						},
					},
				},
			}

			err := info.Methods.Prepare()
			if err != nil {
				t.Fatal(err)
			}

			err = info.makeParamsDescription()
			if err != nil {
				t.Fatal(err)
			}

			got := info.Methods.Methods[stdhttp.MethodGET].ParamsDescription
			if got != tc.expected {
				t.Errorf(`got "%s", expected "%s"`, got, tc.expected)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

type testExclObj struct {
	ID   uint64 `json:"id" db:"id" role:"primary"`
	Name string `json:"name" db:"obj_name"`
	Note string `json:"note" db:"obj_note"`
}

func TestExcludedFields(t *testing.T) {
	excluded := []struct {
		name string
		ef   misc.StringMap
	}{
		{"empty value", misc.StringMap{"obj_note": ""}},
		{"json name value", misc.StringMap{"obj_note": "note"}},
		{"db name value", misc.StringMap{"obj_note": "obj_note"}},
	}

	prepare := func(t *testing.T, m string) path.Params {
		p := path.Params{
			Request: path.RequestParams{
				ParamsObject: path.ParamsObject{
					Name:    "testExclObj",
					Pattern: testExclObj{},
				},
			},
			Response: path.ResponseParams{
				ParamsObject: path.ParamsObject{
					Name:    "testExclObj",
					Pattern: testExclObj{},
				},
			},
		}

		msgs := misc.NewMessages()
		defer msgs.Free()

		err := p.Prepare(m, msgs)
		if err == nil {
			err = msgs.Error()
		}
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	for _, tc := range excluded {
		t.Run("save/"+tc.name, func(t *testing.T) {
			proc := &ProcOptions{ExcludedFields: tc.ef}
			proc.ChainLocal.Params = prepare(t, stdhttp.MethodPOST)
			proc.Fields = []misc.InterfaceMap{{"obj_name": "n", "obj_note": "x"}}

			execResult := NewExecResult()
			if !proc.checkFields(false, execResult) {
				t.Fatalf("checkFields failed: %q", execResult.Rows[0].Messages)
			}

			if _, exists := proc.Fields[0]["obj_note"]; exists {
				t.Errorf("excluded field is not removed: %v", proc.Fields[0])
			}

			expected := []string{`excluded field "note" ignored`}
			if !slices.Equal(execResult.Rows[0].Messages, expected) {
				t.Errorf("got messages %q, expected %q", execResult.Rows[0].Messages, expected)
			}
		})

		t.Run("get/"+tc.name, func(t *testing.T) {
			var queried []string

			old := queryTx
			t.Cleanup(func() { queryTx = old })
			queryTx = func(_ *db.DB, _ *sqlx.Tx, _ any, _ string, fields []string, _ []any) error {
				queried = slices.Clone(fields)
				return nil
			}

			proc := &ProcOptions{
				handler:        testAPI{},
				Info:           &Info{},
				db:             &db.DB{},
				ExcludedFields: tc.ef,
			}
			proc.ChainLocal.Params = prepare(t, stdhttp.MethodGET)

			_, _, err := proc.get()
			if err != nil {
				t.Fatal(err)
			}

			if len(queried) != 2 {
				t.Fatalf("got fields %q, expected 2 fields", queried)
			}

			for _, f := range queried {
				if strings.Contains(f, "obj_note") {
					t.Errorf("excluded field is queried: %q", queried)
				}
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestMultiDefer(t *testing.T) {
	row := func(code int) *ExecResultRow {
		r := NewExecResultRow()
		r.Code = code
		return r
	}

	testErr := fmt.Errorf("test error")
	other := "not exec result"

	type state struct {
		result any
		code   int
		err    error
	}

	tests := []struct {
		name     string
		rows     []*ExecResultRow // строки ExecResult, на котором вызывается MultiDefer
		in       state
		expected state // result == nil -- ожидается ExecResult, проверяемый ниже
		total    uint64
		success  uint64
		failed   uint64
		rowCodes []int
	}{
		{
			name:     "all ok",
			rows:     []*ExecResultRow{row(200), row(200)},
			expected: state{code: 200},
			total:    2, success: 2, failed: 0,
			rowCodes: []int{200, 200},
		},
		{
			name:     "mixed",
			rows:     []*ExecResultRow{row(200), row(404)},
			expected: state{code: http.StatusMultiStatus},
			total:    2, success: 1, failed: 1,
			rowCodes: []int{200, 404},
		},
		{
			name:     "all failed same code",
			rows:     []*ExecResultRow{row(404), row(404)},
			expected: state{code: 404},
			total:    2, success: 0, failed: 2,
			rowCodes: []int{404, 404},
		},
		{
			name:     "no rows",
			expected: state{code: 0},
			total:    0, success: 0, failed: 0,
		},
		{
			name:     "error with rows",
			rows:     []*ExecResultRow{row(200), row(404)},
			in:       state{err: testErr},
			expected: state{code: http.StatusUnprocessableEntity, err: testErr},
			total:    2, success: 0, failed: 2,
			rowCodes: []int{422, 422},
		},
		{
			name:     "error without rows",
			in:       state{err: testErr, code: http.StatusInternalServerError},
			expected: state{code: http.StatusUnprocessableEntity, err: testErr},
			total:    1, success: 0, failed: 1,
			rowCodes: []int{422},
		},
		{
			name:     "processed",
			rows:     []*ExecResultRow{row(404)},
			in:       state{code: StatusProcessed},
			expected: state{code: StatusProcessed, result: "untouched"},
		},
		{
			name:     "other result",
			rows:     []*ExecResultRow{row(404)},
			in:       state{result: other, code: 201},
			expected: state{result: other, code: 201},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execResult := NewExecResult()
			execResult.Rows = tc.rows

			result, code, err := tc.in.result, tc.in.code, tc.in.err
			execResult.MultiDefer(&result, &code, &err)

			if code != tc.expected.code || err != tc.expected.err {
				t.Errorf("got (%d, %v), expected (%d, %v)", code, err, tc.expected.code, tc.expected.err)
			}

			if tc.expected.result != nil {
				expected := tc.expected.result
				if expected == "untouched" {
					expected = tc.in.result
				}
				if result != expected {
					t.Errorf("got result %v, expected %v", result, expected)
				}
				return
			}

			r, ok := result.(*ExecResult)
			if !ok || r != execResult {
				t.Fatalf("got result %T, expected the same *ExecResult", result)
			}

			if r.TotalRows != tc.total || r.SuccessRows != tc.success || r.FailedRows != tc.failed {
				t.Errorf("got total/success/failed %d/%d/%d, expected %d/%d/%d",
					r.TotalRows, r.SuccessRows, r.FailedRows, tc.total, tc.success, tc.failed)
			}

			codes := make([]int, len(r.Rows))
			for i, row := range r.Rows {
				codes[i] = row.Code
				if tc.in.err != nil && !slices.Contains(row.Errors(), tc.in.err) {
					t.Errorf("[%d] error is not added to the row", i)
				}
			}
			if !slices.Equal(codes, tc.rowCodes) {
				t.Errorf("got row codes %v, expected %v", codes, tc.rowCodes)
			}
		})
	}
}

func TestMultiDeferResultExecResult(t *testing.T) {
	// ExecResult из result имеет приоритет над тем, на котором вызван MultiDefer
	own := NewExecResult()
	own.AddRow(NewExecResultRow())

	fromResult := NewExecResult()
	r := NewExecResultRow()
	r.Code = http.StatusCreated
	fromResult.AddRow(r)

	var result any = fromResult
	code := 0
	var err error

	own.MultiDefer(&result, &code, &err)

	if result != fromResult {
		t.Fatalf("got result %v, expected ExecResult from result", result)
	}

	if code != http.StatusCreated || fromResult.TotalRows != 1 || fromResult.SuccessRows != 1 {
		t.Errorf("got code %d total %d success %d, expected 201 1 1", code, fromResult.TotalRows, fromResult.SuccessRows)
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestDbResultParser(t *testing.T) {
	testErr := fmt.Errorf("db error")

	type add struct {
		affected int64 // < 0 -- без sql.Result
		err      error
	}

	tests := []struct {
		name      string
		rows      int // строк в ExecResult до разбора
		adds      []add
		nilResult bool
		returns   []*ExecResultRow // nil -- без RETURNING
		codes     []int
		success   uint64
		withErr   []bool // в каких строках должна быть ошибка
	}{
		{
			name:    "affected",
			rows:    2,
			adds:    []add{{1, nil}, {1, nil}},
			codes:   []int{200, 200},
			success: 2,
		},
		{
			name:    "not affected",
			rows:    1,
			adds:    []add{{0, nil}},
			codes:   []int{404},
			success: 0,
		},
		{
			name:      "nil result",
			rows:      1,
			nilResult: true,
			codes:     []int{404},
		},
		{
			name:    "one of two failed",
			rows:    2,
			adds:    []add{{1, nil}, {-1, testErr}},
			codes:   []int{200, 500},
			success: 1,
			withErr: []bool{false, true},
		},
		{
			name:    "errors without rows",
			rows:    0,
			adds:    []add{{-1, testErr}, {-1, testErr}},
			codes:   []int{500, 500},
			withErr: []bool{true, true},
		},
		{
			name:    "returning",
			rows:    3,
			adds:    []add{{1, nil}, {0, nil}, {0, nil}},
			returns: []*ExecResultRow{{ID: 1, GUID: "g1"}, nil, {ID: 0}},
			codes:   []int{200, 404, 404},
			success: 1,
		},
		{
			name:    "returning more than rows",
			rows:    0,
			returns: []*ExecResultRow{{ID: 1}, {ID: 2}},
			codes:   []int{200, 200},
			success: 2,
		},
		{
			name:    "returning with error",
			rows:    2,
			adds:    []add{{1, nil}, {-1, testErr}},
			returns: []*ExecResultRow{{ID: 1}, nil},
			codes:   []int{200, 500},
			success: 1,
			withErr: []bool{false, true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execResult := NewExecResult()
			for range tc.rows {
				execResult.AddRow(NewExecResultRow())
			}

			var dbResult *db.Result
			if !tc.nilResult {
				dbResult = &db.Result{}
				for _, a := range tc.adds {
					if a.affected < 0 {
						dbResult.Add(nil, a.err)
					} else {
						dbResult.Add(sqlmock.NewResult(0, a.affected), a.err)
					}
				}
			}

			var returnsObj *[]*ExecResultRow
			if tc.returns != nil {
				returnsObj = &tc.returns
			}

			err := execResult.DbResultParser(dbResult, returnsObj)
			if err != nil {
				t.Fatal(err)
			}

			codes := make([]int, len(execResult.Rows))
			for i, r := range execResult.Rows {
				codes[i] = r.Code

				hasErr := i < len(tc.withErr) && tc.withErr[i]
				if r.HasErrors() != hasErr {
					t.Errorf("[%d] got has error %v, expected %v", i, r.HasErrors(), hasErr)
				}

				if i < len(tc.returns) && tc.returns[i] != nil && tc.returns[i].ID != 0 {
					if r.ID != tc.returns[i].ID || r.GUID != tc.returns[i].GUID {
						t.Errorf("[%d] got id %d guid %q, expected %d %q", i, r.ID, r.GUID, tc.returns[i].ID, tc.returns[i].GUID)
					}
				}
			}

			if !slices.Equal(codes, tc.codes) {
				t.Errorf("got codes %v, expected %v", codes, tc.codes)
			}

			if execResult.SuccessRows != tc.success {
				t.Errorf("got success %d, expected %d", execResult.SuccessRows, tc.success)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
