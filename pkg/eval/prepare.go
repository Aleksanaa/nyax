package eval

import (
	"slices"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// What is true of a node without evaluating it is worked out once, here,
// before the evaluation starts, rather than the first time each node is
// reached. The resolver needs most of it — where a name reads from, what a
// group binds — before anything is evaluated, and settling the rest here too
// means the cache against the syntax is only ever read while evaluating.
//
// That matters because the syntax is shared. A cache filled lazily is a cache
// written while evaluating: `attrs []Sym` is a slice header, so two evaluations
// reaching the same node would tear rather than benignly repeat. Filled in
// advance, it is read-only.
//
// What cannot be settled here stays out: the name of an attribute whose name
// is itself computed, and what a `with` set holds.

// prepare fills in everything about a file's nodes that the syntax decides.
func (f *file) prepare(w *worker, base *staticFrame) {
	pr := &preparer{file: f, w: w}
	// What each node is, first; then where each name in it comes from, which
	// needs the first pass to have settled what every construct binds.
	pr.node(f.parser.Result)
	pr.resolve(f.parser.Result, base)
	f.static.sealed = true
}

type preparer struct {
	file *file
	// w is the worker the file is being read for. A literal the pass has to
	// evaluate, or fail on, is evaluated on it, so that the work belongs to
	// the evaluation that loaded the file.
	w *worker
}

func (pr *preparer) entry(n *p.Node) *static { return pr.file.static.get(n.ID) }

// node fills in what is known about a node and everything under it.
//
// What is under a node is settled first: what the pass records about a node is
// mostly a summary of its children — the name a string spells, the names a
// group binds — and a summary cannot be taken before there is something to
// summarise.
func (pr *preparer) node(n *p.Node) {
	if n == nil {
		return
	}
	for _, c := range n.Nodes {
		pr.node(c)
	}
	switch n.Type {
	case p.IDNode:
		pr.name(n)

	case p.IntNode:
		pr.literal(n, intLiteral)
	case p.FloatNode:
		pr.literal(n, floatLiteral)
	case p.PathNode:
		pr.literal(n, pr.pathLiteral)
	case p.URINode:
		pr.literal(n, uriLiteral)

	case p.StringNode, p.IStringNode:
		pr.str(n)

	case p.AttrPathNode:
		pr.attrPath(n)

	case p.FunctionNode:
		pr.lambda(n)

	case p.SetNode:
		pr.binds(n.Nodes)
	case p.RecSetNode:
		pr.binds(n.Nodes)
		pr.entry(n).group = pr.groupNames(n.Nodes)
	case p.LetNode:
		pr.binds(n.Nodes[0].Nodes)
		pr.entry(n).group = pr.groupNames(n.Nodes[0].Nodes)
	}
}

// name interns an identifier. Every identifier has one, whether it reads a
// variable or names an attribute.
func (pr *preparer) name(n *p.Node) Sym {
	e := pr.entry(n)
	if e.sym == 0 {
		e.sym = Intern(pr.file.parser.TokenString(n.Tokens[0]))
	}
	return e.sym
}

// literal works out the value of a literal node, and the expression that
// stands for it where one is passed on rather than evaluated. That expression
// is shared by every use of the node and outlives them all, so it is allocated
// on its own rather than out of a block that a whole evaluation would pin.
//
// The one way a literal can fail — a number the machine cannot hold — is not
// raised here. The pass runs when the file is loaded, before anything has
// been evaluated and with no backtrace to report; a bad number raised then
// would lose its position. The message is kept against the node instead, and
// raised when the node is evaluated, exactly as it was before the pass
// existed.
func (pr *preparer) literal(n *p.Node, compute func(*worker, string) NixValue) {
	e := pr.entry(n)
	val, bad, kind := literalAt(pr.w, pr.file.parser.TokenString(n.Tokens[0]), compute)
	if bad != "" {
		e.bad, e.badKind = bad, kind
		return
	}
	e.val = val
	e.expr = new(Expression)
	e.expr.setValue(val)
}

// literalAt works out a literal's value, catching the failure an out-of-range
// number or a missing <path> is so that the caller can defer it. The message
// and its kind are kept, so the re-raise preserves them.
func literalAt(w *worker, s string, compute func(*worker, string) NixValue) (val NixValue, bad string, kind ErrorKind) {
	defer func() {
		if r := recover(); r != nil {
			if err := asEvalError(r); err != nil {
				bad, kind = err.Msg, err.Kind
				return
			}
			panic(r)
		}
	}()
	return compute(w, s), "", ErrEval
}

// str works out a string with nothing interpolated into it, which is a literal
// like any other. One with an interpolation has to be built every time, and is
// left alone.
func (pr *preparer) str(n *p.Node) {
	for _, c := range n.Nodes {
		if c.Type != p.TextNode {
			return
		}
	}
	var x Expression
	x.setThunk(&Env{file: pr.file}, n)
	val := x.evalString(pr.w)
	// The content is fixed by the syntax, so its symbol is too. Interning it
	// now means a string literal used as an attribute name costs nothing when
	// the node is reached.
	val.Str().intern()
}

// attrPath works out what a path of plain identifiers names. One with an
// interpolation in it has to be evaluated every time, and is left alone.
func (pr *preparer) attrPath(n *p.Node) {
	attrs := make([]Sym, len(n.Nodes))
	for i, c := range n.Nodes {
		sym, ok := pr.attrName(c)
		if !ok {
			return
		}
		attrs[i] = sym
	}
	pr.entry(n).attrs = attrs
}

// binds records, for the value of each binding, the name it is bound to, which
// is what a backtrace calls the frame. A computed name is not known here; it is
// filled in as the group is evaluated.
func (pr *preparer) binds(bindNodes []*p.Node) {
	for _, c := range bindNodes {
		switch c.Type {
		case p.BindNode:
			path := c.Nodes[0].Nodes
			last := path[len(path)-1]
			if last.Type == p.IDNode {
				sym := pr.name(last)
				pr.entry(c.Nodes[1]).attrSym = int32(sym)
			}
		case p.InheritNode:
			for _, id := range c.Nodes[0].Nodes {
				pr.entry(id).attrSym = int32(pr.name(id))
			}
		}
	}
}

// lambda works out what a function node binds and where its body is. A shape
// the evaluator rejects is recorded rather than raised: the function may never
// be evaluated, and if it is, the failure belongs to that evaluation, with its
// backtrace, exactly as before.
func (pr *preparer) lambda(n *p.Node) {
	e := pr.entry(n)
	fn := &lambdaInfo{Body: n.Nodes[len(n.Nodes)-1]}
	pr.entry(fn.Body).owner = n
	for _, c := range n.Nodes[:len(n.Nodes)-1] {
		switch c.Type {
		case p.IDNode:
			fn.Arg, fn.HasArg = pr.name(c), true
		case p.ArgSetNode:
			fn.HasFormal = true
			fn.Formal = make(map[Sym]*p.Node, len(c.Nodes))
			fn.FormalOrder = make([]Sym, 0, len(c.Nodes))
			for _, arg := range c.Nodes {
				if len(arg.Nodes) == 0 {
					fn.HasEllipsis = true // `...`
					continue
				}
				sym := pr.name(arg.Nodes[0])
				var def *p.Node // `a ? default`
				if len(arg.Nodes) == 2 {
					def = arg.Nodes[1]
					// A default is the value of the formal, so a backtrace
					// names it after the formal, exactly as the evaluator will
					// when it binds one. Settled here so evaluation never
					// writes it.
					pr.entry(def).attrSym = int32(sym)
				}
				if _, dup := fn.Formal[sym]; dup {
					e.bad = dupFormal(sym)
					return
				}
				fn.Formal[sym] = def
				fn.FormalOrder = append(fn.FormalOrder, sym)
			}
		default:
			e.bad = "unsupported function part: " + c.Type.String()
			return
		}
	}
	if fn.HasArg && fn.HasFormal {
		if _, dup := fn.Formal[fn.Arg]; dup {
			e.bad = dupFormal(fn.Arg)
			return
		}
	}
	e.lambda = fn
}

func dupFormal(sym Sym) string {
	return "duplicate formal function argument '" + sym.String() + "'"
}

// groupNames is the names a binding group binds outright, in order, so that a
// lookup can ask whether a name is one of them.
//
// Only the names the syntax gives are here. A computed one is not known until
// the group is evaluated, and is not in scope of the group's own bindings
// either — `rec { ${k} = 1; b = ???; }` cannot name it — so leaving it out is
// what the language says as well as what the pass can do.
func (pr *preparer) groupNames(bindNodes []*p.Node) []Sym {
	var syms []Sym
	add := func(sym Sym) {
		if i, found := slices.BinarySearch(syms, sym); !found {
			syms = slices.Insert(syms, i, sym)
		}
	}
	for _, c := range bindNodes {
		switch c.Type {
		case p.BindNode:
			// The first component is what the group binds; the rest name
			// attributes of the set it binds, which are not in scope here.
			if path := c.Nodes[0].Nodes; len(path) > 0 {
				if sym, ok := pr.attrName(path[0]); ok {
					add(sym)
				}
			}
		case p.InheritNode:
			for _, id := range c.Nodes[0].Nodes {
				add(pr.name(id))
			}
		case p.InheritFromNode:
			for _, id := range c.Nodes[1].Nodes {
				add(pr.name(id))
			}
		}
	}
	return syms
}

// attrName is the name a component of an attribute path gives outright, and
// whether it gives one. An identifier is one, and so is a string with nothing
// interpolated into it — the parser has already folded `${"a"}` into one of
// those, which is what makes it as static as `a` is.
func (pr *preparer) attrName(n *p.Node) (Sym, bool) {
	switch n.Type {
	case p.IDNode:
		return pr.name(n), true
	case p.StringNode, p.IStringNode:
		if s := pr.entry(n).val.Str(); s != nil {
			return s.intern(), true
		}
	}
	return 0, false
}
