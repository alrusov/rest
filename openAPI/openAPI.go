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
	rest "github.com/alrusov/rest/v4"
	path "github.com/alrusov/rest/v4/path"
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

		Server       string   `toml:"server"`
		ExtraServers []string `toml:"extra-servers"`
		Protocol     string   `toml:"protocol"`
		Host         string   `toml:"host"`
		Port         uint     `toml:"port"`
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

	TuneType func() reflect.Type
)

const (
	refComponentsSchemas = "#/components/schemas/"
	refComponentsHeaders = "#/components/headers/"

	TuneTypeFuncName = "TuneType"
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

func Compose(logFacility *log.Facility, cfg *Config, httpCfg *config.Listener, prefix string) (result *oa.T, err error) {
	if prefix != "" {
		prefix = "/" + strings.Trim(prefix, "/")
	}

	proc := &processor{
		oaCfg:   cfg,
		httpCfg: httpCfg,
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

	err = proc.addTags()
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
				proc.result.Paths.Set(urlPath, pi)
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

	servers := make(oa.Servers, 1, 1+len(proc.oaCfg.ExtraServers))
	servers[0] = &oa.Server{
		URL: url,
	}

	for _, url := range proc.oaCfg.ExtraServers {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}

		servers = append(servers,
			&oa.Server{
				URL: url,
			},
		)
	}

	securitySchemes := oa.SecuritySchemes{}

	if m, exists := proc.httpCfg.Auth.Methods["jwt"]; exists {
		if m.Enabled {
			name := "jwtAuth"
			securitySchemes[name] = &oa.SecuritySchemeRef{
				Value: oa.NewJWTSecurityScheme(),
			}
		}
	}

	if m, exists := proc.httpCfg.Auth.Methods["basic"]; exists {
		if m.Enabled {
			name := "basicAuth"
			securitySchemes[name] = &oa.SecuritySchemeRef{
				Value: &oa.SecurityScheme{
					Type:   "http",
					Scheme: "basic",
				},
			}
		}
	}

	proc.result = &oa.T{
		OpenAPI: oaCfg.APIversion,
		Components: &oa.Components{
			Schemas:         make(oa.Schemas),
			SecuritySchemes: securitySchemes,
		},
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
		Paths:    oa.NewPaths(),
		Security: *oa.NewSecurityRequirements(),
		Servers:  servers,
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

		makeSchema := func(withoutReadOnly bool, suffix string) {
			name := name + suffix

			var objSchema *oa.Schema
			objSchema, err = proc.makeObjectSchema(name, t.Type, withoutReadOnly)
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

		makeSchema(false, "")

		if t.WithCU {
			makeSchema(true, "CU")
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

// Добавляем теги
func (proc *processor) addTags() (err error) {
	tags := rest.GetTags()

	if len(tags) == 0 {
		return
	}

	oaTags := make(oa.Tags, 0, len(tags))

	for _, tag := range tags {
		oaTag := &oa.Tag{
			Name:        tag.Name,
			Description: tag.Description,
			ExternalDocs: &oa.ExternalDocs{
				Description: tag.ExternalDocs.Description,
				URL:         tag.ExternalDocs.URL,
			},
		}

		oaTags = append(oaTags, oaTag)
	}

	proc.result.Tags = oaTags
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Сканирует все цепочки с добавлением
func (proc *processor) scanChains(chains *path.Set, urlPath string, info *rest.Info) (err error) {
	// Парсим query параметры

	okStr := "OK"

	// Сканируем цепочки отдельно для каждого из методов
	for method, chains := range chains.Methods {
		// Бежим по цепочкам
		for _, chain := range chains.Chains {
			// Парсим request параметры
			var requestSchema *oa.SchemaRef

			// Response headers

			var responseHeaders map[string]*oa.HeaderRef
			if len(chain.Params.OutHeaders) > 0 {
				responseHeaders = make(map[string]*oa.HeaderRef, 16)
			}

			for name, descr := range chain.Params.OutHeaders {
				err = proc.addComponentHeader(name, descr)
				if err != nil {
					return
				}
				responseHeaders[name] = &oa.HeaderRef{
					Ref: refComponentsHeaders + name,
				}
			}

			if chain.Params.Request.Name != "" {
				name := chain.Params.Request.Name + "CU" // CU == (create + update) without readonly fields
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

			if chain.Params.Response.Name != "" {
				name := chain.Params.Response.Name

				if chain.Params.Flags&path.FlagResponseIsNotArray == 0 {
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

			// Парсим путь
			urlPath, pathExpr, pathParams, e := proc.makePathParameters(urlPath, chain)
			if e != nil {
				err = fmt.Errorf("%s.%s: %s", method, chain.Name, e)
				return
			}

			urlPath = proc.prefix + urlPath

			// Ищем сохраненный путь, если его нет - создаем

			pi := proc.result.Paths.Value(urlPath)
			if pi == nil {
				pathDescr := info.Summary
				if len(pathExpr) != 0 {
					pathDescr = fmt.Sprintf("%s. Разбор пути: %s", pathDescr, pathExpr)
				}
				pi = &oa.PathItem{
					Summary:     info.Summary,
					Description: pathDescr,
				}
				proc.result.Paths.Set(urlPath, pi)
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
				Summary:     strings.Join([]string{info.Summary, chains.Summary, chain.Summary}, " "),
				Description: strings.Join([]string{info.Description, chains.Description, chain.Description}, " "),
				OperationID: oid,
			}

			if requestSchema != nil {
				enc := chain.Params.Request.ContentType
				if enc == "" {
					enc = "application/json"
				}

				op.RequestBody = &oa.RequestBodyRef{
					Value: &oa.RequestBody{
						Content: oa.Content{
							enc: &oa.MediaType{
								Schema: requestSchema,
							},
						},
					},
				}
			}

			if responseSchema != nil {
				enc := chain.Params.Response.ContentType
				if enc == "" {
					enc = "application/json"
				}

				op.AddResponse(http.StatusOK,
					&oa.Response{
						Description: &okStr,
						Headers:     responseHeaders,
						Content: oa.Content{
							enc: &oa.MediaType{
								Schema: responseSchema,
							},
						},
					},
				)
			}

			// Стандартные ответы
			switch method {
			case stdhttp.MethodGET:
				op.AddResponse(http.StatusNoContent, oa.NewResponse().WithDescription("No content"))
				op.AddResponse(http.StatusUnprocessableEntity, oa.NewResponse().WithDescription("Unprocessable entity"))
			case stdhttp.MethodPOST,
				stdhttp.MethodPUT,
				stdhttp.MethodPATCH,
				stdhttp.MethodDELETE:
				op.AddResponse(http.StatusMultiStatus, oa.NewResponse().WithDescription("Multi status"))
			}

			op.AddResponse(http.StatusUnauthorized, oa.NewResponse().WithDescription("Unauthorized"))
			op.AddResponse(http.StatusForbidden, oa.NewResponse().WithDescription("Forbidden"))
			op.AddResponse(http.StatusBadRequest, oa.NewResponse().WithDescription("Bad request"))
			op.AddResponse(http.StatusInternalServerError, oa.NewResponse().WithDescription("Internal server error"))

			// Request headers

			for name, descr := range chain.Params.InHeaders {
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

			queryParams, e := proc.makeQueryParameters(chain)
			if e != nil {
				err = fmt.Errorf("%s", e)
				return
			}

			for _, p := range queryParams {
				op.AddParameter(p)
			}

			op.Tags = info.Tags

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

		enumItems := makeEnum(token.Expr, "|")

		field, exists := chain.Params.PathParamsType.FieldByName(token.VarName)
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

		sample := field.Tag.Get(path.TagSample)
		if sample != "" {
			tokenDescr = fmt.Sprintf("%s. Example: %s", tokenDescr, sample)
		}

		if tp == "" {
			err = fmt.Errorf("%s: unsupported type", token.VarName)
			return
		}

		pathElems = append(pathElems, "{"+token.VarName+"}")

		oaTp := field.Tag.Get(path.TagOAtype)
		if oaTp == "" {
			oaTp = tp
		}

		oaFmt := field.Tag.Get(path.TagOAformat)
		if oaFmt == "" {
			oaFmt = format
		}

		p := &oa.Parameter{
			Name:        token.VarName,
			In:          "path",
			Description: tokenDescr,
			Required:    true,
			Schema: &oa.SchemaRef{
				Value: &oa.Schema{
					Type:   oaTp,
					Format: oaFmt,
					Enum:   enumItems,
				},
			},
		}
		if sample != "" {
			//p.Schema.Value.Example = sample
		}

		pathParams = append(pathParams, p)
	}

	fullPath = strings.Join(append([]string{urlPath}, pathElems...), "/")
	descr = strings.Join(descrElems, "/")

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) makeQueryParameters(chain *path.Chain) (qp []*oa.Parameter, err error) {
	if chain.Params.QueryParamsType == nil {
		return
	}

	qp, err = proc.makeParameters(chain.Params.QueryParamsType, "query")
	if err != nil {
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (proc *processor) makeParameters(t reflect.Type, in string) (pp []*oa.Parameter, err error) {
	pp = make([]*oa.Parameter, 0, 32)

	err = proc.scanObject(&misc.BoolMap{}, nil, t, false,
		func(_ *oa.SchemaRef, field *reflect.StructField, tp string, format string) *oa.SchemaRef {
			switch tp {
			case "array", "object":
				// Параметры плоские
				return nil
			}

			name := field.Tag.Get(path.TagOA)
			if name == "-" {
				return nil
			}

			if name == "" {
				name = misc.StructTagName(field, path.TagJSON)
				if name == "-" {
					return nil
				}
			}

			descr := field.Tag.Get(path.TagComment)
			sample := field.Tag.Get(path.TagSample)
			if sample != "" {
				descr = fmt.Sprintf("%s. Example: %s", descr, sample)
			}

			var enumItems []any
			enum, enumExists := field.Tag.Lookup(path.TagEnum)
			if enumExists {
				enumItems = makeEnum(enum, ",")
			}

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
						Enum:   enumItems,
					},
				},
			}
			if sample != "" {
				//p.Schema.Value.Example = sample
			}

			defVal, defExists := field.Tag.Lookup(path.TagDefault)
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

func (proc *processor) makeObjectSchema(topName string, t reflect.Type, withoutReadOnly bool) (schema *oa.Schema, err error) {
	schemaRef := &oa.SchemaRef{
		Value: &oa.Schema{
			Description: "",
			Type:        "object",
			Format:      "",
			Properties:  make(oa.Schemas),
		},
	}

	err = proc.scanObject(&misc.BoolMap{}, schemaRef, t, withoutReadOnly,
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

			name := field.Tag.Get(path.TagOA)
			if name == "-" {
				return nil
			}

			if name == "" {
				name = misc.StructTagName(field, path.TagJSON)
				if name == "-" {
					return nil
				}
			}

			if withoutReadOnly {
				readonly := field.Tag.Get(path.TagReadonly)
				if readonly == "true" {
					return nil
				}
			}

			descr := field.Tag.Get(path.TagComment)
			sample := field.Tag.Get(path.TagSample)
			if sample != "" {
				descr = fmt.Sprintf("%s. Example: %s", descr, sample)
			}

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
				var enumItems []any
				enum, enumExists := field.Tag.Lookup(path.TagEnum)
				if enumExists {
					enumItems = makeEnum(enum, ",")
				}

				s = &oa.SchemaRef{
					Value: &oa.Schema{
						Description: descr,
						Type:        tp,
						Format:      format,
						Properties:  make(oa.Schemas),
						Enum:        enumItems,
					},
				}
				if sample != "" {
					//s.Value.Example = sample
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
		reflect.TypeOf(db.Duration(0)).String():   {"string", ""},
	}
)

func (proc *processor) scanObject(parentList *misc.BoolMap, parent *oa.SchemaRef, t reflect.Type, withoutReadOnly bool, filler filler) (err error) {
	tp := t.String()
	if _, exists := (*parentList)[tp]; exists {
		// Циклическая структура
		filler(parent, nil, "$", tp)
		return
	}

	(*parentList)[tp] = true
	defer delete(*parentList, tp)

	v := reflect.New(t)
	m := v.MethodByName(TuneTypeFuncName)
	if m.Kind() == reflect.Func {
		res := m.Call(nil)
		if len(res) == 1 && res[0].Kind() == reflect.Interface {
			if tuned, ok := res[0].Interface().(reflect.Type); ok {
				t = tuned
			}
		}
	}

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

		if field.Tag.Get(path.TagOA) == "-" {
			continue
		}

		if misc.StructTagName(&field, path.TagJSON) == "-" {
			continue
		}

		if withoutReadOnly {
			readonly := field.Tag.Get(path.TagReadonly)
			if readonly == "true" {
				continue
			}
		}

		fType := field.Type
		if fType.Kind() == reflect.Pointer {
			fType = fType.Elem()
		}

		d, exists := specialTypes[fType.String()]
		if exists {
			oaTp := field.Tag.Get(path.TagOAtype)
			if oaTp == "" {
				oaTp = d.tp
			}

			oaFmt := field.Tag.Get(path.TagOAformat)
			if oaFmt == "" {
				oaFmt = d.format
			}

			filler(parent, &field, oaTp, oaFmt)
			continue
		}

		kind, tp, format, e := proc.entityType(fType)
		if e != nil {
			err = e
			return
		}

		switch kind {
		case reflect.Interface:
			oaTp := field.Tag.Get(path.TagOAtype)
			oaFmt := field.Tag.Get(path.TagOAformat)
			filler(parent, &field, oaTp, oaFmt)

		case reflect.Struct:
			me := parent
			if !field.Anonymous {
				me = filler(parent, &field, "", "")
			}

			if me != nil || field.Anonymous {
				ref := field.Tag.Get(path.TagRef)
				if ref != "" {
					if withoutReadOnly {
						ref += "CU"
					}
					filler(me, nil, "ref", ref)
				} else {
					e := proc.scanObject(parentList, me, fType, withoutReadOnly, filler)
					if e != nil {
						err = e
						return
					}
				}
			}

		case reflect.Map:
			fallthrough
		case reflect.Slice:
			oaTp := field.Tag.Get(path.TagOAtype)
			if oaTp == "" {
				oaTp = tp
			}

			oaFmt := field.Tag.Get(path.TagOAformat)
			if oaFmt == "" {
				oaFmt = format
			}

			me := filler(parent, &field, oaTp, oaFmt)
			if me != nil {
				ref := field.Tag.Get(path.TagRef)
				if ref != "" {
					if withoutReadOnly {
						ref += "CU"
					}
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
						e := proc.scanObject(parentList, elemRef, elem, withoutReadOnly, filler)
						if e != nil {
							err = e
							return
						}
					}
				}
				if me.Value.Type == "map" {
					me.Value.Type = "object"
					me.Value.AdditionalProperties.Schema = me.Value.Items
					me.Value.Items = nil
				}
			}

		default:
			if tp == "" {
				tp = "UNKNOWN_" + fType.String()
			}

			oaTp := field.Tag.Get(path.TagOAtype)
			if oaTp == "" {
				oaTp = tp
			}

			oaFmt := field.Tag.Get(path.TagOAformat)
			if oaFmt == "" {
				oaFmt = format
			}

			filler(parent, &field, oaTp, oaFmt)
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

func makeEnum(s string, dlm string) (enumItems []any) {
	if s == "" || !strings.Contains(s, dlm) {
		return
	}

	list := strings.Split(s, dlm)
	enumItems = make([]any, len(list))
	for i, v := range list {
		enumItems[i] = strings.TrimSpace(v)
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//
