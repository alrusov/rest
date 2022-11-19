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
		Flags       Flags      `json:"flags,omitempty"`
		Summary     string     `json:"summary"`
		Description string     `json:"description"`
		Chains      ChainsList `json:"chains"`

		PathParamsPattern any          `json:"pathParams"`
		PathParamsType    reflect.Type `json:"-"`

		QueryParamsPattern any          `json:"queryParams"`
		QueryParamsType    reflect.Type `json:"-"`

		RequestObjectName      string         `json:"requestObjectName"`
		RequestContentType     string         `json:"requestContentType,omitempty"` // Значение по умолчанию для Content-Type на входе. По умолчанию JSON
		RequestPattern         any            `json:"request"`
		RequestType            reflect.Type   `json:"-"`
		RequestFlatModel       misc.StringMap `json:"-"`                      // ключ - путь до поля, значение - его tag db
		RequestUniqueKeyFields []string       `json:"requestUniqueKeyFields"` // уникальные поля, первый - primary key (формально)

		ResponseObjectName  string       `json:"responseObjectName"`
		ResponseContentType string       `json:"responseContentType,omitempty"` // Значение по умолчанию для Content-Type на выходе. По умолчанию JSON
		ResponsePattern     any          `json:"response"`
		ResponseType        reflect.Type `json:"-"`
		SrcResponsePattern  any          `json:"-"`
		SrcResponseType     reflect.Type `json:"-"`

		DBFields []string `json:"-"`

		prepared bool
	}

	ChainsList []*Chain

	Chain struct {
		Parent      *Chains  `json:"-"`
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
	FlagRequestIsNotArray        = Flags(0x00000002)
	FlagRequestDontMakeFlatModel = Flags(0x00000004)
	FlagResponseIsNotArray       = Flags(0x00000008)

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
	TagRole     = "role"
	TagRef      = "ref"
	TagDefault  = "default"

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

		err := c.Prepare()
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

func (chains *Chains) Prepare() (err error) {
	msgs := misc.NewMessages()
	defer func() {
		err = msgs.Error()
	}()

	if chains.prepared {
		msgs.Add("already prepared")
		return
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

	if chains.RequestPattern != nil {
		if chains.RequestObjectName == "" {
			msgs.Add("RequestObjectName not defined")
			return
		}

		chains.RequestType, err = StructType(chains.RequestPattern)
		if err != nil {
			msgs.Add("RequestPattern %s", err)
			return
		}

		if chains.Flags&FlagRequestDontMakeFlatModel == 0 {
			err = chains.makeTypeFlatModel()
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

		dbPattern := chains.SrcResponsePattern
		if dbPattern == nil {
			dbPattern = chains.ResponsePattern
		}

		chains.DBFields, err = db.FieldsList(dbPattern)
		if err != nil {
			msgs.Add("ResponsePattern %s", err)
			return
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
	defer func() {
		err = msgs.Error()
	}()

	if !chains.prepared {
		msgs.Add("not prepared")
		code = http.StatusInternalServerError
		return
	}

	pp := reflect.New(chains.PathParamsType)

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

		if len(chain.Tokens) < ln {
			continue
		}

		if len(chain.Tokens) > ln {
			code = http.StatusNotFound
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
		code = http.StatusNotFound
		return
	}

	ppp := pp.Elem()

	for i, token := range matched.Tokens {
		if token.VarName == VarIgnore {
			continue
		}

		err = misc.Iface2IfacePtr(path[i], ppp.FieldByName(token.VarName).Addr().Interface())
		if err != nil {
			msgs.AddError(err)
		}
	}

	pathParams = pp.Interface()

	err = msgs.Error()
	if err != nil {
		code = http.StatusNotFound
		matched = nil
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

func (chains *Chains) makeTypeFlatModel() (err error) {
	chains.RequestFlatModel = make(misc.StringMap, 32)

	err = chains.typeFlatModelIterator("", &chains.RequestFlatModel, chains.RequestType)
	if err != nil {
		return
	}

	if chains.RequestUniqueKeyFields[0] == "" {
		err = fmt.Errorf("undefiled request primary key field")
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

		dbName := misc.StructFieldName(&f, TagDB)
		if dbName == "-" {
			continue
		}

		fName := misc.StructFieldName(&f, TagJSON)
		if fName == "-" {
			fName = f.Name
		}

		if base != "" {
			fName = base + "." + fName
		}

		switch f.Type.Kind() {
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
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (chains *Chains) ExtractFieldsFromBody(body []byte) (fields misc.InterfaceMap, notice error, err error) {
	if chains.RequestFlatModel == nil || len(chains.RequestFlatModel) == 0 {
		err = fmt.Errorf("chain doesn't have a request body")
		return
	}

	if len(body) == 0 {
		err = fmt.Errorf("empty body")
		return
	}

	var obj any
	err = jsonw.Unmarshal(body, &obj) // Все структуры, включая вложенные, получатся как map[string]any
	if err != nil {
		err = fmt.Errorf("unmarshal: %s", err)
		return
	}

	fields = make(misc.InterfaceMap, len(chains.RequestFlatModel))
	msgs := misc.NewMessages()
	err = chains.extractFieldsFromBodyIterator("", &fields, obj, msgs)
	notice = msgs.Error()
	return
}

func (chains *Chains) extractFieldsFromBodyIterator(base string, fields *misc.InterfaceMap, obj any, msgs *misc.Messages) (err error) {
	m, ok := obj.(map[string]any)
	if !ok {
		// Нестандартный формат, пусть дальше сами разбираются
		return
	}

	for fName, v := range m {
		if base != "" {
			fName = base + "." + fName
		}

		switch v.(type) {
		case map[string]any:
			err = chains.extractFieldsFromBodyIterator(fName, fields, v, msgs)
			if err != nil {
				return
			}

		default:
			dbName, exists := chains.RequestFlatModel[fName]
			if !exists {
				msgs.Add(`unknown field "%s"`, fName)
				continue
			}
			(*fields)[dbName] = v
		}
	}
	/*
		v := reflect.ValueOf(obj)

		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}

		ln := t.NumField()

		for i := 0; i < ln; i++ {
			f := t.Field(i)

			if !f.IsExported() {
				continue
			}

			if f.Tag.Get(TagDB) == "-" {
				continue
			}

			fName := f.Tag.Get(TagJSON)
			if fName == "" {
				fName = f.Name
			} else {
				fName = strings.TrimSpace(strings.Split(fName, ",")[0])
			}
			if base != "" {
				fName = base + "." + fName
			}

			switch f.Type.Kind() {
			default:
				dbName := f.Tag.Get(TagDB)
				if dbName == "" {
					dbName = fName
				}
				(*model)[fName] = dbName

			case reflect.Struct:
				typeFlatModelIterator(fName, model, f.Type)

			case reflect.Slice, reflect.Array,
				reflect.Invalid, reflect.Chan, reflect.Func, reflect.Map:
				err = fmt.Errorf("unsupported type %s", f.Type.String())
				return
			}
		}
	*/

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
		t = reflect.TypeOf(t.Elem())
	}

	if t.Kind() != reflect.Struct {
		err = fmt.Errorf(`"%T" is not a struct or pointer to struct`, v)
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//
