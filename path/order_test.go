package path

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

const (
	testREguid = `(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`
	testREany  = `.+`
)

// TestFindDeclarationOrder -- путь подходит под две цепочки одной длины ({guid} и {любая строка}),
// выигрывать должна объявленная раньше, при любом числе других цепочек
func TestFindDeclarationOrder(t *testing.T) {
	type pp struct {
		GUID  string
		Child string
		Name  string
	}

	const (
		guid1 = "0f8a1c2e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"
		guid2 = "1a2b3c4d-5e6f-4a1b-9c2d-3e4f5a6b7c8d"
	)

	for extra := 0; extra <= 40; extra++ {
		c := &Chains{
			StdParams: Params{
				PathParamsPattern: pp{},
			},
		}

		// Посторонние цепочки разной длины -- от их числа зависело, переставит ли нестабильная сортировка нужные
		for i := range extra {
			tokens := []*Token{{Expr: testREguid, VarName: "GUID"}}
			for range i % 4 {
				tokens = append(tokens, &Token{Expr: fmt.Sprintf("x%d", i), VarName: VarIgnore})
			}
			c.Chains = append(c.Chains, &Chain{Name: fmt.Sprintf("extra%d", i), Tokens: tokens})
		}

		c.Chains = append(c.Chains,
			&Chain{
				Name: "byGUID",
				Tokens: []*Token{
					{Expr: testREguid, VarName: "GUID"},
					{Expr: "attributes", VarName: VarIgnore},
					{Expr: testREguid, VarName: "Child"},
				},
			},
			&Chain{
				Name: "byName",
				Tokens: []*Token{
					{Expr: testREguid, VarName: "GUID"},
					{Expr: "attributes", VarName: VarIgnore},
					{Expr: testREany, VarName: "Name"},
				},
			},
		)

		// Перемешиваем посторонние цепочки вокруг нужных, сохраняя порядок byGUID перед byName
		r := rand.New(rand.NewSource(int64(extra)))
		r.Shuffle(len(c.Chains), func(i, j int) { c.Chains[i], c.Chains[j] = c.Chains[j], c.Chains[i] })
		gi, ni := -1, -1
		for i, ch := range c.Chains {
			switch ch.Name {
			case "byGUID":
				gi = i
			case "byName":
				ni = i
			}
		}
		if gi > ni {
			c.Chains[gi], c.Chains[ni] = c.Chains[ni], c.Chains[gi]
		}

		err := c.Prepare(stdhttp.MethodGET)
		if err != nil {
			t.Fatalf("extra=%d: %s", extra, err)
		}

		cases := []struct {
			path []string
			want string
		}{
			{[]string{guid1, "attributes", guid2}, "byGUID"},
			{[]string{guid1, "attributes", "Наименование"}, "byName"},
		}

		for _, tc := range cases {
			matched, _, code, err := c.Find(tc.path)
			if err != nil {
				t.Fatalf("extra=%d %v: %s", extra, tc.path, err)
			}
			if matched == nil {
				t.Fatalf("extra=%d %v: not matched, code %d", extra, tc.path, code)
			}
			if matched.Name != tc.want {
				t.Errorf("extra=%d %v: matched %q, expected %q", extra, tc.path, matched.Name, tc.want)
			}
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
