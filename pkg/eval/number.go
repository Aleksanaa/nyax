package eval

import (
	"path"
	"strconv"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// printInt renders an integer the way Nix does. It is worth going through
// strconv rather than fmt: toString on a number is common enough in Nix code
// that formatting showed up in the profile of a workload that only builds
// attribute names.
func printInt(i int64) string { return strconv.FormatInt(i, 10) }

// printFloat matches the %.6g Nix uses to display floats.
func printFloat(f float64) string { return strconv.FormatFloat(f, 'g', 6, 64) }

// NixNumber is what arithmetic works on. The operand types are known where
// arith is called, so this is one of the places a type parameter costs
// nothing: each instance is compiled for the numbers it actually adds.
type NixNumber interface{ int64 | float64 }

// arith applies an arithmetic operator to two numbers of the same type, and
// wraps the result as the value of that type.
func arith[T NixNumber](w *worker, a, b T, op p.NodeType, wrap func(T) NixValue) NixValue {
	switch op {
	case p.OpAddNode:
		return wrap(a + b)
	case p.OpReduceNode:
		return wrap(a - b)
	case p.OpMultiplyNode:
		return wrap(a * b)
	case p.OpDivideNode:
		if b == 0 {
			w.throwf(ErrEval, "division by zero")
		}
		return wrap(a / b)
	}
	w.throwf(ErrEval, "unsupported arithmetic operator: %v", op)
	return NixValue{}
}

// Arith applies an arithmetic operator, promoting to float unless both
// operands are integers.
func Arith(w *worker, a, b NixValue, op p.NodeType) NixValue {
	if a.kind == KindInt && b.kind == KindInt {
		return arith(w, a.Int(), b.Int(), op, Int)
	}
	if !a.IsNumber() || !b.IsNumber() {
		bad := a
		if a.IsNumber() {
			bad = b
		}
		w.throwf(ErrType, "value is %s while a number was expected", anTypeName(bad))
	}
	return arith(w, a.toFloat(), b.toFloat(), op, Float)
}

// Add implements `+`, which concatenates strings and paths as well as adding
// numbers. As in Nix, the first operand decides: a number makes it arithmetic,
// a path makes the result a path, and anything else — a string, or a set with
// an outPath or a __toString, which is how `drv + "/subdir"` works — makes the
// result a string.
func Add(w *worker, a, b NixValue) NixValue {
	switch a.kind {
	case KindInt, KindFloat:
		return Arith(w, a, b, p.OpAddNode)

	case KindPath:
		// The operands are concatenated with no separator, so `./a + "b"`
		// names `./ab` rather than `./a/b`; the result is then canonicalized.
		// A path is not copied to the store — the result is a path, which is
		// copied when it is used in a derivation — and a reference to a store
		// path cannot be folded into a path this way.
		sa, sb := CoerceNoCopy(w, a), CoerceNoCopy(w, b)
		if hasContext(sa) || hasContext(sb) {
			w.throwf(ErrEval, "a string that refers to a store path cannot be appended to a path")
		}
		return PathValue(&NixPath{Path: path.Clean(sa.Content + sb.Content)})
	}
	return StrValue(CoerceToString(w, a).Concat(CoerceToString(w, b)))
}

// hasContext reports whether a string refers to a derivation or a store path.
func hasContext(s *NixString) bool {
	return s.extra != nil && len(s.extra.Context) != 0
}
