// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package main

import (
	"fmt"

	"github.com/alecthomas/kingpin"
	"github.com/aleksanaa/nyax/pkg/eval"
	"github.com/aleksanaa/nyax/pkg/parser"
)

var (
	evalCmd     = kingpin.Command("eval", "Eval Nix expression.")
	evalExprArg = evalCmd.Arg("expr", "Expression.").Required().String()
	evalFile    = evalCmd.Flag("file", "Evaluate a file.").Short('f').Bool()
	evalDepth   = evalCmd.Flag("depth", "How deep to print nested lists and sets.").Default("1").Int()
	evalStrict  = evalCmd.Flag("strict", "Evaluate the result completely.").Bool()
)

var evalMain = register("eval", func() {
	parse := parser.ParseString
	if *evalFile {
		parse = parser.ParseFile
	}
	pr, err := timed2("parse", func() (*parser.Parser, error) { return parse(*evalExprArg) })
	fail(err)
	val, err := timed2("eval", func() (eval.NixValue, error) { return eval.Eval(pr) })
	fail(err)
	depth := *evalDepth
	if *evalStrict {
		depth = -1 // negative never reaches zero, so nothing is abbreviated
	}
	// Printing forces the value, so it can fail just as evaluating it can.
	s, err := timed2("print", func() (string, error) { return eval.Print(val, depth) })
	fail(err)
	fmt.Println(s)
})
