package api

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/alrusov/db"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
	"github.com/alrusov/rest/path"
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
func (proc *ProcOptions) rest() (headers misc.StringMap, result any, code int, err error) {
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
func (proc *ProcOptions) Get() (headers misc.StringMap, result any, code int, err error) {
	srcTp := proc.Chain.Parent.SrcResponseType
	if srcTp == nil {
		srcTp = proc.Chain.Parent.ResponseType
	}
	proc.DBqueryResult = reflect.New(reflect.MakeSlice(reflect.SliceOf(srcTp), 0, 0).Type()).Interface()

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

	err = db.Query(proc.Info.DBtype, proc.Info.DBidx, proc.DBqueryResult, proc.DBqueryName, proc.Chain.Parent.DBFields, proc.DBqueryVars)
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
func (proc *ProcOptions) Create() (headers misc.StringMap, result any, code int, err error) {
	return proc.save(false)
}

// Update -- изменить
func (proc *ProcOptions) Update() (headers misc.StringMap, result any, code int, err error) {
	return proc.save(true)
}

func (proc *ProcOptions) save(forUpdate bool) (headers misc.StringMap, result any, code int, err error) {
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

	res := &ExecResult{}
	defer func() {
		res.Notice = proc.Notices.String()
	}()

	if proc.Fields == nil && proc.Chain.Parent.Flags&path.FlagRequestDontMakeFlatModel == 0 {
		var notice error

		proc.Fields, notice, err = proc.Chain.Parent.ExtractFieldsFromBody(proc.RawBody)
		if err != nil {
			return
		}

		if notice != nil {
			proc.Notices.AddError(notice)
		}
	}

	if forUpdate {
		for i, f := range proc.Chain.Parent.RequestUniqueKeyFields {
			if _, exists := proc.Fields[f]; exists {
				delete(proc.Fields, f)
				tp := ""
				if i == 0 {
					tp = "primary "
				}
				proc.Notices.Add(`%skey field "%s" ignored`, tp, f)
			}
		}
	}

	if len(proc.Fields) == 0 {
		proc.Notices.Add("no fields")
		result = res
		return
	}

	proc.RequestBodyNames = make([]string, 0, len(proc.Fields))
	proc.RequestBodyVals = make([]any, 0, len(proc.Fields))
	for n, v := range proc.Fields {
		proc.RequestBodyNames = append(proc.RequestBodyNames, n)
		proc.RequestBodyVals = append(proc.RequestBodyVals, v)
	}

	startIdx := len(proc.DBqueryVars) + 1
	proc.DBqueryVars = append(proc.DBqueryVars, proc.RequestBodyVals...)

	patternType := db.PatternTypeInsert
	if forUpdate {
		patternType = db.PatternTypeUpdate
	}

	proc.ExecResult, err = db.ExecEx(proc.Info.DBtype, proc.Info.DBidx, proc.DBqueryName, patternType, startIdx, proc.RequestBodyNames, proc.DBqueryVars)
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

	res.AffectedRows, _ = proc.ExecResult.RowsAffected()
	result = res

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Delete -- удалить
func (proc *ProcOptions) Delete() (headers misc.StringMap, result any, code int, err error) {
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

	proc.ExecResult, err = db.Exec(proc.Info.DBtype, proc.Info.DBidx, proc.DBqueryName, proc.DBqueryVars)
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

	n, _ := proc.ExecResult.RowsAffected()
	result = ExecResult{
		AffectedRows: n,
	}

	return
}

// ----------------------------------------------------------------------------------------------------------------------------//

// Other -- другой запрос
func (proc *ProcOptions) Others() (headers misc.StringMap, result any, code int, err error) {
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

	stdDescr := "см. ./.info"

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

				sample, defExists := fieldT.Tag.Lookup(path.TagDefault)
				if !defExists || sample == "" {
					defExists = false
					sample = fieldT.Tag.Get(path.TagSample)
				}

				eq := ""

				if fieldT.Type.Kind() != reflect.Bool {
					if sample != "" {
						if !defExists {
							sample = "<" + sample + ">"
						}
					} else {
						sample = "<" + name + ">"
					}
				}

				if sample != "" {
					eq = "="
				}

				required := fieldT.Tag.Get(path.TagRequired)
				op := ""
				cp := ""
				if required != "true" {
					op = "["
					cp = "]"
				}

				d = append(d, fmt.Sprintf("%s%s%s%s%s", op, name, eq, sample, cp))
			}
		}

		if len(d) == 0 {
			d = []string{"-"}
		}

		mDef.Description = fmt.Sprintf("%s. Параметры: %s", mDef.Description, strings.Join(append(d, stdDescr), ", "))
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
