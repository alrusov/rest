package path

import (
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/alrusov/db"
	"github.com/alrusov/jsonw"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	Set struct {
		Flags       Flags   `json:"flags,omitempty"`
		Description string  `json:"description"`
		Methods     Methods `json:"methods"`
	}

	Methods map[Method]*Chains

	Method = string

	Chains struct {
		Flags       Flags          `json:"flags,omitempty"`
		InHeaders   misc.StringMap `json:"inHeaders,omitempty"`  // name -> description
		OutHeaders  misc.StringMap `json:"outHeaders,omitempty"` // name -> description
		Summary     string         `json:"summary"`
		Description string         `json:"description"`
		Chains      ChainsList     `json:"chains"`

		PathParamsPattern any          `json:"pathParams"`
		PathParamsType    reflect.Type `json:"-"`

		QueryParamsPattern any          `json:"queryParams"`
		QueryParamsType    reflect.Type `json:"-"`

		RequestObjectName      string         `json:"requestObjectName"`
		RequestContentType     string         `json:"requestContentType,omitempty"` // Значение по умолчанию для Content-Type на входе. По умолчанию JSON
		RequestPattern         any            `json:"request"`
		RequestType            reflect.Type   `json:"-"`
		RequestFlatModel       misc.StringMap `json:"-"`                      // ключ - путь до поля, значение - его tag db
		RequestRequiredFields  misc.StringMap `json:"-"`                      // обязательные поля, значение - его tag db
		RequestReadonlyFields  misc.StringMap `json:"-"`                      // поля только на чтение, значение - его tag db
		RequestUniqueKeyFields []string       `json:"requestUniqueKeyFields"` // уникальные поля, первый - primary key (формально)

		ResponseObjectName  string       `json:"responseObjectName"`
		ResponseContentType string       `json:"responseContentType,omitempty"` // Значение по умолчанию для Content-Type на выходе. По умолчанию JSON
		ResponsePattern     any          `json:"response"`
		ResponseType        reflect.Type `json:"-"`
		SrcResponsePattern  any          `json:"-"`
		SrcResponseType     reflect.Type `json:"-"`

		DBFields *db.FieldsList `json:"-"`

		prepared bool
	}

	ChainsList []*Chain

	Chain struct {
		Parent      *Chains  `json:"-"`
		Flags       Flags    `json:"flags,omitempty"`
		Description string   `json:"description"`
		Name        string   `json:"name"`
		Scope       string   `json:"scope,omitempty"`
		Tokens      []*Token `json:"tokens"`
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
	}

	Vars misc.InterfaceMap

	Flags uint64
)

const (
	NoFlags = Flags(0x00000000)

	FlagResponseHashed           = Flags(0x00000001)
	FlagRequestDontMakeFlatModel = Flags(0x00000002)
	FlagResponseIsNotArray       = Flags(0x00000004)
	FlagCreateReturnsObject      = Flags(0x00000008)

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
	TagDB       = "db"
	TagSample   = "sample"
	TagComment  = "comment"
	TagRequired = "required"
	TagReadonly = "readonly"
	TagRole     = "role"
	TagRef      = "ref"
	TagDefault  = "default"
	TagEnum     = "enum"
	TagOAtype   = "oaType"
	TagOAformat = "oaFormat"

	RolePrimary     = "primary"
	RoleKey         = "key"
	StdPrimaryField = VarID
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

func (set *Set) Find(m Method, path []string) (matched *Chain, pathParams any, result any, code int, err error) {
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

	if chains.Flags&FlagResponseHashed != 0 {
		if chains.OutHeaders == nil {
			chains.OutHeaders = make(misc.StringMap, 8)
		}
		chains.OutHeaders[stdhttp.HTTPheaderHash] = "Response body hash"
	}

	if chains.PathParamsPattern == nil {
		chains.PathParamsPattern = struct{}{}
	}

	chains.PathParamsType, err = StructType(chains.PathParamsPattern)
	if err != nil {
		msgs.Add("PathParamsPattern %s", err)
		return
	}

	if chains.QueryParamsPattern != nil {
		chains.QueryParamsType, err = StructType(chains.QueryParamsPattern)
		if err != nil {
			msgs.Add("QueryParamsPattern %s", err)
			return
		}
	}

	if len(chains.RequestUniqueKeyFields) == 0 {
		chains.RequestUniqueKeyFields = make([]string, 1, 16) // [0] это primary key
	}

	if len(chains.RequestRequiredFields) == 0 {
		chains.RequestRequiredFields = make(misc.StringMap, 16)
	}

	if len(chains.RequestReadonlyFields) == 0 {
		chains.RequestReadonlyFields = make(misc.StringMap, 16)
	}

	if chains.RequestPattern != nil {
		if chains.RequestObjectName == "" {
			msgs.Add("RequestObjectName not defined")
			return
		}

		if chains.RequestContentType == "" {
			chains.RequestContentType = stdhttp.ContentTypeJSON
		}

		chains.RequestType, err = StructType(chains.RequestPattern)
		if err != nil {
			msgs.Add("RequestPattern %s", err)
			return
		}

		if chains.Flags&FlagRequestDontMakeFlatModel == 0 {
			err = chains.MakeTypeFlatModel()
			if err != nil {
				msgs.Add("RequestPattern %s", err)
				return
			}
		}

		err = SaveObject(chains.RequestObjectName, chains.RequestType, chains.Flags&FlagResponseIsNotArray == 0)
		if err != nil {
			msgs.AddError(err)
			return
		}
	}

	if chains.ResponsePattern != nil {
		if chains.ResponseObjectName == "" {
			msgs.Add("ResponseObjectName not defined")
			return
		}

		chains.ResponseType, err = StructType(chains.ResponsePattern)
		if err != nil {
			msgs.Add("ResponsePattern %s", err)
			return
		}

		err = SaveObject(chains.ResponseObjectName, chains.ResponseType, chains.Flags&FlagResponseIsNotArray == 0)
		if err != nil {
			msgs.AddError(err)
			return
		}

		if chains.SrcResponsePattern != nil {
			chains.SrcResponseType, err = StructType(chains.SrcResponsePattern)
			if err != nil {
				msgs.Add("SrcResponsePattern %s", err)
				return
			}
		}
	}

	switch m {
	case stdhttp.MethodGET:
		dbPattern := chains.SrcResponsePattern
		if dbPattern == nil {
			dbPattern = chains.ResponsePattern
		}

		if dbPattern != nil {
			chains.DBFields, err = db.MakeFieldsList(dbPattern)
			if err != nil {
				msgs.Add("ResponsePattern %s", err)
				return
			}
		}

	default:
		if chains.RequestPattern != nil {
			chains.DBFields, err = db.MakeFieldsList(chains.RequestPattern)
			if err != nil {
				msgs.Add("RequestPattern %s", err)
				return
			}
		}
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
				field, exists := chains.PathParamsType.FieldByName(token.VarName)
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

	pp := reflect.New(chains.PathParamsType)

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

		ppp := pp.Elem()

		for i, token := range matched.Tokens {
			if token.VarName == VarIgnore {
				continue
			}

			if i >= len(path) {
				break
			}

			err = misc.Iface2IfacePtr(path[i], ppp.FieldByName(token.VarName).Addr().Interface())
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
		pathParams = pp.Interface()
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

func (chains *Chains) MakeTypeFlatModel() (err error) {
	chains.RequestFlatModel = make(misc.StringMap, 64)

	err = chains.typeFlatModelIterator("", &chains.RequestFlatModel, chains.RequestType)
	if err != nil {
		return
	}

	return
}

func (chains *Chains) typeFlatModelIterator(base string, model *misc.StringMap, t reflect.Type) (err error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	ln := t.NumField()

	for i := 0; i < ln; i++ {
		f := t.Field(i)

		if !f.IsExported() {
			continue
		}

		dbName := misc.StructTagName(&f, TagDB)
		if dbName == "-" {
			continue
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
				err = chains.typeFlatModelIterator(fName, model, f.Type)
				if err != nil {
					return
				}
				continue
			}

			fallthrough

		default:
			(*model)[fName] = dbName

			if f.Tag.Get(TagRole) == RolePrimary && base == "" { // Предполагается только на первом уровне
				if chains.RequestUniqueKeyFields[0] != "" {
					err = fmt.Errorf(`duplicated primary key: "%s" and "%s"`, chains.RequestUniqueKeyFields[0], fName)
					return
				}
				chains.RequestUniqueKeyFields[0] = fName
			}

			if f.Tag.Get(TagRole) == RoleKey {
				chains.RequestUniqueKeyFields = append(chains.RequestUniqueKeyFields, fName)
			}

			if misc.StructTagName(&f, TagRequired) == "true" {
				chains.RequestRequiredFields[fName] = dbName
			}

			if misc.StructTagName(&f, TagReadonly) == "true" {
				chains.RequestReadonlyFields[fName] = dbName
			}

		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chains *Chains) ExtractFieldsFromBody(body []byte) (fieldsSlice []misc.InterfaceMap, err error) {
	if chains.RequestFlatModel == nil || len(chains.RequestFlatModel) == 0 {
		err = fmt.Errorf("chain doesn't have a request body")
		return
	}

	if len(body) == 0 {
		err = fmt.Errorf("empty body")
		return
	}

	var data any
	err = jsonw.Unmarshal(body, &data) // Все структуры, включая вложенные, получатся как map[string]any
	if err != nil {
		err = fmt.Errorf("unmarshal: %s", err)
		return
	}

	var dataSlice []any

	var ok bool
	dataSlice, ok = data.([]any)
	if !ok {
		err = fmt.Errorf("body is %T, expected %T", data, dataSlice)
		return
	}

	fieldsSlice = make([]misc.InterfaceMap, 0, len(dataSlice))

	for i, obj := range dataSlice {
		objMap, ok := obj.(map[string]any)
		if !ok {
			err = fmt.Errorf("body[%d] is %T, expected %T", i, obj, misc.InterfaceMap(objMap))
			return
		}

		fields := make(misc.InterfaceMap, len(chains.RequestFlatModel))

		err = chains.extractFieldsFromBodyIterator("", &fields, objMap)
		if err != nil {
			err = fmt.Errorf("body[%d] %s", i, err)
			return
		}

		fieldsSlice = append(fieldsSlice, fields)
	}
	return
}

func (chains *Chains) extractFieldsFromBodyIterator(base string, fields *misc.InterfaceMap, m misc.InterfaceMap) (err error) {
	for fName, v := range m {
		if base != "" {
			fName = base + "." + fName
		}

		switch v := v.(type) {
		case map[string]any:
			err = chains.extractFieldsFromBodyIterator(fName, fields, misc.InterfaceMap(v))
			if err != nil {
				return
			}

		default:
			dbName, exists := chains.RequestFlatModel[fName]
			if !exists {
				continue
			}
			(*fields)[dbName] = v
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func GetKnownObjects() map[string]*Object {
	return knownObjects
}

//----------------------------------------------------------------------------------------------------------------------------//

func SaveObject(name string, obj reflect.Type, withArray bool) (err error) {
	if name == VarIgnore {
		return
	}

	r, exists := knownObjects[name]

	if exists && r.Type != obj {
		err = fmt.Errorf(`object "%s" already registered with another structure (%s != %s)`, name, obj, r.Type)
		return
	}

	objArray := reflect.Type(nil)
	if withArray {
		objArray = reflect.MakeSlice(reflect.SliceOf(obj), 0, 0).Type()
	} else if exists {
		objArray = r.ArrayType
	}

	knownObjects[name] = &Object{
		Type:      obj,
		ArrayType: objArray,
	}
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
