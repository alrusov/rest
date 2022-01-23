package pathparser

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/alrusov/misc"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	Chains struct {
		mutex     *sync.RWMutex
		prepared  bool
		list      []*chain
		knownVars misc.BoolMap
	}

	chain struct {
		tokens []*token
	}

	token struct {
		expr string
		dest string
		re   *regexp.Regexp
	}

	Vars misc.InterfaceMap
)

//----------------------------------------------------------------------------------------------------------------------------//

func NewChains(capacity int) *Chains {
	return &Chains{
		mutex:     new(sync.RWMutex),
		prepared:  false,
		list:      make([]*chain, 0, capacity),
		knownVars: make(misc.BoolMap, 16),
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) NewChain(capacity int) (idx int, err error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.prepared {
		return -1, fmt.Errorf("already prepared")
	}

	idx = len(c.list)
	c.list = append(c.list,
		&chain{
			tokens: make([]*token, 0, capacity),
		},
	)

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) Add(chainIdx int, expr string, dest string) (err error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.prepared {
		return fmt.Errorf("already prepared")
	}

	if chainIdx < 0 || chainIdx >= len(c.list) {
		return fmt.Errorf(`illegal idx=%d`, chainIdx)
	}

	if dest == "" {
		return fmt.Errorf(`empty dest for "%s"`, expr)
	}

	c.list[chainIdx].tokens = append(c.list[chainIdx].tokens,
		&token{
			expr: expr,
			dest: dest,
		},
	)

	c.knownVars[dest] = true

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) Prepare() (err error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.prepared {
		return fmt.Errorf("already prepared")
	}

	if len(c.list) == 0 {
		return fmt.Errorf("is empty")
	}

	c.prepared = true

	msgs := misc.NewMessages()

	for ci, chain := range c.list {
		if len(chain.tokens) == 0 {
			msgs.Add("[%d] chain is empty", ci)
		}
		for ti, token := range chain.tokens {
			if token.expr == "" && len(chain.tokens) > 1 {
				msgs.Add("[%d.%d] an empty expression is allowed only in a chain of one element", ci, ti)
				continue
			}
			token.re, err = regexp.Compile(`^` + token.expr + `$`)
			if err != nil {
				msgs.Add("[%d.%d] %s", ci, ti, err)
				continue
			}
		}
	}

	err = msgs.Error()
	if err != nil {
		return
	}

	sort.Sort(c)

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (c *Chains) Do(path []string, vars Vars) (found bool, err error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

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
		len(c.list[0].tokens) == 1 &&
		c.list[0].tokens[0].expr == "" {
		// Пустой путь
		found = true
		return
	}

	var matched *chain

	for _, chain := range c.list {
		if len(chain.tokens) < ln {
			continue
		}

		if len(chain.tokens) > ln {
			// Не найдено
			return
		}

		matched = chain

		for i, token := range chain.tokens {
			if !token.re.MatchString(path[i]) {
				matched = nil
				break
			}
		}

		if matched != nil {
			break
		}
	}

	if matched == nil {
		// Не найдено
		return
	}

	for i, token := range matched.tokens {
		err = misc.Iface2IfacePtr(path[i], vars[token.dest])
		if err != nil {
			msgs.AddError(err)
		}
	}

	err = msgs.Error()
	if err != nil {
		return
	}

	found = true
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

// Implementing a sort interface for Chains

func (c *Chains) Len() int {
	return len(c.list)
}

func (c *Chains) Less(i, j int) bool {
	ln1 := len(c.list[i].tokens)
	ln2 := len(c.list[j].tokens)

	if ln1 != ln2 {
		return ln1 < ln2
	}

	return strings.Compare(c.list[i].tokens[0].expr, c.list[j].tokens[0].expr) < 0
}

func (c *Chains) Swap(i, j int) {
	c.list[i], c.list[j] = c.list[j], c.list[i]
}

//----------------------------------------------------------------------------------------------------------------------------//
