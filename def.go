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
	path "github.com/alrusov/rest/v3/path"
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
		Notices          *misc.Messages      // Предупреждения и замечания обработчика
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
		AffectedRows uint64          `json:"affectedRows" comment:"Количеcтво затронутых записей"`
		Rows         []ExecResultRow `json:"rows,omitempty" comment:"Созданные записи" ref:"execResultRow"`
		Notice       string          `json:"notice,omitempty" comment:"Сообщения"`
	}

	ExecResultRow struct {
		ID      uint64 `json:"id,omitempty" comment:"ID созданной записи"`
		GUID    string `json:"guid,omitempty" comment:"GUID созданной записи"`
		Message string `json:"message,omitempty" comment:"Сообщения"`
		err     error  `json:"-"`
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

//----------------------------------------------------------------------------------------------------------------------------//

func GetTags() Tags {
	return tags
}

//----------------------------------------------------------------------------------------------------------------------------//

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

//----------------------------------------------------------------------------------------------------------------------------//

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

func (r *ExecResult) Error() (err error) {
	if len(r.Rows) == 0 {
		return
	}

	msgs := misc.NewMessages()

	for i, row := range r.Rows {
		if row.err == nil {
			continue
		}

		msgs.Add(fmt.Sprintf("[%d] %s", i, row.err))
	}

	return msgs.Error()
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResult) AddRow(row ExecResultRow) {
	r.Rows = append(r.Rows, row)
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResult) Cleanup() {
	for _, r := range r.Rows {
		if !r.IsEmpty() {
			return
		}
	}

	r.Rows = nil
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResultRow) SetError(err error) {
	r.err = err
	r.Message = err.Error()
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResultRow) Error() (err error) {
	return r.err
}

//----------------------------------------------------------------------------------------------------------------------------//

func (r *ExecResultRow) IsEmpty() bool {
	return r.ID == 0 && r.GUID == "" && r.Message == "" && r.err == nil
}

//----------------------------------------------------------------------------------------------------------------------------//
