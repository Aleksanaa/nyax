// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

// Package nixhash implements nix derivation hashing.
package nixhash

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"

	"github.com/orivej/e"
)

var narVersionMagic1 = []byte("nix-archive-1")

type Hash []byte

// String sha256.
func String(s string) Hash {
	b := sha256.Sum256([]byte(s))
	return Hash(b[:])
}

// File sha256.
func File(path string) Hash {
	f, err := os.Open(path)
	e.Exit(err)
	defer e.CloseOrExit(f)

	h := sha256.New()
	_, err = io.Copy(h, f)
	e.Exit(err)

	return Hash(h.Sum(nil))
}

// Path hash.
func Path(path string) Hash {
	h, err := PathFiltered(path, nil)
	e.Exit(err)
	return h
}

// PathFilter decides whether a path is included in a NAR dump. It is called
// for each directory entry — never the root — with the entry's absolute path
// and its type, one of "regular", "directory" or "symlink". Returning false
// leaves the entry out of the archive.
type PathFilter func(path, typ string) bool

// PathFiltered is the NAR hash of a path, filtering which entries are
// included. It reports the error rather than exiting, which the evaluator
// needs so that a bad path is a Nix error rather than a crash.
func PathFiltered(path string, filter PathFilter) (Hash, error) {
	h := sha256.New()
	if err := DumpPath(h, path, filter); err != nil {
		return nil, err
	}
	return Hash(h.Sum(nil)), nil
}

// DumpPath writes the NAR encoding of a path to w, which is what the daemon
// expects for addToStore and what PathFiltered hashes.
func DumpPath(w io.Writer, pathname string, filter PathFilter) error {
	sink := NewSink(w)
	if _, err := sink.Write(narVersionMagic1); err != nil {
		return err
	}
	return dump(pathname, sink, filter)
}

// Compress the hash down to size bytes.
func (h Hash) Compress(size int) Hash {
	r := make([]byte, size)
	for i, b := range h {
		r[i%size] ^= b
	}
	return Hash(r)
}

// String encodes the hash to a string in a given base (16, 32 or 64).
func (h Hash) String(base int) string {
	switch base {
	case 16:
		return hex.EncodeToString(h)
	case 32:
		return string(base32Encode(h))
	case 64:
		return base64.StdEncoding.EncodeToString(h)
	default:
		panic("unsupported base")
	}
}

// TypeString prepends "sha256:" to String.
func (h Hash) TypeString(base int) string {
	return "sha256:" + h.String(base)
}
