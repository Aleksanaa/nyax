// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package parser

//go:generate ragel -Z -G2 machine.rl

import (
	"fmt"
	"go/token"
	"io/ioutil"
)

type lexerToken struct{ sym, pos, end, prev int }

type lexResult struct {
	file     *token.File
	data     []byte
	tokens   []lexerToken
	comments []lexerToken
}

var fileset = token.NewFileSet()

// StringPath is the pseudo-path given to an expression that was parsed from
// text rather than a file. Positions still carry it, but Nix treats such
// positions as having no source at all (builtins.unsafeGetAttrPos returns
// null for them), so callers test against it.
const StringPath = "«string»"

func newLexResult(path string, size int) *lexResult {
	if path == "" {
		path = StringPath
	}
	return &lexResult{file: fileset.AddFile(path, -1, size)}
}

func (r *lexResult) TokenPos(i int) *LexPosition {
	p := LexPosition(r.file.Position(r.file.Pos(r.tokens[i].pos)))
	return &p
}

// StartPos is the very beginning of the input. It is where a failure with no
// token to blame is reported, which is what an empty input produces: the
// parser rejects it before it has consumed anything.
func (r *lexResult) StartPos() *LexPosition {
	p := LexPosition(r.file.Position(r.file.Pos(0)))
	return &p
}

func (r *lexResult) TokenBytes(i int) []byte {
	tok := r.tokens[i]
	return r.data[tok.pos:tok.end]
}

func (r *lexResult) TokenString(i int) string {
	return string(r.TokenBytes(i))
}

// Last returns the index of the last token, or -1 when there is none.
func (r *lexResult) Last() int {
	return len(r.tokens) - 1
}

// symString names a lexer symbol the way the grammar spells it.
func symString(sym int) string {
	if sym >= yyPrivate-1 && sym < yyPrivate+len(yyToknames) {
		return yyToknames[sym-yyPrivate+1]
	}
	return fmt.Sprintf("'%c'", sym)
}

func (r *lexResult) TokenSymString(i int) string {
	return symString(r.tokens[i].sym)
}

func (r *lexResult) Errorf(format string) error {
	last := r.Last()
	pos := r.TokenPos(last)
	return &LexerError{Pos: pos, Line: r.SourceLine(pos), Desc: fmt.Sprintf("%s %s", r.TokenSymString(last), format)}
}

func lex(data []byte, path string) (r *lexResult, err error) {
	r = newLexResult(path, len(data))
	err = lexData(data, r)
	return
}

func lexFile(path string) (r *lexResult, err error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return
	}
	return lex(data, path)
}

type Backrefs [][2]int

func (stack *Backrefs) Push(i, fin int) {
	*stack = append(*stack, [2]int{i, fin})
}

func (stack *Backrefs) Pop() (i, fin int) {
	backref := (*stack)[len(*stack)-1]
	*stack = (*stack)[:len(*stack)-1]
	return backref[0], backref[1]
}
