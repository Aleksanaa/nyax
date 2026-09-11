package eval

import (
	"fmt"
	"unsafe"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// blameKind labels why an expression is being evaluated, so that a backtrace
// frame can be described without building the string on the happy path.
type blameKind uint8

const (
	blameNone     blameKind = iota
	blameAttr               // "while evaluating the attribute 'x'"
	blameCall               // "while calling a function"
	blamePrimop             // "while calling the 'head' builtin"
	blameListElem           // "while evaluating a list element"
	blameInterp             // "while evaluating a string interpolation"
	blameSelect             // "while evaluating the attribute path"
	blameCond               // "while evaluating a condition"
	blameWith               // "while evaluating the set of a with expression"
	blameAssert             // "while evaluating an assertion"
)

func (b blameKind) describe(sym Sym) string {
	switch b {
	case blameAttr:
		return fmt.Sprintf("while evaluating the attribute '%s'", sym)
	case blameCall:
		return "while calling a function"
	case blamePrimop:
		return fmt.Sprintf("while calling the '%s' builtin", sym)
	case blameListElem:
		return "while evaluating a list element"
	case blameInterp:
		return "while evaluating a string interpolation"
	case blameSelect:
		return "while evaluating an attribute path"
	case blameCond:
		return "while evaluating a condition"
	case blameWith:
		return "while evaluating the set of a with expression"
	case blameAssert:
		return "while evaluating an assertion"
	}
	return ""
}

// Expression is a thunk: an unevaluated expression together with the scope it
// closes over, holding its value once forced.
//
// The two words a and b are read according to kind, which is what keeps a
// thunk down to 32 bytes — the single most numerous object an evaluation
// allocates, so its size decides how much of a run is spent in the garbage
// collector:
//
//	kind == KindNone, b != nil   a is the scope and b the node to evaluate
//	kind == KindNone, b == nil   a is a builtin call to run
//	kind != KindNone             the value, whose pointer is a and whose
//	                             word — an integer, a list's length — is num
//
// Resolving a node may point the expression at another one to carry on with in
// place, or hand back an expression to evaluate in its stead (a selected
// attribute, the binding a name refers to); the value is kept here either way,
// so a chain is only ever walked once.
type Expression struct {
	a, b unsafe.Pointer
	num  int64
	kind Kind

	// blame says what a backtrace should call this frame. What it needs
	// besides the kind — the name of an attribute, the function a call
	// belongs to — is a property of the node, and is kept against the node
	// rather than in every expression that mentions it.
	blame blameKind

	// depth is where on the worker's stack the force of this expression
	// began. Keeping it here rather than passing it to the deferred call that
	// ends the force is what keeps that call to two words — the worker has to
	// be one of them, and a third was worth 2-4% of a run. It is int16
	// because the depth is bounded by maxCallDepth, which leaves the room the
	// state below needs without widening the thunk.
	depth int16

	// state is how far this thunk has been forced: see stateFree below. It
	// lives in the thunk rather than in a table beside it, because anything the
	// heap can reach holding an *Expression would make every operand escape to
	// the heap. It is a state of its own rather than of kind so that a thunk
	// under way is told apart from one that has not been touched, which is what
	// catches a value defined in terms of itself.
	state uint8
}

// Val is the value this expression has been forced to, or no value at all.
func (x *Expression) Val() NixValue {
	return NixValue{ptr: x.a, num: x.num, kind: x.kind}
}

// setValue records the value the expression has been forced to, which also
// releases the scope and the node that produced it: they are no longer part of
// what this expression is.
func (x *Expression) setValue(v NixValue) {
	x.a, x.b, x.num, x.kind = v.ptr, nil, v.num, v.kind
	// Having the value is also what ends the force: the mark release would
	// take off is replaced by the value itself.
	x.state = stateForced
}

// scope is the scope an unforced expression evaluates in.
func (x *Expression) scope() *Env {
	if x.kind != KindNone || x.b == nil {
		return nil
	}
	return (*Env)(x.a)
}

// node is the syntax an unforced expression evaluates, or nil for a builtin
// call and for one that has been forced already.
func (x *Expression) node() *p.Node {
	if x.kind != KindNone {
		return nil
	}
	return (*p.Node)(x.b)
}

// native is the builtin call an unforced expression runs, if it is one.
func (x *Expression) native() *nativeCall {
	if x.kind != KindNone || x.b != nil {
		return nil
	}
	return (*nativeCall)(x.a)
}

// setThunk points the expression at a node to evaluate in a scope.
func (x *Expression) setThunk(scope *Env, n *p.Node) {
	x.a, x.b = unsafe.Pointer(scope), unsafe.Pointer(n)
}

// exprSlabSize is how many expressions are allocated at a time, or 1 to
// allocate them one by one.
//
// Handing thunks out from a block trades one allocation per thunk for one per
// block, the same trick the parser uses for nodes. A block cannot be freed
// until every thunk in it is unreachable, so a larger block can pin memory a
// smaller one would release; but the benchmark workloads allocate thunks whose
// lifetimes line up, so they do not. Measured on them, 2048 is ~5% faster than
// 256 for less total allocation and no more live heap: the win is fewer GC
// rounds, not a smaller heap.
const exprSlabSize = 2048

// parser is the parser the expression's node belongs to. It is reached through
// the scope rather than stored per expression, of which there are far more.
func (x *Expression) parser() *p.Parser {
	return x.scope().parser()
}

// newScoped is an unevaluated expression for a node in a scope.
func newScoped(w *worker, scope *Env, n *p.Node) *Expression {
	y := w.newExpr()
	y.setThunk(scope, n)
	return y
}

// blaming labels x with what a backtrace should say about it. It returns x for
// chaining.
func (x *Expression) blaming(kind blameKind) *Expression {
	x.blame = kind
	return x
}

// blamingAttr labels x as the value of an attribute. The name is recorded
// against whatever produces the value, so that the expression itself does not
// have to carry a field for it.
func (x *Expression) blamingAttr(sym Sym) *Expression {
	x.blame = blameAttr
	if c := x.native(); c != nil {
		c.sym = sym
		return x
	}
	if n := x.node(); n != nil {
		// The same node is bound to the same name on every evaluation, so this
		// is a write to a page that is already right almost every time.
		// The pass settled the name for every attribute the syntax names
		// outright, so this is a load and a compare; only a computed name is
		// ever stored, and then only the first time the group is evaluated.
		if e := x.scope().file.static.get(n.ID); e.attrSym != int32(sym) {
			e.attrSym = int32(sym)
		}
	}
	return x
}

// Pos reports where this expression starts, or nil when it has no syntax (a
// builtin thunk, or a value constructed by Go).
func (x *Expression) Pos() *p.LexPosition {
	pr := x.parser()
	n := x.node()
	if pr == nil || n == nil {
		return nil
	}
	return pr.NodePos(n)
}

// parser is the file the frame's node was parsed from, or nil for a frame with
// no syntax behind it, such as a builtin.
func (f evalFrame) parser() *p.Parser {
	return f.scope.parser()
}

// pos reports where the frame's expression starts, or nil when it has no
// syntax (a builtin thunk, or a value constructed by Go).
func (f evalFrame) pos() *p.LexPosition {
	pr := f.parser()
	if pr == nil || f.node == nil {
		return nil
	}
	return pr.NodePos(f.node)
}

// traceFrame renders the frame for a backtrace. What the frame is called, and
// where it points, are read from what the node is known to be: a call points
// at the function rather than at its body, and the value of an attribute is
// named after it.
func (f evalFrame) traceFrame() Frame {
	pr := f.parser()
	pos := f.pos()
	var sym Sym
	if f.native != nil {
		sym = f.native.sym
		if f.native.op != nil {
			sym = f.native.op.Sym
		}
	}
	if f.node != nil && f.scope != nil {
		e := f.scope.file.static.get(f.node.ID)
		if f.blame == blameAttr {
			sym = Sym(e.attrSym)
		}
		if f.blame == blameCall && e.owner != nil && pr != nil {
			pos = pr.NodePos(e.owner)
		}
	}
	var line string
	if pos != nil {
		line = pr.SourceLine(pos)
	}
	return Frame{Pos: pos, Line: line, Desc: f.blame.describe(sym)}
}

// Eval forces the expression to a value, memoizing the result.
func (x *Expression) Eval(w *worker) NixValue {
	// The state says whether there is a value, rather than the kind: a thunk
	// that has been forced to no value at all — `null` — is KindNull, and one
	// that has not been forced is KindNone only until resolve writes to it.
	if x.state == stateForced {
		return x.Val()
	}
	return x.force(w)
}

// maxCallDepth bounds how deeply expressions may nest at run time, so that a
// non-terminating definition is reported as an error instead of exhausting the
// Go stack. Nix imposes the same kind of limit.
const maxCallDepth = 20000

// evalFrame is what the evaluation stack records about one expression being
// forced. It is a copy of the few fields an error report needs rather than a
// pointer, so that an expression evaluated only for its value never has to
// outlive the Go stack frame that forced it.
type evalFrame struct {
	scope  *Env
	node   *p.Node
	native *nativeCall
	blame  blameKind
}

func (x *Expression) force(w *worker) NixValue {
	// Mark the thunk as being forced, which is what an expression defined in
	// terms of itself runs into instead of descending for ever. Eval only
	// calls force for a thunk that has no value yet, so a thunk already marked
	// is one whose value is defined in terms of itself.
	if x.state == stateForcing {
		w.throwf(ErrInfiniteRecursion, "infinite recursion encountered")
	}
	x.state = stateForcing

	depth := w.depth
	x.depth = int16(depth)
	defer x.finish(w)

	// An expression that only stands in front of another one — a parenthesis,
	// the branch an `if` took, the body of a `let` or of a call — is continued
	// in place rather than delegated to a thunk of its own: resolve points
	// this expression at the next node and the loop goes round again. That is
	// one allocation and one nested force fewer per level, and the backtrace
	// is unchanged because each turn still records a frame.
	for {
		if w.depth == maxCallDepth {
			w.throwf(ErrEval, "stack overflow; possible infinite recursion")
		}
		// The frame is built inline rather than through the scope, node and
		// native accessors: this is the hottest loop in the evaluator, and
		// here what an unforced expression holds is already known — a and b
		// are set, kind is KindNone, and b distinguishes a node from a
		// builtin call.
		frame := evalFrame{blame: x.blame}
		var lower *Expression
		if x.b != nil {
			frame.scope, frame.node = (*Env)(x.a), (*p.Node)(x.b)
			w.stack[w.depth] = frame
			w.depth++
			lower = x.resolve(w)
		} else if x.a != nil {
			call := (*nativeCall)(x.a)
			frame.native = call
			w.stack[w.depth] = frame
			w.depth++
			x.setValue(call.run(w))
		} else {
			w.stack[w.depth] = frame
			w.depth++
			w.throwf(ErrEval, "expression has nothing to evaluate")
		}
		if x.kind != KindNone {
			break
		}
		if lower != nil {
			// resolve delegated to an expression that already exists, and
			// which something else may hold. Evaluating it here (rather than
			// continuing in place) keeps every thunk on the stack, so that a
			// cycle through several bindings is detected and each contributes
			// a trace frame.
			x.setValue(lower.Eval(w))
			break
		}
	}
	// Recording the value is also what releases the scope chain and the
	// expressions underneath it, which would otherwise stay reachable for as
	// long as the value does: the thunk is now nothing but what it evaluated
	// to. Failure leaves everything in place — the expression is still
	// unevaluated, and the error being raised describes it from the evaluation
	// stack.
	return x.Val()
}

// finish leaves the expression, whether it produced a value or is being
// unwound past by a failure, dropping every frame it pushed and taking the
// forcing mark off if the value has not already replaced it.
//
// The expression is the deferred call's receiver rather than something the
// worker holds: a pointer to it reachable from the heap would make every
// operand escape, and operands staying on the Go stack is what lets an
// arithmetic-heavy expression allocate nothing per operand.
func (x *Expression) finish(w *worker) {
	w.depth = int(x.depth)
	// Take the forcing mark off, unless the value already replaced it: this
	// only does anything when a failure is unwinding past the force.
	if x.state == stateForcing {
		x.state = stateFree
	}
}

// value wraps an already known value as an evaluated expression.
func value(w *worker, v NixValue) *Expression {
	x := w.newExpr()
	x.setValue(v)
	return x
}

// thunk wraps a Go computation as an unevaluated expression.
func thunk(w *worker, f func(*worker) NixValue) *Expression {
	x := w.newExpr()
	x.a = unsafe.Pointer(&nativeCall{fn: f})
	return x
}

// nativeCall is a value produced in Go rather than worked out from syntax: a
// builtin applied to all of its arguments, or a computation the evaluator sets
// up itself. Holding the arguments here rather than in a closure is what keeps
// a builtin call to two objects.
type nativeCall struct {
	op   *NixPrimop
	args [maxPrimopArgs]*Expression
	fn   func(w *worker) NixValue
	// sym is what a backtrace calls this: the builtin's name, or the
	// attribute the computation stands for.
	sym Sym
}

func (c *nativeCall) run(w *worker) NixValue {
	if c.op != nil {
		return c.op.Func(w, c.args[:c.op.ArgNum]...)
	}
	return c.fn(w)
}

// evalNode evaluates a child node in this expression's scope.
//
// It is for operands that are consumed immediately — the two sides of an
// operator, a condition, the function of an application — where nothing keeps
// the thunk afterwards. Because the evaluation stack stores copies rather than
// pointers, the expression can stay on the Go stack, which is what makes an
// arithmetic-heavy expression allocate nothing per operand. Where a value does
// escape, the compiler falls back to allocating it, so this stays correct
// wherever it is used.
func (x *Expression) evalNode(w *worker, n *p.Node) NixValue { return x.scope().evalNode(w, n) }

// evalNodeAs is evalNode for an operand that a backtrace should name.
func (x *Expression) evalNodeAs(w *worker, n *p.Node, kind blameKind) NixValue {
	var y Expression
	y.setThunk(x.scope(), n)
	y.blame = kind
	return y.Eval(w)
}

// take makes this expression stand for what src stands for.
//
// It is a field-by-field copy rather than an assignment because the forcing
// state belongs to the expression it was marked on: src is a scratch
// expression on the Go stack that nobody else can reach, and this one is fresh
// and unmarked, which is what it must stay.
func (x *Expression) take(src *Expression) {
	x.a, x.b, x.num = src.a, src.b, src.num
	x.kind, x.blame, x.depth = src.kind, src.blame, src.depth
}

// The forcing state of a thunk.
//
// A thunk being forced is marked before its value is worked out, so that an
// expression whose value is defined in terms of itself is met as a mark that
// is already set rather than as an endless descent. Nix calls this
// blackholing; the mark is what stands in the thunk while the value does not
// exist yet.
const (
	stateFree    uint8 = iota // nothing has forced it
	stateForcing              // a force is under way
	stateForced               // the value is in hand
)

// sameThunk reports whether two places hold the very same thunk, which makes
// what they hold equal without forcing it or looking at it.
//
// It is what lets a function be equal to itself. A function is equal to
// nothing, not even another written the same way, so `[ f ] == [ f ]` could
// only be false — except that both lists borrowed the binding rather than
// making a thunk of their own, so the two elements are one thunk, and a thing
// is equal to itself. Nix says the same and by the same means: eqValues opens
// with a pointer comparison, over a tree its own maybeThunk shares this way.
//
// The sharing is what carries the meaning, so this is only asked where a
// container holds the thunks — never of the operands of `==`, which are
// evaluated into places of their own and are never the same thunk, in Nix as
// here.
func sameThunk(a, b *Expression) bool { return a == b }
