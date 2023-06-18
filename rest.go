package rest

import (
	"database/sql"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/alrusov/cache"
	"github.com/alrusov/db"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v3/path"
	"github.com/alrusov/stdhttp"
)

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

//----------------------------------------------------------------------------------------------------------------------------//

// Точка входа
func (proc *ProcOptions) rest() (result any, code int, err error) {
	switch proc.R.Method {
	default:
		return proc.Others()

	case stdhttp.MethodGET:
		return proc.Get()

	case stdhttp.MethodPOST:
		return proc.Create()

	case stdhttp.MethodPUT:
		return proc.Update()

	case stdhttp.MethodPATCH:
		return proc.Update()

	case stdhttp.MethodDELETE:
		return proc.Delete()
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

// Get -- получить данные
func (proc *ProcOptions) Get() (result any, code int, err error) {
	if proc.ChainLocal.CacheLifetime > 0 {
		var ce *cache.Elem
		ce, result, code = cache.Get(proc.ID, proc.Path, proc.R.RequestURI, proc.PathParams, proc.QueryParams)
		if ce == nil {
			return
		}

		defer func() {
			ce.Commit(proc.ID, result, code, proc.ChainLocal.CacheLifetime)
		}()
	}

	result, code, err = proc.before()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	f := proc.ChainLocal.Params.DBFields.AllDbSelect()
	fields := make([]string, len(f))
	copy(fields, f)

	compressionNeeded := false

	if len(proc.Fields) != 0 { // В стандартном случае должно быть 0 или 1
		src := proc.ChainLocal.Params.DBFields.AllDbNames()

		for i, name := range src {
			if _, exists := proc.Fields[0][name]; !exists {
				fields[i] = ""
				compressionNeeded = true
			}
		}
	}

	if proc.ExcludedFields != nil {
		src := proc.ChainLocal.Params.DBFields.AllDbNames()

		for i, name := range src {
			if fields[i] == "" {
				continue
			}

			if _, exists := proc.ExcludedFields[name]; exists {
				fields[i] = ""
				compressionNeeded = true
			}
		}
	}

	if compressionNeeded {
		dstI := 0

		for srcI, name := range fields {
			if fields[srcI] == "" {
				continue
			}

			if srcI != dstI {
				fields[dstI] = name
			}

			dstI++
		}

		if dstI != len(fields) {
			fields = fields[:dstI]
		}
	}

	if len(fields) == 0 {
		err = fmt.Errorf("empty fields list")
		code = http.StatusBadRequest
		return
	}

	proc.DBqueryVars = append(proc.DBqueryVars,
		db.Subst(db.SubstJbFields, proc.ChainLocal.Params.DBFields.JbFieldsStr()),
	)

	for {
		srcTp := proc.ChainLocal.Params.Response.SrcType
		if srcTp == nil {
			srcTp = proc.ChainLocal.Params.Response.Type
		}
		proc.DBqueryResult = reflect.New(reflect.SliceOf(srcTp)).Interface()

		err = db.Query(proc.Info.DBtype, proc.DBqueryResult, proc.DBqueryName, fields, proc.DBqueryVars)
		if err != nil {
			code = http.StatusInternalServerError
			return
		}

		result, code, err = proc.after()
		if err != nil {
			if code == 0 {
				code = http.StatusBadRequest
			}
			return
		}

		if code == StatusRetry {
			continue
		}

		if code != 0 || result != nil {
			return
		}

		break
	}

	if proc.ChainLocal.Params.Flags&path.FlagResponseIsNotArray != 0 {
		v := reflect.ValueOf(proc.DBqueryResult).Elem()
		if v.Len() == 0 {
			code = http.StatusNoContent
			return
		} else {
			result = v.Index(0).Interface()
		}
	} else {
		result = proc.DBqueryResult
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Create -- создать
func (proc *ProcOptions) Create() (result any, code int, err error) {
	return proc.save(false)
}

// Update -- изменить
func (proc *ProcOptions) Update() (result any, code int, err error) {
	return proc.save(true)
}

func (proc *ProcOptions) save(forUpdate bool) (result any, code int, err error) {
	res := &ExecResult{}
	defer func() {
		res.Notice = proc.Notices.String()
	}()

	if proc.ChainLocal.Params.Flags&path.FlagRequestDontMakeFlatModel == 0 {
		var msgs *misc.Messages
		proc.Fields, msgs, err = proc.ChainLocal.Params.ExtractFieldsFromBody(proc.RawBody)
		if msgs.Len() > 0 {
			Log.Message(log.DEBUG, "[%d] %s", proc.ID, msgs.String())
		}
		if err != nil {
			return
		}
	}

	result, code, err = proc.before()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}

	if code != 0 || result != nil {
		return
	}

	// Check fields
	if len(proc.Fields) > 0 {
		var lacks []string
		var requiredCounts misc.IntMap

		rqLn := len(proc.ChainLocal.Params.Request.RequiredFields)
		if rqLn > 0 {
			lacks = make([]string, 0, rqLn)
			requiredCounts = make(misc.IntMap, rqLn)

			ln := len(proc.Fields)
			for _, f := range proc.ChainLocal.Params.Request.RequiredFields {
				requiredCounts[f] = ln
			}
		}

		if forUpdate {
			// Update: check a key fields & empty required string fields

			if len(proc.Fields) > 1 {
				err = fmt.Errorf("%d records updated, expected 1", len(proc.Fields))
				return
			}

			proc.Fields = proc.Fields[0:1]
			fields := proc.Fields[0]

			for f := range requiredCounts {
				v, exists := fields[f]
				if !exists {
					continue
				}

				vv := reflect.ValueOf(v)
				if vv.Kind() == reflect.String && vv.IsZero() {
					lacks = append(lacks, f) // empty string for a required field
				}
			}

			if len(lacks) == 0 {
				for i, f := range proc.ChainLocal.Params.Request.UniqueKeyFields {
					if _, exists := fields[f]; exists {
						delete(fields, f)
						tp := ""
						if i == 0 {
							tp = "primary "
						}
						proc.Notices.Add(`%skey field "%s" ignored`, tp, f)
					}
				}
			}
		} else {
			// Insert: check a required fields
			if rqLn > 0 {
				for _, fields := range proc.Fields {
					for f, n := range requiredCounts {
						v, exists := fields[f]
						if !exists {
							continue
						}

						vv := reflect.ValueOf(v)
						if vv.Kind() == reflect.String && vv.IsZero() {
							continue
						}

						requiredCounts[f] = n - 1
					}
				}

				for f, n := range requiredCounts {
					if n != 0 {
						lacks = append(lacks, f)
					}
				}
			}
		}

		if len(lacks) != 0 {
			err = fmt.Errorf("empty fields: %s", strings.Join(lacks, ", "))
			return
		}
	}

	excluded := make([]string, 0, 16)

	if proc.ExcludedFields != nil {
		for i := range proc.Fields {
			for jName, dbName := range proc.ExcludedFields {
				if _, exists := proc.Fields[i][dbName]; exists {
					excluded = append(excluded, jName)
					delete(proc.Fields[i], dbName)
				}
			}
		}
	}

	if proc.ChainLocal.Params.Request.ReadonlyFields != nil {
		for i := range proc.Fields {
			for jName, dbName := range proc.ChainLocal.Params.Request.ReadonlyFields {
				if _, exists := proc.Fields[i][dbName]; exists {
					excluded = append(excluded, jName)
					delete(proc.Fields[i], dbName)
				}
			}
		}
	}

	if len(excluded) != 0 {
		proc.Notices.Add("readonly fields were ignored: %s", strings.Join(excluded, ", "))
	}

	if len(proc.Fields) == 0 || len(proc.Fields[0]) == 0 {
		proc.Notices.Add("no valid data to save")
		result = res
		return
	}

	jbPairs, fieldNames, fieldVals := proc.ChainLocal.Params.DBFields.Prepare(proc.Fields)
	if err != nil {
		code = http.StatusBadRequest
		return
	}

	// Собираем общие переменные

	commonVals := make([]any, 0, len(proc.DBqueryVars))

	for _, v := range proc.DBqueryVars {
		switch v.(type) {
		default:
			commonVals = append(commonVals, v)

		case *db.SubstArg:
		}
	}

	startIdx := len(commonVals) + 1

	// Добавляем общие поля в начало

	if len(commonVals) > 0 {
		for i := range fieldVals {
			fieldVals[i] = append(commonVals, fieldVals[i]...)
		}
	}

	// Сдвигаем индексы jb полей - они идут после общих переменных и обычных полей

	if len(jbPairs) > 0 {
		n := startIdx + len(fieldNames)
		for _, f := range jbPairs {
			f.Idx += n

			if forUpdate {
				if s, exists := f.FieldInfo.Tags["update"]; exists {
					f.Format = strings.ReplaceAll(s, "#", ",")
				}
			}
		}
	}

	// Добавляем описание jb полей

	proc.DBqueryVars = append(proc.DBqueryVars,
		db.Subst(db.SubstJbFields, jbPairs),
	)

	// Добавляем значения всех полей

	proc.DBqueryVars = append(proc.DBqueryVars, fieldVals)

	// Тип шаблона запроса

	patternType := db.PatternTypeInsert
	if forUpdate {
		patternType = db.PatternTypeUpdate
	}

	var returnsObj *[]ExecResultRow

	if !forUpdate && proc.ChainLocal.Params.Flags&path.FlagCreateReturnsObject != 0 {
		// Get result from INSERT ... RETURNING id
		returnsObj = &res.Rows
	}

	// Делаем запрос

	var stdExecResult sql.Result
	stdExecResult, err = db.ExecEx(proc.Info.DBtype, returnsObj, proc.DBqueryName, patternType, startIdx, fieldNames, proc.DBqueryVars)
	if err != nil {
		code = http.StatusInternalServerError
		return
	}

	if returnsObj != nil {
		res.AffectedRows = uint64(len(res.Rows))
	} else {
		n, err := stdExecResult.RowsAffected()
		if err != nil {
			Log.Message(log.NOTICE, "[%d] RowsAffected: %s", proc.ID, err)
		} else {
			res.AffectedRows = uint64(n)
		}

		if !forUpdate {
			lastID, err := stdExecResult.LastInsertId()
			if err != nil {
				Log.Message(log.NOTICE, "[%d] LastInsertId: %s", proc.ID, err)
			} else {
				res.Rows = []ExecResultRow{{ID: uint64(lastID)}}
			}
		}
	}

	proc.ExecResult = res

	result, code, err = proc.after()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	result = res

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Delete -- удалить
func (proc *ProcOptions) Delete() (result any, code int, err error) {
	result, code, err = proc.before()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	var execResult sql.Result
	execResult, err = db.Exec(proc.Info.DBtype, proc.DBqueryName, proc.DBqueryVars)
	if err != nil {
		code = http.StatusInternalServerError
		return
	}

	n, _ := execResult.RowsAffected()
	proc.ExecResult = &ExecResult{
		AffectedRows: uint64(n),
	}

	result, code, err = proc.after()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	result = proc.ExecResult

	return
}

// ----------------------------------------------------------------------------------------------------------------------------//

// Other -- другой запрос
func (proc *ProcOptions) Others() (result any, code int, err error) {
	result, code, err = proc.before()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	result, code, err = proc.after()
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	code, err = NotAllowed("")

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (info *Info) makeParamsDescription() (err error) {
	if info.Description != "" {
		return
	}

	for method, chains := range info.Methods.Methods {
		var d []string

		t := chains.StdParams.QueryParamsType
		if t != nil {
			if t.Kind() == reflect.Pointer {
				t = t.Elem()
			}

			if t.Kind() != reflect.Struct {
				err = fmt.Errorf("GetParam[%s] is not to a struct (%s)", method, t)
				return
			}

			ln := t.NumField()
			d = make([]string, 0, ln)

			for i := 0; i < ln; i++ {
				fieldT := t.Field(i)

				name := misc.StructTagName(&fieldT, path.TagJSON)
				if name == "-" {
					continue
				}

				sample := fieldT.Tag.Get(path.TagSample)
				if sample == "" {
					if fieldT.Type.Kind() == reflect.Bool {
						sample = name
					} else {
						val := fieldT.Tag.Get(path.TagDefault)
						if val == "" {
							val = "..."
						}
						sample = fmt.Sprintf("%s=%s", name, val)
					}
				}

				d = append(d, sample)
			}
		}

		if len(d) == 0 {
			d = []string{"-"}
		}

		chains.Description = fmt.Sprintf("%s. Параметры: %s", chains.Description, strings.Join(d, " & "))
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) before() (result any, code int, err error) {
	if proc.Info.Before != nil {
		result, code, err = proc.Info.Before(proc)
		if err != nil {
			if code == 0 {
				code = http.StatusBadRequest
			}
			return
		}
		if code != 0 || result != nil {
			return
		}
	}

	result, code, err = proc.handler.Before(proc)
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	return
}

func (proc *ProcOptions) after() (result any, code int, err error) {
	result, code, err = proc.handler.After(proc)
	if err != nil {
		if code == 0 {
			code = http.StatusBadRequest
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	if proc.Info.After != nil {
		result, code, err = proc.Info.After(proc)
		if err != nil {
			if code == 0 {
				code = http.StatusBadRequest
			}
			return
		}
		if code != 0 || result != nil {
			return
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//
