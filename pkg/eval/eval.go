// Package eval implements a lazy evaluator for the Nix expression language.
//
// Evaluation is thunk based: every expression is an [Expression], forced at
// most once and memoized. Failures are raised as panics carrying an
// [EvalError] and are annotated with a source position and a backtrace as they
// unwind; Eval and EvalString at the top of this file turn them back into
// ordinary Go errors.
package eval

import (
	"slices"
	"strings"
	"unsafe"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// Eval evaluates a parsed expression to a value.
//
// The result is only evaluated as far as its outermost value: forcing what it
// contains, as Print does, can fail in turn, so use Print rather than calling
// the method directly.
func Eval(pr *p.Parser) (NixValue, error) {
	return EvalIn(DefaultScope, pr)
}

// EvalIn evaluates a parsed expression in a scope other than the default one.
// It is how the REPL evaluates an entry, so that the entry sees the names the
// session has bound so far.
func EvalIn(scope *Scope, pr *p.Parser) (NixValue, error) {
	return catching(func() NixValue { return delay(mainWorker, scope, pr).Eval(mainWorker) })
}

// Delay returns a parsed expression as an unforced thunk in a scope.
//
// The REPL binds names to these rather than to values, which is what makes
// `x = <something that fails>` legal until x is used, just as a `let` is.
func delay(w *worker, scope *Scope, pr *p.Parser) *Expression {
	x := w.newExpr()
	x.setThunk(scope.ForFile(w, pr), pr.Result)
	return x
}

// Print renders a value, forcing it down to the given depth. A negative depth
// forces it completely.
func Print(val NixValue, depth int) (string, error) {
	return catching(func() string {
		// A full print is the one traversal the depth bound cannot stop, so it
		// tracks what it has already printed to report self-referential values
		// rather than recurse without end.
		if depth < 0 {
			mainWorker.seen = make(map[unsafe.Pointer]bool)
		} else {
			mainWorker.seen = nil
		}
		return val.Print(mainWorker, depth)
	})
}

// catching runs f, turning an evaluation failure into an error. Any other
// panic is a bug in the evaluator and keeps unwinding.
func catching[T any](f func() T) (result T, err error) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		e := asEvalError(v)
		if e == nil {
			panic(v)
		}
		var zero T
		result, err = zero, e
	}()
	return f(), nil
}

// EvalString parses and evaluates a Nix expression given as text.
func EvalString(s string) (NixValue, error) {
	pr, err := p.ParseString(s)
	if err != nil {
		return NixValue{}, err
	}
	return Eval(pr)
}

// resolve evaluates one syntax node. It either sets Value, points the
// expression at another node to continue with in place, or returns an
// expression to evaluate in its stead — one that already exists, and that
// something else may be holding.
func (x *Expression) resolve(w *worker) *Expression {
	n := x.node()
	scope := x.scope()
	switch nt := n.Type; nt {
	default:
		w.throwf(ErrEval, "unsupported expression: %v", nt)

	// A literal is the same value however often it is evaluated, so each of
	// these is worked out once and kept against the node.
	case p.URINode, p.PathNode, p.FloatNode, p.IntNode:
		x.setValue(scope.literal(w, n))

	case p.StringNode, p.IStringNode:
		x.setValue(x.evalString(w))

	case p.IDNode:
		return scope.lookup(w, n)

	case p.ParensNode:
		x.setThunk(scope, n.Nodes[0])

	case p.ListNode:
		list := make(NixList, len(n.Nodes))
		for i, c := range n.Nodes {
			// An element that names a binding is that binding, as it is for the
			// argument of a call: Nix shares it the same way, in ExprList::eval.
			// A shared thunk is described by whoever it belongs to, so only a
			// thunk made here is labelled as an element of this list.
			y, shared := scope.thunkFor(w, c)
			if !shared {
				y.blaming(blameListElem)
			}
			list[i] = y
		}
		x.setValue(ListValue(list))

	case p.SetNode, p.RecSetNode, p.LetNode:
		x.evalBinds(w, nt)

	case p.SelectNode, p.SelectOrNode:
		return x.evalSelect(w, nt)

	case p.WithNode:
		// The with-set is not forced here: a name is only looked for in it when
		// the frames do not bind it, and forcing it eagerly makes a fixpoint
		// whose body is `with self; …` recurse. See Env.fromWith.
		x.setThunk(scope.bind1(w, newScoped(w, scope, n.Nodes[0])), n.Nodes[1])

	case p.IfNode:
		cond := assertBool(w, x.evalNodeAs(w, n.Nodes[0], blameCond))
		if cond {
			x.setThunk(scope, n.Nodes[1])
		} else {
			x.setThunk(scope, n.Nodes[2])
		}

	case p.AssertNode:
		if !assertBool(w, x.evalNodeAs(w, n.Nodes[0], blameAssert)) {
			w.throwf(ErrAssertion, "assertion '%s' failed", x.parser().NodeString(n.Nodes[0]))
		}
		x.setThunk(scope, n.Nodes[1])

	case p.FunctionNode:
		x.setValue(x.evalFunction(w))

	case p.ApplyNode:
		// `f a b c` parses as `((f a) b) c`; the whole chain is gathered so
		// that the call is entered once. See applySpine.
		var args [maxSpine]*p.Node
		head, k := n, 0
		for head.Type == p.ApplyNode && k < maxSpine {
			args[k] = head.Nodes[1]
			head = head.Nodes[0]
			k++
		}
		fn := assertLambda(w, x.evalNode(w, head))
		// The arguments were gathered from the outside in, so they come out
		// in reverse.
		var thunks [maxSpine]*Expression
		for i := range k {
			thunks[i], _ = scope.thunkFor(w, args[k-1-i])
		}
		return applySpine(w, x, fn, thunks[:k])

	case p.OpNegateNode, p.OpNotNode, p.OpQuestionNode:
		x.setValue(x.evalUnaryOp(w, nt))

	case p.OpAddNode, p.OpReduceNode, p.OpMultiplyNode, p.OpDivideNode,
		p.OpGreaterNode, p.OpLessNode, p.OpGeqNode, p.OpLeqNode,
		p.OpConcatNode, p.OpUpdateNode, p.OpAndNode, p.OpOrNode, p.OpImplNode,
		p.OpEqNode, p.OpNeqNode:
		x.setValue(x.evalBinaryOp(w, nt))
	}
	return nil
}

// evalString evaluates a quoted or an indented string literal. One with no
// interpolation in it is a literal like any other, and is kept against the
// node rather than rebuilt on every evaluation.
func (x *Expression) evalString(w *worker) NixValue {
	entry := x.scope().file.static.get(x.node().ID)
	if !entry.val.IsNone() {
		return entry.val
	}
	if x.node().Type == p.IStringNode {
		return x.evalIndentedString(w, entry)
	}
	// A quoted string is built as it is read: what a piece contributes does
	// not depend on the pieces after it, so there is nothing to collect first.
	interpolated := false
	result := &NixString{}
	var b strings.Builder
	b.Grow(x.stringSize())
	for _, c := range x.node().Nodes {
		switch c.Type {
		default:
			w.throwf(ErrEval, "unsupported string part: %v", c.Type)
		case p.TextNode:
			b.WriteString(unescapeQuoted(x.parser().TokenString(c.Tokens[0])))
		case p.InterpNode:
			// Interpolations are evaluated in source order, as Nix does.
			interpolated = true
			part := CoerceToString(w, x.evalNodeAs(w, c.Nodes[0], blameInterp))
			b.WriteString(part.Content)
			result.absorb(part)
		}
	}
	result.Content = b.String()
	val := StrValue(result)
	if !interpolated {
		entry.cache(x.scope().file, val)
	}
	return val
}

// stringSize guesses how long the string will be, so that building it does not
// have to grow the buffer: the literal text is known exactly, and an
// interpolation is guessed at.
func (x *Expression) stringSize() int {
	n := 0
	for _, c := range x.node().Nodes {
		if c.Type == p.TextNode {
			n += len(x.parser().TokenBytes(c.Tokens[0]))
		} else {
			n += 16
		}
	}
	return n
}

// evalIndentedString evaluates a `”…”` string, whose pieces have to be
// collected before any of them can be written: how much indentation to strip
// is decided by all of them together.
func (x *Expression) evalIndentedString(w *worker, entry *static) NixValue {
	interpolated := false
	parts := make([]stringPart, 0, len(x.node().Nodes))
	for _, c := range x.node().Nodes {
		switch c.Type {
		default:
			w.throwf(ErrEval, "unsupported string part: %v", c.Type)
		case p.TextNode:
			parts = append(parts, stringPart{text: x.parser().TokenString(c.Tokens[0])})
		case p.InterpNode:
			interpolated = true
			part := x.evalNodeAs(w, c.Nodes[0], blameInterp)
			parts = append(parts, stringPart{interp: CoerceToString(w, part)})
		}
	}
	parts = stripIndentation(parts)

	result := &NixString{}
	var b strings.Builder
	for _, part := range parts {
		if part.interp != nil {
			b.WriteString(part.interp.Content)
			result.absorb(part.interp)
			continue
		}
		b.WriteString(unescapeIndented(part.text))
	}
	result.Content = b.String()
	val := StrValue(result)
	if !interpolated {
		entry.cache(x.scope().file, val)
	}
	return val
}

// evalBinds evaluates a set, a recursive set, or the bindings of a `let`.
//
// A `let` and a recursive set are in scope of their own bindings, so they open
// a frame of the names the pass said they bind, and fill each slot as it is
// bound. A binding written further down is an empty slot until then, which is
// what a name used in a dynamic attribute name can see, and the only way to
// see one; everything else reads the frame long after it is full, because a
// binding is a thunk and nothing here forces one.
//
// A name the syntax does not give — `${e} = v` — is bound in the set but has no
// slot, since the pass could not know it was coming. That is what Nix does too,
// and it is why such a name is not in scope of the group's own bindings.
func (x *Expression) evalBinds(w *worker, nt p.NodeType) {
	n := x.node()
	outer := x.scope()
	bindNodes := n.Nodes
	if nt == p.LetNode {
		bindNodes = n.Nodes[0].Nodes
	}

	env, group := outer, []Sym(nil)
	if nt == p.RecSetNode || nt == p.LetNode {
		group = outer.file.static.get(n.ID).group
		env = outer.child(w, len(group))
	}
	// slot is where a name of this group goes, or -1 for one it does not bind.
	slot := func(sym Sym) int {
		if i, ok := slices.BinarySearch(group, sym); ok {
			return i
		}
		return -1
	}

	// Inherited bindings make the set larger than this estimate.
	set := NewSet(len(bindNodes))
	for _, c := range bindNodes {
		switch c.Type {
		default:
			w.throwf(ErrEval, "unsupported binding: %v", c.Type)

		case p.BindNode:
			// A dynamic component that evaluates to null skips the whole
			// binding, which is how `{ ${null} = 1; }` binds nothing.
			if env.attrPathNull(w, c.Nodes[0]) {
				continue
			}
			attrpath := env.evalAttrPath(w, c.Nodes[0])
			// A value that names a binding is that binding, as a list element
			// and a call argument are: Nix borrows the same way, in
			// ExprAttrs::eval. What is borrowed is named by whoever it belongs
			// to, so only a thunk of this binding's own is named after it.
			y, borrowed := env.thunkForBinding(w, c.Nodes[1])
			if !borrowed {
				y.blamingAttr(attrpath[len(attrpath)-1])
			}
			leaf := set.Bind(w, attrpath, y)
			if i := slot(attrpath[0]); i >= 0 {
				// A path of one component binds the value itself; a longer one
				// binds the set the rest of it was nested into.
				if len(attrpath) == 1 {
					env.vals[i] = y
				} else if top, ok := set.Get(attrpath[0]); ok {
					env.vals[i] = top
				}
			}
			// The position is the attribute's name, so unsafeGetAttrPos can
			// point back at it.
			if comps := c.Nodes[0].Nodes; len(comps) > 0 {
				leaf.setPos(attrpath[len(attrpath)-1], outer.parser().NodePos(comps[len(comps)-1]))
			}

		case p.InheritNode:
			// `inherit a;` is `a = a;` read from the scope around the group, so
			// that it takes the name it shadows rather than itself.
			for _, id := range c.Nodes[0].Nodes {
				sym := outer.file.static.get(id.ID).sym
				y := newScoped(w, outer, id).blamingAttr(sym)
				set.Bind1(sym, y)
				set.setPos(sym, outer.parser().NodePos(id))
				if i := slot(sym); i >= 0 {
					env.vals[i] = y
				}
			}

		case p.InheritFromNode:
			// `inherit (e) a;` is `a = (e).a;`; e itself stays lazy, and is
			// shared by every name inherited from it.
			from := newScoped(w, env, c.Nodes[0])
			for _, id := range c.Nodes[1].Nodes {
				sym := outer.file.static.get(id.ID).sym
				y := from.selectAttr(w, sym).blamingAttr(sym)
				set.Bind1(sym, y)
				set.setPos(sym, outer.parser().NodePos(id))
				if i := slot(sym); i >= 0 {
					env.vals[i] = y
				}
			}
		}
	}
	// The names are put in order now that the group is complete, which is also
	// when a name bound twice is caught. Nothing has read the set yet: the
	// bindings are thunks, and the body below is only pointed at.
	set.finishAll(w)
	if nt == p.LetNode {
		x.setThunk(env, n.Nodes[1])
	} else {
		x.setValue(SetValue(set))
	}
}

// evalSelect evaluates `e.a.b` and `e.a.b or fallback`, returning the
// expression the path selects.
func (x *Expression) evalSelect(w *worker, nt p.NodeType) *Expression {
	n := x.node()
	attrpath := x.scope().evalAttrPath(w, n.Nodes[1])
	var or *Expression
	if nt == p.SelectOrNode {
		or = newScoped(w, x.scope(), n.Nodes[2])
	}
	// Only the leading expression is labelled: the attributes selected along
	// the way are shared with the set that holds them, and already carry their
	// own label.
	expr := newScoped(w, x.scope(), n.Nodes[0]).blaming(blameSelect)
	for _, sym := range attrpath {
		// As in Nix, `or` also covers selecting from a non-set.
		val := expr.Eval(w)
		if val.Kind() == KindSet {
			if y, found := val.Set().Get(sym); found {
				expr = y
				continue
			}
		}
		if or != nil {
			expr = or
			break
		}
		if val.Kind() != KindSet {
			w.throwf(ErrType, "value is %s while a set was expected", anTypeName(val))
		}
		w.throwf(ErrMissingAttribute, "attribute '%s' missing", sym)
	}
	return expr
}

// evalFunction builds a closure from a function node.
//
// Everything but the scope is decided by the syntax, so it is worked out once
// per node and shared: a closure is then two words. That matters because a
// function written inside another one — every curried definition — is created
// afresh on each call, and rebuilding the formal-argument map with it was one
// of the largest sources of allocation in the evaluator.
func (x *Expression) evalFunction(w *worker) NixValue {
	scope := x.scope()
	return LambdaValue(w, &NixExprLambda{lambdaInfo: scope.lambdaInfo(w, x.node()), Env: scope})
}

// lambdaInfo describes a function node: the names it binds and where its body
// is. The grammar hands us the body last, preceded by an identifier
// (`a: …` or `…@a: …`) and/or a formal argument set (`{ a, b ? 1, ... }: …`).
// lambdaInfo is what a function node binds and where its body is, worked out
// before the evaluation started; see prepare.go. A shape the pass could not
// accept is raised here rather than there, so that the failure belongs to the
// evaluation that reached it and carries its backtrace.
func (env *Env) lambdaInfo(w *worker, n *p.Node) *lambdaInfo {
	e := env.file.static.get(n.ID)
	if e.lambda == nil {
		w.throwf(ErrEval, "%s", e.bad)
	}
	return e.lambda
}

// selectAttr returns the attribute sym of the set this expression evaluates
// to, without forcing it yet.
func (x *Expression) selectAttr(w *worker, sym Sym) *Expression {
	// The worker is the one that forces this, not the one that made it.
	return thunk(w, func(w *worker) NixValue {
		set := assertSet(w, x.Eval(w))
		y, ok := set.Get(sym)
		if !ok {
			w.throwf(ErrMissingAttribute, "attribute '%s' missing", sym)
		}
		return y.Eval(w)
	})
}

// Delay is an unevaluated expression for a parsed file in a scope, for a
// caller outside the evaluator. See delay.
func Delay(scope *Scope, pr *p.Parser) *Expression {
	return delay(mainWorker, scope, pr)
}
