package eval

import (
	"fmt"
	"unsafe"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// NixLambda is anything that can be applied to an argument. Apply is lazy: it
// returns the unevaluated expression standing for the call, so that an
// application only costs what its result is actually used for.
type NixLambda interface {
	Apply(w *worker, arg *Expression) *Expression
}

// functorLambda is a set used as a function through its __functor attribute.
// Applying it calls __functor with the set prepended to the arguments.
type functorLambda struct {
	self NixValue
	fn   NixLambda
}

func (f *functorLambda) Apply(w *worker, arg *Expression) *Expression {
	return apply2(w, f.fn, value(w, f.self), arg)
}

// lambdaInfo is everything about a function that its syntax decides. It does
// not depend on the scope the function was created in, so it is worked out
// once per node and shared by every closure made from that node.
type lambdaInfo struct {
	Arg    Sym // `arg: body` or `{ ... }@arg: body`
	HasArg bool

	Formal      map[Sym]*p.Node // formal name to its default, or nil
	FormalOrder []Sym           // formals in source order, for stable errors
	HasFormal   bool
	HasEllipsis bool

	Body *p.Node

	// next is the function this one's body is, for a curried definition like
	// `a: b: …`. Walking that chain is how a call binds several arguments at
	// once, so it is linked up on first use rather than looked up per call.
	next *lambdaInfo
}

// NixExprLambda is a closure: a function written in Nix, together with the
// frame it was defined in. Everything else about it is decided by the syntax
// and shared through lambdaInfo, so making one costs two words.
type NixExprLambda struct {
	*lambdaInfo
	// Env is the frame the function was defined in. It is held directly rather
	// than through the defining Expression, which drops it once it has been
	// forced.
	Env *Env
}

func (f *NixExprLambda) Apply(w *worker, arg *Expression) *Expression {
	// The frame holds what the function binds, in the slots the pass gave
	// them: the `@` name first, if there is one, then the formals in the order
	// they were written. See resolver.function.
	env := f.Env.child(w, f.slots())
	if f.HasArg {
		env.vals[0] = arg
	}
	if f.HasFormal {
		f.bindFormals(w, env, arg)
	}
	// The frame points at the function, which says more than its body would.
	return newScoped(w, env, f.Body).blaming(blameCall)
}

// slots is how many names the function binds.
func (f *lambdaInfo) slots() int {
	n := len(f.FormalOrder)
	if f.HasArg {
		n++
	}
	return n
}

// formalSlot is where a formal's value goes, after the `@` name if there is one.
func (f *lambdaInfo) formalSlot(i int) int {
	if f.HasArg {
		return i + 1
	}
	return i
}

// bindFormals matches the argument against `{ a, b ? d, ... }` and fills the
// frame with it. Defaults are evaluated in the function's own frame, so one
// formal may refer to another.
func (f *NixExprLambda) bindFormals(w *worker, env *Env, arg *Expression) {
	val := arg.Eval(w)
	if val.Kind() != KindSet {
		w.throwf(ErrType, "value is %s while a set was expected, as the function takes formal arguments",
			anTypeName(val))
	}
	args := val.Set()
	for i, sym := range f.FormalOrder {
		switch y, given := args.Get(sym); {
		case given:
			env.vals[f.formalSlot(i)] = y
		case f.Formal[sym] != nil:
			env.vals[f.formalSlot(i)] = newScoped(w, env, f.Formal[sym]).blamingAttr(sym)
		default:
			w.throwf(ErrEval, "function called without required argument '%s'", sym)
		}
	}
	if f.HasEllipsis || args.Len() <= len(f.Formal) {
		// Without a surplus there is nothing unexpected to report; the loop
		// above already rejected anything missing.
		return
	}
	unexpected := make([]Sym, 0, args.Len()-len(f.Formal))
	for _, a := range args.attrs {
		if _, ok := f.Formal[a.sym]; !ok {
			unexpected = append(unexpected, a.sym)
		}
	}
	if len(unexpected) != 0 {
		SortSym(unexpected) // report the same one on every run
		w.throwf(ErrEval, "function called with unexpected argument '%s'", unexpected[0])
	}
}

// maxPrimopArgs is the highest arity in the builtin table. Collecting a
// call's arguments in an array of that size, rather than a slice, is what
// keeps a builtin call to one object for the arguments and one for the
// expression.
const maxPrimopArgs = 3

// NixPrimop is a builtin function. Func is called once ArgNum arguments have
// been collected; until then application yields a NixPartialPrimop.
type NixPrimop struct {
	Func   func(*worker, ...*Expression) NixValue
	Doc    string
	Sym    Sym
	ArgNum int
}

// printOp is how a builtin renders, which is all a value of one can be asked.
func (op *NixPrimop) printOp() string {
	return fmt.Sprintf("«primop %s»", op.Sym)
}

func (op *NixPrimop) Apply(w *worker, arg *Expression) *Expression {
	var args [maxPrimopArgs]*Expression
	args[0] = arg
	if op.ArgNum == 1 {
		return op.call(w, args)
	}
	return newPartial(op, args, 1)
}

func (op *NixPrimop) call(w *worker, args [maxPrimopArgs]*Expression) *Expression {
	x := w.newExpr()
	x.a = unsafe.Pointer(&nativeCall{op: op, args: args})
	x.blame = blamePrimop
	return x
}

// NixPartialPrimop is a builtin applied to some but not all of its arguments.
// Args holds the N it has been given so far.
type NixPartialPrimop struct {
	Primop *NixPrimop
	Args   [maxPrimopArgs]*Expression
	N      int
}

func (pp *NixPartialPrimop) printOp() string {
	return fmt.Sprintf("«primop %s, %d of %d arguments»", pp.Primop.Sym, pp.N, pp.Primop.ArgNum)
}

func (pp *NixPartialPrimop) Apply(w *worker, arg *Expression) *Expression {
	// The arguments are copied rather than extended in place: a partially
	// applied builtin is a value, and may be applied to several arguments.
	args := pp.Args
	args[pp.N] = arg
	if pp.N+1 == pp.Primop.ArgNum {
		return pp.Primop.call(w, args)
	}
	return newPartial(pp.Primop, args, pp.N+1)
}

// maxSpine is how many arguments of one call are entered together. Beyond
// this the rest are applied one at a time, which is what happened to every
// argument before this existed.
const maxSpine = 8

// applySpine applies a chain of arguments to a function, entering as many
// plain one-name functions as it can in a single step.
//
// `f a b c` is three applications, and taking them one at a time allocates a
// scope for each and a closure for each function in between. Here the scopes
// of one call are allocated together — each already keeps the one before it
// alive, so nothing is retained that was not before — and the closures in
// between are never built at all. It is what Nix's call loop does, and on a
// four-argument recursion it is the difference between five allocations per
// call and two.
//
// It returns the expression standing for the result, or nil when it has
// pointed x at the body to carry on with in place.
func applySpine(w *worker, x *Expression, fn NixLambda, args []*Expression) *Expression {
	for {
		if op, ok := fn.(*NixPrimop); ok && len(args) >= op.ArgNum {
			// The spine holds every argument the builtin takes, so the call
			// is built outright: no partial application to construct, force
			// and take apart again. `bitAnd x 3` and its kin are what a
			// filter or a fold spends most of its time on.
			var call [maxPrimopArgs]*Expression
			copy(call[:], args[:op.ArgNum])
			y := x
			if len(args) > op.ArgNum {
				// More arguments than the builtin takes: it is being used
				// curried, and the result has to be forced to find the
				// function the rest apply to. That cannot happen in place —
				// x is already being forced — so the call gets its own
				// expression.
				y = w.newExpr()
			}
			y.a = unsafe.Pointer(&nativeCall{op: op, args: call})
			y.b = nil
			y.blame = blamePrimop
			if y == x {
				return nil
			}
			fn, args = assertLambda(w, y.Eval(w)), args[op.ArgNum:]
			continue
		}

		lam, ok := fn.(*NixExprLambda)
		if !ok || lam.HasFormal {
			// A builtin, or a function matching a formal argument set: those
			// take one argument at a time.
			y := fn.Apply(w, args[0])
			if len(args) == 1 {
				return y
			}
			fn, args = assertLambda(w, y.Eval(w)), args[1:]
			continue
		}

		// How far the run of plain one-name functions goes, and so how many
		// arguments this step binds.
		var infos [maxSpine]*lambdaInfo
		infos[0] = lam.lambdaInfo
		n := 1
		for n < len(args) && infos[n-1].Body.Type == p.FunctionNode {
			prev := infos[n-1]
			if prev.next == nil {
				prev.next = lam.Env.lambdaInfo(w, prev.Body)
			}
			if prev.next.HasFormal {
				break
			}
			infos[n], n = prev.next, n+1
		}

		parent := bindArgs(w, lam.Env, args[:n])
		body := infos[n-1].Body
		if args = args[n:]; len(args) == 0 {
			// The frame points at the function, which says more than its body
			// would; continuing in place saves the thunk for the body too.
			x.setThunk(parent, body)
			x.blame = blameCall
			return nil
		}
		// More arguments than this run of functions takes, so the body has to
		// be evaluated to find the next one.
		var y Expression
		y.setThunk(parent, body)
		y.blame = blameCall
		fn = assertLambda(w, y.Eval(w))
	}
}

// apply2 applies f to two arguments, for the builtins that take a callback.
func apply2(w *worker, f NixLambda, a, b *Expression) *Expression {
	var x Expression
	args := [2]*Expression{a, b}
	if y := applySpine(w, &x, f, args[:]); y != nil {
		return y
	}
	// applySpine pointed the scratch expression at the body; give it one of
	// its own, since the caller keeps it.
	y := w.newExpr()
	y.take(&x)
	return y
}

// partialValue is a partial application together with the expression that
// holds it, which is the only thing ever done with one.
type partialValue struct {
	partial NixPartialPrimop
	expr    Expression
}

// newPartial is a builtin that has been given some of its arguments.
func newPartial(op *NixPrimop, args [maxPrimopArgs]*Expression, n int) *Expression {
	pv := &partialValue{partial: NixPartialPrimop{Primop: op, Args: args, N: n}}
	pv.expr.setValue(NixValue{kind: KindPartial, ptr: unsafe.Pointer(&pv.partial)})
	return &pv.expr
}

// applyToValue applies a function to a value the evaluator has in hand.
func applyToValue(w *worker, f NixLambda, v NixValue) *Expression {
	return f.Apply(w, value(w, v))
}

// bindArgs binds a call's arguments, one scope per argument, and returns the
// innermost. Each scope keeps the one before it alive anyway, so taking them
// from the same block retains nothing extra, and a call of several arguments
// costs one slab allocation rather than one allocation per argument.
func bindArgs(w *worker, outer *Env, args []*Expression) *Env {
	parent := outer
	for _, arg := range args {
		parent = parent.bind1(w, arg)
	}
	return parent
}
