/*
Описание структур для API и инициализация
*/
package rest

import (
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/alrusov/config"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v2/path"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	// Интерфейс API метода
	API interface {
		// Получение информации о методе
		Info() *Info

		// Возврат (code<0 && err!=nil) -- отсылается ответ с ошибкой в json и кодом abs(code), даже если не стоит FlagJSONReply
		// Если code==0, то result содержит готовый ответ в []byte, отсылаем как есть с кодом 200
		//Handler(options *ExecOptions) (result any, code int, headers misc.StringMap, err error)

		// Вызывается перед обращением к базе, используется, например, для добавления дополнительных параметров или проверок
		// Если возвращает code != 0 или data != nil, то они и будут результатом
		Before(proc *ProcOptions) (result any, code int, err error)

		// Вызывается почле обращения к базе, используется, например, для обогащения результата
		// Если возвращает code != 0 или data != nil, то они и будут результатом
		After(proc *ProcOptions) (result any, code int, err error)
	}

	FuncInit   func(info *Info) (err error)
	FuncBefore func(proc *ProcOptions) (result any, code int, err error)
	FuncAfter  func(proc *ProcOptions) (result any, code int, err error)

	// Информация о методе
	Info struct {
		Path          string        // Относительный (от базового) путь в URL
		Name          string        // Имя, желательно  чтобы по правилам имен переменных
		Summary       string        // Краткое описание
		Description   string        // Описание, по умолчанию сформированное из Summary и query параметров
		Flags         path.Flags    // Флаги
		Methods       *path.Set     // Цепочки обработки
		Config        any           // Кастомные параметры в конфиг файле
		CacheLifetime time.Duration // Время жизни кэша, если 0, то не использовать
		DBtype        string        // Тип базы. Если пусто, то по умолчанию из конфига
		QueryPrefix   string        // Префикс имени запроса в базу
		Init          FuncInit      // User defined Init
		Before        FuncBefore    // User defined Before query
		After         FuncAfter     // User defined After query

		//json2dbFields misc.StringMap
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
		R                *http.Request       // Запрос
		W                http.ResponseWriter // Интерфейс для ответа
		Chain            *path.Chain         // Обрабатываемая цепочка
		Scope            string              // Обрабатываемый Scope
		RawBody          []byte              // Тело запроса. В R.Body уже nil!
		PathParams       any                 // Path параметры
		QueryParams      any                 // Query параметры
		RequestParams    any                 // Request параметры
		RequestObject    any                 // Объект из тела запроса
		DBqueryName      string              // Имя запроса к базе данных
		RequestBodyNames []string            // Для запросов в телом - полученные имена полей
		RequestBodyVals  []any               // и соответствующие им значения
		DBqueryVars      []any               // Переменные для заполнения запроса
		DBqueryResult    any                 // Результат выполненеия Query (слайс)
		Fields           misc.InterfaceMap   // Поля для insert или update
		ExcludedFields   misc.InterfaceMap   // Поля, которые надо исключить из запроса
		Notices          *misc.Messages      // Предупреждения и замечания обработчика
		ExecResult       *ExecResult         // Результат выполнения Exec
		Custom           any                 // Произвольные пользовательские данные
	}

	// Обработчик
	module struct {
		rawURL      string
		relativeURL string        // URL без учета базовой части
		handler     API           // Интерфейс метода
		info        *Info         // Информация о методе
		logFacility *log.Facility // Log facility
	}

	FieldDef struct {
		JSONname string
		DBname   string
		Type     reflect.Kind
	}

	ExecResult struct {
		AffectedRows uint64 `json:"affectedRows" comment:"Количеcтво затронутых записей"`
		ID           uint64 `json:"id,omitempty" comment:"ID созданной записи"`
		GUID         string `json:"guid,omitempty" comment:"GUID созданной записи"`
		Notice       string `json:"notice,omitempty" comment:"Предупреждения и замечания"`
	}
)

//----------------------------------------------------------------------------------------------------------------------------//

const (
	FlagLogUnknownParams = 0x00000001 // Логировать полученные query параметры, которые не описаны в методе
	//FlagDynamic          = 0x00000002 // Разрешить обработку с использованием этого пути как базового. Например Path="xxx/yyy", будет обрабатывать и запросы типа "xxx/yyy/zzz/qqq"
	//FlagDontParseBody    = 0x00000004 // Не пытаться разбирать тело запроса
	FlagConvertReplyToJSON = 0x00000008 // Конвертировать ответ в json? Если он будет заранее подготовлен уже в таком формате, то НЕ СТАВИТЬ этот флаг!
	//FlagRESTful          = 0x00000010 // Это RESTful. Будут автоматически добавлены флаги FlagDynamic, FlagDontParseBody и FlagJSONReply

	StatusProcessed = 999 // Специальный тип возврата, говорящий о том, что уже все ответы отправлены

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
	ScopeDeleteID      = "delete.id"
	ScopeDeleteGUID    = "delete.guid"

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

	ExecResultName = "execResult"

	DefaultMaxCount  = 10000
	DefaultMaxPeriod = config.Duration(3600 * time.Second)
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
	modules      = map[string]*module{} // Обработчики API
)

//----------------------------------------------------------------------------------------------------------------------------//

type Enumerator func(path string, info *Info) (err error)

func Enumerate(e Enumerator) (err error) {
	modulesMutex.RLock()
	defer modulesMutex.RUnlock()

	for path, df := range modules {
		err = e(path, df.info)
		if err != nil {
			return
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//
