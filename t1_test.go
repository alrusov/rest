package rest

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alrusov/config"
	"github.com/alrusov/jsonw"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

const (
	testPort = 35555
)

type (
	testHTTP struct {
		mutex *sync.Mutex
		h     *stdhttp.HTTP
		list  testMap
	}

	testMap map[uint64]*testData

	testData struct {
		ID      uint64
		Payload string
	}

	testHandler struct {
		Base string
	}
)

var (
	testH *testHTTP
)

//----------------------------------------------------------------------------------------------------------------------------//

func TestComplex(t *testing.T) {
	var err error

	log.Disable()

	config.SetCommon(
		&config.Common{},
	)

	listenerCfg := &config.Listener{
		Addr:    fmt.Sprintf("127.0.0.1:%d", testPort),
		Timeout: config.Duration(5 * time.Second),
	}

	endpoints := []*testHandler{
		{Base: "/qqq"},
		{Base: "/qqq/www"},
		{Base: "/qqq/www/eee"},
	}

	for _, endpoint := range endpoints {
		err = RegisterEndpoint(endpoint.Base, endpoint)
		if err != nil {
			t.Fatal(err)
		}
	}

	testH = &testHTTP{
		mutex: new(sync.Mutex),
		list:  make(testMap),
	}

	testH.h, err = stdhttp.NewListener(listenerCfg, testH)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		misc.Sleep(3 * time.Second)

		defer testH.h.Stop()

		request := func(method string, objectID uint64, data *testData) (code int, out interface{}, err error) {
			var jData []byte
			if data != nil {
				jData, err = jsonw.Marshal(data)
				if err != nil {
					return
				}
			}

			oid := ""
			if objectID > 0 {
				oid = fmt.Sprintf("/%d", objectID)
			}

			uri := fmt.Sprintf("http://%s%s%s", listenerCfg.Addr, endpoints[0].Base, oid)
			buf, resp, err := stdhttp.Request(method, uri, 500*time.Second, nil, nil, jData)

			if buf != nil {
				out = buf.Bytes()
			}

			if resp != nil {
				code = resp.StatusCode
			}

			return
		}

		data1 := &testData{ID: 1, Payload: "Record 1"}
		_, _, err = request(stdhttp.MethodPOST, 0, data1)
		if err != nil {
			t.Error(err)
			return
		}

		data2 := &testData{ID: 2, Payload: "Record 2"}
		_, _, err = request(stdhttp.MethodPOST, 0, data2)
		if err != nil {
			t.Error(err)
			return
		}

		data3 := &testData{ID: 3, Payload: "Record 3"}
		_, _, err = request(stdhttp.MethodPOST, 0, data3)
		if err != nil {
			t.Error(err)
			return
		}

		data3v1 := &testData{ID: 3, Payload: "Record 3.1"}
		_, _, err = request(stdhttp.MethodPATCH, 3, data3v1)
		if err != nil {
			t.Error(err)
			return
		}

		_, out, err := request(stdhttp.MethodGET, 2, nil)
		if err != nil {
			t.Error(err)
			return
		}

		{
			var data testData
			err = jsonw.Unmarshal(out.([]byte), &data)
			if err != nil {
				return
			}

			if !reflect.DeepEqual(data, *data2) {
				t.Errorf(`got %#v, %#v expected`, data, *data2)
				return
			}

			_, _, err = request(stdhttp.MethodDELETE, 2, nil)
			if err != nil {
				t.Error(err)
				return
			}
		}

		{
			_, out, err = request(stdhttp.MethodGET, 0, nil)
			if err != nil {
				t.Error(err)
				return
			}

			var data testMap
			err = jsonw.Unmarshal(out.([]byte), &data)
			if err != nil {
				return
			}

			if !reflect.DeepEqual(data, testH.list) {
				t.Errorf(`got %#v, %#v expected`, data, testH.list)
				return
			}
		}

	}()

	err = testH.h.Start()
	if err != nil {
		t.Fatal(err)
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func (h *testHTTP) Handler(id uint64, prefix string, path string, w http.ResponseWriter, r *http.Request) (processed bool) {
	testH.mutex.Lock()
	defer testH.mutex.Unlock()
	return Handler(id, prefix, path, w, r)
}

//----------------------------------------------------------------------------------------------------------------------------//

func (th *testHandler) Create(params *Params) (data interface{}, code int, err error) {
	if len(params.Body) == 0 {
		err = fmt.Errorf("Empty body")
		return
	}

	var data0 testData
	data = &data0

	err = jsonw.Unmarshal(params.Body, &data0)
	if err != nil {
		return
	}

	_, exists := testH.list[data0.ID]
	if exists {
		code = http.StatusBadRequest
		err = fmt.Errorf(`"%d" already exists`, data0.ID)
		return
	}

	testH.list[data0.ID] = &data0

	code = http.StatusCreated
	return
}

func (th *testHandler) Get(params *Params) (data interface{}, code int, err error) {
	if len(params.ExtraPath) == 0 {
		data = testH.list
	} else {
		var id uint64
		id, code, err = getID(params.ExtraPath[0])
		if err != nil {
			return
		}

		var exists bool
		data, exists = testH.list[id]
		if !exists {
			code = http.StatusNotFound
			err = fmt.Errorf(`"%d" not found`, id)
			return
		}
	}

	return
}

func (th *testHandler) Replace(params *Params) (data interface{}, code int, err error) {
	return th.Modify(params)
}

func (th *testHandler) Modify(params *Params) (data interface{}, code int, err error) {
	var id uint64
	id, code, err = getID(params.ExtraPath[0])
	if err != nil {
		return
	}

	if len(params.Body) == 0 {
		err = fmt.Errorf("Empty body")
		return
	}

	var data0 testData
	data = &data0

	err = jsonw.Unmarshal(params.Body, &data0)
	if err != nil {
		return
	}

	_, exists := testH.list[data0.ID]
	if !exists {
		code = http.StatusNotFound
		err = fmt.Errorf(`"%d" not found`, data0.ID)
		return
	}

	if id != data0.ID {
		code = http.StatusBadRequest
		err = fmt.Errorf(`IDs mismatched (%d != %d)`, id, data0.ID)
		return
	}

	testH.list[id] = &data0

	code = http.StatusNoContent
	return
}

func (th *testHandler) Delete(params *Params) (data interface{}, code int, err error) {
	var id uint64
	id, code, err = getID(params.ExtraPath[0])
	if err != nil {
		return
	}

	_, exists := testH.list[id]
	if !exists {
		code = http.StatusNotFound
		err = fmt.Errorf(`"%d" not found`, id)
		return
	}

	delete(testH.list, id)
	code = http.StatusNoContent

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func getID(s string) (id uint64, code int, err error) {
	id, err = strconv.ParseUint(s, 10, 64)
	if err != nil {
		code = http.StatusBadRequest
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestGetOptions(t *testing.T) {
	type paramsBlock struct {
		path    string
		base    string
		options string
	}

	params := []paramsBlock{
		{"", "", ""},
		{"/", "/", ""},
		{"/", "", ""},
		{"/qqq", "/qqq", ""},
		{"/qqq", "/", "qqq"},
		{"/qqq/www/", "/qqq/www", ""},
		{"/qqq/www/", "/qqq/", "www"},
		{"/qqq/www/eee", "/qqq/", "www/eee"},
		{"/qqq/www/eee/", "/qqq/www", "eee"},
	}

	for i, p := range params {
		i++

		options := strings.Trim(p.path[len(p.base):], "/")

		if options != p.options {
			t.Errorf(`%d: got "%s", "%s" expected`, i, options, p.options)
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
