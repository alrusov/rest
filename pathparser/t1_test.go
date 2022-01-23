package pathparser

import (
	"reflect"
	"testing"
)

//----------------------------------------------------------------------------------------------------------------------------//

func TestParser(t *testing.T) {
	// unsorted!
	src := [][]struct {
		expr string
		dest string
	}{
		// Active or blocked users in group with GroupID
		{
			{expr: `group`, dest: `command`},
			{expr: `\d+`, dest: `gid`},
			{expr: `active|blocked`, dest: `sub_command`},
		},
		// Active or blocked users
		{
			{expr: `active|blocked`, dest: `command`},
		},
		// User by ID
		{
			{expr: `\d+`, dest: `id`},
		},
		// Users in group with GroupID
		{
			{expr: `group`, dest: `command`},
			{expr: `\d+`, dest: `gid`},
		},
		// All users
		{
			{expr: ``, dest: `command`},
		},

		// Illegal 0
		{},
		// Illegal 1
		{
			{expr: ``, dest: `command`},
			{expr: `\d+`, dest: `id`},
		},
		// Illegal 2
		{
			{expr: `group`, dest: `command`},
			{expr: `[\d+`, dest: `gid`},
		},
	}

	var (
		command    string
		subCommand string
		id         int
		gid        uint32
	)

	vars := Vars{
		"command":     &command,
		"sub_command": &subCommand,
		"id":          &id,
		"gid":         &gid,
	}

	variants := []struct {
		path     []string
		expected Vars
		found    bool
		isErr    bool
	}{
		{
			path:     []string{},
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"24"},
			expected: Vars{"command": "", "sub_command": "", "id": int(24), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"active"},
			expected: Vars{"command": "active", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"blocked"},
			expected: Vars{"command": "blocked", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"group", "335"},
			expected: Vars{"command": "group", "sub_command": "", "id": int(0), "gid": uint32(335)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"group", "335", "active"},
			expected: Vars{"command": "group", "sub_command": "active", "id": int(0), "gid": uint32(335)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"group", "335", "blocked"},
			expected: Vars{"command": "group", "sub_command": "blocked", "id": int(0), "gid": uint32(335)},
			found:    true,
			isErr:    false,
		},
	}

	cleanVars := func() {
		command = ""
		subCommand = ""
		id = 0
		gid = 0
	}

	fill := func(fromIdx int) (c *Chains) {
		c = NewChains(0)
		for ci, chain := range src[fromIdx:] {
			idx, err := c.NewChain(0)
			if err != nil {
				t.Fatalf(`[%d] %s`, ci, err)
			}

			for ti, token := range chain {
				err := c.Add(idx, token.expr, token.dest)
				if err != nil {
					t.Fatalf(`[%d.%d] %s`, ci, ti, err)
				}
			}
		}
		return c
	}

	for i := 3; i > 0; i-- {
		ii := len(src) - 1

		c := fill(ii)
		err := c.Prepare()
		if err == nil {
			t.Fatalf("[%d] error expected, but not found", i)
		}

		src = src[:ii]
	}

	c := fill(0)
	err := c.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	var prev *chain
	prevLn := -1

	for _, chain := range c.list {
		ln := len(chain.tokens)
		if ln < prevLn {
			t.Errorf(`Unsorted: %#v after %#v`, chain, prev)
		}
		prev = chain
		prevLn = len(chain.tokens)

		for _, token := range chain.tokens {
			if token.re == nil {
				t.Errorf(`re is nil: %#v`, token)
			}
		}
	}

	for i, df := range variants {
		cleanVars()
		found, err := c.Do(df.path, vars)

		if err != nil {
			if !df.isErr {
				t.Errorf("[%d] %s", i, err)
			}
			continue
		}

		if found != df.found {
			t.Errorf("[%d] found is %v, %v expected", i, found, df.found)
			continue
		}

		if len(vars) != len(df.expected) {
			t.Errorf("[%d] gor %d variables %d expected", i, len(vars), len(df.expected))
			continue
		}

		for name, v := range vars {
			expected, exists := df.expected[name]
			if !exists {
				t.Errorf("[%d] %s -- unknown", i, name)
				continue
			}

			v := reflect.ValueOf(v).Elem().Interface()
			if !reflect.DeepEqual(v, expected) {
				t.Errorf("[%d] %s is %T(%v), %T(%v) expected", i, name, v, v, expected, expected)
				continue
			}
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
