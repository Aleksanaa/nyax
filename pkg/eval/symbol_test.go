package eval

import (
	"testing"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// TestStringLiteralsArePreInterned pins that a string literal's symbol is
// worked out by the pass, so that using one as an attribute name at evaluation
// time costs no interning.
func TestStringLiteralsArePreInterned(t *testing.T) {
	pr, err := p.ParseString(`{ "common" = 1; "key${1}" = 2; x = "value"; }`)
	if err != nil {
		t.Fatal(err)
	}
	f := newFile(mainWorker, pr, DefaultScope.frame)
	var check func(n *p.Node)
	check = func(n *p.Node) {
		if n.Type == p.StringNode || n.Type == p.IStringNode {
			// val is set only for a string with nothing interpolated; an
			// interpolated one has no literal value to pre-intern.
			if s := f.static.get(n.ID).val.Str(); s != nil && s.sym == 0 {
				t.Errorf("string literal %q not pre-interned", s.Content)
			}
		}
		for _, c := range n.Nodes {
			check(c)
		}
	}
	check(pr.Result)
}
