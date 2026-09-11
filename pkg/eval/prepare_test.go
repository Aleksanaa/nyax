package eval

import (
	"slices"
	"testing"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// TestPreparedCacheIsReadOnly pins that what is cached
// against the syntax is settled before the evaluation starts and never written
// again, so that every evaluation reads the same facts.
//
// The one exception is an attribute whose name is itself computed, which is
// only known once the group is evaluated; the second case below is what pins
// the exception to exactly that field.
func TestPreparedCacheIsReadOnly(t *testing.T) {
	for _, src := range []string{
		`let f = x: x + 1; g = { a, b ? 2, ... }: a + b; in
		 rec { p = f 1; q = g { a = 3; }; r = [ 1 2.5 "s" ''i'' ]; s = { t.u = p; }; v = s.t.u; }`,
		`let xs = builtins.genList (i: i * 2) 5; in
		 with { w = 1; }; builtins.foldl' (a: b: a + b + w) 0 xs`,
		`let a = 1; in { inherit a; b = a; }.b`,
	} {
		before, after, err := cacheAround(t, src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		for id := range before {
			if d := diffStatic(&before[id], &after[id]); d != "" {
				t.Errorf("%s: node %d changed while evaluating: %s", src, id, d)
			}
		}
	}
}

// TestComputedAttrNameIsTheOnlyWrite pins the exception: a name the syntax does
// not give is filled in as the group is evaluated, and nothing else is.
func TestComputedAttrNameIsTheOnlyWrite(t *testing.T) {
	before, after, err := cacheAround(t, `let k = "a"; in (rec { ${k} = 1; b = 2; }).b`)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for id := range before {
		if d := diffStatic(&before[id], &after[id]); d != "" {
			if d != "attrSym" {
				t.Errorf("node %d changed while evaluating: %s", id, d)
			}
			writes++
		}
	}
	if writes == 0 {
		t.Error("expected the computed name to be filled in while evaluating")
	}
}

// TestInvalidLiteralKeepsPosition pins that a literal the pass cannot take —
// a number out of range — still reports as a syntax error at its own position,
// and only when it is evaluated. The pass sees it when the file is loaded,
// which is before there is any evaluation to hang a backtrace off.
func TestInvalidLiteralKeepsPosition(t *testing.T) {
	_, err := EvalString(`let f = x: x; in f 999999999999999999999999999999`)
	e, ok := err.(*EvalError)
	if !ok {
		t.Fatalf("expected an *EvalError, got %T", err)
	}
	if e.Kind != ErrSyntax {
		t.Errorf("kind = %v, want ErrSyntax", e.Kind)
	}
	if e.Pos == nil || e.Pos.Column != 18 { // the argument, not the let
		t.Errorf("position = %+v, want column 18", e.Pos)
	}
	// A bad number that is never forced must not be reported.
	if _, err := EvalString(`let unused = x: 999999999999999999999999999999; in 42`); err != nil {
		t.Errorf("unused bad literal reported: %v", err)
	}
}

// staticSnap is what is compared before and after an evaluation: the fields of
// a static entry, copied out so that the two can be told apart.
type staticSnap struct {
	val     NixValue
	expr    *Expression
	lambda  *lambdaInfo
	attrs   []Sym
	owner   *p.Node
	sym     Sym
	bad     string
	badKind ErrorKind
	attrSym int32
}

// cacheAround evaluates src and returns the cache as the pass left it and as
// the evaluation left it.
func cacheAround(t *testing.T, src string) (before, after []staticSnap, err error) {
	t.Helper()
	pr, perr := p.ParseString(src)
	if perr != nil {
		return nil, nil, perr
	}
	// delay is what binds the parse to a file and runs the pass, so the
	// snapshot has to come from the expression it returns — taking one from a
	// file made separately would compare a store nothing evaluated in.
	w := newWorker()
	x := delay(w, DefaultScope, pr)
	entries := x.scope().file.static.entries
	before = snap(entries)
	val, err := catching(func() NixValue { return x.Eval(w) })
	if err != nil {
		return nil, nil, err
	}
	// Printing forces the whole value, so every node is reached.
	if _, err := catching(func() string { return val.Print(w, -1) }); err != nil {
		return nil, nil, err
	}
	after = snap(entries)
	return before, after, nil
}

// snap reads the comparable fields of a set of entries out of them.
func snap(entries []static) []staticSnap {
	s := make([]staticSnap, len(entries))
	for i := range entries {
		e := &entries[i]
		s[i] = staticSnap{
			val: e.val, expr: e.expr, lambda: e.lambda, attrs: e.attrs,
			owner: e.owner, sym: e.sym, bad: e.bad, badKind: e.badKind,
			attrSym: e.attrSym,
		}
	}
	return s
}

// diffStatic names the first field that differs, or "" if none does.
func diffStatic(a, b *staticSnap) string {
	switch {
	case a.val != b.val:
		return "val"
	case a.expr != b.expr:
		return "expr"
	case a.lambda != b.lambda:
		return "lambda"
	case !slices.Equal(a.attrs, b.attrs):
		return "attrs"
	case a.owner != b.owner:
		return "owner"
	case a.sym != b.sym:
		return "sym"
	case a.bad != b.bad:
		return "bad"
	case a.badKind != b.badKind:
		return "badKind"
	case a.attrSym != b.attrSym:
		return "attrSym"
	}
	return ""
}
