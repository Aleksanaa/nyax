// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package parser

import (
	"fmt"
	"go/token"
	"strings"
)

// LexPosition is a resolved source position: file, line, column and offset.
type LexPosition token.Position

func (p *LexPosition) String() string {
	return fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column)
}

// Snippet renders pos as an indented source excerpt with a caret under the
// offending column, in the shape Nix uses for its own diagnostics:
//
//	at «string»:1:15:
//	     1| let a = 1; in b
//	      |               ^
//
// line is the source text of pos.Line; when it is empty only the "at" line is
// produced.
func Snippet(pos *LexPosition, line, indent string) string {
	if pos == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%sat %s:", indent, pos)
	if line == "" {
		return b.String()
	}
	col := pos.Column - 1
	if col < 0 {
		col = 0
	}
	fmt.Fprintf(&b, "\n%s%6d| %s", indent, pos.Line, line)
	fmt.Fprintf(&b, "\n%s      | %s^", indent, strings.Repeat(" ", col))
	return b.String()
}

// LexerError is a lexing failure at a known position.
type LexerError struct {
	Pos  *LexPosition
	Line string // source text of Pos.Line, for the excerpt
	Desc string
}

func (e *LexerError) Error() string {
	return diagnostic("syntax error: "+e.Desc, e.Pos, e.Line)
}

// ParserError is a parse failure at a known position.
type ParserError struct {
	Pos  *LexPosition
	Line string // source text of Pos.Line, for the excerpt
	Desc string
}

func (e *ParserError) Error() string {
	return diagnostic(e.Desc, e.Pos, e.Line)
}

// ParserErrors is every parse failure found in one file.
type ParserErrors []*ParserError

func (es ParserErrors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n\n")
}

func diagnostic(desc string, pos *LexPosition, line string) string {
	s := "error: " + desc
	if snippet := Snippet(pos, line, "       "); snippet != "" {
		s += "\n\n" + snippet
	}
	return s
}
