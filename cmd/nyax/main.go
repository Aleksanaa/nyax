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
	"os"

	"github.com/alecthomas/kingpin"
	"github.com/pkg/profile"
)

var (
	actions = map[string]func(){}
	// Both kinds of profile are worth having: evaluation is allocation bound,
	// so the allocation profile is usually the one that says where the time
	// went, and it is the one to watch when optimizing.
	prof = kingpin.Flag("profile",
		"Write a profile to the current directory: cpu or alloc.").Enum("cpu", "alloc")
)

func register(name string, f func()) func() {
	actions[name] = f
	return f
}

// fail reports an error the way Nix does, on stderr and with a failing exit
// status. Parse and evaluation errors render as a message, a source excerpt
// and a backtrace.
func fail(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	kingpin.HelpFlag.Short('h')
	action := kingpin.Parse()
	switch *prof {
	case "cpu":
		defer profile.Start(profile.ProfilePath(".")).Stop()
	case "alloc":
		// Sample every allocation: the point is to count them, not to sample
		// a long-running heap.
		defer profile.Start(profile.MemProfile, profile.MemProfileRate(1),
			profile.ProfilePath(".")).Stop()
	}
	actions[action]()
	report()
}
