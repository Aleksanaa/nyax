// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package nixhash

import (
	"errors"
	"io"
	"os"
	"path"
	"sort"
)

// dump writes the NAR encoding of p, following Nix's serialisation. filter, if
// set, is called for each directory entry (never the root) with the entry's
// absolute path and type; an entry it rejects is left out of the archive.
func dump(p string, sink Sink, filter PathFilter) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	sink.S("(")
	switch {
	case fi.Mode().IsRegular():
		sink.S("type", "regular")
		if fi.Mode()&0100 != 0 {
			sink.S("executable", "")
		}
		if err := dumpContents(p, fi.Size(), sink); err != nil {
			return err
		}
	case fi.IsDir():
		sink.S("type", "directory")
		names, err := readDirectory(p)
		if err != nil {
			return err
		}
		for _, name := range names {
			entry := path.Join(p, name)
			if filter != nil && !filter(entry, fileType(entry)) {
				continue
			}
			sink.S("entry", "(", "name", name, "node")
			if err := dump(entry, sink, filter); err != nil {
				return err
			}
			sink.S(")")
		}
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		sink.S("type", "symlink", "target", target)
	default:
		return errors.New("illegal file: " + p)
	}
	sink.S(")")
	return nil
}

// fileType is the name of a file's type, the way the path filter and
// builtins.readDir spell it.
func fileType(p string) string {
	fi, err := os.Lstat(p)
	if err != nil {
		return "unknown"
	}
	switch {
	case fi.Mode().IsRegular():
		return "regular"
	case fi.IsDir():
		return "directory"
	case fi.Mode()&os.ModeSymlink != 0:
		return "symlink"
	}
	return "unknown"
}

func dumpContents(p string, size int64, sink Sink) error {
	sink.S("contents")
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := sink.Begin(size); err != nil {
		return err
	}
	if _, err := io.Copy(sink, f); err != nil {
		return err
	}
	return sink.End()
}

func readDirectory(p string) ([]string, error) {
	fis, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fis))
	for _, fi := range fis {
		if name := fi.Name(); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
