package pathparser

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/alrusov/misc"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	ChainSet struct {
		Description string `json:"description"`
		Set         Map    `json:"set"`
	}

	Map map[For]*Chains

	For uint

	Chains struct {
		Description string `json:"description"`
		prepared    bool
		Chains      []Chain `json:"chains"`
		knownVars   misc.BoolMap
	}

	Chain struct {
		Description string  `json:"description"`
		Name        string  `json:"-"`
		List        []Token `json:"list"`
	}

	Token struct {
		Expr    string `json:"expr"`
		VarName string `json:"-"`
		re      *regexp.Regexp
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
	if !exists {
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

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) Prepare() (err error) {
	if c.prepared {
		return fmt.Errorf("already prepared")
	}

	msgs := misc.NewMessages()

	c.knownVars = make(misc.BoolMap, 16)

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

			c.knownVars[token.VarName] = true

			if token.Expr == "" && len(chain.List) > 1 {
				msgs.Add("[%d.%d] an empty expression is allowed only in a chain of one element", ci, ti)
				continue
			}

			var re *regexp.Regexp
			re, err = regexp.Compile(`^` + token.Expr + `$`)
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

	return strings.Compare(c.Chains[i].List[0].Expr, c.Chains[j].List[0].Expr) < 0
}

func (c *Chains) Swap(i, j int) {
	c.Chains[i], c.Chains[j] = c.Chains[j], c.Chains[i]
}

//----------------------------------------------------------------------------------------------------------------------------//
