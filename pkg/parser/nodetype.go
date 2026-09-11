// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package parser

type NodeType uint16

const (
	ApplyNode NodeType = iota
	ArgNode
	ArgSetNode
	AssertNode
	AttrPathNode
	BindNode
	BindsNode
	FloatNode
	FunctionNode
	IDNode
	IStringNode
	IfNode
	InheritFromNode
	InheritListNode
	InheritNode
	IntNode
	InterpNode
	LetNode
	ListNode
	ParensNode
	PathNode
	RecSetNode
	SelectNode
	SelectOrNode
	SetNode
	StringNode
	TextNode
	URINode
	WithNode

	OpNode
)

const (
	OpNegateNode   = OpNode + negate
	OpQuestionNode = OpNode + '?'
	OpConcatNode   = OpNode + concat
	OpDivideNode   = OpNode + '/'
	OpMultiplyNode = OpNode + '*'
	OpReduceNode   = OpNode + '-'
	OpAddNode      = OpNode + '+'
	OpNotNode      = OpNode + '!'
	OpUpdateNode   = OpNode + update
	OpGeqNode      = OpNode + geq
	OpLeqNode      = OpNode + leq
	OpGreaterNode  = OpNode + '>'
	OpLessNode     = OpNode + '<'
	OpNeqNode      = OpNode + neq
	OpEqNode       = OpNode + eq
	OpAndNode      = OpNode + and
	OpOrNode       = OpNode + or
	OpImplNode     = OpNode + impl
)

var nodeName = map[NodeType]string{
	ApplyNode:       "apply",
	ArgNode:         "arg",
	ArgSetNode:      "argset",
	AssertNode:      "assert",
	AttrPathNode:    "attrpath",
	BindNode:        "bind",
	BindsNode:       "binds",
	FloatNode:       "float",
	FunctionNode:    "function",
	IDNode:          "id",
	IStringNode:     "istring",
	IfNode:          "if",
	InheritFromNode: "inheritfrom",
	InheritListNode: "inheritlist",
	InheritNode:     "inherit",
	IntNode:         "int",
	InterpNode:      "interp",
	LetNode:         "let",
	ListNode:        "list",
	ParensNode:      "parens",
	PathNode:        "path",
	RecSetNode:      "recset",
	SelectNode:      "select",
	SelectOrNode:    "selector",
	SetNode:         "set",
	StringNode:      "string",
	TextNode:        "text",
	URINode:         "uri",
	WithNode:        "with",

	OpNegateNode:   "ne",
	OpQuestionNode: "?",
	OpConcatNode:   "++",
	OpDivideNode:   "/",
	OpMultiplyNode: "*",
	OpReduceNode:   "-",
	OpAddNode:      "+",
	OpNotNode:      "!",
	OpUpdateNode:   "//",
	OpGeqNode:      ">=",
	OpLeqNode:      "<=",
	OpGreaterNode:  ">",
	OpLessNode:     "<",
	OpNeqNode:      "!=",
	OpEqNode:       "==",
	OpAndNode:      "&&",
	OpOrNode:       "||",
	OpImplNode:     "->",
}

func (nt NodeType) String() string {
	if s, ok := nodeName[nt]; ok {
		return s
	}
	panic("unknown node type")
}
