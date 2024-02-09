package rest

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/alrusov/cache"
	"github.com/alrusov/db"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v4/path"
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

type (
	shaper struct {
		cond *sync.Cond
		*ProcOptions
		result any
		code   int
		err    error
	}
)

var (
	maxShaperLen = 32 // proc.Info.shaperQueueLen > maxShaperLen is without limitation
)

//----------------------------------------------------------------------------------------------------------------------------//

func SetMaxShaperLen(n int) {
	if n < 0 {
		n = 0
	}

	maxShaperLen = n
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) rest() (result any, code int, err error) {
	if proc.Info.shaperQueueLen == 0 {
		return proc.do()
	}

	s := &shaper{
		cond:        sync.NewCond(&sync.Mutex{}),
		ProcOptions: proc,
	}

	proc.Info.shaperQueue <- s

	s.cond.L.Lock()
	s.cond.Wait()
	s.cond.L.Unlock()

	result = s.result
	code = s.code
	err = s.err
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (info *Info) ShaperWorkers(n int) (err error) {
	if info.shaperQueueLen != 0 {
		err = fmt.Errorf("shaper already initialized")
		return
	}

	if n < 0 || n > maxShaperLen {
		n = 0
	}

	if n == 0 {
		return
	}

	info.shaperQueueLen = n
	info.shaperQueue = make(chan *shaper, n)

	for i := 0; i < n; i++ {
		go func() {
			for {
				s, ok := <-info.shaperQueue
				if !ok {
					// chan closed
					return
				}

				s.result, s.code, s.err = s.do()

				s.cond.L.Lock()
				s.cond.Signal()
				s.cond.L.Unlock()
			}
		}()
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Обработка
func (proc *ProcOptions) do() (result any, code int, err error) {
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

type (
	cachedData struct {
		headers misc.StringMap
		result  any
	}
)

// Get -- получить данные
func (proc *ProcOptions) Get() (result any, code int, err error) {
	if proc.ChainLocal.CacheLifetime > 0 {
		ce, res, resCode := cache.Get(proc.ID, proc.Path, proc.R.RequestURI, proc.PathParams, proc.QueryParams)
		if ce == nil {
			cd, ok := res.(cachedData)
			if !ok {
				code, err = InternalServerError("Illegal cached data: got %T, expected %T", res, cd)
				return
			}

			result = cd.result
			proc.ExtraHeaders = cd.headers
			code = resCode
			return
		}

		defer func() {
			cd := cachedData{
				headers: proc.ExtraHeaders,
				result:  result,
			}
			ce.Commit(proc.ID, cd, code, proc.ChainLocal.CacheLifetime)
		}()
	}

	result, code, err = proc.before()
	if err != nil {
		if code == 0 {
			code = http.StatusUnprocessableEntity
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	f := proc.ChainLocal.Params.DBFields.AllDbSelect()
	fields := make([]string, len(f))
	copy(fields, f)

	withSkippedFields := false

	if len(proc.Fields) != 0 { // В стандартном случае должно быть 0 или 1
		src := proc.ChainLocal.Params.DBFields.AllDbNames()

		for i, name := range src {
			if _, exists := proc.Fields[0][name]; !exists {
				fields[i] = ""
				withSkippedFields = true
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
				withSkippedFields = true
			}
		}
	}

	if withSkippedFields {
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
		code = http.StatusUnprocessableEntity
		return
	}

	proc.DBqueryVars = append(proc.DBqueryVars,
		db.Subst(db.SubstJbFields, proc.ChainLocal.Params.DBFields.JbFieldsStr()),
	)

	for {
		if proc.ResultAsRows {
			err = db.Query(proc.Info.DBtype, &proc.DBqueryRows, proc.DBqueryName, fields, proc.DBqueryVars)
		} else {
			srcTp := proc.responseSouceType()
			proc.DBqueryResult = reflect.New(reflect.SliceOf(srcTp)).Interface()

			err = db.Query(proc.Info.DBtype, proc.DBqueryResult, proc.DBqueryName, fields, proc.DBqueryVars)
		}

		if err != nil {
			code = http.StatusInternalServerError
			return
		}

		result, code, err = proc.after()
		if err != nil {
			if code == 0 {
				code = http.StatusUnprocessableEntity
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
	execResult := NewExecResult()

	defer func() {
		execResult.MultiDefer(&result, &code, &err)
	}()

	err = proc.prepareFields(execResult)
	if err != nil {
		code = http.StatusBadRequest
		return
	}

	result, code, err = proc.before()
	if code != 0 || result != nil || err != nil {
		return
	}

	ok := proc.checkFields(forUpdate, execResult)
	if !ok {
		return
	}

	startIdx, fieldNames := proc.makeQueryVars(forUpdate)

	// Тип шаблона запроса

	patternType := db.PatternTypeInsert
	if forUpdate {
		patternType = db.PatternTypeUpdate
	}

	var returnsObj *[]*ExecResultRow

	if proc.ChainLocal.Params.Flags&path.FlagUDqueriesReturnsID != 0 {
		// Get result from queries like a
		// INSERT ... RETURNING id, guid
		// UPDATE ... RETURNING id, guid
		requestRes := NewExecResult()
		returnsObj = &requestRes.Rows
	}

	// Делаем запрос

	var stdExecResult *db.Result
	stdExecResult, err = db.ExecEx(proc.Info.DBtype, returnsObj, proc.DBqueryName, patternType, startIdx, fieldNames, proc.DBqueryVars)
	if err != nil {
		code = http.StatusInternalServerError
		return
	}

	execResult.TotalRows = uint64(len(proc.Fields))

	err = execResult.DbResultParser(stdExecResult, returnsObj)
	if err != nil {
		code = http.StatusInternalServerError
		return
	}

	proc.ExecResult = execResult

	result, code, err = proc.after()
	if code != 0 || result != nil || err != nil {
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) prepareFields(execResult *ExecResult) (err error) {
	if proc.ChainLocal.Params.Flags&path.FlagRequestDontMakeFlatModel != 0 {
		return
	}

	var allMessages [][]string
	proc.Fields, allMessages, err = proc.ChainLocal.Params.ExtractFieldsFromBody(proc.RawBody)
	if err != nil {
		return
	}

	for i := range proc.Fields {
		r := NewExecResultRow()
		r.AddMessages(allMessages[i])
		execResult.AddRow(r)
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) checkFields(forUpdate bool, execResult *ExecResult) (success bool) {
	success = true

	if len(proc.Fields) == 0 {
		r := NewExecResultRow()
		r.AddMessage("no data for save found")
		execResult.AddRow(r)

		success = false
		return
	}

	if len(execResult.Rows) == 0 {
		for range proc.Fields {
			r := NewExecResultRow()
			execResult.AddRow(r)
		}
	}

	if forUpdate && len(proc.Fields) > 1 {
		execResult.Rows[0].AddMessage("%d records updated, expected 1", len(proc.Fields))
		success = false
		return
	}

	fieldsInfo := proc.ChainLocal.Params.DBFields.ByDbName()

	fieldName := func(dbName string) (name string) {
		name = dbName
		fi, exists := fieldsInfo[dbName]
		if exists {
			name = fi.JsonName
		}

		return
	}

	for i, fields := range proc.Fields {
		// check for required fields
		for _, dbName := range proc.ChainLocal.Params.Request.RequiredFields {
			if v, exists := fields[dbName]; !exists {
				if forUpdate {
					continue
				}
			} else {
				vv := reflect.ValueOf(v)
				if vv.Kind() != reflect.String || !vv.IsZero() {
					continue
				}
			}

			execResult.Rows[i].AddMessage(`mandatory field "%s" is not found`, fieldName(dbName))
			success = false
		}

		if forUpdate {
			// check for key fields
			for i, dbName := range proc.ChainLocal.Params.Request.UniqueKeyFields {
				if _, exists := fields[dbName]; !exists {
					continue
				}

				delete(fields, dbName)
				tp := ""
				if i == 0 {
					tp = "primary "
				}
				execResult.Rows[i].AddMessage(`%skey field "%s" ignored`, tp, fieldName(dbName))
			}
		}

		// check for excluded fields
		for _, dbName := range proc.ExcludedFields {
			if _, exists := fields[dbName]; !exists {
				continue
			}

			delete(fields, dbName)
			execResult.Rows[i].AddMessage(`excluded field "%s" ignored`, fieldName(dbName))
		}

		// check for readonly fields
		for _, dbName := range proc.ChainLocal.Params.Request.ReadonlyFields {
			if _, exists := fields[dbName]; !exists {
				continue
			}

			delete(fields, dbName)
			execResult.Rows[i].AddMessage(`readonly field "%s" ignored`, fieldName(dbName))
		}
	}

	totalFields := 0
	for _, fields := range proc.Fields {
		totalFields += len(fields)
	}

	if totalFields == 0 {
		for i := range proc.Fields {
			execResult.Rows[i].AddMessage("no data for save found")
		}

		success = false
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) makeQueryVars(forUpdate bool) (startIdx int, fieldNames []string) {
	jbPairs, fieldNames, fieldVals := proc.ChainLocal.Params.DBFields.Prepare(proc.Fields)

	// Собираем общие переменные

	commonVals := make([]any, 0, len(proc.DBqueryVars))

	for _, v := range proc.DBqueryVars {
		switch v.(type) {
		default:
			commonVals = append(commonVals, v)

		case *db.SubstArg:
		}
	}

	startIdx = len(commonVals) + 1

	// Добавляем общие поля в начало

	if len(commonVals) > 0 {
		for i, fv := range fieldVals {
			fieldVals[i] = append(slices.Clone(commonVals), fv...)
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
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Delete -- удалить
func (proc *ProcOptions) Delete() (result any, code int, err error) {
	execResult := NewExecResult()
	execResult.TotalRows = 1
	resultRow := NewExecResultRow()
	execResult.AddRow(resultRow)

	defer func() {
		execResult.MultiDefer(&result, &code, &err)
	}()

	result, code, err = proc.before()
	if code != 0 || result != nil || err != nil {
		return
	}

	var returnsObj *[]*ExecResultRow

	if proc.ChainLocal.Params.Flags&path.FlagUDqueriesReturnsID != 0 {
		// Get result from queries like a
		// DELETE ... RETURNING id, guid
		requestRes := NewExecResult()
		returnsObj = &requestRes.Rows
	}

	var stdExecResult *db.Result

	// Делаем запрос

	stdExecResult, err = db.ExecEx(proc.Info.DBtype, returnsObj, proc.DBqueryName, db.PatternTypeNone, 0, nil, proc.DBqueryVars)
	if err != nil {
		code = http.StatusInternalServerError
		return
	}

	err = execResult.DbResultParser(stdExecResult, returnsObj)
	if err != nil {
		code = http.StatusInternalServerError
		return
	}

	proc.ExecResult = execResult

	result, code, err = proc.after()
	if code != 0 || result != nil || err != nil {
		return
	}

	return
}

// ----------------------------------------------------------------------------------------------------------------------------//

// Other -- другой запрос
func (proc *ProcOptions) Others() (result any, code int, err error) {
	result, code, err = proc.before()
	if err != nil {
		if code == 0 {
			code = http.StatusUnprocessableEntity
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	result, code, err = proc.after()
	if err != nil {
		if code == 0 {
			code = http.StatusUnprocessableEntity
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

		chains.ParamsDescription = strings.Join(d, " & ")
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) before() (result any, code int, err error) {
	if proc.Info.Before != nil {
		result, code, err = proc.Info.Before(proc)
		if err != nil {
			if code == 0 {
				code = http.StatusUnprocessableEntity
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
			code = http.StatusUnprocessableEntity
		}
		return
	}
	if code != 0 || result != nil {
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *ProcOptions) after() (result any, code int, err error) {
	result, code, err = proc.handler.After(proc)
	if err != nil {
		if code == 0 {
			code = http.StatusUnprocessableEntity
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
				code = http.StatusUnprocessableEntity
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

func (execResult *ExecResult) MultiDefer(pResult *any, pCode *int, pErr *error) {
	if *pResult != nil || *pCode != 0 {
		return
	}

	if *pErr != nil {
		if len(execResult.Rows) == 0 {
			r := NewExecResultRow()
			execResult.AddRow(r)
		}

		for _, r := range execResult.Rows {
			r.Code = http.StatusUnprocessableEntity
			r.AddError(*pErr)
		}

		execResult.TotalRows = uint64(len(execResult.Rows))
		execResult.FailedRows = execResult.TotalRows
	}

	*pResult = execResult
	*pCode = http.StatusMultiStatus
}

//----------------------------------------------------------------------------------------------------------------------------//

func (execResult *ExecResult) DbResultParser(stdExecResult *db.Result, returnsObj *[]*ExecResultRow) (err error) {
	if stdExecResult.HasError() {
		for i, e := range stdExecResult.Errors() {
			if i > len(execResult.Rows) {
				execResult.AddRow(NewExecResultRow())
			}

			if e == nil {
				continue
			}

			r := execResult.Rows[i]
			r.Code = http.StatusInternalServerError
			r.AddError(e)
		}
	}

	if returnsObj != nil {
		for i, src := range *returnsObj {
			if i == len(execResult.Rows) {
				execResult.AddRow(NewExecResultRow())
			}

			r := execResult.Rows[i]

			if src == nil {
				r.Code = http.StatusNotFound
				continue
			}

			if src.ID == 0 {
				r.Code = http.StatusNotFound
			} else {
				r.ID = src.ID
				r.GUID = src.GUID
				r.Code = http.StatusOK
				execResult.SuccessRows++
			}
		}
	} else {
		var n int64
		n, err = stdExecResult.RowsAffected()
		if err != nil {
			err = fmt.Errorf("RowsAffected: %s", err)
			return
		}

		execResult.SuccessRows = uint64(n)
		c := http.StatusOK
		if n == 0 {
			c = http.StatusNotFound
		}
		for _, r := range execResult.Rows {
			r.Code = c
		}
	}

	for _, r := range execResult.Rows {
		if r.Code == 0 {
			r.Code = http.StatusNotFound
		}
	}

	execResult.FailedRows = execResult.TotalRows - execResult.SuccessRows
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
