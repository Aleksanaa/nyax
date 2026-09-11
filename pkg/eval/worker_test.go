package eval

import (
	"testing"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// TestWorkerHoldsItsOwnState pins that an evaluation runs on the worker handed
// to it and not on state the package keeps: a second worker draws its
// expressions from its own block, keeps its own stack depth, and leaves the
// package's worker alone.
func TestWorkerHoldsItsOwnState(t *testing.T) {
	pr, err := p.ParseString(`let f = x: x + 1; in f 41`)
	if err != nil {
		t.Fatal(err)
	}
	mainBefore := mainWorker.exprs

	other := newWorker()
	val, err := catching(func() NixValue { return delay(other, DefaultScope, pr).Eval(other) })
	if err != nil {
		t.Fatal(err)
	}
	if got := val.Int(); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
	if other.depth != 0 {
		t.Errorf("worker left %d frames on its stack", other.depth)
	}
	if len(other.exprs) == 0 {
		t.Error("worker did not hand out expressions from its own block")
	}
	if !sameBlock(mainWorker.exprs, mainBefore) {
		t.Error("evaluation on another worker drew from the package's worker")
	}

	// A failure must unwind the worker it happened on, and only that one.
	badpr, err := p.ParseString(`let f = x: x + 1; in f (throw "boom")`)
	if err != nil {
		t.Fatal(err)
	}
	third := newWorker()
	if _, err := catching(func() NixValue {
		return delay(third, DefaultScope, badpr).Eval(third)
	}); err == nil {
		t.Fatal("expected the evaluation to fail")
	}
	if third.depth != 0 {
		t.Errorf("a failed evaluation left %d frames behind", third.depth)
	}
	if other.depth != 0 || mainWorker.depth != 0 {
		t.Error("a failure on one worker disturbed another")
	}
}

// sameBlock reports whether two views of a slab block are the same view.
func sameBlock(a, b []Expression) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}
