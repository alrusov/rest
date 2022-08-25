package rest

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

/*
Recommended behavior

+--------+----------------+------------------------------------------------------------------------------------------------------+----------------------------------------------------------------------------+
| Method | Operation      | Entire Collection (e.g. /customers)                                                                  | Specific Item (e.g. /customers/{id})                                       |
+--------+----------------+------------------------------------------------------------------------------------------------------+----------------------------------------------------------------------------+
| POST   | Create         | 201 (Created), 'Location' header with link to /customers/{id} containing new ID.                     | 404 (Not Found), 409 (Conflict) if resource already exists..               |
| GET    | Read           | 200 (OK), list of customers. Use pagination, sorting and filtering to navigate big lists.            | 200 (OK), single customer. 404 (Not Found), if ID not found or invalid.    |
| PUT    | Update/Replace | 405 (Method Not Allowed), unless you want to update/replace every resource in the entire collection. | 200 (OK) or 204 (No Content). 404 (Not Found), if ID not found or invalid. |
| PATCH  | Update/Modify  | 405 (Method Not Allowed), unless you want to modify the collection itself.                           | 200 (OK) or 204 (No Content). 404 (Not Found), if ID not found or invalid. |
| DELETE | Delete         | 405 (Method Not Allowed), unless you want to delete the whole collection—not often desirable.        | 200 (OK). 404 (Not Found), if ID not found or invalid.                     |
+--------+----------------+------------------------------------------------------------------------------------------------------+----------------------------------------------------------------------------+
*/

type (
	// Endpoint --
	Endpoint interface {
		Create(params *Params) (data interface{}, code int, err error)
		Get(params *Params) (data interface{}, code int, err error)
		Replace(params *Params) (data interface{}, code int, err error)
		Modify(params *Params) (data interface{}, code int, err error)
		Delete(params *Params) (data interface{}, code int, err error)
	}

	endpointDef struct {
		endpoint      Endpoint
		useHashForGet bool
	}
	// Flags --
	UseHashForGet bool

	// Params --
	Params struct {
		ID        uint64              `json:"id"`
		Prefix    string              `json:"prefix"`
		Path      string              `json:"path"`
		W         http.ResponseWriter `json:"-"`
		R         *http.Request       `json:"-"`
		Base      string              `json:"base"`
		ExtraPath []string            `json:"extraPath"`
		Body      []byte              `json:"body"`
		df        *endpointDef
	}
)

var (
	mutex     = new(sync.RWMutex)
	endpoints = make(map[string]*endpointDef)
)

//----------------------------------------------------------------------------------------------------------------------------//

// RegisterEndpoint --
func RegisterEndpoint(url string, endpoint Endpoint, options ...interface{}) (err error) {
	mutex.Lock()
	defer mutex.Unlock()

	_, exists := endpoints[url]
	if exists {
		err = fmt.Errorf(`"%s" is already defined`, url)
		return
	}

	df := &endpointDef{
		endpoint: endpoint,
	}

	for _, opt := range options {
		switch opt := opt.(type) {
		case UseHashForGet:
			df.useHashForGet = bool(opt)
		}
	}

	endpoints[url] = df
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Handler --
func Handler(id uint64, prefix string, path string, w http.ResponseWriter, r *http.Request) (processed bool) {
	processed = false

	path = misc.NormalizeSlashes(path)
	var df *endpointDef

	mutex.RLock()

	base := path
	for {
		e, exists := endpoints[base]
		if exists {
			df = e
			break
		}

		idx := strings.LastIndex(base, "/")
		if idx <= 0 {
			break
		}

		base = base[:idx]
	}

	mutex.RUnlock()

	if df == nil {
		return
	}

	var err error

	processed = true

	var body []byte

	if r.Body != nil {
		var bodyBuf *bytes.Buffer
		bodyBuf, _, err = stdhttp.ReadData(r.Header, r.Body)
		if err != nil {
			stdhttp.Error(id, false, w, r, http.StatusInternalServerError, err.Error(), nil)
			return
		}
		r.Body.Close()
		body = bodyBuf.Bytes()
	}

	extraPath := strings.Split(strings.Trim(path[len(base):], "/"), "/")
	if len(extraPath) == 1 && extraPath[0] == "" {
		extraPath = []string{}
	}

	params := &Params{
		ID:        id,
		Prefix:    prefix,
		Path:      path,
		W:         w,
		R:         r,
		Base:      base,
		ExtraPath: extraPath,
		Body:      body,
		df:        df,
	}

	headers := make(misc.StringMap, 2)

	code := 0
	var data interface{}
	var objectID interface{}

	withHash := false
	hash := ""

	switch r.Method {
	case stdhttp.MethodPOST:
		if len(params.ExtraPath) != 0 {
			data, code, err = NotFound(params)
		} else {
			data, code, err = df.endpoint.Create(params)
			if err == nil && code == http.StatusCreated && objectID != nil {
				headers["Location"] = fmt.Sprintf("%s/%v", params.Base, objectID)
			}
		}

	case stdhttp.MethodGET:
		data, code, err = df.endpoint.Get(params)
		withHash = df.useHashForGet
		if withHash {
			hash = r.URL.Query().Get("hash")
		}

	case stdhttp.MethodPUT:
		if len(params.ExtraPath) == 0 {
			data, code, err = NotAllowed(params)
		} else {
			data, code, err = df.endpoint.Replace(params)
		}

	case stdhttp.MethodPATCH:
		if len(params.ExtraPath) == 0 {
			data, code, err = NotAllowed(params)
		} else {
			data, code, err = df.endpoint.Modify(params)
		}

	case stdhttp.MethodDELETE:
		if len(params.ExtraPath) == 0 {
			data, code, err = NotAllowed(params)
		} else {
			data, code, err = df.endpoint.Delete(params)
		}

	default:
		data, code, err = NotAllowed(params)
	}

	if code < 0 {
		code = -code
	} else if code == 0 {
		if err != nil {
			code = http.StatusInternalServerError
		} else if data == nil {
			code = http.StatusNoContent
		} else {
			code = http.StatusOK
		}
	}

	if err != nil {
		stdhttp.Error(id, false, w, r, code, err.Error(), nil)
		return
	}

	var jData []byte
	contentType := stdhttp.ContentTypeJSON

	if code == http.StatusNoContent {
		// nothing to do
		contentType = stdhttp.ContentTypeText
	} else if data == nil {
		jData = []byte("{}")
	} else {
		code2 := code
		jData, code2, headers, err = stdhttp.JSONResultWithDataHash(data, withHash && code/100 == 2, hash, headers)
		if code2 != http.StatusOK {
			code = code2
		}
		if err != nil {
			stdhttp.Error(id, false, w, r, code, err.Error(), nil)
			return
		}
	}

	stdhttp.WriteReply(w, r, code, contentType, headers, jData)
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// NotFound --
func NotFound(params *Params) (data interface{}, code int, err error) {
	return nil, http.StatusNotFound, fmt.Errorf(`method "%s" is not found for this case`, params.R.Method)
}

// NotImplemented --
func NotImplemented(params *Params) (data interface{}, code int, err error) {
	return nil, http.StatusNotImplemented, fmt.Errorf(`method "%s" is not implemented for this case`, params.R.Method)
}

// NotAllowed --
func NotAllowed(params *Params) (data interface{}, code int, err error) {
	return nil, http.StatusMethodNotAllowed, fmt.Errorf(`method "%s" is not allowed for this case`, params.R.Method)
}

//----------------------------------------------------------------------------------------------------------------------------//
