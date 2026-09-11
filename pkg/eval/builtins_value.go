package eval

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unsafe"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// arithOp is the builtin for add, sub, mul and div: an arithmetic operator
// applied to two arguments, which Arith promotes the way `+` does.
func arithOp(op p.NodeType) func(*worker, ...*Expression) NixValue {
	return func(w *worker, args ...*Expression) NixValue {
		return Arith(w, args[0].Eval(w), args[1].Eval(w), op)
	}
}

// bLessThan orders values the way the < operator does.
func bLessThan(w *worker, args ...*Expression) NixValue {
	return Bool(CompareOrder(w, args[0].Eval(w), args[1].Eval(w)) < 0)
}

// bitOp is the builtin for bitAnd, bitOr and bitXor: a bitwise operator
// applied to two integers.
func bitOp(op func(a, b int64) int64) func(*worker, ...*Expression) NixValue {
	return func(w *worker, args ...*Expression) NixValue {
		return Int(op(assertInt(w, args[0].Eval(w)), assertInt(w, args[1].Eval(w))))
	}
}

// bCeil and bFloor accept an integer as well, where they are the identity.
func bCeil(w *worker, args ...*Expression) NixValue {
	return roundTo(w, args[0].Eval(w), math.Ceil)
}

func bFloor(w *worker, args ...*Expression) NixValue {
	return roundTo(w, args[0].Eval(w), math.Floor)
}

func roundTo(w *worker, val NixValue, round func(float64) float64) NixValue {
	if !val.IsNumber() {
		w.throwf(ErrType, "value is %s while a number was expected", anTypeName(val))
	}
	return Int(int64(round(val.toFloat())))
}

// bCompareVersions implements Nix version ordering: versions are split into
// runs of digits and of letters, and compared component by component, with an
// empty component and "pre" sorting before anything else.
func bCompareVersions(w *worker, args ...*Expression) NixValue {
	a := splitVersion(assertString(w, args[0].Eval(w)).Content)
	b := splitVersion(assertString(w, args[1].Eval(w)).Content)
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if c := compareVersionComponent(x, y); c != 0 {
			return Int(int64(c))
		}
	}
	return Int(0)
}

func splitVersion(s string) []string {
	var parts []string
	for i := 0; i < len(s); {
		c := s[i]
		if c == '.' || c == '-' {
			i++
			continue
		}
		digit := isDigit(c)
		j := i
		for j < len(s) && s[j] != '.' && s[j] != '-' && isDigit(s[j]) == digit {
			j++
		}
		parts = append(parts, s[i:j])
		i = j
	}
	return parts
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func compareVersionComponent(a, b string) int {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return sign(ai - bi)
	case a == b:
		return 0
	// A "pre" component sorts before everything, an absent one before
	// everything but "pre".
	case a == "pre":
		return -1
	case b == "pre":
		return 1
	case a == "":
		return -1
	case b == "":
		return 1
	case aerr == nil: // a number sorts after a letter component
		return 1
	case berr == nil:
		return -1
	}
	return sign(strings.Compare(a, b))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Types.

func bTypeOf(w *worker, args ...*Expression) NixValue { return String(TypeName(args[0].Eval(w))) }

// isKind is the shape of every builtins.isX: force the argument and say what
// kind came back.
func isKind(w *worker, x *Expression, kind Kind) NixValue { return Bool(x.Eval(w).Kind() == kind) }

func bIsAttrs(w *worker, args ...*Expression) NixValue    { return isKind(w, args[0], KindSet) }
func bIsBool(w *worker, args ...*Expression) NixValue     { return isKind(w, args[0], KindBool) }
func bIsFloat(w *worker, args ...*Expression) NixValue    { return isKind(w, args[0], KindFloat) }
func bIsFunction(w *worker, args ...*Expression) NixValue { return Bool(args[0].Eval(w).IsLambda()) }
func bIsInt(w *worker, args ...*Expression) NixValue      { return isKind(w, args[0], KindInt) }
func bIsList(w *worker, args ...*Expression) NixValue     { return isKind(w, args[0], KindList) }
func bIsNull(w *worker, args ...*Expression) NixValue     { return isKind(w, args[0], KindNull) }
func bIsPath(w *worker, args ...*Expression) NixValue     { return isKind(w, args[0], KindPath) }
func bIsString(w *worker, args ...*Expression) NixValue   { return isKind(w, args[0], KindString) }

// Evaluation control.

func bSeq(w *worker, args ...*Expression) NixValue {
	args[0].Eval(w)
	return args[1].Eval(w)
}

func bDeepSeq(w *worker, args ...*Expression) NixValue {
	deepForce(w, args[0].Eval(w), make(map[unsafe.Pointer]bool))
	return args[1].Eval(w)
}

// deepForce evaluates a value and everything reachable from it. seen is the
// sets and lists already walked, so a value that reaches back to itself is not
// walked again.
func deepForce(w *worker, val NixValue, seen map[unsafe.Pointer]bool) {
	switch val.Kind() {
	case KindList:
		l := val.List()
		if len(l) == 0 {
			return
		}
		key := unsafe.Pointer(&l[0])
		if seen[key] {
			return
		}
		seen[key] = true
		for _, x := range l {
			deepForce(w, x.Eval(w), seen)
		}
	case KindSet:
		key := val.ptr
		if seen[key] {
			return
		}
		seen[key] = true
		for _, a := range val.Set().attrs {
			deepForce(w, a.x.Eval(w), seen)
		}
	}
}

func bThrow(w *worker, args ...*Expression) NixValue {
	w.throwf(ErrThrown, "%s", assertString(w, args[0].Eval(w)).Content)
	return NixValue{}
}

func bAbort(w *worker, args ...*Expression) NixValue {
	w.throwf(ErrAborted, "evaluation aborted with the following error message: '%s'",
		assertString(w, args[0].Eval(w)).Content)
	return NixValue{}
}

// bTryEval evaluates its argument shallowly, reporting failure instead of
// propagating it. As in Nix, only assertions and throws are caught: a type
// error still aborts the whole evaluation.
func bTryEval(w *worker, args ...*Expression) (result NixValue) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		if err := asEvalError(v); err == nil || !err.Kind.Catchable() {
			panic(v)
		}
		result = pair(w, symSuccess, False, symValue, False)
	}()
	val := args[0].Eval(w)
	return pair(w, symSuccess, True, symValue, val)
}

func bTrace(w *worker, args ...*Expression) NixValue {
	fmt.Fprintln(os.Stderr, "trace:", printTraced(w, args[0].Eval(w)))
	return args[1].Eval(w)
}

// printTraced renders a traced value the way Nix does: strings unquoted,
// everything else printed one level deep.
func printTraced(w *worker, val NixValue) string {
	if val.Kind() == KindString {
		return val.Str().Content
	}
	return val.Print(w, 1)
}
