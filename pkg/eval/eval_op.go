package eval

import (
	p "github.com/aleksanaa/nyax/pkg/parser"
)

// operand evaluates the i-th child of the current node.
func (x *Expression) operand(w *worker, i int) NixValue {
	return x.evalNode(w, x.node().Nodes[i])
}

func (x *Expression) evalUnaryOp(w *worker, nt p.NodeType) NixValue {
	switch nt {
	case p.OpNotNode:
		return Bool(!assertBool(w, x.operand(w, 0)))

	case p.OpNegateNode:
		switch v := x.operand(w, 0); v.Kind() {
		case KindInt:
			return Int(-v.Int())
		case KindFloat:
			return Float(-v.Float())
		default:
			w.throwf(ErrType, "value is %s while a number was expected", anTypeName(v))
		}

	case p.OpQuestionNode:
		// `e ? a.b` tests for an attribute path without forcing the value it
		// finds; a non-set anywhere along the path simply makes it false.
		val := x.operand(w, 0)
		path := x.scope().evalAttrPath(w, x.node().Nodes[1])
		for i, sym := range path {
			if val.Kind() != KindSet {
				return False
			}
			y, found := val.Set().Get(sym)
			if !found {
				return False
			}
			if i == len(path)-1 {
				// The last component is only tested for, never forced.
				break
			}
			val = y.Eval(w)
		}
		return True
	}
	w.throwf(ErrEval, "unsupported unary operator: %v", nt)
	return NixValue{}
}

func (x *Expression) evalBinaryOp(w *worker, nt p.NodeType) NixValue {
	switch nt {
	// `&&`, `||` and `->` are short-circuiting, so the right operand is only
	// forced when the left one does not already decide the answer.
	case p.OpAndNode:
		if !assertBool(w, x.operand(w, 0)) {
			return False
		}
		return Bool(assertBool(w, x.operand(w, 1)))

	case p.OpOrNode:
		if assertBool(w, x.operand(w, 0)) {
			return True
		}
		return Bool(assertBool(w, x.operand(w, 1)))

	case p.OpImplNode:
		if !assertBool(w, x.operand(w, 0)) {
			return True
		}
		return Bool(assertBool(w, x.operand(w, 1)))
	}

	// Everything below forces both sides, left first, as the language says.
	lhs, rhs := x.operand(w, 0), x.operand(w, 1)
	switch nt {
	case p.OpAddNode:
		return Add(w, lhs, rhs)

	case p.OpConcatNode:
		return ListValue(assertList(w, lhs).Concat(assertList(w, rhs)))

	case p.OpUpdateNode:
		return SetValue(assertSet(w, lhs).Update(assertSet(w, rhs)))

	case p.OpEqNode:
		return Bool(lhs.Compare(w, rhs))

	case p.OpLessNode:
		return Bool(CompareOrder(w, lhs, rhs) < 0)

	case p.OpGreaterNode:
		return Bool(CompareOrder(w, lhs, rhs) > 0)

	case p.OpLeqNode:
		return Bool(CompareOrder(w, lhs, rhs) <= 0)

	case p.OpGeqNode:
		return Bool(CompareOrder(w, lhs, rhs) >= 0)

	case p.OpNeqNode:
		return Bool(!lhs.Compare(w, rhs))

	default:
		return Arith(w, lhs, rhs, nt)
	}
}
