package pathparser

import (
	"reflect"
	"testing"
)

//----------------------------------------------------------------------------------------------------------------------------//

func TestParser(t *testing.T) {

	// Check illegal
	illegal := []*Chains{
		{
			Chains: []Chain{
				// Illegal 1 -- empty
				{},
			},
		},
		{
			Chains: []Chain{
				// Illegal 2 -- 2 tokens with empty first expression
				{
					{Expr: ``, VarName: `command`},
					{Expr: `\d+`, VarName: `id`},
				},
			},
		},
		{
			Chains: []Chain{
				// Illegal 3 -- bad regexp
				{
					{Expr: `group`, VarName: `command`},
					{Expr: `[\d+`, VarName: `gid`},
				},
			},
		},
	}

	for i, c := range illegal {
		err := c.Prepare()
		if err == nil {
			t.Fatalf("[%d] error expected, but not found (%#v)", i, c)
		}
	}

	// Check valid

	c := Chains{
		// unsorted!

		Chains: []Chain{
			// Active or blocked users in group with ID=gid
			{
				{Expr: `group`, VarName: `command`},
				{Expr: `\d+`, VarName: `gid`},
				{Expr: `active|blocked`, VarName: `sub_command`},
			},

			// Active or blocked users
			{
				{Expr: `active|blocked`, VarName: `command`},
			},

			// User by ID
			{
				{Expr: `\d+`, VarName: `id`},
			},

			// Users in group with ID=gid
			{
				{Expr: `group`, VarName: `command`},
				{Expr: `\d+`, VarName: `gid`},
			},

			// All users
			{
				{Expr: ``, VarName: `command`},
			},
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
		vars     Vars
		expected Vars
		found    bool
		isErr    bool
	}{
		{
			path:     []string{},
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			vars:     vars,
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"24"},
			vars:     vars,
			expected: Vars{"command": "", "sub_command": "", "id": int(24), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"active"},
			vars:     vars,
			expected: Vars{"command": "active", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"blocked"},
			vars:     vars,
			expected: Vars{"command": "blocked", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"group", "335"},
			vars:     vars,
			expected: Vars{"command": "group", "sub_command": "", "id": int(0), "gid": uint32(335)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"group", "335", "active"},
			vars:     vars,
			expected: Vars{"command": "group", "sub_command": "active", "id": int(0), "gid": uint32(335)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"group", "335", "blocked"},
			vars:     vars,
			expected: Vars{"command": "group", "sub_command": "blocked", "id": int(0), "gid": uint32(335)},
			found:    true,
			isErr:    false,
		},
		{
			path:     []string{"123", ""},
			vars:     vars,
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    false,
		},
		{
			path:     []string{"123", "active"},
			vars:     vars,
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    false,
		},
		{
			path:     []string{"123", ""},
			vars:     vars,
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    false,
		},
		{
			path:     []string{"active", "123"},
			vars:     vars,
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    false,
		},
		{
			path:     []string{""},
			vars:     Vars{"command": &command, "sub_command": &subCommand, "idXXX": &id, "gid": &gid},
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    true,
		},
		{
			path:     []string{""},
			vars:     Vars{"command": &command, "sub_command": subCommand, "id": &id, "gid": &gid},
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    true,
		},
		{
			path:     []string{""},
			vars:     Vars{"sub_command": &subCommand, "id": &id, "gid": &gid},
			expected: Vars{"command": "", "sub_command": "", "id": int(0), "gid": uint32(0)},
			found:    false,
			isErr:    true,
		},
	}

	cleanVars := func() {
		command = ""
		subCommand = ""
		id = 0
		gid = 0
	}

	err := c.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	var prev Chain
	prevLn := -1

	for _, chain := range c.Chains {
		ln := len(chain)
		if ln < prevLn {
			t.Errorf(`Unsorted: %#v after %#v`, chain, prev)
		}
		prev = chain
		prevLn = len(chain)

		for _, token := range chain {
			if token.re == nil {
				t.Errorf(`re is nil: %#v`, token)
			}
		}
	}

	for i, df := range variants {
		cleanVars()
		found, err := c.Do(df.path, df.vars)

		if !df.isErr && err != nil {
			t.Errorf("[%d] %s (%#v)", i, err, df)
			continue
		}

		if df.isErr && err == nil {
			t.Errorf("[%d] error expected (%#v)", i, df)
			continue
		}

		if err != nil {
			continue
		}

		if found != df.found {
			t.Errorf("[%d] found is %v, %v expected  (%#v)", i, found, df.found, df)
			continue
		}

		if len(vars) != len(df.expected) {
			t.Errorf("[%d] got %d variables %d expected (%#v)", i, len(vars), len(df.expected), df)
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
				t.Errorf("[%d] %s is %T(%v), %T(%v) expected (%#v)", i, name, v, v, expected, expected, df)
				continue
			}
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
