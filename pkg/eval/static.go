package eval

import (
	"slices"
	"strconv"

	p "github.com/aleksanaa/nyax/pkg/parser"
	"github.com/aleksanaa/nyax/pkg/source"
)

// Static facts about a syntax node — the value of a literal, the interned name
// of an identifier — do not change between evaluations, but a node inside a
// function body is evaluated once per call. This file caches them.
//
// The cache is a flat array keyed by the node's dense ID, so a lookup is one
// bounds check and an index, with no hashing and no indirection. The parser
// says how many nodes it made, so the array is the right size from the start;
// it was pages of a sparse array before, and reaching through the page table
// was 11% of the evaluator on a call-heavy workload.

// static is what is known about a node without evaluating it in a scope.
type static struct {
	// val is the value of a literal: a number, a path, or a string with no
	// interpolation in it.
	val NixValue
	// expr is a literal node as an expression, for where one is passed on
	// rather than evaluated.
	expr *Expression
	// lambda is what a function node binds and where its body is, neither of
	// which depends on the scope a closure over it is made in.
	lambda *lambdaInfo
	// attrs is what an attribute path of plain identifiers names, which does
	// not change between evaluations.
	attrs []Sym
	// group is the names a binding group binds outright, in order, for a `let`
	// or a recursive set — the ones its own bindings are in scope of. A name
	// the syntax does not give, because it is computed, is not among them and
	// is not in scope of the group either, which is what Nix does too.
	//
	// It is what lets a group still being built say that a name is none of its
	// own, so that a binding may borrow one from further out while the group
	// it is in has bound almost nothing yet. Nix reads the same fact off an env
	// whose slots the parser counted; here the pass counts them instead.
	group []Sym
	// attrSym is the name of the attribute whose value this node is, for the
	// backtrace frame that says which attribute failed.
	//
	// It is the one thing here that evaluation can still write: an attribute
	// whose name is itself computed is only named once the group is evaluated.
	// A name the pass settled is never written again.
	attrSym int32
	// bad is why the pass could not make sense of this node, raised if and
	// when the node is evaluated so that the failure keeps its backtrace.
	bad string
	// badKind is the kind of the failure in bad, kept so the re-raise can
	// preserve it: a missing <path> is a thrown error, an out-of-range
	// number a syntax error.
	badKind ErrorKind
	// owner is the function a body node belongs to, which is where a call's
	// frame points.
	owner *p.Node
	// sym is the interned name of an identifier or of an attribute-path
	// component. The zero Sym is the empty name, which no identifier has, so
	// it doubles as "not computed yet".
	sym Sym

	// up and slot are where an identifier reads its value from: up frames out
	// from the one it is evaluated in, in that frame's slot. A slot of noSlot
	// means no construct binds the name, and up counts to the nearest `with`
	// instead, whose set may have it.
	//
	// On a `with` node they say the same about the next `with` outwards, so
	// that a name the nearer one does not have can be looked for in the next.
	// See resolve.go.
	up   int32
	slot int32
	// withNode is the `with` whose set may hold this name, one more than its
	// node id so that zero means none. It is what makes the chain of `with`s
	// walkable from a name none of them has yet been asked for.
	withNode uint32
}

type staticStore struct {
	entries []static
	// sealed says the pass has finished and nothing may write here again.
	// Evaluation only reads what is known about the syntax, so any write that
	// slips back in is a bug; this catches it rather than a corruption that
	// only shows up later.
	sealed bool
}

// get returns the entry for a node.
func (s *staticStore) get(id uint32) *static {
	if int(id) >= len(s.entries) {
		s.grow(id)
	}
	return &s.entries[id]
}

// grow makes room for a node the array does not cover yet, which happens once
// per file: the first node evaluated sizes it for the whole parse.
func (s *staticStore) grow(id uint32) {
	s.entries = slices.Grow(s.entries, int(id)+1-len(s.entries))[:id+1]
}

// file is the state an evaluation keeps per parsed file: the source itself and
// what has been worked out about its nodes. Scopes hold a pointer to it, so an
// expression reaches both through its scope and neither costs a field of its
// own.
type file struct {
	parser *p.Parser
	static staticStore
	// dir is the directory the file lives in, which is what the relative
	// paths inside it are resolved against.
	dir string
}

// newFile binds a parse to the facts worked out about its nodes. The parser
// counted them, so the array they go in is allocated once, at the right size.
func newFile(w *worker, pr *p.Parser, base *staticFrame) *file {
	f := &file{
		parser: pr,
		static: staticStore{entries: make([]static, pr.NodeCount())},
		dir:    source.Dir(source.Abs(pr.Path())),
	}
	f.prepare(w, base)
	return f
}

// The value of each kind of literal, computed at most once per node.

func uriLiteral(w *worker, s string) NixValue { return String(s) }

// pathLiteral is the value of a path literal, resolved once per file against
// the file's own directory. It is a method so that it can reach that
// directory.
func (pr *preparer) pathLiteral(w *worker, s string) NixValue {
	r := resolvePathLiteral(pr.file.dir, s)
	if r == "" {
		// Nix raises this as a ThrownError, so tryEval catches it.
		w.throwf(ErrThrown, "file '%s' was not found in the Nix search path (add it using $NIX_PATH or -I)", s[1:len(s)-1])
	}
	return PathValue(&NixPath{Path: r})
}

func floatLiteral(w *worker, s string) NixValue {
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		w.throwf(ErrSyntax, "invalid float %q", s)
	}
	return Float(val)
}

func intLiteral(w *worker, s string) NixValue {
	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		w.throwf(ErrSyntax, "invalid integer %q", s)
	}
	return Int(val)
}

// literalValue returns the value of a literal node, computing it at most
// once. It is for reading a literal with no expression around it, where there
// is no thunk to hold the value: an operand of an arithmetic expression is
// usually a literal or a name, and going through the whole force machinery
// for a number was a measurable cost on call-heavy workloads.
func (env *Env) literalValue(w *worker, n *p.Node) (NixValue, bool) {
	switch n.Type {
	case p.IntNode, p.FloatNode, p.PathNode, p.URINode:
		return env.literal(w, n), true
	}
	return NixValue{}, false
}

// literal returns the value of a literal node, which the pass worked out
// before the evaluation started. A literal the pass could not take — a number
// out of range — is raised here, where the evaluation has a backtrace to
// attach, rather than when the file was loaded.
func (env *Env) literal(w *worker, n *p.Node) NixValue {
	e := env.file.static.get(n.ID)
	if e.bad != "" {
		w.throwf(e.badKind, "%s", e.bad)
	}
	return e.val
}

// literalExpr returns a literal node as an already evaluated expression. A
// literal is the same value however often it is reached, so one expression
// serves every use of the node, and passing one as an argument costs nothing.
func (env *Env) literalExpr(w *worker, n *p.Node) *Expression {
	e := env.file.static.get(n.ID)
	if e.bad != "" {
		w.throwf(e.badKind, "%s", e.bad)
	}
	return e.expr
}

// cache records the value of a literal that the pass works out, and refuses to
// once the pass is done. The pass evaluates every string with nothing
// interpolated into it, so evaluation never reaches this; if it ever does, a
// write is reaching the syntax cache while it may be read, and saying so here
// is better than a corruption that only shows up later.
func (e *static) cache(f *file, val NixValue) {
	if f.static.sealed {
		panic("eval: the cache against the syntax was written while evaluating")
	}
	e.val = val
}
