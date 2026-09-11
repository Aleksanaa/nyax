// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package nixhash

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert"
	"github.com/orivej/e"
)

func TestHash(t *testing.T) {
	dir, err := ioutil.TempDir("", "TestHash")
	e.Panic(err)
	defer func() { e.Panic(os.RemoveAll(dir)) }()

	file := filepath.Join(dir, "name")
	err = ioutil.WriteFile(file, []byte("text\n"), 0644)
	e.Panic(err)

	assert.Equal(t, "/nix/store/a68bcimav7hwazsfk1iiabv7fxyr3dh4-name",
		StorePath(file, ""))
}
