package rest

import (
	"database/sql"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/alrusov/db"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v2/path"
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
	srcTp := proc.Chain.Parent.SrcResponseType
	if srcTp == nil {
		srcTp = proc.Chain.Parent.ResponseType
	}
	proc.DBqueryResult = reflect.New(reflect.SliceOf(srcTp)).Interface()

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

	f := proc.Chain.Parent.DBFields.All()
	fields := make([]string, len(f))
	copy(fields, f)

	compressionNeeded := false

	if len(proc.Fields) != 0 { // В стардартном случае должно быть 0 или 1
		src := proc.Chain.Parent.DBFields.AllSrc()

		for i, name := range src {
			if _, exists := proc.Fields[0][name]; !exists {
				fields[i] = ""
				compressionNeeded = true
			}
		}
	}

	if proc.ExcludedFields != nil {
		src := proc.Chain.Parent.DBFields.AllSrc()

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
		db.Subst(db.SubstJbFields, proc.Chain.Parent.DBFields.JbSelectStr()),
	)

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
	if code != 0 || result != nil {
		return
	}

	if proc.Chain.Parent.Flags&path.FlagResponseIsNotArray != 0 {
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

	if proc.Chain.Parent.Flags&path.FlagRequestDontMakeFlatModel == 0 {
		proc.Fields, err = proc.Chain.Parent.ExtractFieldsFromBody(proc.RawBody)
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

	if forUpdate && len(proc.Fields) > 0 {
		proc.Fields = proc.Fields[0:1] // Для Update должен быть только один блок

		for i, f := range proc.Chain.Parent.RequestUniqueKeyFields {
			if _, exists := proc.Fields[0][f]; exists {
				delete(proc.Fields[0], f)
				tp := ""
				if i == 0 {
					tp = "primary "
				}
				proc.Notices.Add(`%skey field "%s" ignored`, tp, f)
			}
		}
	}

	if proc.ExcludedFields != nil {
		for i := range proc.Fields {
			for name := range proc.ExcludedFields {
				delete(proc.Fields[i], name)
			}
		}
	}

	if len(proc.Fields) == 0 || len(proc.Fields[0]) == 0 {
		proc.Notices.Add("no fields")
		result = res
		return
	}

	jbPairs, fieldNames, fieldVals := proc.Chain.Parent.DBFields.Prepare(proc.Fields)
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

	if !forUpdate && proc.Chain.Parent.Flags&path.FlagCreateReturnsObject != 0 {
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

	for method, mDef := range info.Methods.Methods {
		var d []string

		t := mDef.QueryParamsType
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

				name := misc.StructFieldName(&fieldT, path.TagJSON)
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

		mDef.Description = fmt.Sprintf("%s. Параметры: %s", mDef.Description, strings.Join(d, " & "))
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
