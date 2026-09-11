// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecthomas/kingpin"
	"github.com/aleksanaa/nyax/pkg/eval"
	"github.com/aleksanaa/nyax/pkg/nixjson"
	"github.com/aleksanaa/nyax/pkg/parser"
)

var (
	derivationCmd  = kingpin.Command("derivation", "Show the derivation a Nix expression evaluates to.")
	derivationExpr = derivationCmd.Arg("expr", "Expression.").Required().String()
	derivationFile = derivationCmd.Flag("file", "Evaluate a file.").Short('f').Bool()
)

var derivationMain = register("derivation", func() {
	parse := parser.ParseString
	if *derivationFile {
		parse = parser.ParseFile
	}
	pr, err := parse(*derivationExpr)
	fail(err)
	val, err := eval.Eval(pr)
	fail(err)

	d := eval.DerivationOf(val)
	if d == nil {
		fail(fmt.Errorf("expression did not evaluate to a derivation"))
	}

	body, err := d.JSON()
	fail(err)
	// The derivation path is named by its store-path name, as `nix
	// derivation show` does.
	name := strings.TrimPrefix(d.DrvPath(), "/nix/store/")
	out := map[string]any{
		"version": 4,
		"derivations": map[string]json.RawMessage{
			name: body,
		},
	}
	data, err := nixjson.Marshal(out)
	fail(err)
	fmt.Println(string(data))
})
