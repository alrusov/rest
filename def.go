/*
Описание структур для API и инициализация
*/
package rest

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/alrusov/config"
	"github.com/alrusov/db"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v4/path"
	"github.com/alrusov/stdhttp"
	"github.com/jmoiron/sqlx"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	// Интерфейс API метода
	API interface {
		// Получение информации о методе
		Info() *Info

		// Вызывается перед обращением к базе, используется, например, для добавления дополнительных параметров или проверок
		// Если возвращает code != 0 или result != nil, то они и будут результатом
		// Если code<0, то result содержит готовый ответ в []byte, отсылаем как есть с кодом -code
		Before(proc *ProcOptions) (result any, code int, err error)

		// Вызывается после обращения к базе, используется, например, для обогащения результата
		// Если возвращает code != 0 или result != nil, то они и будут результатом
		// Если code<0, то result содержит готовый ответ в []byte, отсылаем как есть с кодом -code
		After(proc *ProcOptions) (result any, code int, err error)
	}

	FuncInit        func(info *Info) (err error)
	FuncBefore      func(proc *ProcOptions) (result any, code int, err error)
	FuncAfter       func(proc *ProcOptions) (result any, code int, err error)
	FuncResultTuner func(proc *ProcOptions, result0 any, code0 int, err0 error) (result any, code int, err error)

	// Информация о методе
	Info struct {
		Path           string          // Относительный (от базового) путь в URL
		Name           string          // Имя, желательно  чтобы по правилам имен переменных
		Summary        string          // Краткое описание
		Description    string          // Описание, по умолчанию сформированное из Summary и query параметров
		Tags           []string        // Имена тегов для группировки
		Flags          path.Flags      // Флаги
		Methods        *path.Set       // Цепочки обработки
		Config         any             // Кастомные параметры в конфиг файле
		DBtype         string          // Тип базы. Если пусто, то по умолчанию из конфига
		QueryPrefix    string          // Префикс имени запроса в базу
		Init           FuncInit        // User defined Init
		Before         FuncBefore      // User defined Before query
		After          FuncAfter       // User defined After query
		shaperQueueLen int             // Shaper queue length, 0 - direct mode (without shaper)
		shaperQueue    chan *shaper    // Shaper queue
		ResultTuner    FuncResultTuner // The last step result tuner
	}

	// Опции запроса к методу
	ProcOptions struct {
		handler          API                 // Интерфейс метода
		LogFacility      *log.Facility       // Предпочтительная facility для логирования
		H                *stdhttp.HTTP       // HTTP листенер
		LogSrc           string              // Строка с ID запроса для MessageWithSource
		Info             *Info               // Информация о методе
		ID               uint64              // ID запроса
		Prefix           string              // Префикс пути запроса (при работе через прокси)
		Path             string              // Путь запроса
		Tail             []string            // Остаток пути
		R                *http.Request       // Запрос
		W                http.ResponseWriter // Интерфейс для ответа
		Chain            *path.Chain         // Обрабатываемая цепочка
		ChainLocal       path.Chain          // Копия Chain для возможности ее модификации для работы с динамическими объектами. Рекомендуется использовать её, а не Chain.Parent
		Scope            string              // Обрабатываемый Scope
		RawBody          []byte              // Тело запроса. В R.Body уже nil!
		PathParams       any                 // Path параметры
		QueryParams      any                 // Query параметры
		QueryParamsFound misc.BoolMap        // Query параметры, присутствующие в запросе в явном виде
		RequestParams    any                 // Request параметры
		DBqueryName      string              // Имя запроса к базе данных
		DBqueryVars      []any               // Переменные для формирования запроса
		ResultAsRows     bool                // Возвращать для GET не готовый результат, а *sqlx.Rows, чтобы производить разбор самостоятельно. Актуально для больших результатов.
		DBqueryResult    any                 // Результат выполненения запроса (указатель на слайс) при ResultAsRows==false
		DBqueryRows      *sqlx.Rows          // Результат при ResultAsRows==true
		Fields           []misc.InterfaceMap // Поля (имя из sql запроса) для insert или update. Для select - список полей для выборки из базы, если нужны не все из объекта
		ExcludedFields   misc.StringMap      // Поля ([name]db_name), которые надо исключить из запроса
		ExecResult       *ExecResult         // Результат выполнения Exec
		ExtraHeaders     misc.StringMap      // Дополнительные возвращаемые HTTP заголовки

		Extra  any // Произвольные данные от вызывающего
		Custom any // Произвольные пользовательские данные
	}

	// Обработчик
	Module struct {
		RawURL      string
		RelativeURL string        // URL без учета базовой части
		Handler     API           // Интерфейс метода
		Info        *Info         // Информация о методе
		LogFacility *log.Facility // Log facility
	}

	FieldDef struct {
		JSONname string
		DBname   string
		Type     reflect.Kind
	}

	ExecResult struct {
		Method      string           `json:"-" comment:"Метод"`
		TotalRows   uint64           `json:"totalRows" comment:"Количеcтво затронутых записей"`
		SuccessRows uint64           `json:"successRows" comment:"Количеcтво затронутых записей (успешное завершение)"`
		FailedRows  uint64           `json:"failedRows" comment:"Количеcтво затронутых записей (неуспешное завершение)"`
		Rows        []*ExecResultRow `json:"rows,omitempty" comment:"Созданные записи" ref:"execResultRow"`
	}

	ExecResultRow struct {
		Code int    `json:"code" comment:"Код завершения"`
		ID   uint64 `json:"id,omitempty" comment:"ID созданной записи"`
		GUID string `json:"guid,omitempty" comment:"GUID созданной записи"`
		MessagesBlock
	}

	MessagesBlock struct {
		Messages []string `json:"message,omitempty" comment:"Сообщения"`
		errors   []error
	}

	Tags []*Tag

	Tag struct {
		Name         string
		Aliases      []string
		Description  string
		ExternalDocs ExternalDocs
	}

	ExternalDocs struct {
		Description string
		URL         string
	}
)

//----------------------------------------------------------------------------------------------------------------------------//

const (
	FlagLogUnknownParams   = 0x00000001 // Логировать полученные query параметры, которые не описаны в методе
	FlagConvertReplyToJSON = 0x00000008 // Конвертировать ответ в json? Если он будет заранее подготовлен уже в таком формате, то НЕ СТАВИТЬ этот флаг!

	// Использовать по возможности стандартные имена!
	ParamCount      = "count"
	ParamPeriodFrom = "from" // включая
	ParamPeriodTo   = "to"   // НЕ включая
	ParamIDs        = "ids"
	ParamNames      = "names"

	// Стандартные Scope цепочек разбора пути, они же и суффиксы именён запросов в базу
	ScopeSelectAll     = "select.all"
	ScopeSelectID      = "select.id"
	ScopeSelectGUID    = "select.guid"
	ScopeSelectName    = "select.name"
	ScopeSelectPattern = "select.pattern"
	ScopeSelectStatus  = "select.status"
	ScopeInsert        = "insert"
	ScopeUpdateID      = "update.id"
	ScopeUpdateGUID    = "update.guid"
	ScopeUpdateName    = "update.name"
	ScopeDeleteID      = "delete.id"
	ScopeDeleteGUID    = "delete.guid"
	ScopeDeleteName    = "delete.name"

	// Признак статуса
	StatusActive   = "active"
	StatusInactive = "inactive"

	ExprID      = "id"
	ExprGUID    = "guid"
	ExprName    = "name"
	ExprPattern = "pattern"

	// Стандартные регулярки для Expr
	REempty  = ``
	REany    = `.+`
	REid     = `\d+`
	REstatus = StatusActive + "|" + StatusInactive
	REguid   = `(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`

	ExecResultName    = "execResult"
	ExecResultRowName = "execResultRow"

	DefaultMaxCount  = 10000
	DefaultMaxPeriod = config.Duration(3600 * time.Second)

	StatusProcessed = 999 // Специальный http status, говорящий о том, что все ответы уже отправлены
	StatusRetry     = 998 // Специальный http status, возвращаемый из After для повторного выполнения GET запроса (с возможно измененными там параметрами)
)

//----------------------------------------------------------------------------------------------------------------------------//

var (
	Log = log.NewFacility("api") // Log facility

	appCfg  any
	httpHdl *stdhttp.HTTP
	base    string
	defDB   string
	configs misc.InterfaceMap

	modulesMutex = new(sync.RWMutex)
	modules      = map[string]*Module{} // Обработчики API

	tags    = Tags{}
	tagsMap = map[string]*Tag{}
)

//----------------------------------------------------------------------------------------------------------------------------//

func (po *ProcOptions) QueryParamFound(name string) bool {
	return po.QueryParamsFound[name]
}

//----------------------------------------------------------------------------------------------------------------------------//

type Enumerator func(path string, info *Info) (err error)

func Enumerate(e Enumerator) (err error) {
	modulesMutex.RLock()
	defer modulesMutex.RUnlock()

	for path, df := range modules {
		err = e(path, df.Info)
		if err != nil {
			return
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func AddTag(tag *Tag) error {
	if tag.Name == "" {
		return fmt.Errorf("empty tag name")
	}

	if _, exists := tagsMap[tag.Name]; exists {
		return fmt.Errorf("tag %s already exists", tag.Name)
	}

	for _, alias := range tag.Aliases {
		if _, exists := tagsMap[alias]; exists {
			return fmt.Errorf("tag alias %s already exists", alias)
		}
	}

	tags = append(tags, tag)
	tagsMap[tag.Name] = tag

	for _, alias := range tag.Aliases {
		tagsMap[alias] = tag
	}

	return nil
}

func GetTags() Tags {
	return tags
}

func GetTagName(name string) string {
	tag, exists := tagsMap[name]
	if !exists {
		return ""
	}

	return tag.Name
}

//----------------------------------------------------------------------------------------------------------------------------//

func FindSubstArg(vars []any, name string) (subst *db.SubstArg) {
	for _, v := range vars {
		switch vv := v.(type) {
		case *db.SubstArg:
			if vv.Name() == name {
				subst = vv
				return
			}
		}
	}

	return
}

func DelSubstArg(vars []any, name string) (result []any) {
	for i, v := range vars {
		switch vv := v.(type) {
		case *db.SubstArg:
			if vv.Name() == name {
				result = slices.Delete(vars, i, i+1)
				return
			}
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResult) AddRow(row *ExecResultRow) {
	r.Rows = append(r.Rows, row)
}

func (r *ExecResult) FillMessages() {
	if len(r.Rows) == 0 {
		return
	}

	for _, r := range r.Rows {
		r.MessagesBlock.FillMessages()
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResultRow) AddError(err error) {
	r.MessagesBlock.AddError(err)
}

func (r *ExecResultRow) AddErrors(errs []error) {
	r.MessagesBlock.AddErrors(errs)
}

func (r *ExecResultRow) AddMessage(s string, params ...any) {
	r.MessagesBlock.AddMessage(s, params...)
}

func (r *ExecResultRow) AddMessages(ss []string) {
	r.MessagesBlock.AddMessages(ss)
}

func (r *ExecResultRow) HasErrors() bool {
	return r.MessagesBlock.HasErrors()
}

func (r *ExecResultRow) Errors() (err []error) {
	return r.MessagesBlock.Errors()
}

func (r *ExecResultRow) FillMessages() {
	r.MessagesBlock.FillMessages()
}

//----------------------------------------------------------------------------------------------------------------------------//

func (m *MessagesBlock) AddError(err error) {
	if err == nil {
		return
	}

	m.errors = append(m.errors, err)
}

func (m *MessagesBlock) AddErrors(errs []error) {
	if len(errs) == 0 {
		return
	}

	m.errors = append(m.errors, errs...)
}

func (m *MessagesBlock) AddMessage(s string, params ...any) {
	if s == "" {
		return
	}

	m.errors = append(m.errors, fmt.Errorf(s, params...))
}

func (m *MessagesBlock) AddMessages(ss []string) {
	if len(ss) == 0 {
		return
	}

	for _, s := range ss {
		m.AddMessage(s)
	}
}

func (m *MessagesBlock) HasErrors() bool {
	return len(m.errors) != 0
}

func (m *MessagesBlock) Errors() (errs []error) {
	return m.errors
}

func (m *MessagesBlock) FillMessages() {
	ln := len(m.errors)
	if ln == 0 {
		return
	}

	m.Messages = make([]string, 0, ln)

	for _, e := range m.errors {
		m.Messages = append(m.Messages, e.Error())
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
