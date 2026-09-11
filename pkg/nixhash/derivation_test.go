// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package nixhash

import (
	"strings"
	"testing"
)

func TestDerivationHashing(t *testing.T) {
	dep := &Derivation{
		Name:    "dep",
		System:  "x86_64-linux",
		Builder: "/bin/true",
		Outputs: map[string]Output{"out": {}},
		Env: map[string]string{
			"builder": "/bin/true",
			"name":    "dep",
			"out":     "",
			"system":  "x86_64-linux",
		},
	}
	depOutputHash, ok := dep.OutputHash(nil)
	if !ok {
		t.Fatal("dep output hash missing")
	}
	depOut := dep.OutputPath("out", depOutputHash)
	dep.Outputs["out"] = Output{Path: depOut}
	dep.Env["out"] = depOut
	depDrv := dep.DrvPath()
	depInputHash, ok := dep.InputHash(nil)
	if !ok {
		t.Fatal("dep input hash missing")
	}

	if want := "/nix/store/v9ak4484q7m2q36wrjh2rvpks78h1156-dep.drv"; depDrv != want {
		t.Errorf("dep drvPath = %s, want %s", depDrv, want)
	}
	if want := "/nix/store/q29279kjjvm86j19gld6rsclr0khshzm-dep"; depOut != want {
		t.Errorf("dep outPath = %s, want %s", depOut, want)
	}

	top := &Derivation{
		Name:      "top",
		System:    "x86_64-linux",
		Builder:   "/bin/sh",
		Args:      []string{"-c", "echo hello\nworld"},
		Outputs:   map[string]Output{"dev": {}, "out": {}},
		InputDrvs: map[string][]string{depDrv: {"out"}},
		InputSrcs: []string{depDrv},
		Env: map[string]string{
			"builder": "/bin/sh",
			"dep":     depDrv,
			"dev":     "",
			"name":    "top",
			"note":    "quote \" and backslash \\ and tab \t",
			"out":     "",
			"outputs": "out dev",
			"system":  "x86_64-linux",
		},
	}
	topOutputHash, ok := top.OutputHash(func(drvPath string) (string, bool) {
		if drvPath == depDrv {
			return depInputHash.String(16), true
		}
		return "", false
	})
	if !ok {
		t.Fatal("top output hash missing")
	}

	topOut := top.OutputPath("out", topOutputHash)
	topDev := top.OutputPath("dev", topOutputHash)
	top.Outputs["out"] = Output{Path: topOut}
	top.Outputs["dev"] = Output{Path: topDev}
	top.Env["out"] = topOut
	top.Env["dev"] = topDev

	if want := "/nix/store/d771h1w1bciphqp73a9lxz2s9a1fs9ix-top"; topOut != want {
		t.Errorf("top outPath = %s, want %s", topOut, want)
	}
	if want := "/nix/store/2fi5hx90d7giziggjpa1lw63ky3zvbjd-top-dev"; topDev != want {
		t.Errorf("top devPath = %s, want %s", topDev, want)
	}
	if want := "/nix/store/dgxd7gwfdzrvcqv4yw16cbbk7cg9b84p-top.drv"; top.DrvPath() != want {
		t.Errorf("top drvPath = %s, want %s", top.DrvPath(), want)
	}
}

// TestDerivationJSONEscaping pins that the derivation JSON does not HTML-escape
// characters like ">", which Nix writes verbatim.
func TestDerivationJSONEscaping(t *testing.T) {
	d := &Derivation{
		Name:    "x",
		System:  "x86_64-linux",
		Builder: "/bin/sh",
		Args:    []string{"-c", "a > b"},
		Outputs: map[string]Output{"out": {Path: "/nix/store/abcdef-x"}},
		Env:     map[string]string{"builder": "/bin/sh", "name": "x", "out": "/nix/store/abcdef-x", "system": "x86_64-linux"},
	}
	b, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `\u003e`) {
		t.Errorf("JSON HTML-escaped '>': %s", b)
	}
	if !strings.Contains(string(b), `a > b`) {
		t.Errorf("JSON lost '>': %s", b)
	}
}
