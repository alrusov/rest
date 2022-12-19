package openapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/alrusov/config"
	"github.com/alrusov/db"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	rest "github.com/alrusov/rest/v2"
	path "github.com/alrusov/rest/v2/path"
	"github.com/alrusov/stdhttp"
	oa "github.com/getkin/kin-openapi/openapi3"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	Config struct {
		APIversion string `toml:"api-version"`

		Title          string `toml:"title"`
		Description    string `toml:"description"`
		TermsOfService string `toml:"terms-of-service"`

		ContactName  string `toml:"contact-name"`
		ContactURL   string `toml:"contact-url"`
		ContactEmail string `toml:"contact-email"`

		LicenseName string `toml:"license-name"`
		LicenseURL  string `toml:"license-url"`

		Version string `toml:"version"`

		Server   string `toml:"server"`
		Protocol string `toml:"protocol"`
		Host     string `toml:"host"`
		Port     uint   `toml:"port"`
	}

	processor struct {
		oaCfg   *Config
		httpCfg *config.Listener
		prefix  string
		result  *oa.T
		knownID map[string]uint
		schemas map[string]*oa.Schema
		msgs    *misc.Messages
	}

	filler func(parent *oa.SchemaRef, field *reflect.StructField, tp string, format string) *oa.SchemaRef
)

const (
	refComponentsSchemas = "#/components/schemas/"
	refComponentsHeaders = "#/components/headers/"
)

//----------------------------------------------------------------------------------------------------------------------------//

// Проверка валидности OpenAPI
func (x *Config) Check(cfg any) (err error) {
	msgs := misc.NewMessages()

	if x.APIversion == "" {
		msgs.Add("undefined api-version")
	}

	if x.LicenseName == "" {
		msgs.Add("undefined license-name")
	}

	return msgs.Error()
}

//----------------------------------------------------------------------------------------------------------------------------//

func Compose(logFacility *log.Facility, cfg *Config, prefix string) (result *oa.T, err error) {
	if prefix != "" {
		prefix = "/" + strings.Trim(prefix, "/")
	}

	proc := &processor{
		oaCfg:   cfg,
		prefix:  prefix,
		knownID: make(map[string]uint, 1024),
		schemas: make(map[string]*oa.Schema, 1024),
		msgs:    misc.NewMessages(),
	}

	err = proc.prepare()
	if err != nil {
		return
	}

	err = proc.addComponents()
	if err != nil {
		return
	}

	err = rest.Enumerate(
		func(urlPath string, info *rest.Info) (err error) {
			pi := &oa.PathItem{
				Summary:     info.Summary,
				Description: info.Description,
			}

			chains := info.Methods
			if chains == nil {
				//параметры!
				proc.result.Paths[urlPath] = pi
				return
			}

			err = proc.scanChains(info.Methods, urlPath, info)
			if err != nil {
				err = fmt.Errorf("%s: %s", urlPath, err)
				return
			}

			return
		},
	)

	if err != nil {
		return
	}

	result = proc.result
	err = result.Validate(context.Background())
	if err != nil {
		proc.msgs.Add("validate: %s", err)
	}

	err = proc.msgs.Error()
	if err != nil {
		logFacility.Message(log.NOTICE, "%s", err)
		err = nil
	}
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) prepare() (err error) {
	oaCfg := proc.oaCfg

	url, err := proc.serverURL()
	if err != nil {
		return
	}

	server := &oa.Server{
		URL: url,
	}

	proc.result = &oa.T{
		OpenAPI:    oaCfg.APIversion,
		Components: oa.Components{Schemas: make(oa.Schemas)},
		Info: &oa.Info{
			Title:          oaCfg.Title,
			Description:    oaCfg.Description,
			TermsOfService: oaCfg.TermsOfService,
			Contact: &oa.Contact{
				Name:  oaCfg.ContactName,
				URL:   oaCfg.ContactURL,
				Email: oaCfg.ContactEmail,
			},
			License: &oa.License{
				Name: oaCfg.LicenseName,
				URL:  oaCfg.LicenseURL,
			},
			Version: oaCfg.Version,
		},
		Paths:    oa.Paths{},
		Security: *oa.NewSecurityRequirements(),
		Servers: oa.Servers{
			server,
		},
		//Tags:         oa.Tags{},
		//ExternalDocs: &oa.ExternalDocs{},
	}

	return
}

// ----------------------------------------------------------------------------------------------------------------------------//

// Создает URL на основании конфигурации
func (proc *processor) serverURL() (server string, err error) {
	server = proc.oaCfg.Server
	if server != "" {
		return
	}

	protocol := proc.oaCfg.Protocol
	if protocol == "" {
		protocol = "http"
		if proc.httpCfg.SSLCombinedPem != "" {
			protocol += "s"
		}
	}

	host := proc.oaCfg.Host
	port := uint64(proc.oaCfg.Port)

	if host == "" || port == 0 {
		addr := strings.Split(proc.httpCfg.Addr, ":")
		if host == "" {
			host = addr[0]
			if host == "" {
				host, _ = os.Hostname()
			}
		}

		if port == 0 {
			if len(addr) > 1 {
				port, _ = strconv.ParseUint(addr[1], 10, 16)
			}
			if port == 0 {
				if protocol == "https" {
					port = 443
				} else {
					port = 80
				}
			}
		}
	}

	server = fmt.Sprintf("%s://%s:%d", protocol, host, port)
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) addComponents() (err error) {
	err = proc.addComponentSchemas()
	if err != nil {
		return
	}

	return
}

func (proc *processor) addComponentSchemas() (err error) {
	schemas := make(oa.Schemas)

	list := path.GetKnownObjects()
	for name, t := range list {
		if name == path.VarIgnore {
			continue
		}

		var objSchema *oa.Schema
		objSchema, err = proc.makeObjectSchema(name, t.Type, "")
		if err != nil {
			return
		}

		schemas[name] = &oa.SchemaRef{
			Value: objSchema,
		}

		if t.ArrayType != nil {
			schemas[name+"Array"] = &oa.SchemaRef{
				Value: &oa.Schema{
					Type: "array",
					Items: &oa.SchemaRef{
						Ref:   refComponentsSchemas + name,
						Value: objSchema,
					},
				},
			}
			proc.schemas[name] = objSchema
		}
	}

	proc.result.Components.Schemas = schemas
	return
}

func (proc *processor) addComponentHeader(name string, descr string) (err error) {
	if _, exists := proc.result.Components.Headers[name]; exists {
		return
	}

	if proc.result.Components.Headers == nil {
		proc.result.Components.Headers = make(map[string]*oa.HeaderRef, 16)
	}

	proc.result.Components.Headers[name] = &oa.HeaderRef{
		Value: &oa.Header{
			Parameter: oa.Parameter{
				Description: descr,
				Schema: &oa.SchemaRef{
					Value: &oa.Schema{
						Type: "string",
					},
				},
			},
		},
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Сканирует все цепочки с добавлением
func (proc *processor) scanChains(chains *path.Set, urlPath string, info *rest.Info) (err error) {
	// Парсим query параметры

	okStr := "OK"

	queryParams, e := proc.makeQueryParameters(info.Methods)
	if e != nil {
		err = fmt.Errorf("%s", e)
		return
	}

	// Сканируем цепочки отдельно для каждого из методов
	for method, chains := range chains.Methods {
		// Парсим request параметры
		var requestSchema *oa.SchemaRef

		// Response headers

		var responseHeaders map[string]*oa.HeaderRef
		if len(chains.OutHeaders) > 0 {
			responseHeaders = make(map[string]*oa.HeaderRef, 16)
		}

		for name, descr := range chains.OutHeaders {
			err = proc.addComponentHeader(name, descr)
			if err != nil {
				return
			}
			responseHeaders[name] = &oa.HeaderRef{
				Ref: refComponentsHeaders + name,
			}
		}

		//

		if chains.RequestObjectName != "" {
			name := chains.RequestObjectName
			obj, exists := proc.result.Components.Schemas[name]
			if !exists {
				err = fmt.Errorf("%s: unknown request object %s", method, name)
				return
			}

			requestSchema = &oa.SchemaRef{
				Ref:   refComponentsSchemas + name,
				Value: obj.Value,
			}
			proc.schemas[name] = obj.Value
		}

		// Парсим response параметры

		var responseSchema *oa.SchemaRef

		if chains.ResponseObjectName != "" {
			name := chains.ResponseObjectName

			if chains.Flags&path.FlagResponseIsNotArray == 0 {
				name += "Array"
			}

			obj, exists := proc.result.Components.Schemas[name]
			if !exists {
				err = fmt.Errorf("%s: unknown response object %s", method, name)
				return
			}

			responseSchema = &oa.SchemaRef{
				Ref:   refComponentsSchemas + name,
				Value: obj.Value,
			}
			proc.schemas[name] = obj.Value
		}

		// Бежим по цепочкам
		for _, chain := range chains.Chains {
			// Парсим путь
			urlPath, pathExpr, pathParams, e := proc.makePathParameters(urlPath, chain)
			if e != nil {
				err = fmt.Errorf("%s.%s: %s", method, chain.Name, e)
				return
			}

			urlPath = proc.prefix + urlPath

			// Ищем сохраненный путь, если его нет - создаем

			pi, exists := proc.result.Paths[urlPath]
			if !exists {
				pathDescr := info.Summary
				if len(pathParams) != 0 {
					pathDescr = fmt.Sprintf("%s. Разбор пути: %s", pathDescr, pathExpr)
				}
				pi = &oa.PathItem{
					Summary:     info.Summary,
					Description: pathDescr,
				}
				proc.result.Paths[urlPath] = pi
			}

			// Создаем OperationID

			oidBase := info.Name
			if chain.Scope != "" {
				oidBase += "." + chain.Scope
			}

			oid := oidBase

			ki, exists := proc.knownID[oidBase]
			if !exists {
				ki = 0
			} else {
				// Дублированное - добавляем индекс
				ki++
				oid = fmt.Sprintf("%s.%d", oidBase, ki)
			}
			proc.knownID[oidBase] = ki

			// Создаём операцию

			op := &oa.Operation{
				Summary:     chains.Summary,
				Description: chains.Description,
				OperationID: oid,
			}

			if requestSchema != nil {
				op.RequestBody = &oa.RequestBodyRef{
					Value: &oa.RequestBody{
						Content: oa.Content{
							"application/json": &oa.MediaType{
								Schema: requestSchema,
							},
						},
					},
				}
			}

			if responseSchema != nil {
				op.AddResponse(http.StatusOK,
					&oa.Response{
						Description: &okStr,
						Headers:     responseHeaders,
						Content: oa.Content{
							"application/json": &oa.MediaType{
								Schema: responseSchema,
							},
						},
					},
				)
			}

			// Стандартные ответы

			op.AddResponse(http.StatusNoContent, oa.NewResponse().WithDescription("No content"))
			op.AddResponse(http.StatusBadRequest, oa.NewResponse().WithDescription("Bad request"))
			op.AddResponse(http.StatusUnauthorized, oa.NewResponse().WithDescription("Unauthorized"))
			op.AddResponse(http.StatusForbidden, oa.NewResponse().WithDescription("Forbidden"))
			op.AddResponse(http.StatusNotFound, oa.NewResponse().WithDescription("Not found"))
			op.AddResponse(http.StatusMethodNotAllowed, oa.NewResponse().WithDescription("Not allowed"))
			op.AddResponse(http.StatusInternalServerError, oa.NewResponse().WithDescription("Internal server error"))

			// Request headers

			for name, descr := range chains.InHeaders {
				p := &oa.Parameter{
					Name:        name,
					In:          "header",
					Description: descr,
					Required:    false,
					Schema: &oa.SchemaRef{
						Value: &oa.Schema{
							Type: "string",
						},
					},
				}
				op.AddParameter(p)
			}

			// Добавляем параметры из path

			for _, p := range pathParams {
				op.AddParameter(p)
			}

			// Добавляем query параметры

			if pp, exists := queryParams[method]; exists {
				for _, p := range pp {
					op.AddParameter(p)
				}
			}

			// Добавляем операцию в соответствующий метод

			switch method {
			case stdhttp.MethodPOST:
				pi.Post = op
			case stdhttp.MethodGET:
				pi.Get = op
			case stdhttp.MethodPUT:
				pi.Put = op
			case stdhttp.MethodPATCH:
				pi.Patch = op
			case stdhttp.MethodDELETE:
				pi.Delete = op
			}
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) makePathParameters(urlPath string, chain *path.Chain) (fullPath string, descr string, pathParams []*oa.Parameter, err error) {
	pathElems := make([]string, 0, len(chain.Tokens))
	descrElems := make([]string, 0, len(chain.Tokens))
	pathParams = make([]*oa.Parameter, 0, len(chain.Tokens))

	for _, token := range chain.Tokens {
		if len(token.Expr) != 0 {
			descrElems = append(descrElems, "("+token.Expr+")")
		}

		if token.VarName == path.VarIgnore {
			pathElems = append(pathElems, token.Expr)
			continue
		}

		enumItems := []any{}

		if strings.Contains(token.Expr, "|") {
			list := strings.Split(token.Expr, "|")
			enumItems = make([]any, len(list))
			for i, v := range list {
				enumItems[i] = v
			}
		}

		field, exists := chain.Parent.PathParamsType.FieldByName(token.VarName)
		if !exists {
			err = fmt.Errorf("%s: field not found", token.VarName)
			return
		}

		_, tp, format, e := proc.entityType(field.Type)
		if e != nil {
			err = fmt.Errorf("%s: %s", token.VarName, e)
			return
		}

		tokenDescr := field.Tag.Get(path.TagComment)
		if tokenDescr != "" && token.Description != "" {
			tokenDescr += ". "
		}
		if token.Description != "" {
			tokenDescr += token.Description
		}

		if tp == "" {
			err = fmt.Errorf("%s: unsupported type", token.VarName)
			return
		}

		pathElems = append(pathElems, "{"+token.VarName+"}")

		pathParams = append(pathParams,
			&oa.Parameter{
				Name:        token.VarName,
				In:          "path",
				Description: tokenDescr,
				Required:    true,
				Schema: &oa.SchemaRef{
					Value: &oa.Schema{
						Type:   tp,
						Format: format,
						Enum:   enumItems,
					},
				},
			},
		)
	}

	fullPath = strings.Join(append([]string{urlPath}, pathElems...), "/")
	descr = strings.Join(descrElems, "/")

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) makeQueryParameters(params *path.Set) (pp map[string][]*oa.Parameter, err error) {
	pp = make(map[string][]*oa.Parameter, len(params.Methods))

	for method, df := range params.Methods {
		if df.QueryParamsType == nil {
			continue
		}

		p, e := proc.makeParameters(df.QueryParamsType, "query")
		if e != nil {
			err = e
			return
		}

		pp[method] = append(pp[method], p...)
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) makeParameters(t reflect.Type, in string) (pp []*oa.Parameter, err error) {
	pp = make([]*oa.Parameter, 0, 32)

	err = proc.scanObject(&misc.BoolMap{}, nil, t, in,
		func(_ *oa.SchemaRef, field *reflect.StructField, tp string, format string) *oa.SchemaRef {
			switch tp {
			case "array", "object":
				// Параметры плоские
				return nil
			}

			name := misc.StructFieldName(field, path.TagJSON)
			if name == "-" {
				return nil
			}

			descr := field.Tag.Get(path.TagComment)
			defVal, defExists := field.Tag.Lookup(path.TagDefault)
			required := field.Tag.Get(path.TagRequired) == "true"

			p := &oa.Parameter{
				Name:        name,
				In:          in,
				Description: descr,
				Required:    required,
				Schema: &oa.SchemaRef{
					Value: &oa.Schema{
						Type:   tp,
						Format: format,
					},
				},
			}
			if defExists {
				p.Schema.Value.Default, err = conv(tp, defVal)
				if err != nil {
					proc.msgs.Add("%s.%s: %s", "?", name, err)
				}
			}

			pp = append(pp, p)
			return nil
		},
	)

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) makeObjectSchema(topName string, t reflect.Type, in string) (schema *oa.Schema, err error) {
	schemaRef := &oa.SchemaRef{
		Value: &oa.Schema{
			Description: "",
			Type:        "object",
			Format:      "",
			Properties:  make(oa.Schemas),
		},
	}

	err = proc.scanObject(&misc.BoolMap{}, schemaRef, t, in,
		func(parent *oa.SchemaRef, field *reflect.StructField, tp string, format string) *oa.SchemaRef {
			if field == nil { // array member
				var s *oa.SchemaRef
				switch tp {
				case "$": // ссылка на себя
					parent.Ref = refComponentsSchemas + topName
					return parent
				case "ref":
					//sh := proc.schemas[format]
					s = &oa.SchemaRef{
						Ref: refComponentsSchemas + format,
						//Value: sh,
					}
				default:
					s = &oa.SchemaRef{
						Value: &oa.Schema{
							Description: "",
							Type:        tp,
							Format:      format,
							Properties:  make(oa.Schemas),
						},
					}
				}
				parent.Value.Items = s
				return s
			}

			name := misc.StructFieldName(field, path.TagJSON)
			if name == "-" {
				return nil
			}

			descr := field.Tag.Get(path.TagComment)
			defVal, defExists := field.Tag.Lookup(path.TagDefault)
			required := field.Tag.Get(path.TagRequired) == "true"
			if name == "" {
				name = field.Name
			}

			var s *oa.SchemaRef

			switch tp {
			case "array", "object", "map":
				s = &oa.SchemaRef{
					Value: &oa.Schema{
						Description: descr,
						Type:        tp,
						Format:      "",
						Properties:  make(oa.Schemas, 32),
					},
				}
			case "$": // ссылка на себя
				s = &oa.SchemaRef{
					Ref: refComponentsSchemas + topName,
				}
			default:
				s = &oa.SchemaRef{
					Value: &oa.Schema{
						Description: descr,
						Type:        tp,
						Format:      format,
						Properties:  make(oa.Schemas),
					},
				}
				if defExists {
					s.Value.Default, err = conv(tp, defVal)
					if err != nil {
						proc.msgs.Add("%s.%s: %s", topName, name, err)
					}
				}
			}

			parent.Value.Properties[name] = s
			if required {
				parent.Value.Required = append(parent.Value.Required, name)
			}

			return s
		},
	)

	if err != nil {
		return
	}

	schema = schemaRef.Value
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

type (
	tpData struct {
		tp     string
		format string
	}
)

var (
	specialTypes = map[string]tpData{
		reflect.TypeOf(time.Time{}).String():      {"string", "date-time"},
		reflect.TypeOf(db.NullFloat64{}).String(): {"number", "double"},
		reflect.TypeOf(db.NullInt64{}).String():   {"integer", "int64"},
		reflect.TypeOf(db.NullUint64{}).String():  {"integer", "int64"},
		reflect.TypeOf(db.NullString{}).String():  {"string", ""},
		reflect.TypeOf(db.NullTime{}).String():    {"string", "date-time"},
	}
)

func (proc *processor) scanObject(parentList *misc.BoolMap, parent *oa.SchemaRef, t reflect.Type, in string, filler filler) (err error) {
	tName := t.String()
	if _, exists := (*parentList)[tName]; exists {
		// Циклическая структура
		filler(parent, nil, "$", tName)
		return
	}

	(*parentList)[tName] = true
	defer delete(*parentList, tName)

	srcT := t

	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	/*
		if t.Kind()==reflect.Map {
			me.Value.Type = "object"
			me.Value.AdditionalProperties = me.Value.Items
			me.Value.Items = nil
			filler(parent, nil, "ref", ref)
			return
		}
	*/

	if t.Kind() != reflect.Struct {
		err = fmt.Errorf("%s is not a struct", srcT.Kind())
		return
	}

	ln := t.NumField()

	for i := 0; i < ln; i++ {
		field := t.Field(i)

		if !field.IsExported() {
			continue
		}

		if misc.StructFieldName(&field, path.TagJSON) == "-" {
			continue
		}

		fType := field.Type
		if fType.Kind() == reflect.Pointer {
			fType = fType.Elem()
		}

		d, exists := specialTypes[fType.String()]
		if exists {
			filler(parent, &field, d.tp, d.format)
			continue
		}

		kind, tp, format, e := proc.entityType(fType)
		if e != nil {
			err = e
			return
		}

		switch kind {
		case reflect.Interface:
			filler(parent, &field, "", "")

		case reflect.Struct:
			me := filler(parent, &field, "", "")
			if me != nil {
				e := proc.scanObject(parentList, me, fType, in, filler)
				if e != nil {
					err = e
					return
				}
			}

		case reflect.Map:
			fallthrough
		case reflect.Slice:
			me := filler(parent, &field, tp, format)
			if me != nil {
				ref := field.Tag.Get(path.TagRef)
				if ref != "" {
					filler(me, nil, "ref", ref)
				} else {
					elem := fType.Elem()
					kind, tp, format, e := proc.entityType(elem)
					if e != nil {
						err = e
						return
					}

					elemRef := filler(me, nil, tp, format)

					if kind == reflect.Struct {
						e := proc.scanObject(parentList, elemRef, elem, in, filler)
						if e != nil {
							err = e
							return
						}
					}
				}
				if me.Value.Type == "map" {
					me.Value.Type = "object"
					me.Value.AdditionalProperties = me.Value.Items
					me.Value.Items = nil
				}
			}

		default:
			if tp == "" {
				tp = "UNKNOWN_" + fType.String()
			}

			filler(parent, &field, tp, format)
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) entityType(t reflect.Type) (kind reflect.Kind, tp string, format string, err error) {
	kind = t.Kind()

	if t.Kind() == reflect.Pointer {
		t = t.Elem()
		kind = t.Kind()
	}

	switch kind {
	default:
		err = fmt.Errorf("illegal kind %s", kind)
		return

	case reflect.Interface:
		tp = "object"
		format = ""

	case reflect.Bool:
		tp = "boolean"
		format = ""

	case reflect.Float32, reflect.Float64:
		tp = "number"
		format = "double"

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		tp = "integer"
		format = "int64"

	case reflect.String:
		tp = "string"
		format = ""

	case reflect.Slice:
		tp = "array"
		format = ""

	case reflect.Map:
		tp = "map"
		format = ""

	case reflect.Struct:
		tp = "object"
		format = ""
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func conv(tp string, v string) (any, error) {
	switch tp {
	default:
		return v, fmt.Errorf(`conv: illegal type "%s"`, tp)

	case "boolean":
		return strconv.ParseBool(v)

	case "number":
		return strconv.ParseFloat(v, 64)

	case "integer":
		return strconv.ParseInt(v, 10, 64)

	case "string":
		return v, nil

	}
}

//----------------------------------------------------------------------------------------------------------------------------//
