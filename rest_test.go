package rest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

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
		{"insert absent", false, misc.InterfaceMap{"obj_note": "x"}, []string{"name", "count"}},
		{"insert empty string", false, misc.InterfaceMap{"obj_name": "", "obj_count": 1}, []string{"name"}},
		{"insert null", false, misc.InterfaceMap{"obj_name": nil, "obj_count": nil}, []string{"name", "count"}},
		{"update absent", true, misc.InterfaceMap{"obj_note": "x"}, nil},
		{"update empty string", true, misc.InterfaceMap{"obj_name": ""}, []string{"name"}},
		{"update null", true, misc.InterfaceMap{"obj_name": nil, "obj_note": "x"}, []string{"name"}},
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
				var name string
				if _, err := fmt.Sscanf(m, "mandatory field %q is not found", &name); err == nil {
					got = append(got, name)
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
