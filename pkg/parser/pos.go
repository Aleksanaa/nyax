// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package parser

import (
	"bytes"
	"strings"
)

// FirstToken returns the index of the leftmost token of the subtree rooted at
// n, or -1 if the subtree carries no token at all.
//
// Grammar actions attach tokens to a node in the order they are convenient to
// write, not in source order, and some nodes (application, for one) own no
// token of their own, so the start of an expression has to be searched for.
func (n *Node) FirstToken() int {
	first := -1
	for _, t := range n.Tokens {
		if first == -1 || t < first {
			first = t
		}
	}
	for _, c := range n.Nodes {
		if t := c.FirstToken(); t != -1 && (first == -1 || t < first) {
			first = t
		}
	}
	return first
}

// NodePos reports where the subtree rooted at n starts, or nil if unknown.
func (p *Parser) NodePos(n *Node) *LexPosition {
	if n == nil {
		return nil
	}
	i := n.FirstToken()
	if i == -1 {
		return nil
	}
	return p.TokenPos(i)
}

// SourceLine returns the source text of the line containing pos, with tabs
// expanded to single spaces so that a caret column stays aligned.
func (r *lexResult) SourceLine(pos *LexPosition) string {
	if pos == nil || pos.Offset < 0 || pos.Offset > len(r.data) {
		return ""
	}
	start := bytes.LastIndexByte(r.data[:pos.Offset], '\n') + 1
	end := bytes.IndexByte(r.data[pos.Offset:], '\n')
	if end == -1 {
		end = len(r.data)
	} else {
		end += pos.Offset
	}
	return strings.ReplaceAll(string(r.data[start:end]), "\t", " ")
}

// LastToken returns the index of the rightmost token of the subtree rooted at
// n, or -1 if the subtree carries no token at all.
func (n *Node) LastToken() int {
	last := -1
	for _, t := range n.Tokens {
		if t > last {
			last = t
		}
	}
	for _, c := range n.Nodes {
		if t := c.LastToken(); t > last {
			last = t
		}
	}
	return last
}

// NodeString returns the source text the subtree rooted at n was parsed from.
// It is used to quote an expression back at the user, as in the message of a
// failed assertion.
func (p *Parser) NodeString(n *Node) string {
	first, last := n.FirstToken(), n.LastToken()
	if first == -1 || last == -1 {
		return ""
	}
	start, end := p.tokens[first].pos, p.tokens[last].end
	if start < 0 || end > len(p.data) || start > end {
		return ""
	}
	return string(p.data[start:end])
}
