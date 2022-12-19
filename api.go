/*
Обработка прикладных HTTP запросов
*/
package rest

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/alrusov/jsonw"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v2/path"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

// Обработчик прикладных HTTP запросов
func Handler(h *stdhttp.HTTP, id uint64, prefix string, urlPath string, w http.ResponseWriter, r *http.Request) (basePath string, processed bool) {
	// Ищем обработчик
	module, basePath, tail, found := findModule(urlPath)
	if !found {
		return
	}

	processed = true

	proc := &ProcOptions{
		handler:      module.handler,
		LogFacility:  module.logFacility,
		H:            h,
		LogSrc:       fmt.Sprintf("%d", id),
		Info:         module.info,
		ID:           id,
		Prefix:       prefix,
		Path:         urlPath,
		R:            r,
		W:            w,
		Notices:      misc.NewMessages(),
		ExtraHeaders: make(misc.StringMap, 8),
	}

	var err error
	var result any
	var code int

	if r.Method == "" {
		r.Method = stdhttp.MethodPOST // Это ответ kAPI"
	}

	proc.Chain, proc.PathParams, result, code, err = module.info.Methods.Find(r.Method, tail)

	if err != nil || code != 0 || result != nil {
		proc.reply(code, result, err)
		return
	}

	proc.Scope = proc.Chain.Scope

	// Получаем тело запроса (в разжатом виде, если оно было gz)
	bodyBuf, code, err := stdhttp.ReadRequestBody(r)
	if err != nil {
		proc.reply(code, nil, err)
		return
	}

	r.Body.Close()
	r.Body = nil
	proc.RawBody = bodyBuf.Bytes()

	// Парсим тело
	if len(proc.RawBody) != 0 && proc.Chain.Parent.RequestPattern != nil {
		tp := proc.Chain.Parent.RequestType
		if proc.Chain.Parent.Flags&path.FlagRequestIsNotArray == 0 {
			tp = reflect.SliceOf(proc.Chain.Parent.RequestType)
		}
		proc.RequestParams = reflect.New(reflect.MakeSlice(tp, 0, 0).Type()).Interface()

		switch proc.Chain.Parent.RequestContentType {
		case stdhttp.ContentTypeJSON:
			err = jsonw.Unmarshal(proc.RawBody, &proc.RequestParams)
			if err != nil {
				proc.reply(code, result, err)
				return
			}
		}
	}

	// Парсим query параметры
	err = proc.parseQueryParams(r.URL.Query())
	if err != nil {
		proc.reply(code, result, err)
		return
	}

	// По умолчанию так. Если гдe надо иначе - можно там менять
	proc.DBqueryName = proc.Info.QueryPrefix + proc.Chain.Scope

	// Вызываем обработчик
	result, code, err = proc.rest()

	if err != nil {
		proc.reply(code, result, err)
		return
	}

	if code == StatusProcessed {
		module.logFacility.Message(log.TRACE3, "[%d] Answer already sent, do nothing", id)
		return
	}

	proc.reply(code, result, err)

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Поиск обработчкика для запроса по его URL
func findModule(path string) (module *module, basePath string, extraPath []string, found bool) {
	found = false

	p := path
	n := 0

	for {
		modulesMutex.RLock()
		module, found = modules[p]
		modulesMutex.RUnlock()

		if found {
			basePath = p
			if n > 0 {
				tail := path[len(p)+1:]

				extraPath = strings.Split(strings.Trim(tail, "/"), "/")
				if len(extraPath) == 1 && extraPath[0] == "" {
					extraPath = []string{}
				} else {
					for i, p := range extraPath {
						extraPath[i], _ = url.PathUnescape(p)
					}
				}
			}

			return
		}

		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return
		}

		p = p[:i]
		if p == "" {
			return
		}
		n++
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) reply(code int, result any, err error) {
	readyAnswer := false

	switch code {
	default:
		readyAnswer = code < 0
		if code < 0 {
			code = -code
		}

	case 0:
		if result != nil {
			v := reflect.ValueOf(result)
			k := v.Kind()
			if k == reflect.Pointer {
				v = v.Elem()
				k = v.Kind()
			}
			if k == reflect.Slice && v.IsNil() {
				result = nil
			}
		}

		if err != nil {
			code = http.StatusInternalServerError
		} else if result == nil {
			code = http.StatusNoContent
		} else {
			code = http.StatusOK
		}

	case http.StatusNotImplemented:
		if err == nil {
			code, err = NotImplemented("")
		}

	case http.StatusMethodNotAllowed:
		if err == nil {
			code, err = NotAllowed("")
		}

	case http.StatusNotFound:
		if err == nil {
			code, err = NotFound("")
		}
	}

	if err != nil {
		stdhttp.Error(proc.ID, false, proc.W, proc.R, code, err.Error(), nil)
		return
	}

	var data []byte
	contentType := stdhttp.ContentTypeJSON
	if proc.Chain != nil && proc.Chain.Parent.ResponseContentType != "" {
		contentType = proc.Chain.Parent.ResponseContentType
	}

	if readyAnswer {
		var ok bool
		data, ok = result.([]byte)
		if !ok {
			msg := fmt.Sprintf("resilt is %T, expected %T", result, data)
			stdhttp.Error(proc.ID, false, proc.W, proc.R, code, msg, nil)
			return
		}

	} else {
		if result == nil {
			result = struct{}{}
		}

		if code == http.StatusNoContent {
			// nothing to do
			contentType = stdhttp.ContentTypeText

		} else {
			switch contentType {
			default:
				var ok bool
				data, ok = result.([]byte)
				if !ok {
					msg := fmt.Sprintf("resilt is %T, expected %T", result, data)
					stdhttp.Error(proc.ID, false, proc.W, proc.R, code, msg, nil)
					return
				}

			case stdhttp.ContentTypeJSON:
				withHash := proc.Chain != nil && proc.Chain.Parent.Flags&path.FlagResponseHashed != 0
				hash := ""
				if withHash {
					hash = proc.R.URL.Query().Get("hash")
				}

				var code2 int
				data, code2, proc.ExtraHeaders, err = stdhttp.JSONResultWithDataHash(result, withHash && code/100 == 2, hash, proc.ExtraHeaders)
				if code2 != http.StatusOK {
					code = code2
				}
				if err != nil {
					stdhttp.Error(proc.ID, false, proc.W, proc.R, code, err.Error(), nil)
					return
				}
			}
		}
	}

	proc.LogFacility.Message(log.TRACE3, `[%d] WriteReply: %d (%s)`, proc.ID, code, contentType)

	stdhttp.WriteReply(proc.W, proc.R, code, contentType, proc.ExtraHeaders, data)
	if err != nil {
		proc.LogFacility.Message(log.NOTICE, "[%d] WriteReply error: %s", proc.ID, err)
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

// Преобразование query параметров в структуру API соответствующего метода
func (proc *ProcOptions) parseQueryParams(src url.Values) (err error) {
	if proc.Chain.Parent.QueryParamsType == nil {
		return
	}

	proc.QueryParams = reflect.New(proc.Chain.Parent.QueryParamsType).Interface()
	paramsT := proc.Chain.Parent.QueryParamsType
	paramsV := reflect.ValueOf(proc.QueryParams).Elem()

	nameMap := misc.StringMap{}
	ln := paramsT.NumField()
	for i := 0; i < ln; i++ {
		fieldT := paramsT.Field(i)

		// Имя поля для JSON
		name := fieldT.Name
		fieldName := misc.StructFieldName(&fieldT, path.TagJSON)
		if fieldName == "-" {
			continue
		}

		// Значение по умолчанию
		defVal := fieldT.Tag.Get(path.TagDefault)
		if defVal != "" {
			if err := convert(defVal, paramsV.Field(i), fieldT.Type); err != nil {
				proc.LogFacility.Message(log.DEBUG, "[%d] parseQueryParams: %s", proc.ID, err)
			}
		}

		nameMap[fieldName] = name
	}

	msgs := misc.NewMessages()

	for srcName, val := range src {
		name, exists := nameMap[srcName]
		if !exists {
			if proc.Info.Flags&FlagLogUnknownParams != 0 {
				msgs.Add(`[%d] Unknown query parameter %s="%v"`, proc.ID, srcName, val)
			}
			continue
		}

		ln := len(val)
		field := paramsV.FieldByName(name)
		fieldT := field.Type()

		if field.Kind() == reflect.Slice {
			slice := reflect.MakeSlice(field.Type(), ln, ln)
			fieldT = field.Type()
			field.Set(slice)

			for i := 0; i < ln; i++ {
				err := convert(val[i], slice.Index(i), fieldT)
				if err != nil {
					msgs.Add(`[%d] query parameter "%s"[%d]: %s`, proc.ID, name, i, err)
				}
			}
		} else {
			if ln > 1 {
				val[0] = strings.Join(val, ",")
			}

			err := convert(val[0], field, fieldT)
			if err != nil {
				msgs.Add(`[%d] query parameter "%s": %s`, proc.ID, name, err)
			}
		}
	}

	err = msgs.Error()
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Преобразование query параметров к требующемуся типу
func convert(s string, field reflect.Value, fieldT reflect.Type) (err error) {
	s = strings.TrimSpace(s)

	switch fieldT.Kind() {
	case reflect.String:
		field.SetString(s)
		return

	case reflect.Bool:
		s = strings.ToLower(s)
		b := true
		switch s {
		case "0", "n", "no", "f", "false":
			b = false
		}
		field.SetBool(b)
		return

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var x int64
		x, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf(`convertion error from "%s" to int (%s)`, s, err)
		}
		field.SetInt(x)
		return

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var x uint64
		x, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			return fmt.Errorf(`convertion error from "%s" to uint (%s)`, s, err)
		}
		field.SetUint(x)
		return

	case reflect.Float32, reflect.Float64:
		var x float64
		x, err = strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf(`convertion error from "%s" to float (%s)`, s, err)
		}
		field.SetFloat(x)
		return

	case reflect.Struct:
		switch fieldT {
		case reflect.TypeOf(time.Time{}):
			var x time.Time
			x, err = ParseTime(s)
			if err != nil {
				return fmt.Errorf(`convertion error from "%s" to time (%s)`, s, err)
			}
			field.Set(reflect.ValueOf(x.UTC()))
			return
		}
		return
	}

	err = fmt.Errorf(`has an illegal type %v`, fieldT)
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func CheckPeriod(from time.Time, to time.Time, maxPeriod time.Duration, stdPeriod time.Duration) (normFrom time.Time, normTo time.Time, code int, err error) {
	if to.IsZero() {
		if from.IsZero() {
			to = misc.NowUTC()
		} else {
			to = from.Add(stdPeriod)
		}
	}

	if from.IsZero() {
		from = to.Add(-stdPeriod)
	} else {
		if !from.Before(to) {
			code, err = BadRequest(`%s(%s) должно быть меньше %s(%s)`, ParamPeriodFrom, from, ParamPeriodTo, to)
			return
		}

		if from.Add(maxPeriod).Before(to) {
			code, err = BadRequest(`период между %s(%s) и  %s(%s) не должен превышать %d секунд`, ParamPeriodFrom, from, ParamPeriodTo, to, maxPeriod/time.Second)
			return
		}
	}

	normFrom = from
	normTo = to
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
