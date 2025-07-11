/*
Обработка прикладных HTTP запросов
*/
package rest

import (
	"bytes"
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
	path "github.com/alrusov/rest/v4/path"
	"github.com/alrusov/stdhttp"
)

type (
	FindModule func(path string) (module *Module, basePath string, extraPath []string, found bool)
)

//----------------------------------------------------------------------------------------------------------------------------//

// Обработчик прикладных HTTP запросов
func Handler(h *stdhttp.HTTP, id uint64, prefix string, urlPath string, w http.ResponseWriter, r *http.Request) (basePath string, processed bool) {
	return HandlerEx(findModule, nil, h, id, prefix, urlPath, w, r)
}

func HandlerEx(find FindModule, extra any, h *stdhttp.HTTP, id uint64, prefix string, urlPath string, w http.ResponseWriter, r *http.Request) (basePath string, processed bool) {
	// Ищем обработчик
	module, basePath, tail, found := find(urlPath)
	if !found {
		return
	}

	processed = true

	proc := &ProcOptions{
		handler:      module.Handler,
		LogFacility:  module.LogFacility,
		H:            h,
		LogSrc:       fmt.Sprintf("%d", id),
		Info:         module.Info,
		ID:           id,
		Prefix:       prefix,
		Path:         urlPath,
		Tail:         tail,
		R:            r,
		W:            w,
		Extra:        extra,
		ExtraHeaders: make(misc.StringMap, 8),
	}

	var err error
	var result any
	var code int

	proc.AuthIdentity, err = stdhttp.GetIdentityFromRequestContext(r)
	if err != nil {
		proc.reply(result, code, err)
		return
	}

	if r.Method == "" {
		r.Method = stdhttp.MethodPOST // Это ответ kAPI"
	}

	proc.Chain, proc.PathParams, result, code, err = module.Info.Methods.Find(r.Method, tail)

	if err != nil || code != 0 || !misc.IsNil(result) {
		proc.reply(result, code, err)
		return
	}

	// Копия Chain для возможности ее модификации для работы с динамическими объектами. Рекомендуется использовать её, а не Chain.Parent
	proc.ChainLocal = *proc.Chain

	proc.Scope = proc.Chain.Scope

	if proc.ChainLocal.Params.Flags&path.FlagDontReadBody == 0 {
		bodyBuf := new(bytes.Buffer)
		_, err = bodyBuf.ReadFrom(r.Body)
		if err != nil {
			proc.reply(nil, code, err)
			return
		}

		r.Body = nil
		proc.RawBody = bodyBuf.Bytes()

		// Парсим тело
		requestObject := proc.ChainLocal.Params.Request

		if len(proc.RawBody) != 0 && requestObject.Pattern != nil {
			proc.RawBody = bytes.TrimSpace(proc.RawBody)
			proc.RequestParams = reflect.New(
				reflect.SliceOf(requestObject.Type),
			).Interface()

			switch requestObject.ContentType {
			case stdhttp.ContentTypeJSON:
				if len(proc.RawBody) > 0 && proc.RawBody[0] != '[' {
					proc.RawBody = bytes.Join([][]byte{{'['}, proc.RawBody, {']'}}, []byte{})
				}

				err = jsonw.Unmarshal(proc.RawBody, &proc.RequestParams)
				if err != nil {
					code = http.StatusBadRequest
					proc.reply(result, code, err)
					Log.Message(log.ERR, "%s\n%v\n%s", err, proc.R.Header, proc.RawBody)
					return
				}
			}
		}
	}

	// Парсим query параметры
	err = proc.parseQueryParams(r.URL.Query())
	if err != nil {
		code = http.StatusBadRequest
		proc.reply(result, code, err)
		return
	}

	// По умолчанию так. Если гдe надо иначе - можно менять в Before
	proc.DBqueryName = proc.Info.QueryPrefix + proc.Chain.Scope

	// Вызываем обработчик
	result, code, err = proc.rest()

	if code == StatusProcessed {
		module.LogFacility.Message(log.TRACE3, "[%d] Answer already sent, do nothing", id)
		return
	}

	proc.reply(result, code, err)
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Поиск обработчкика для запроса по его URL
func findModule(path string) (module *Module, basePath string, extraPath []string, found bool) {
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

func (proc *ProcOptions) reply(result any, code int, err error) {
	if proc.Info.ResultTuner != nil {
		result, code, err = proc.Info.ResultTuner(proc, result, code, err)
	}

	switch r := result.(type) {
	case *ExecResult:
		r.FillMessages()
	}

	readyAnswer := false

	switch code {
	default:
		readyAnswer = code < 0
		if code < 0 {
			code = -code
		}

	case 0:
		if !misc.IsNil(result) {
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
		} else if misc.IsNil(result) {
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
	contentType := proc.httpContentType()

	if readyAnswer {
		var ok bool
		data, ok = result.([]byte)
		if !ok {
			msg := fmt.Sprintf("result is %T, expected %T", result, data)
			stdhttp.Error(proc.ID, false, proc.W, proc.R, code, msg, nil)
			return
		}

	} else {
		if misc.IsNil(result) {
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
				withHash := proc.Chain != nil && proc.ChainLocal.Params.Flags&path.FlagResponseHashed != 0
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

func (proc *ProcOptions) httpContentType() (tp string) {
	tp = stdhttp.ContentTypeJSON
	if proc.Chain != nil && proc.ChainLocal.Params.Response.ContentType != "" {
		tp = proc.ChainLocal.Params.Response.ContentType
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) responseSouceType() (tp reflect.Type) {
	tp = proc.ChainLocal.Params.Response.SrcType
	if tp == nil {
		tp = proc.ChainLocal.Params.Response.Type
	}
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Преобразование query параметров в структуру API соответствующего метода
func (proc *ProcOptions) parseQueryParams(src url.Values) (err error) {
	if proc.ChainLocal.Params.QueryParamsType == nil {
		return
	}

	proc.QueryParamsFound = make(misc.BoolMap, len(src))

	proc.QueryParams = reflect.New(proc.ChainLocal.Params.QueryParamsType).Interface()
	paramsT := proc.ChainLocal.Params.QueryParamsType
	paramsV := reflect.ValueOf(proc.QueryParams).Elem()

	nameMap := make(misc.StringMap, paramsT.NumField())

	scanQueryParams(proc, paramsT, paramsV, nameMap)

	msgs := misc.NewMessages()

	for srcName, val := range src {
		name, exists := nameMap[srcName]
		if !exists {
			if proc.Info.Flags&FlagLogUnknownParams != 0 {
				msgs.Add(`[%d] Unknown query parameter %s="%v"`, proc.ID, srcName, val)
			}
			continue
		}

		proc.QueryParamsFound[name] = true

		ln := len(val)
		field := paramsV.FieldByName(name)

		if field.Kind() == reflect.Slice {
			slice := reflect.MakeSlice(field.Type(), ln, ln)
			field.Set(slice)

			for i := 0; i < ln; i++ {
				err := convert(val[i], slice.Index(i))
				if err != nil {
					msgs.Add(`[%d] query parameter "%s"[%d]: %s`, proc.ID, name, i, err)
				}
			}
		} else {
			if ln > 1 {
				val[0] = strings.Join(val, ",")
			}

			err := convert(val[0], field)
			if err != nil {
				msgs.Add(`[%d] query parameter "%s": %s`, proc.ID, name, err)
			}
		}
	}

	err = msgs.Error()
	return
}

func scanQueryParams(proc *ProcOptions, t reflect.Type, v reflect.Value, nameMap misc.StringMap) {
	ln := t.NumField()
	for i := 0; i < ln; i++ {
		fieldT := t.Field(i)

		if !fieldT.IsExported() {
			continue
		}

		if misc.StructTagName(&fieldT, path.TagSkip) == "true" {
			continue
		}

		if fieldT.Type.Kind() == reflect.Struct && fieldT.Anonymous {
			scanQueryParams(proc, fieldT.Type, v.Field(i), nameMap)
			continue
		}

		// Имя поля для JSON
		name := fieldT.Name
		fieldName := misc.StructTagName(&fieldT, path.TagJSON)
		if fieldName == "-" {
			continue
		}

		// Значение по умолчанию
		defVal := fieldT.Tag.Get(path.TagDefault)
		if defVal != "" && defVal != path.DefaultValueNull {
			if err := convert(defVal, v.Field(i)); err != nil {
				proc.LogFacility.Message(log.DEBUG, "[%d] parseQueryParams: %s", proc.ID, err)
			}
		}

		nameMap[fieldName] = name
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

// Преобразование query параметров к требующемуся типу
func convert(s string, field reflect.Value) (err error) {
	s = strings.TrimSpace(s)

	switch field.Kind() {
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

		switch field.Interface().(type) {
		default:
			x, err = strconv.ParseInt(s, 10, 64)

		case time.Duration:
			x, err = misc.Interval2Int64(s)
		}

		if err != nil {
			return
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
		switch field.Interface().(type) {
		case time.Time:
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

	err = fmt.Errorf(`has an illegal type %T`, field.Interface())
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
			code, err = BadRequest(`%s(%s) must be less than %s(%s)`, ParamPeriodFrom, from, ParamPeriodTo, to)
			return
		}

		if from.Add(maxPeriod).Before(to) {
			code, err = BadRequest(`the period between %s(%s) and %s(%s) must not exceed %d seconds`, ParamPeriodFrom, from, ParamPeriodTo, to, maxPeriod/time.Second)
			return
		}
	}

	normFrom = from
	normTo = to
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
