package path

import (
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/alrusov/config"
	"github.com/alrusov/db"
	"github.com/alrusov/jsonw"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	Set struct {
		Flags       Flags   `json:"flags,omitempty"`
		Summary     string  `json:"summary"`
		Description string  `json:"description"`
		Methods     Methods `json:"methods"`
	}

	Methods map[string]*Chains

	Chains struct {
		Summary           string     `json:"summary"`
		Description       string     `json:"description"`
		ParamsDescription string     `json:"paramsDescription"`
		Chains            ChainsList `json:"chains"`
		StdParams         Params     `json:"params"`
		DefaultHttpCode   int        `json:"defaultHttpCode"`
		HttpCodes         []int      `json:"httpCodes"`

		prepared bool
	}

	ChainsList []*Chain

	Chain struct {
		Parent        *Chains         `json:"-"`
		Flags         Flags           `json:"flags,omitempty"`
		Summary       string          `json:"summary"`
		Description   string          `json:"description"`
		Name          string          `json:"name"`
		Scope         string          `json:"scope,omitempty"`
		Params        Params          `json:"params"`
		Tokens        []*Token        `json:"tokens"`
		CacheLifetime config.Duration `json:"cacheLifetime"` // Время жизни кэша, если 0, то не использовать
	}

	Token struct {
		Description string `json:"description"`
		Expr        string `json:"expr"`
		VarName     string `json:"varName"`
		re          *regexp.Regexp
	}

	Object struct {
		Type      reflect.Type
		ArrayType reflect.Type
		WithCU    bool
	}

	Vars misc.InterfaceMap

	Flags uint64

	Params struct {
		Flags Flags `json:"flags,omitempty"`

		InHeaders  misc.StringMap `json:"inHeaders,omitempty"`  // name -> description
		OutHeaders misc.StringMap `json:"outHeaders,omitempty"` // name -> description

		PathParamsPattern any          `json:"pathParams"`
		PathParamsType    reflect.Type `json:"-"`

		QueryParamsPattern any          `json:"queryParams"`
		QueryParamsType    reflect.Type `json:"-"`

		Request  RequestParams  `json:"requestParams"`
		Response ResponseParams `json:"responseParams"`

		DBFields *db.FieldsList `json:"-"`
	}

	RequestParams struct {
		ParamsObject    `json:"object"`
		FlatModel       misc.StringMap    `json:"-"`                      // ключ - путь до поля, значение - db name
		BlankTemplate   misc.InterfaceMap `json:"-"`                      // Все поля, которые могут быть изменены, заполненные пустыми значениями, ключ - db name
		RequiredFields  misc.StringMap    `json:"-"`                      // обязательные поля, ключ - путь до поля, значение - db name
		ReadonlyFields  misc.StringMap    `json:"-"`                      // поля только на чтение, ключ - путь до поля, значение - db name
		UniqueKeyFields []string          `json:"requestUniqueKeyFields"` // уникальные поля, первый - primary key (формально)
	}

	ResponseParams struct {
		ParamsObject `json:"object"`
		SrcPattern   any          `json:"-"`
		SrcType      reflect.Type `json:"-"`
	}

	ParamsObject struct {
		Name        string       `json:"name"`
		ContentType string       `json:"contentType,omitempty"`
		Pattern     any          `json:"pattern"`
		Type        reflect.Type `json:"-"`
	}
)

const (
	NoFlags = Flags(0x00000000)

	FlagResponseHashed           = Flags(0x00000001)
	FlagRequestDontMakeFlatModel = Flags(0x00000002)
	FlagResponseIsNotArray       = Flags(0x00000004)
	FlagUDqueriesReturnsID       = Flags(0x00000008)
	FlagWithoutCU                = Flags(0x00000010)

	FlagChainDefault    = Flags(0x00000001)
	FlagChainEnableTail = Flags(0x00000002)

	// VarName
	VarIgnore = "_"
	VarID     = "ID"
	VarGUID   = "GUID"
	VarName   = "Name"
	VarStatus = "Status"

	// Имена тегов полей в описании query параметров
	TagJSON     = "json"
	TagDB       = db.TagDB      // Database field definition
	TagDBAlt    = db.TagDBAlt   // Database field definition (alternative)
	TagDefault  = db.TagDefault // default value
	TagSample   = "sample"      // Sample field value
	TagComment  = "comment"     // Field comment
	TagRequired = "required"    // Is field required
	TagReadonly = "readonly"    // Is field readonly
	TagRole     = "role"        // Field role (see Role* below)
	TagRef      = "ref"         // OpenAPI ref
	TagEnum     = "enum"        // OpenAPI enum content
	TagOA       = "oa"          // OpenAPI name
	TagOAtype   = "oaType"      // OpenAPI type
	TagOAformat = "oaFormat"    // OpenAPI format

	RolePrimary     = "primary"
	RoleKey         = "key"
	StdPrimaryField = VarID

	DefaultValueNull = db.DefaultValueNull
)

var (
	knownObjects = map[string]*Object{}
)

//----------------------------------------------------------------------------------------------------------------------------//

func (set *Set) Prepare() (err error) {
	msgs := misc.NewMessages()

	if set.Methods[stdhttp.MethodPUT] == nil && set.Methods[stdhttp.MethodPATCH] != nil {
		set.Methods[stdhttp.MethodPUT] = set.Methods[stdhttp.MethodPATCH].Clone()
	} else if set.Methods[stdhttp.MethodPATCH] == nil && set.Methods[stdhttp.MethodPUT] != nil {
		set.Methods[stdhttp.MethodPATCH] = set.Methods[stdhttp.MethodPUT].Clone()
	}

	for m, c := range set.Methods {
		if len(c.Chains) == 0 {
			delete(set.Methods, m)
			continue
		}

		err := c.Prepare(m)
		if err != nil {
			msgs.Add(`%s: %s`, m, err)
			continue
		}
	}

	return msgs.Error()
}

//----------------------------------------------------------------------------------------------------------------------------//

func (set *Set) Find(m string, path []string) (matched *Chain, pathParams any, result any, code int, err error) {
	c, exists := set.Methods[m]
	if !exists || len(c.Chains) == 0 {
		code = http.StatusMethodNotAllowed
		return
	}

	if len(path) == 1 {
		switch path[0] {
		case ".info":
			code = http.StatusOK
			result = set
			return
		}
	}

	matched, pathParams, code, err = c.Find(path)
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chains *Chains) Prepare(m string) (err error) {
	msgs := misc.NewMessages()
	defer func() {
		err = msgs.Error()
	}()

	if chains.prepared {
		msgs.Add("already prepared")
		return
	}

	err = chains.StdParams.Prepare(m, msgs)
	if err != nil {
		return
	}

	// Убираем nil цепочки

	dstI := 0

	for i := 0; i < len(chains.Chains); i++ {
		if chains.Chains[i] == nil {
			continue
		}

		if dstI == i {
			dstI++
			continue
		}

		chains.Chains[dstI] = chains.Chains[i]
		dstI++
	}

	if dstI != len(chains.Chains) {
		chains.Chains = chains.Chains[:dstI]
	}

	// Анализ цепочек

	for ci, chain := range chains.Chains {
		if reflect.DeepEqual(chain.Params, Params{}) {
			chain.Params = chains.StdParams
		} else {
			chain.Params.Prepare(m, msgs)
		}

		chain.Parent = chains

		if len(chain.Tokens) == 0 {
			msgs.Add("[%d] chain is empty", ci)
			continue
		}

		for ti, token := range chain.Tokens {
			if token.VarName == "" {
				msgs.Add(`[%d.%d] empty var name`, ci, ti)
				continue
			}

			if token.VarName != VarIgnore {
				field, exists := chain.Params.PathParamsType.FieldByName(token.VarName)
				if !exists {
					msgs.Add(`[%d.%d] field "%s" not found in PathParamsPattern`, ci, ti, token.VarName)
					continue
				}

				k := field.Type.Kind()
				switch k {
				default:
					msgs.Add(`[%d.%d] field "%s" is not a string or integer`, ci, ti, token.VarName)
					continue
				case reflect.String:
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				}
			}

			if token.Expr == "" && len(chain.Tokens) > 1 {
				msgs.Add("[%d.%d] an empty expression is allowed only in a chain with one element", ci, ti)
				continue
			}

			var re *regexp.Regexp
			re, err = regexp.Compile(`^(` + token.Expr + `)$`)
			if err != nil {
				msgs.Add("[%d.%d] %s", ci, ti, err)
				continue
			}
			chain.Tokens[ti].re = re
		}
	}

	if msgs.Len() > 0 {
		return
	}

	sort.Sort(chains)

	chains.prepared = true
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chains *Chains) Find(path []string) (matched *Chain, pathParams any, code int, err error) {
	code = 0
	msgs := misc.NewMessages()

	defer func() {
		err = msgs.Error()
		if err != nil {
			return
		}

		if matched == nil {
			c := chains.Chains[len(chains.Chains)-1]
			if c.Flags&FlagChainDefault == 0 {
				code = http.StatusNotFound
				return
			}

			matched = c
			code = 0
		}

		pp := reflect.New(matched.Params.PathParamsType)
		ppe := pp.Elem()

		for i, token := range matched.Tokens {
			if token.VarName == VarIgnore {
				continue
			}

			if i >= len(path) {
				break
			}

			err = misc.Iface2IfacePtr(path[i], ppe.FieldByName(token.VarName).Addr().Interface())
			if err != nil {
				msgs.AddError(err)
			}
		}

		pathParams = pp.Interface()

		err = msgs.Error()
		if err != nil {
			matched = nil
			return
		}
	}()

	if !chains.prepared {
		msgs.Add("not prepared")
		code = http.StatusInternalServerError
		return
	}

	ln := len(path)

	if ln == 0 &&
		len(chains.Chains[0].Tokens) == 1 && chains.Chains[0].Tokens[0].Expr == "" {
		// empty path -- OK
		matched = chains.Chains[0]
		return
	}

	for ci := 0; ci < len(chains.Chains); ci++ {
		chain := chains.Chains[ci]

		if len(chain.Tokens) < ln && chain.Flags&FlagChainEnableTail == 0 {
			continue
		}

		if len(chain.Tokens) > ln {
			return
		}

		for i, token := range chain.Tokens {
			if !token.re.MatchString(path[i]) {
				chain = nil
				break
			}
		}

		if chain != nil {
			matched = chain
			break
		}
	}

	if matched == nil {
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Implementing a sort interface for Chains

func (chains *Chains) Len() int {
	return len(chains.Chains)
}

func (chains *Chains) Less(i, j int) bool {
	if chains.Chains[i].Flags&FlagChainDefault != chains.Chains[j].Flags&FlagChainDefault {
		return chains.Chains[i].Flags&FlagChainDefault == 0
	}

	ln1 := len(chains.Chains[i].Tokens)
	ln2 := len(chains.Chains[j].Tokens)

	if ln1 != ln2 {
		return ln1 < ln2
	}

	if chains.Chains[i].Tokens[0].Expr == "" {
		return chains.Chains[j].Tokens[0].Expr != ""
	}

	return i < j
}

func (c *Chains) Swap(i, j int) {
	c.Chains[i], c.Chains[j] = c.Chains[j], c.Chains[i]
}

//----------------------------------------------------------------------------------------------------------------------------//

func GetKnownObjects() map[string]*Object {
	return knownObjects
}

//----------------------------------------------------------------------------------------------------------------------------//

func SaveObject(name string, obj reflect.Type, withArray bool, withCU bool) (err error) {
	if name == VarIgnore {
		return
	}

	r, exists := knownObjects[name]
	if !exists {
		r = &Object{
			Type:   obj,
			WithCU: withCU,
		}

		knownObjects[name] = r

	} else {
		if r.Type != obj {
			err = fmt.Errorf(`object "%s" already registered with another structure (%s != %s)`, name, obj, r.Type)
			return
		}

		if withCU {
			r.WithCU = true
		}
	}

	if !withArray || r.ArrayType != nil {
		return
	}

	r.ArrayType = reflect.MakeSlice(reflect.SliceOf(obj), 0, 0).Type()

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (set *Set) Clone() *Set {
	new := *set

	new.Methods = make(Methods, len(set.Methods))

	for name, df := range set.Methods {
		new.Methods[name] = df.Clone()
	}

	return &new
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chains *Chains) Clone() *Chains {
	new := *chains
	new.Chains = chains.Chains.Clone()

	return &new
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chainsList ChainsList) Clone() ChainsList {
	new := make(ChainsList, len(chainsList))

	for i, df := range chainsList {
		new[i] = df.Clone()
	}

	return new
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chain *Chain) Clone() *Chain {
	if chain == nil {
		return nil
	}

	new := *chain

	new.Tokens = make([]*Token, len(chain.Tokens))

	for i, df := range chain.Tokens {
		new.Tokens[i] = df.Clone()
	}

	return &new
}

//----------------------------------------------------------------------------------------------------------------------------//

func (token *Token) Clone() *Token {
	new := *token

	return &new
}

//----------------------------------------------------------------------------------------------------------------------------//

func StructType(v any) (t reflect.Type, err error) {
	t = reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		err = fmt.Errorf(`"%T" is not a struct or pointer to struct (%s)`, v, t.Kind())
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (p *Params) Prepare(m string, msgs *misc.Messages) (err error) {
	if p.PathParamsPattern == nil {
		p.PathParamsPattern = struct{}{}
	}

	p.PathParamsType, err = StructType(p.PathParamsPattern)
	if err != nil {
		msgs.Add("PathParamsPattern %s", err)
		return
	}

	if p.QueryParamsPattern != nil {
		p.QueryParamsType, err = StructType(p.QueryParamsPattern)
		if err != nil {
			msgs.Add("QueryParamsPattern %s", err)
			return
		}
	}

	if len(p.Request.UniqueKeyFields) == 0 {
		p.Request.UniqueKeyFields = make([]string, 1, 16) // [0] это primary key
	}

	if len(p.Request.RequiredFields) == 0 {
		p.Request.RequiredFields = make(misc.StringMap, 16)
	}

	if len(p.Request.ReadonlyFields) == 0 {
		p.Request.ReadonlyFields = make(misc.StringMap, 16)
	}

	if p.Request.Pattern != nil {
		if p.Request.Name == "" {
			msgs.Add("RequestObjectName not defined")
			return
		}

		if p.Request.ContentType == "" {
			p.Request.ContentType = stdhttp.ContentTypeJSON
		}

		p.Request.Type, err = StructType(p.Request.Pattern)
		if err != nil {
			msgs.Add("RequestPattern %s", err)
			return
		}

		if p.Flags&FlagRequestDontMakeFlatModel == 0 {
			err = p.MakeTypeFlatModel()
			if err != nil {
				msgs.Add("RequestPattern %s", err)
				return
			}
		}

		err = SaveObject(p.Request.Name, p.Request.Type, p.Flags&FlagResponseIsNotArray == 0, p.Flags&FlagWithoutCU == 0)
		if err != nil {
			msgs.AddError(err)
			return
		}
	}

	if p.Response.Pattern != nil {
		if p.Response.Name == "" {
			msgs.Add("ResponseParams.Name not defined")
			return
		}

		p.Response.Type, err = StructType(p.Response.Pattern)
		if err != nil {
			msgs.Add("ResponsePattern %s", err)
			return
		}

		err = SaveObject(p.Response.Name, p.Response.Type, p.Flags&FlagResponseIsNotArray == 0, false)
		if err != nil {
			msgs.AddError(err)
			return
		}

		if p.Response.SrcPattern != nil {
			p.Response.SrcType, err = StructType(p.Response.SrcPattern)
			if err != nil {
				msgs.Add("SrcResponsePattern %s", err)
				return
			}
		}
	}

	switch m {
	case stdhttp.MethodGET:
		dbPattern := p.Response.SrcPattern
		if dbPattern == nil {
			dbPattern = p.Response.Pattern
		}

		if dbPattern != nil {
			p.DBFields, err = db.MakeFieldsList(dbPattern)
			if err != nil {
				msgs.Add("Response.Pattern %s", err)
				return
			}
		}

	default:
		if p.Request.Pattern != nil {
			p.DBFields, err = db.MakeFieldsList(p.Request.Pattern)
			if err != nil {
				msgs.Add("Request.Pattern %s", err)
				return
			}
		}
	}
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

var (
	specialTypes = misc.BoolMap{
		reflect.TypeOf(time.Time{}).String():      true,
		reflect.TypeOf(db.NullFloat64{}).String(): true,
		reflect.TypeOf(db.NullInt64{}).String():   true,
		reflect.TypeOf(db.NullUint64{}).String():  true,
		reflect.TypeOf(db.NullString{}).String():  true,
		reflect.TypeOf(db.NullTime{}).String():    true,
	}
)

func (p *Params) MakeTypeFlatModel() (err error) {
	p.Request.FlatModel = make(misc.StringMap, 64)
	p.Request.BlankTemplate = make(misc.InterfaceMap, 64)

	err = p.typeFlatModelIterator(db.Tag(), "", &p.Request.FlatModel, &p.Request.BlankTemplate, p.Request.Type)
	if err != nil {
		return
	}

	return
}

func (p *Params) typeFlatModelIterator(tagDB string, base string, model *misc.StringMap, blank *misc.InterfaceMap, t reflect.Type) (err error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	ln := t.NumField()

	for i := 0; i < ln; i++ {
		f := t.Field(i)

		if !f.IsExported() {
			continue
		}

		dbName := misc.StructTagName(&f, tagDB)
		if dbName == "-" {
			dbTags := misc.StructTagOpts(&f, tagDB)
			if dbName = dbTags["clean"]; dbName == "" {
				continue
			}
		}

		fName := misc.StructTagName(&f, TagJSON)
		if fName == "-" {
			fName = f.Name
		}

		if f.Anonymous {
			fName = ""
		}

		if base != "" {
			if fName == "" {
				fName = base
			} else {
				fName = base + "." + fName
			}
		}

		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		switch ft.Kind() {
		case reflect.Slice, reflect.Array,
			reflect.Invalid, reflect.Chan, reflect.Func, reflect.Map:
			err = fmt.Errorf("unsupported type %s", f.Type.String())
			return

		case reflect.Struct:
			if _, exists := specialTypes[f.Type.String()]; !exists {
				err = p.typeFlatModelIterator(tagDB, fName, model, blank, f.Type)
				if err != nil {
					return
				}
				continue
			}

			fallthrough

		default:
			if f.Tag.Get(TagRole) == RolePrimary && base == "" { // Предполагается только на первом уровне
				if p.Request.UniqueKeyFields[0] != "" {
					err = fmt.Errorf(`duplicated primary key: "%s" and "%s"`, p.Request.UniqueKeyFields[0], fName)
					return
				}
				p.Request.UniqueKeyFields[0] = fName
			}

			if f.Tag.Get(TagRole) == RoleKey {
				p.Request.UniqueKeyFields = append(p.Request.UniqueKeyFields, fName)
			}

			if f.Tag.Get(TagRequired) == "true" {
				p.Request.RequiredFields[fName] = dbName
			}

			if f.Tag.Get(TagReadonly) == "true" {
				p.Request.ReadonlyFields[fName] = dbName
				continue
			}

			(*model)[fName] = dbName

			defVal := f.Tag.Get(TagDefault)
			if defVal == DefaultValueNull {
				(*blank)[dbName] = nil
			} else {
				(*blank)[dbName] = reflect.New(ft).Elem().Interface()
			}
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (p *Params) ExtractFieldsFromBody(body []byte) (fieldsSlice []misc.InterfaceMap, allMessages [][]string, err error) {
	if len(body) == 0 {
		err = fmt.Errorf("empty body")
		return
	}

	if p.Request.FlatModel == nil {
		err = fmt.Errorf("chain doesn't have a request body")
		return
	}

	if len(p.Request.FlatModel) == 0 {
		return
	}

	var data []map[string]any
	err = jsonw.Unmarshal(body, &data) // Все структуры, включая вложенные, получатся как map[string]any
	if err != nil {
		err = fmt.Errorf("unmarshal: %s", err)
		return
	}

	fieldsSlice = make([]misc.InterfaceMap, 0, len(data))

	allMessages = make([][]string, len(data))

	for i, objMap := range data {
		fields := make(misc.InterfaceMap, len(p.Request.FlatModel))
		messages := p.extractFieldsFromBodyIterator("", &fields, objMap)
		if len(messages) > 0 {
			allMessages[i] = messages
		}
		fieldsSlice = append(fieldsSlice, fields)
	}
	return
}

func (p *Params) extractFieldsFromBodyIterator(base string, fields *misc.InterfaceMap, m misc.InterfaceMap) (messages []string) {
	for fName, v := range m {
		if base != "" {
			fName = base + "." + fName
		}

		switch v := v.(type) {
		case map[string]any:
			m := p.extractFieldsFromBodyIterator(fName, fields, misc.InterfaceMap(v))
			if len(m) > 0 {
				messages = append(messages, m...)
			}

		default:
			dbName, exists := p.Request.FlatModel[fName]
			if !exists {
				messages = append(messages, fmt.Sprintf(`unknown field "%s"`, fName))
				continue
			}
			if dbName == "" {
				dbName = fName
			}
			(*fields)[dbName] = v
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//
