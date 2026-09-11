// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package parser

import (
	"testing"

	"github.com/aleksanaa/nyax/pkg/source"
)

// The paths the parser tests read are found through pkg/source, which is where
// NIX_PATH resolution lives; the parser itself does not look paths up.
var allPackages = source.Resolve("", "<nixpkgs/pkgs/top-level/all-packages.nix>")
var nixpkgs = source.Resolve("", "<nixpkgs>")
var attrsets = source.Resolve("", "<nixpkgs/lib/attrsets.nix>")

func skipWithoutNixPath(t *testing.T) {
	t.Helper()
	if len(source.NixPath) == 0 {
		t.Skip("NIX_PATH is not set")
	}
	if nixpkgs == "" || attrsets == "" {
		t.Skip("nixpkgs is not in NIX_PATH")
	}
}
