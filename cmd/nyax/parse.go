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
	"github.com/aleksanaa/nyax/pkg/parser"
)

var (
	parseCmd     = kingpin.Command("parse", "Parse Nix expression.")
	parseExprArg = parseCmd.Arg("expr", "Expression.").Required().String()
	parseFile    = parseCmd.Flag("file", "Parse file.").Short('f').Bool()
)

var parseMain = register("parse", func() {
	parse := parser.ParseString
	if *parseFile {
		parse = parser.ParseFile
	}
	pr, err := parse(*parseExprArg)
	fail(err)
	fmt.Println(pr.LispResult())
})
