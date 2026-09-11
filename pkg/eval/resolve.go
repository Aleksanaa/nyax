package eval

import (
	p "github.com/aleksanaa/nyax/pkg/parser"
)

// Working out where every name comes from, before anything is evaluated.
//
// Which binding a name refers to is decided entirely by the syntax around it:
// `x` in `let x = 1; in f x` is the `x` two constructs out, in the first slot
// that construct binds, however often the expression is evaluated and whatever
// the values turn out to be. So it is settled once, here, and an evaluation
// reads the answer instead of searching for it.
//
// What that replaces is a search of the scope chain per name, with a per-worker
// memo in front of it to make the second search cheap. The memo could only ever
// be a guess checked at run time, because the slot it named was a position in a
// set built while evaluating; and it was written while evaluating, which is
// what made borrowing a binding cost a forced `with` set and a poisoned memo.
// None of that survives a resolved name: the frame is counted out and the slot
// is indexed.
//
// `with` is the one thing the syntax cannot settle, since what a set holds is
// not known until it is evaluated. A name no construct binds is looked for in
// the nearest enclosing `with` at run time, and in the ones outside it after
// that; a name with no `with` anywhere around it is undefined, and saying so
// is the resolver's job rather than the evaluator's — which is why a typo in a
// branch that is never taken is still an error, here as in Nix.

// noSlot is the slot of a name no construct binds: one that a `with` may
// provide, or none at all.
const noSlot = -1

// staticFrame is a frame of the chain as the resolver sees it — the names a
// construct binds and the slots it gives them, with no values in it yet. The
// chain of these has the same shape as the chain of Envs an evaluation builds,
// which is what makes a count of frames worked out here good at run time.
//
// A `with` frame binds no names: what its set holds is not known until it is
// evaluated, so a lexical lookup passes straight through it.
type staticFrame struct {
	up     *staticFrame
	slots  map[Sym]int32
	isWith bool
	// node is the `with` this frame belongs to, one more than its node id so
	// that zero means none, so that a name looked for in one can be looked for
	// in the next one out.
	node uint32
}

// lookup finds the frame and slot a name is bound in, counting frames from
// here. It passes over `with` frames: a name a construct binds beats every
// `with`, however near.
func (f *staticFrame) lookup(sym Sym) (up, slot int32, ok bool) {
	for s := f; s != nil; s, up = s.up, up+1 {
		if s.isWith {
			continue
		}
		if slot, ok := s.slots[sym]; ok {
			return up, slot, true
		}
	}
	return 0, 0, false
}

// nearestWith finds the `with` a name not bound anywhere may yet come from,
// counting frames from here.
func (f *staticFrame) nearestWith() (up int32, node uint32, ok bool) {
	for s := f; s != nil; s, up = s.up, up+1 {
		if s.isWith {
			return up, s.node, true
		}
	}
	return 0, 0, false
}

// frameOf makes a frame binding the given names, in the order given.
func frameOf(up *staticFrame, syms []Sym) *staticFrame {
	slots := make(map[Sym]int32, len(syms))
	for i, sym := range syms {
		slots[sym] = int32(i)
	}
	return &staticFrame{up: up, slots: slots}
}

// resolve settles every name in the file, against the frame its root is
// evaluated in.
func (pr *preparer) resolve(root *p.Node, base *staticFrame) {
	r := &resolver{pr: pr, frame: base}
	r.node(root)
}

type resolver struct {
	pr    *preparer
	frame *staticFrame
}

// in runs f with an extra frame pushed, and pops it again.
func (r *resolver) in(f *staticFrame, body func()) {
	was := r.frame
	r.frame = f
	body()
	r.frame = was
}

// node resolves the names in a node and everything under it.
//
// It walks the tree itself rather than riding the pass's general walk, because
// which frame a child is resolved in is exactly what differs between children:
// the set of a `with` is outside it, the body inside; a function's defaults are
// in scope of its own arguments; an inherited name is read from the scope
// around the group that binds it.
func (r *resolver) node(n *p.Node) {
	if n == nil {
		return
	}
	switch n.Type {
	case p.IDNode:
		r.variable(n)

	case p.FunctionNode:
		r.function(n)

	case p.LetNode:
		r.binds(n, n.Nodes[0].Nodes, func() { r.node(n.Nodes[1]) })

	case p.RecSetNode:
		r.binds(n, n.Nodes, nil)

	case p.SetNode:
		// A plain set binds nothing its own values can see, so its bindings
		// are resolved where the set is written.
		for _, c := range n.Nodes {
			r.bind(c, r.frame)
		}

	case p.WithNode:
		// The set is outside the `with` it introduces; the body is inside it.
		r.node(n.Nodes[0])
		f := &staticFrame{up: r.frame, isWith: true, node: n.ID + 1}
		up, next, ok := r.frame.nearestWith()
		e := r.pr.entry(n)
		if ok {
			// Counted from this `with`'s own frame, which is one further in
			// than the frame the enclosing one was found from.
			e.up, e.withNode = up+1, next
		} else {
			e.up, e.withNode = 0, 0
		}
		r.in(f, func() { r.node(n.Nodes[1]) })

	case p.SelectNode, p.SelectOrNode:
		// `e.a.b` and `e.a.b or d`: the path names attributes rather than
		// reading variables, so only the subject and the fallback are names.
		r.node(n.Nodes[0])
		r.attrPath(n.Nodes[1])
		if len(n.Nodes) > 2 {
			r.node(n.Nodes[2])
		}

	case p.OpQuestionNode:
		r.node(n.Nodes[0])
		r.attrPath(n.Nodes[1])

	default:
		for _, c := range n.Nodes {
			r.node(c)
		}
	}
}

// attrPath resolves the computed components of an attribute path. A plain
// identifier there is the name of an attribute and reads no variable.
func (r *resolver) attrPath(path *p.Node) {
	for _, c := range path.Nodes {
		if c.Type != p.IDNode {
			r.node(c)
		}
	}
}

// binds resolves a binding group that its own bindings can see — a `let` or a
// recursive set — in a frame of the names it binds. body, when given, is the
// `let`'s body, which is in scope of the same names.
func (r *resolver) binds(n *p.Node, bindNodes []*p.Node, body func()) {
	outer := r.frame
	f := frameOf(outer, r.pr.entry(n).group)
	r.in(f, func() {
		for _, c := range bindNodes {
			r.bind(c, outer)
		}
		if body != nil {
			body()
		}
	})
}

// bind resolves one binding of a group. outer is the frame the group is
// written in, which is where an inherited name is read from — `inherit x;` is
// `x = x;` with the right-hand side outside the group, so that it takes the
// name it shadows rather than itself.
func (r *resolver) bind(c *p.Node, outer *staticFrame) {
	switch c.Type {
	case p.BindNode:
		r.attrPath(c.Nodes[0])
		r.node(c.Nodes[1])

	case p.InheritNode:
		for _, id := range c.Nodes[0].Nodes {
			r.in(outer, func() { r.variable(id) })
		}

	case p.InheritFromNode:
		// `inherit (e) a b;`: e is an expression, in scope of the group as
		// Nix has it; the names after it are attributes of what it evaluates
		// to, and read no variable.
		r.node(c.Nodes[0])

	default:
		r.node(c)
	}
}

// function resolves a function in a frame of what it binds: its argument, its
// formals, or both. A default is in scope of the whole set of formals, so that
// one may be written in terms of another.
func (r *resolver) function(n *p.Node) {
	e := r.pr.entry(n)
	if e.lambda == nil {
		// The pass could not make sense of the arguments — a duplicate formal —
		// and the failure is raised when the function is reached.
		return
	}
	fn := e.lambda
	syms := make([]Sym, 0, 1+len(fn.FormalOrder))
	if fn.HasArg {
		syms = append(syms, fn.Arg)
	}
	syms = append(syms, fn.FormalOrder...)

	f := frameOf(r.frame, syms)
	r.in(f, func() {
		for _, sym := range fn.FormalOrder {
			r.node(fn.Formal[sym])
		}
		r.node(fn.Body)
	})
}

// variable settles one identifier that reads a name.
func (r *resolver) variable(n *p.Node) {
	e := r.pr.entry(n)
	if up, slot, ok := r.frame.lookup(e.sym); ok {
		e.up, e.slot, e.withNode = up, slot, 0
		return
	}
	if up, node, ok := r.frame.nearestWith(); ok {
		e.up, e.slot, e.withNode = up, noSlot, node
		return
	}
	// No construct binds it and no `with` can provide it, so it is a name that
	// is not there. Nix says so here too, before evaluating anything, which is
	// why an unused branch with a typo in it is still an error.
	e.slot = noSlot
	e.bad, e.badKind = "undefined variable '"+e.sym.String()+"'", ErrUndefinedVariable
}

// baseFrame is the shape of the scope a file is evaluated in: the names the
// default scope binds, and the slots they are in. It is set when that scope is
// built, which is before any file is read.
var baseFrame *staticFrame

// frameOfSet is the frame a finished set makes: its names, in the slots the
// set holds them in.
func frameOfSet(set NixSet) *staticFrame {
	slots := make(map[Sym]int32, len(set.attrs))
	for i, a := range set.attrs {
		slots[a.sym] = int32(i)
	}
	return &staticFrame{slots: slots}
}
