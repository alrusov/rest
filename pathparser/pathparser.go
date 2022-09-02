package pathparser

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"

	"github.com/alrusov/misc"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	ChainSet struct {
		Description string `json:"description"`
		Set         Map    `json:"set"`
		Flags       uint64 `json:"-"`
	}

	Map map[For]*Chains

	For uint

	Chains struct {
		Description string `json:"description"`
		prepared    bool
		Chains      []Chain `json:"chains"`
		Flags       uint64  `json:"-"`
		knownVars   map[string]reflect.Kind
	}

	Chain struct {
		Description string  `json:"description"`
		Name        string  `json:"name"`
		Scope       string  `json:"scope,omitempty"`
		List        []Token `json:"list"`
		Flags       uint64  `json:"-"`
	}

	Token struct {
		Expr       string      `json:"expr"`
		VarName    string      `json:"varName"`
		ValPattern interface{} `json:"varPattern"`
		Flags      uint64      `json:"-"`
		re         *regexp.Regexp
	}

	Vars misc.InterfaceMap
)

const (
	Create For = iota
	Get
	Replace
	Modify
	Delete
)

var (
	forNames = map[For]string{
		Create:  "Create",
		Get:     "Get",
		Replace: "Replace",
		Modify:  "Modify",
		Delete:  "Delete",
	}
)

//----------------------------------------------------------------------------------------------------------------------------//

func (f For) Name() string {
	return forNames[f]
}

//----------------------------------------------------------------------------------------------------------------------------//

func (cs *ChainSet) Prepare() (err error) {
	msgs := misc.NewMessages()

	for f, c := range cs.Set {
		err := c.Prepare()
		if err != nil {
			msgs.Add(`%s: %s`, f.Name(), err)
		}
	}

	return msgs.Error()
}

func (cs *ChainSet) Do(f For, path []string, vars Vars) (matched *Chain, result interface{}, err error) {
	c, exists := cs.Set[f]
	if !exists || len(c.Chains) == 0 {
		return
	}

	if len(path) == 1 {
		switch path[0] {
		case ".info":
			list := make(map[string]*Chains, len(cs.Set))

			for f, c := range cs.Set {
				list[f.Name()] = c
			}

			result = list
			return
		}
	}

	matched, err = c.Do(path, vars)
	return
}

func (cs *ChainSet) Do2(f For, path []string) (matched *Chain, result interface{}, vars Vars, err error) {
	c, exists := cs.Set[f]
	if !exists || len(c.Chains) == 0 {
		return
	}

	vars = make(Vars, len(c.knownVars))
	for n, k := range c.knownVars {
		switch k {
		case reflect.Int64:
			v := int64(0)
			vars[n] = &v
		case reflect.Uint64:
			v := uint64(0)
			vars[n] = &v
		case reflect.String:
			v := ""
			vars[n] = &v
		}
	}

	matched, result, err = cs.Do(f, path, vars)

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) Prepare() (err error) {
	if c.prepared {
		return fmt.Errorf("already prepared")
	}

	msgs := misc.NewMessages()

	c.knownVars = make(map[string]reflect.Kind, 16)

	for ci, chain := range c.Chains {
		if len(chain.List) == 0 {
			msgs.Add("[%d] chain is empty", ci)
			continue
		}

		for ti, token := range chain.List {
			if token.VarName == "" {
				msgs.Add(`[%d.%d] empty var name for "%s"`, ci, ti, token.Expr)
				continue
			}

			k := reflect.ValueOf(token.ValPattern).Kind()
			if kk, exists := c.knownVars[token.VarName]; exists && kk != k {
				msgs.Add(`[%d.%d] variable "%s" defined as %s and %s in different chains`, ci, ti, token.VarName, k, kk)
				continue
			}
			c.knownVars[token.VarName] = k

			if token.Expr == "" && len(chain.List) > 1 {
				msgs.Add("[%d.%d] an empty expression is allowed only in a chain of one element", ci, ti)
				continue
			}

			var re *regexp.Regexp
			re, err = regexp.Compile(`^(` + token.Expr + `)$`)
			if err != nil {
				msgs.Add("[%d.%d] %s", ci, ti, err)
				continue
			}
			chain.List[ti].re = re
		}
	}

	err = msgs.Error()
	if err != nil {
		return
	}

	sort.Sort(c)

	c.prepared = true
	return

}

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) Do(path []string, vars Vars) (matched *Chain, err error) {
	if !c.prepared {
		err = fmt.Errorf("not prepared")
		return
	}

	msgs := misc.NewMessages()

	for name, x := range vars {
		if _, exists := c.knownVars[name]; !exists {
			msgs.Add(`unknown variable "%s"`, name)
		}

		v := reflect.ValueOf(x)
		if v.Kind() != reflect.Ptr {
			msgs.Add(`%s: "%#v" is not a pointer`, name, x)
		}
	}

	for name := range c.knownVars {
		if _, exists := vars[name]; !exists {
			msgs.Add(`not handled variable "%s"`, name)
		}
	}

	err = msgs.Error()
	if err != nil {
		return
	}

	ln := len(path)

	if ln == 0 &&
		len(c.Chains[0].List) == 1 && c.Chains[0].List[0].Expr == "" {
		// Пустой путь
		matched = &c.Chains[0]
		return
	}

	for ci := 0; ci < len(c.Chains); ci++ {
		chain := &c.Chains[ci]

		if len(chain.List) < ln {
			continue
		}

		if len(chain.List) > ln {
			// Не найдено
			return
		}

		for i, token := range chain.List {
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
		// Не найдено
		return
	}

	for i, token := range matched.List {
		err = misc.Iface2IfacePtr(path[i], vars[token.VarName])
		if err != nil {
			msgs.AddError(err)
		}
	}

	err = msgs.Error()
	if err != nil {
		matched = nil
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Implementing a sort interface for Chains

func (c *Chains) Len() int {
	return len(c.Chains)
}

func (c *Chains) Less(i, j int) bool {
	ln1 := len(c.Chains[i].List)
	ln2 := len(c.Chains[j].List)

	if ln1 != ln2 {
		return ln1 < ln2
	}

	if c.Chains[i].List[0].Expr == "" {
		return c.Chains[j].List[0].Expr != ""
	}

	return i < j
}

func (c *Chains) Swap(i, j int) {
	c.Chains[i], c.Chains[j] = c.Chains[j], c.Chains[i]
}

//----------------------------------------------------------------------------------------------------------------------------//

func (vars Vars) Int(name string) (v int64, err error) {
	x, exists := vars[name]
	if !exists {
		err = fmt.Errorf(`unknown variable "%s"`, name)
		return
	}

	return misc.Iface2Int(x)
}

func (vars Vars) Uint(name string) (v uint64, err error) {
	x, exists := vars[name]
	if !exists {
		err = fmt.Errorf(`unknown variable "%s"`, name)
		return
	}

	return misc.Iface2Uint(x)
}

func (vars Vars) String(name string) (v string, err error) {
	x, exists := vars[name]
	if !exists {
		err = fmt.Errorf(`unknown variable "%s"`, name)
		return
	}

	return misc.Iface2String(x)
}

//----------------------------------------------------------------------------------------------------------------------------//
