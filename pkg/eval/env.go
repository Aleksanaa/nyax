package eval

import (
	p "github.com/aleksanaa/nyax/pkg/parser"
)

// An Env is one frame of the chain a name is read through: the values one
// construct binds, in the slots the resolver gave them.
//
// There is nothing in it to search. Which frame a name is in, and which slot of
// that frame, were settled before the evaluation started — see resolve.go — so
// reading a name is counting out frames and indexing, and a frame does not even
// hold the names it binds.
//
// A `with` frame is the exception, because what a set holds is not known until
// it is evaluated. It binds no slots of its own; slot zero holds the set's
// expression, unforced until a name is actually looked for in it.
type Env struct {
	up   *Env
	vals []*Expression
	// file is the source the expressions in this frame were parsed from,
	// together with what the pass worked out about its nodes. It lives here
	// rather than on every Expression, of which there are far more.
	file *file
	// inline is where the slots of a small frame live, so that the frame and
	// what it binds are one object rather than two. Most frames are one slot —
	// every argument of every curried function makes one — and paying a second
	// allocation for a single pointer made frames the largest thing an
	// evaluation allocated, ahead of the thunks themselves. Nix keeps the two
	// together as well, as an array at the end of the Env it allocates.
	inline [inlineSlots]*Expression
}

// inlineSlots is how many slots a frame holds without a block of its own.
const inlineSlots = 1

// envSlabSize is how many frame slots are handed out at a time. A frame is only
// ever reached from the ones nested inside it, so a block is retained exactly as
// long as its liveliest member — the same trade the expression slab makes.
const envSlabSize = 1024

// child nests a frame of n slots, all empty. The caller fills them as it binds.
func (env *Env) child(w *worker, n int) *Env {
	e := w.newEnv()
	e.up, e.file = env, env.file
	if n <= inlineSlots {
		e.vals = e.inline[:n:n]
	} else {
		e.vals = w.newSlots(n)
	}
	return e
}

// bind1 nests a frame of one slot, which is what `arg: body` needs, and what a
// `with` holds its set in. It is the commonest frame there is — every curried
// function makes one per argument.
func (env *Env) bind1(w *worker, x *Expression) *Env {
	e := env.child(w, 1)
	e.vals[0] = x
	return e
}

// out counts n frames outward.
func (env *Env) out(n int32) *Env {
	for ; n > 0; n-- {
		env = env.up
	}
	return env
}

// A Scope is somewhere a file can be evaluated: the frame its free names are
// read from, and the shape of that frame, which the resolver needs to settle
// those names before the file is evaluated at all.
//
// The two are separate because only a file is ever read into a scope. Every
// frame an expression makes for itself was resolved when its file was, so the
// shape is not carried past here and an Env stays three words.
type Scope struct {
	env   *Env
	frame *staticFrame
}

// ForFile reads a parsed file into this scope, resolving its names against the
// scope's shape: the default one for an ordinary import, and one of its own for
// builtins.scopedImport.
func (s *Scope) ForFile(w *worker, pr *p.Parser) *Env {
	e := *s.env
	e.file = newFile(w, pr, s.frame)
	return &e
}

// Subscope is this scope with a set of names bound over it, which is how a
// file is read into a scope of its own — builtins.scopedImport. The names are
// taken as they stand: a scope is what a file is resolved against, so a name
// bound later needs a scope of its own. See Session for one that grows.
func (s *Scope) Subscope(w *worker, binds NixSet) *Scope {
	set := binds.finish(w)
	env := s.env.child(w, len(set.attrs))
	for i, a := range set.attrs {
		env.vals[i] = a.x
	}
	f := frameOfSet(set)
	f.up = s.frame
	return &Scope{env: env, frame: f}
}

// parser is the source the expressions in this frame were parsed from, or nil
// for a frame with no syntax behind it.
func (env *Env) parser() *p.Parser {
	if env == nil || env.file == nil {
		return nil
	}
	return env.file.parser
}

// lookup is the value an identifier names.
//
// The resolver settled which frame and which slot, so the lexical case is a
// walk of a known length and an index. What is left for run time is `with`,
// whose set has to be forced before it can say whether it has the name, and
// the chain of `with`s outside it if it does not.
func (env *Env) lookup(w *worker, n *p.Node) *Expression {
	e := env.file.static.get(n.ID)
	if e.bad != "" {
		// The resolver found no binding for this name and no `with` that could
		// provide one. Nix reports that as it reads the file; this reports it
		// when the name is reached, which differs only for a name that is
		// never evaluated at all.
		w.throwAt(env, n, e.badKind, "%s", e.bad)
	}
	frame := env.out(e.up)
	if e.slot != noSlot {
		if x := frame.vals[e.slot]; x != nil {
			return x
		}
		// The slot is one the group has not filled in yet, which only the name
		// of a dynamic attribute can reach: `rec { ${a} = 1; a = "x"; }` asks
		// for `a` while the group that binds it is still being built.
		w.throwAt(env, n, ErrEval,
			"attribute '%s' is used in a name of the very group that binds it", e.sym)
	}
	return env.fromWith(w, n, frame, e)
}

// fromWith looks a name up in the `with` set the resolver pointed at, and in
// the ones outside it if that one does not have it.
func (env *Env) fromWith(w *worker, n *p.Node, frame *Env, e *static) *Expression {
	for node := e.withNode; ; {
		if x, ok := assertSet(w, frame.vals[0].Eval(w)).Get(e.sym); ok {
			return x
		}
		next := env.file.static.get(node - 1)
		if next.withNode == 0 {
			break
		}
		frame, node = frame.out(next.up), next.withNode
	}
	w.throwAt(env, n, ErrUndefinedVariable, "undefined variable '%s'", e.sym)
	return nil
}

// borrow is the binding an identifier names, for a place that would take it
// rather than make a thunk of its own — a list element, a call argument, the
// value of an attribute. It reports nothing where there is nothing to take.
//
// Two places that borrow one binding hold the very same thunk, which is what
// makes them equal when what they hold is a value equal to nothing else, a
// function. See sameThunk.
//
// A name a `with` may provide is not borrowed: finding out whether it has the
// name means forcing the set, and the expression may never ask. Nor is a slot
// the group is still filling in. Nix gives up in both places and for the same
// reasons — see ExprVar::maybeThunk, whose comment covers the first, and whose
// "The value might not be initialised in the environment yet" covers the second.
func (env *Env) borrow(n *p.Node) *Expression {
	e := env.file.static.get(n.ID)
	if e.slot == noSlot {
		return nil
	}
	return env.out(e.up).vals[e.slot]
}

// evalNode evaluates a node in this frame, with no surrounding expression to
// take the frame from.
func (env *Env) evalNode(w *worker, n *p.Node) NixValue {
	// Reading a name is the value of the thunk it is bound to. An expression of
	// its own would mean forcing twice — once for the read, once for the
	// binding — and a backtrace frame that says nothing the binding's own frame
	// does not.
	if n.Type == p.IDNode {
		return env.lookup(w, n).Eval(w)
	}
	// A literal is already a value: it needs no thunk, no evaluation frame and
	// no forcing, only the number read out of the node's cache.
	if val, ok := env.literalValue(w, n); ok {
		return val
	}
	var y Expression
	y.setThunk(env, n)
	return y.Eval(w)
}

// evalAttrPath evaluates the names of an attribute path, such as the `a."b".`
// of a binding or a selection. The path node is passed in rather than wrapped
// in an expression, since nothing needs it afterwards.
//
// A path of plain identifiers names the same attributes however often it is
// reached, so it is worked out once and kept against the node — which matters
// because a selection inside a loop is evaluated once per turn, and interning
// its components again each time was the largest single cost in the evaluator
// after allocation. A path with an interpolation in it has to be evaluated
// every time, and is not kept.
func (env *Env) evalAttrPath(w *worker, path *p.Node) []Sym {
	e := env.file.static.get(path.ID)
	if e.attrs != nil {
		return e.attrs
	}
	attrs := make([]Sym, len(path.Nodes))
	for i, c := range path.Nodes {
		attrs[i] = env.attrSym(w, c)
	}
	return attrs
}

// attrPathNull reports whether any dynamic component of an attribute path
// evaluates to null, which is what skips a binding in Nix: `{ ${null} = 1; }`
// binds nothing.
func (env *Env) attrPathNull(w *worker, path *p.Node) bool {
	for _, c := range path.Nodes {
		if c.Type == p.InterpNode && env.evalNode(w, c.Nodes[0]).Kind() == KindNull {
			return true
		}
	}
	return false
}

// attrSym evaluates one component of an attribute path to its interned name. A
// plain identifier is interned by the pass; a computed one has to be evaluated
// every time.
func (env *Env) attrSym(w *worker, n *p.Node) Sym {
	switch n.Type {
	case p.IDNode:
		return env.file.static.get(n.ID).sym
	case p.StringNode, p.IStringNode:
		return CoerceToString(w, env.evalNode(w, n)).intern()
	case p.InterpNode:
		return CoerceToString(w, env.evalNode(w, n.Nodes[0])).intern()
	default:
		w.throwf(ErrEval, "unsupported attribute name: %v", n.Type)
		return 0
	}
}

// thunkFor is the expression to pass where something else will hold on to the
// node rather than evaluate it at once — a list element, the argument of a call.
//
// An identifier the frames already hold needs no thunk of its own: the binding
// it names is one, and sharing it is also what makes the argument evaluated at
// most once however many times the callee uses it. A literal is the same value
// every time, so one expression per node serves every use. Nix does the same,
// in Expr::maybeThunk.
//
// It reports whether what comes back was borrowed from another binding. A
// borrowed expression is described by whoever it belongs to, so nothing may
// relabel it for a backtrace; a literal is not borrowed in that sense, since
// the expression a literal node is worked out into belongs to that node alone.
func (env *Env) thunkFor(w *worker, n *p.Node) (*Expression, bool) {
	switch n.Type {
	case p.IDNode:
		if y := env.borrow(n); y != nil {
			return y, true
		}
	case p.IntNode, p.FloatNode, p.PathNode, p.URINode:
		return env.literalExpr(w, n), false
	}
	return newScoped(w, env, n), false
}

// thunkForBinding is thunkFor for the value of an attribute, which a backtrace
// names after that attribute.
//
// It borrows a binding the same way, but takes no shortcut for a literal: the
// expression a literal is worked out into is already a value, with no node left
// to record the attribute's name against, and the name is worth more here than
// the allocation it costs to keep.
func (env *Env) thunkForBinding(w *worker, n *p.Node) (*Expression, bool) {
	if n.Type == p.IDNode {
		if y := env.borrow(n); y != nil {
			return y, true
		}
	}
	return newScoped(w, env, n), false
}

// sessionSlots is how many names a session may bind. The frame is made this
// big at the start and filled as names arrive, so that it — and so the slots
// every earlier thunk was resolved against — stays where it is. Nix's REPL
// works the same way, to a limit of its own.
const sessionSlots = 4096

// A Session is a scope that grows: names are bound into it one at a time, and
// a thunk made earlier sees one bound later, which is what makes definitions
// in a REPL behave like the bindings of a `let`.
//
// It is the one place where what a scope binds is not settled before anything
// is evaluated, and it is settled per entry instead: each line is resolved
// against the names bound when it is read.
type Session struct {
	scope *Scope
	slots map[Sym]int32
	next  int32
}

// NewSession opens a session over a scope, for a caller outside the evaluator.
func NewSession(base *Scope) *Session {
	env := base.env.child(mainWorker, sessionSlots)
	slots := map[Sym]int32{}
	frame := &staticFrame{up: base.frame, slots: slots}
	return &Session{scope: &Scope{env: env, frame: frame}, slots: slots}
}

// Scope is what to evaluate an entry in.
func (s *Session) Scope() *Scope { return s.scope }

// Bind gives a name a value, replacing what it had. It reports false when the
// session has no room left for a name it has not seen before.
func (s *Session) Bind(sym Sym, x *Expression) bool {
	slot, ok := s.slots[sym]
	if !ok {
		if int(s.next) == sessionSlots {
			return false
		}
		slot, s.next = s.next, s.next+1
		s.slots[sym] = slot
	}
	s.scope.env.vals[slot] = x
	return true
}

// Names lists what the session and the scope under it make visible, for
// completion.
func (s *Session) Names() []string {
	names := make([]string, 0, len(s.slots))
	for sym := range s.slots {
		names = append(names, sym.String())
	}
	return append(names, s.scope.frame.up.names()...)
}

// names lists what a frame binds, for completion.
func (f *staticFrame) names() []string {
	names := make([]string, 0, len(f.slots))
	for sym := range f.slots {
		names = append(names, sym.String())
	}
	return names
}
