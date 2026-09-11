// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package nixhash

import "testing"

// TestParseHashSRI covers SRI's base64 padding: Nix skips everything from the
// first '=' on, so it accepts a hash with no padding or with more than the
// canonical amount, and nixpkgs contains both spellings.
func TestParseHashSRI(t *testing.T) {
	for _, test := range []struct{ hash, base16 string }{
		// nixpkgs ffmpeg, written without padding.
		{"sha256-UlI+6OMUj5F6uVAw+Mg2wOZrjfdRq73d1qufaXVI/go", "52523ee8e3148f917ab95030f8c836c0e66b8df751abbdddd6ab9f697548fe0a"},
		// nixpkgs epiphany, written with an extra '='.
		{"sha256-9m8R5GUOBCm3xDXVHBDrV/HbjfIDL+D3wUGkkqc4RmA==", "f66f11e4650e0429b7c435d51c10eb57f1db8df2032fe0f7c141a492a7384660"},
	} {
		got, algo, err := ParseHash(test.hash, "")
		if err != nil {
			t.Fatalf("ParseHash(%s): %v", test.hash, err)
		}
		if got != test.base16 {
			t.Errorf("ParseHash(%s) = %s, want %s", test.hash, got, test.base16)
		}
		if algo != "sha256" {
			t.Errorf("ParseHash(%s) algo = %s, want sha256", test.hash, algo)
		}
	}
}

// TestBase32 covers Nix's base-32 hash encoding, whose byte order is the
// reverse of the usual one: it packs the digest starting from the last byte.
// A decoder that does not undo that yields a digest that is byte-reversed,
// which silently changes every fixed-output derivation that uses a legacy
// `sha256 = "1z4…"` hash.
func TestBase32(t *testing.T) {
	// nix hash convert --to base16 \
	//   --hash-algo sha256 1z4bibjm7ldvjwq3hmyifyb429rs2d9bdwkvs0r171vv1khpdwmb
	const (
		nix32  = "1z4bibjm7ldvjwq3hmyifyb429rs2d9bdwkvs0r171vv1khpdwmb"
		base16 = "abf276e10c7b871332d07bf2b652133a27419677d157383097bbd153e58a8bfc"
	)

	got, err := ConvertHash(nix32, "sha256", "base16")
	if err != nil {
		t.Fatal(err)
	}
	if got != base16 {
		t.Errorf("ConvertHash(%s) = %s, want %s", nix32, got, base16)
	}

	// Decoding must invert encoding.
	digest, err := base32Decode(nix32, 32)
	if err != nil {
		t.Fatal(err)
	}
	if reencoded := string(base32Encode(digest)); reencoded != nix32 {
		t.Errorf("base32Encode(base32Decode(%s)) = %s, want %s", nix32, reencoded, nix32)
	}
}
