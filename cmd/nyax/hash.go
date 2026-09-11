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
	"github.com/aleksanaa/nyax/pkg/nixhash"
)

var (
	hashCmd  = kingpin.Command("hash", "Hash a path.")
	hashPath = hashCmd.Arg("path", "path").Required().String()
	hashName = hashCmd.Flag("name", "Name in store.").Short('n').String()
)

var hashMain = register("hash", func() {
	fmt.Println(nixhash.StorePath(*hashPath, *hashName))
})
