// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package nixhash

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/aleksanaa/nyax/pkg/nixjson"
)

// Derivation hashing.
//
// A derivation's store path and the store paths of its outputs are pure
// functions of the derivation, so they are computed here without touching the
// store: Nix's read-only mode does exactly this (see `computeStorePath` in
// `src/libstore/derivations.cc`). The serialised form is Nix's ATerm encoding
// (`src/libstore/derivation/aterm.cc`), and the one thing the hash of a
// derivation that depends on other derivations needs is their hashes modulo
// their own output paths, which the caller supplies through resolve.

const drvExtension = ".drv"

// Output is one output of a derivation. An input-addressed output has only
// its Path set; a fixed-output output has HashAlgo, Hash and Method set, and
// its Path is computed from those. The HashAlgo and Hash fields are unused for
// the content-addressed outputs this package does not produce.
type Output struct {
	Path     string
	HashAlgo string
	Hash     string // base-16
	// Method is the file ingestion method of a fixed output: "flat",
	// "recursive" or "git". It is empty for an input-addressed output.
	Method string
}

// FixedOutputPath is Nix's makeFixedOutputPath: the store path of a
// fixed-output derivation's output. It is a pure function of the method, the
// hash and the output path name, not of the derivation, which is why a fixed
// output needs no hash modulo.
func FixedOutputPath(name, method, hashAlgo, hashBase16 string) string {
	if hashAlgo == "sha256" && method == "recursive" {
		return makeStorePathString("source", "sha256:"+hashBase16, name)
	}
	payload := "fixed:out:" + ingestionPrefix(method) + hashAlgo + ":" + hashBase16 + ":"
	return makeStorePathString("output:out", String(payload).TypeString(16), name)
}

// FixedOutputInputHash is the hash a fixed output presents to a derivation
// that depends on it: Nix's hashInput for a fixed-output derivation. It folds
// the output path into a content hash so that the provenance of the fixed
// output does not leak into the dependent's hash.
func FixedOutputInputHash(method, hashAlgo, hashBase16, outPath string) string {
	algo := ingestionPrefix(method) + hashAlgo
	return String("fixed:out:" + algo + ":" + hashBase16 + ":" + outPath).String(16)
}

// OutputHashAlgo is how a fixed output's hash algorithm is written in the
// .drv: prefixed with the ingestion method, as in "r:sha256" for a recursive
// hash.
func OutputHashAlgo(method, hashAlgo string) string {
	return ingestionPrefix(method) + hashAlgo
}

// ingestionPrefix is Nix's makeFileIngestionPrefix: "r:" for a recursive hash,
// "git:" for a git-hashed one, and empty for a flat one, which is unprefixed
// for backward compatibility.
func ingestionPrefix(method string) string {
	switch method {
	case "recursive":
		return "r:"
	case "git":
		return "git:"
	}
	return ""
}

// caMethodName is how a fixed output's method is named in derivation JSON,
// which is Nix's content-address method name rather than the outputHashMode
// spelling: a recursive hash is a "nar".
func caMethodName(method string) string {
	if method == "recursive" {
		return "nar"
	}
	return method
}

// Derivation is what the hash of a derivation depends on. The map-valued
// fields are serialised in ascending key order, which is the canonical order
// the ATerm encoding requires.
type Derivation struct {
	Name      string
	System    string
	Builder   string
	Args      []string
	Outputs   map[string]Output
	InputDrvs map[string][]string // derivation path -> its output names, sorted
	InputSrcs []string            // source store paths, sorted
	Env       map[string]string
}

// Unparse serialises the derivation in Nix's ATerm format.
func (d *Derivation) Unparse() string { return d.aterm(false, false, nil) }

// OutputHash is the hash modulo used to compute the derivation's own output
// paths: the SHA-256 of UnparseModulo. The second result is false when an
// input derivation's hash is not known, which resolve reports.
func (d *Derivation) OutputHash(resolve func(drvPath string) (string, bool)) (Hash, bool) {
	var missing bool
	contents := d.aterm(true, true, func(drvPath string) string {
		h, ok := resolve(drvPath)
		if !ok {
			missing = true
		}
		return h
	})
	if missing {
		return nil, false
	}
	return String(contents), true
}

// InputHash is the hash modulo a derivation takes when it is used as an input
// to another: its input derivations are replaced by their hashes modulo, but
// its own output paths are kept. It is what the caller records against the
// derivation's path and hands back to resolve.
func (d *Derivation) InputHash(resolve func(drvPath string) (string, bool)) (Hash, bool) {
	var missing bool
	contents := d.aterm(false, true, func(drvPath string) string {
		h, ok := resolve(drvPath)
		if !ok {
			missing = true
		}
		return h
	})
	if missing {
		return nil, false
	}
	return String(contents), true
}

// DrvPath is the store path of the derivation: the hash of its serialised
// form together with the store paths it refers to.
func (d *Derivation) DrvPath() string {
	contents := d.Unparse()
	h := String(contents)
	refs := d.references()
	typ := "text"
	for _, r := range refs {
		typ += ":" + r
	}
	return makeStorePathString(typ, h.TypeString(16), d.Name+drvExtension)
}

// OutputPath is the store path of one output, from the derivation's hash
// modulo.
func (d *Derivation) OutputPath(outputName string, hashModulo Hash) string {
	return makeStorePathString("output:"+outputName, hashModulo.TypeString(16), outputPathName(d.Name, outputName))
}

// references returns the store paths the derivation depends on: the source
// paths and the derivation paths of its inputs, in ascending order.
func (d *Derivation) references() []string {
	set := make(map[string]struct{}, len(d.InputSrcs)+len(d.InputDrvs))
	for _, src := range d.InputSrcs {
		set[src] = struct{}{}
	}
	for drvPath := range d.InputDrvs {
		set[drvPath] = struct{}{}
	}
	refs := make([]string, 0, len(set))
	for r := range set {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	return refs
}

// aterm writes the ATerm encoding. maskOutputs blanks the derivation's own
// output paths and the environment variables named after them, so the result
// does not depend on those paths; maskInputs substitutes each input
// derivation path with the hash modulo resolve returns for it.
func (d *Derivation) aterm(maskOutputs, maskInputs bool, resolve func(drvPath string) string) string {
	var b strings.Builder
	b.Grow(1024)
	b.WriteString("Derive(")

	// Outputs.
	b.WriteByte('[')
	for i, name := range slices.Sorted(maps.Keys(d.Outputs)) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		printQuoted(&b, name, false)
		b.WriteByte(',')
		if maskOutputs {
			printQuoted(&b, "", false)
			b.WriteByte(',')
			printQuoted(&b, "", false)
			b.WriteByte(',')
			printQuoted(&b, "", false)
		} else {
			printQuoted(&b, d.Outputs[name].Path, false)
			b.WriteByte(',')
			printQuoted(&b, d.Outputs[name].HashAlgo, false)
			b.WriteByte(',')
			printQuoted(&b, d.Outputs[name].Hash, false)
		}
		b.WriteByte(')')
	}
	b.WriteByte(']')
	b.WriteByte(',')

	// Input derivations. When the inputs are masked, each is named by its
	// hash rather than its path, and — as in Nix's maskInputDrvs — the entries
	// are keyed and ordered by that hash, with the outputs of inputs that
	// share a hash collected together.
	b.WriteByte('[')
	if maskInputs {
		byHash := map[string][]string{}
		for drvPath, outs := range d.InputDrvs {
			h := resolve(drvPath)
			byHash[h] = append(byHash[h], outs...)
		}
		for i, h := range slices.Sorted(maps.Keys(byHash)) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			printQuoted(&b, h, false)
			b.WriteByte(',')
			b.WriteByte('[')
			outs := byHash[h]
			sort.Strings(outs)
			outs = slices.Compact(outs)
			for j, out := range outs {
				if j > 0 {
					b.WriteByte(',')
				}
				printQuoted(&b, out, false)
			}
			b.WriteByte(']')
			b.WriteByte(')')
		}
	} else {
		for i, drvPath := range slices.Sorted(maps.Keys(d.InputDrvs)) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			printQuoted(&b, drvPath, false)
			b.WriteByte(',')
			b.WriteByte('[')
			for j, out := range d.InputDrvs[drvPath] {
				if j > 0 {
					b.WriteByte(',')
				}
				printQuoted(&b, out, false)
			}
			b.WriteByte(']')
			b.WriteByte(')')
		}
	}
	b.WriteByte(']')
	b.WriteByte(',')

	// Source paths.
	b.WriteByte('[')
	for i, src := range d.InputSrcs {
		if i > 0 {
			b.WriteByte(',')
		}
		printQuoted(&b, src, false)
	}
	b.WriteByte(']')
	b.WriteByte(',')

	printQuoted(&b, d.System, false)
	b.WriteByte(',')
	printQuoted(&b, d.Builder, true)
	b.WriteByte(',')

	// Arguments.
	b.WriteByte('[')
	for i, arg := range d.Args {
		if i > 0 {
			b.WriteByte(',')
		}
		printQuoted(&b, arg, true)
	}
	b.WriteByte(']')
	b.WriteByte(',')

	// Environment.
	b.WriteByte('[')
	for i, name := range slices.Sorted(maps.Keys(d.Env)) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		printQuoted(&b, name, true)
		b.WriteByte(',')
		value := d.Env[name]
		if maskOutputs {
			if _, isOutput := d.Outputs[name]; isOutput {
				value = ""
			}
		}
		printQuoted(&b, value, true)
		b.WriteByte(')')
	}
	b.WriteByte(']')

	b.WriteByte(')')
	return b.String()
}

// printQuoted writes a double-quoted string. escape selects between the two
// string encodings the ATerm format distinguishes: store paths and output
// names, drawn from restricted alphabets, are written verbatim, while the
// builder, its arguments and the environment escape backslashes, quotes and
// the control characters.
func printQuoted(b *strings.Builder, s string, escape bool) {
	b.WriteByte('"')
	if !escape {
		b.WriteString(s)
		b.WriteByte('"')
		return
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

func outputPathName(drvName, outputName string) string {
	if outputName == "out" {
		return drvName
	}
	return drvName + "-" + outputName
}

// makeStorePathString is Nix's makeStorePath over a pre-rendered type string
// and hash, for the kinds of path this package computes directly.
func makeStorePathString(typ, hashHex, name string) string {
	s := typ + ":" + hashHex + ":" + storeDir + ":" + name
	return storeDir + "/" + String(s).Compress(20).String(32) + "-" + name
}

// storePathName is a store path without its store directory, which is how the
// derivation JSON spells paths (see `nix derivation show`).
func storePathName(p string) string {
	return strings.TrimPrefix(p, storeDir+"/")
}

// TextStorePath returns the store path of a text file holding contents, which
// is what builtins.toFile produces: a path of type "text" hashing the file's
// contents together with the store paths it references.
func TextStorePath(name, contents string, refs []string) string {
	typ := "text"
	for _, r := range refs {
		typ += ":" + r
	}
	return makeStorePathString(typ, String(contents).TypeString(16), name)
}

// OutputPlaceholder returns the placeholder string an output is known by
// before it is built, which builtins.placeholder produces.
func OutputPlaceholder(outputName string) string {
	return "/" + String("nix-output:"+outputName).String(32)
}

// derivationJSONVersion is Nix's expectedJsonVersionDerivation.
const derivationJSONVersion = 4

// JSON serialises the derivation the way `nix derivation show` does: outputs
// and inputs are named by their store-path names, while the environment keeps
// the full paths.
func (d *Derivation) JSON() ([]byte, error) {
	outputs := make(map[string]any, len(d.Outputs))
	for name, o := range d.Outputs {
		if o.Method != "" {
			// A fixed output is described by its content address rather than
			// by a path, which Nix leaves out of the JSON for the same reason
			// it computes it: see `derivation/json.cc`. The method is Nix's
			// content-address name, and the algorithm is the unprefixed one.
			algo := o.HashAlgo
			algo = strings.TrimPrefix(algo, "r:")
			algo = strings.TrimPrefix(algo, "git:")
			sri, _ := ConvertHash(o.Hash, algo, "sri")
			outputs[name] = map[string]any{"hash": sri, "method": caMethodName(o.Method)}
			continue
		}
		outputs[name] = map[string]any{"path": storePathName(o.Path)}
	}

	drvs := make(map[string]any, len(d.InputDrvs))
	for drvPath, outs := range d.InputDrvs {
		drvs[storePathName(drvPath)] = map[string]any{
			"outputs":        outs,
			"dynamicOutputs": map[string]any{},
		}
	}

	srcs := d.InputSrcs
	if srcs == nil {
		srcs = []string{}
	}
	srcNames := make([]string, len(srcs))
	for i, s := range srcs {
		srcNames[i] = storePathName(s)
	}

	args := d.Args
	if args == nil {
		args = []string{}
	}

	return nixjson.Marshal(map[string]any{
		"name":    d.Name,
		"version": derivationJSONVersion,
		"outputs": outputs,
		"inputs": map[string]any{
			"drvs": drvs,
			"srcs": srcNames,
		},
		"system":  d.System,
		"builder": d.Builder,
		"args":    args,
		"env":     d.Env,
	})
}
