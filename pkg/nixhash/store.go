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
	"path"
	"regexp"

	"github.com/orivej/e"
)

const storeDir = "/nix/store"

var rxStoreName = regexp.MustCompile(`^[A-Za-z0-9+._?=-]+$`)

// StorePath returns nix store path of the pathname.
func StorePath(pathname, basename string) string {
	p, err := StorePathFiltered(pathname, basename, nil)
	e.Exit(err)
	return p
}

// StorePathFiltered is StorePath with a filter on which entries the archive
// includes, reporting the error rather than exiting.
func StorePathFiltered(pathname, basename string, filter PathFilter) (string, error) {
	if basename == "" {
		basename = path.Base(pathname)
	}
	if err := CheckStoreName(basename); err != nil {
		return "", err
	}
	h, err := PathFiltered(pathname, filter)
	if err != nil {
		return "", err
	}
	return fixedOutputPath(true, h, basename), nil
}

// CheckStoreName reports whether a name is a legal store name.
func CheckStoreName(name string) error {
	if !rxStoreName.MatchString(name) || name[0] == '.' {
		return errors.New("illegal name: " + name)
	}
	return nil
}

func fixedOutputPath(recursive bool, contentHash Hash, name string) string {
	if recursive {
		return makeStorePath("source", contentHash, name)
	}
	h := String("fixed:out" + contentHash.TypeString(16) + ":")
	return makeStorePath("output:out", h, name)
}

func makeStorePath(pathType string, h Hash, name string) string {
	s := pathType + ":" + h.TypeString(16) + ":" + storeDir + ":" + name
	return storeDir + "/" + String(s).Compress(20).String(32) + "-" + name
}
