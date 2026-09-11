// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package parser

//go:generate goyacc nix.y
//go:generate sed "/yyS :=/a\\\tp := yylex.(*Parser)" -i y.go

type Node struct {
	Type NodeType
	// ID numbers the nodes of one parse densely from zero, so that a pass over
	// the tree can keep its own data about a node in a plain array instead of a
	// map. It packs into the padding after Type, so it costs nothing.
	ID     uint32
	Tokens []int
	Nodes  []*Node

	preallocatedNodes [2]*Node
}

const nodesBlock = 1024

type Parser struct {
	*lexResult
	prev   int
	last   int
	errors ParserErrors

	nodes     []Node
	nextNode  int
	nodeCount uint32

	Result *Node
}

func newParser(lr *lexResult) *Parser {
	return &Parser{lexResult: lr, prev: -1, last: len(lr.tokens) - 1}
}

// Path is the name of the source this was parsed from: a file path, or
// "«string»" for an expression given as text. The evaluator uses it to
// resolve the relative paths a file contains against the file's directory.
func (p *Parser) Path() string { return p.file.Name() }

func (p *Parser) Lex(lval *yySymType) int {
	if p.prev == p.last {
		return 0
	}
	p.prev++
	lval.token = p.prev
	return p.tokens[p.prev].sym
}

func (p *Parser) Error(s string) {
	// prev is still -1 when the parser fails without having consumed a token,
	// which is what an empty input does.
	pos := p.StartPos()
	if p.prev >= 0 {
		pos = p.TokenPos(p.prev)
	}
	err := &ParserError{Pos: pos, Line: p.SourceLine(pos), Desc: s}
	p.errors = append(p.errors, err)
}

func (p *Parser) NewNode(t NodeType, tokens ...int) *Node {
	if p.nextNode == len(p.nodes) {
		p.nodes = make([]Node, nodesBlock)
		p.nextNode = 0
	}
	n := &p.nodes[p.nextNode]
	p.nextNode++
	n.Type = t
	n.ID = p.nodeCount
	p.nodeCount++
	n.Tokens = tokens
	n.Nodes = n.preallocatedNodes[:0]
	return n
}

func (p *Parser) Op(t NodeType, tokens ...int) *Node { return p.NewNode(OpNode+t, tokens...) }

func (n *Node) SetType(t NodeType) *Node { n.Type = t; return n }

func (n *Node) T(tokens ...int) *Node   { n.Tokens = append(n.Tokens, tokens...); return n }
func (n *Node) T1(t0 int) *Node         { n.Tokens = append(n.Tokens, t0); return n }
func (n *Node) T2(t0, t1 int) *Node     { n.Tokens = append(n.Tokens, t0, t1); return n }
func (n *Node) T3(t0, t1, t2 int) *Node { n.Tokens = append(n.Tokens, t0, t1, t2); return n }

func (n *Node) N(nodes ...*Node) *Node    { n.Nodes = append(n.Nodes, nodes...); return n }
func (n *Node) N1(n0 *Node) *Node         { n.Nodes = append(n.Nodes, n0); return n }
func (n *Node) N2(n0, n1 *Node) *Node     { n.Nodes = append(n.Nodes, n0, n1); return n }
func (n *Node) N3(n0, n1, n2 *Node) *Node { n.Nodes = append(n.Nodes, n0, n1, n2); return n }

// NodeCount is how many nodes the parse produced, and so the exclusive upper
// bound of every Node.ID it produced.
func (p *Parser) NodeCount() uint32 { return p.nodeCount }

func (p *Parser) LispResult() string {
	return p.Result.lispFormat(p)
}

func (n *Node) lispFormat(p *Parser) string {
	s := n.Type.String()
	if s == "" {
		panic("unknown node type")
	}
	for _, child := range n.Nodes {
		s += " " + child.lispFormat(p)
	}
	switch n.Type {
	case URINode, PathNode, FloatNode, IntNode:
		s += " " + p.TokenString(n.Tokens[0])
	case IDNode:
		s += " |" + p.TokenString(n.Tokens[0]) + "|"
	case TextNode:
		s += ` "` + p.TokenString(n.Tokens[0]) + `"`
	}
	return "(" + s + ")"
}

func parse(lr *lexResult) (p *Parser, err error) {
	p = newParser(lr)
	yyErrorVerbose = true
	yyParse(p)
	if len(p.errors) == 0 {
		// A name that only looks computed is folded first, so that everything
		// after it — merging, and the pass that resolves names — sees one kind
		// of static name rather than two.
		p.foldAttrNames(p.Result)
		p.normalize(p.Result)
	}
	if len(p.errors) == 0 {
		return
	}
	err = p.errors
	return
}

func ParseFile(path string) (p *Parser, err error) {
	lr, err := lexFile(path)
	if err != nil {
		return
	}
	return parse(lr)
}

func ParseString(s string) (p *Parser, err error) {
	lr, err := lex([]byte(s), "")
	if err != nil {
		return
	}
	return parse(lr)
}
