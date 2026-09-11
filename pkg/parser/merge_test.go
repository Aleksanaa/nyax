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

	"github.com/alecthomas/assert"
)

// TestMergeAttrPaths covers the parse-time merge of attribute paths. A path and
// a set literal that name the same attribute become one set, so the evaluator
// sees a single binding for `a` whose value holds both leaves.
func TestMergeAttrPaths(t *testing.T) {
	for _, src := range []string{
		`{ a.b = 1; a.c = 2; }`,
		`{ a = { b = 1; }; a.c = 2; }`,
		`{ a = { b = 1; }; a = { c = 2; }; }`,
		`{ a.b = 1; a = { c = 2; }; }`,
	} {
		p, err := ParseString(src)
		assert.NoError(t, err, src)

		set := p.Result
		assert.Equal(t, 1, len(set.Nodes), src)
		assert.Equal(t, "a", bindName(p, set.Nodes[0]), src)

		inner := set.Nodes[0].Nodes[1]
		assert.Equal(t, SetNode, inner.Type, src)
		assert.Equal(t, 2, len(inner.Nodes), src)
		assert.Equal(t, "b", bindName(p, inner.Nodes[0]), src)
		assert.Equal(t, "c", bindName(p, inner.Nodes[1]), src)
	}
}

// TestMergeAttrPathsComputed covers that a path with a computed component is
// unfolded like any other, into nested sets — the nesting is known at parse
// time even though the name is not — so the name is only forced when its set
// is, and a sibling sharing the prefix still merges.
func TestMergeAttrPathsComputed(t *testing.T) {
	p, err := ParseString(`{ a.${builtins.toString 1} = 1; a.x = 2; }`)
	assert.NoError(t, err)

	set := p.Result
	assert.Equal(t, 1, len(set.Nodes))
	assert.Equal(t, "a", bindName(p, set.Nodes[0]))

	inner := set.Nodes[0].Nodes[1]
	assert.Equal(t, SetNode, inner.Type)
	assert.Equal(t, 2, len(inner.Nodes))
	for _, b := range inner.Nodes {
		assert.Equal(t, 1, len(b.Nodes[0].Nodes))
	}
}

// TestMergeAttrPathsRejectsDuplicate covers that a repeat which cannot be
// merged is a parse error naming the full path.
func TestMergeAttrPathsRejectsDuplicate(t *testing.T) {
	for _, test := range [][2]string{
		{`{ a = 1; a = 2; }`, "a"},
		{`{ a = 1; a.b = 2; }`, "a"},
		{`{ a.b = 1; a.b = 2; }`, "a.b"},
		{`{ a = { b = 1; }; a.b = 2; }`, "a.b"},
		{`{ a = { b = 1; }; a = { b = 2; }; }`, "a.b"},
	} {
		_, err := ParseString(test[0])
		assert.Error(t, err, test[0])
		assert.Contains(t, err.Error(), "attribute '"+test[1]+"' already defined", test[0])
	}
}

// bindName is the static name of a binding whose path is a single identifier.
func bindName(p *Parser, b *Node) string {
	path := b.Nodes[0].Nodes
	return p.TokenString(path[len(path)-1].Tokens[0])
}

// TestMergeQuotedAttrNames covers that a quoted name with nothing to
// interpolate is as static as an identifier, so bindings sharing one merge in
// the parser. The dot in `"a.b"` belongs to the name, so the merged binding is
// a single attribute and not a nested path.
func TestMergeQuotedAttrNames(t *testing.T) {
	for _, src := range []string{
		`{ "a.b" = { x = 1; }; "a.b".y = 2; }`,
		`{ "a.b".x = 1; "a.b".y = 2; }`,
		`{ "a.b" = { x = 1; }; "a.b" = { y = 2; }; }`,
	} {
		p, err := ParseString(src)
		assert.NoError(t, err, src)

		set := p.Result
		assert.Equal(t, 1, len(set.Nodes), src)
		path := set.Nodes[0].Nodes[0].Nodes
		assert.Equal(t, 1, len(path), src)
		name, ok := staticName(p, path[0])
		assert.True(t, ok, src)
		assert.Equal(t, "a.b", name, src)

		inner := set.Nodes[0].Nodes[1]
		assert.Equal(t, SetNode, inner.Type, src)
		assert.Equal(t, 2, len(inner.Nodes), src)
		assert.Equal(t, "x", bindName(p, inner.Nodes[0]), src)
		assert.Equal(t, "y", bindName(p, inner.Nodes[1]), src)
	}
}

// TestStaticName covers which path components the parser settles outright. An
// escape is left to the evaluator: undoing it is eval's job, and this package
// is underneath that one.
func TestStaticName(t *testing.T) {
	for _, test := range []struct {
		src  string
		name string
		ok   bool
	}{
		{`{ a = 1; }`, "a", true},
		{`{ "a" = 1; }`, "a", true},
		{`{ "a.b" = 1; }`, "a.b", true},
		{`{ "" = 1; }`, "", true},
		{`{ "a\tb" = 1; }`, "", false},
		{`{ "x${"y"}" = 1; }`, "", false},
		// `${"a"}` is not computed at all: the parser folds it to the plain
		// name it spells, as Nix does, so the group that binds it has it in
		// scope like any other name.
		{`{ ${"a"} = 1; }`, "a", true},
		{`{ ${"a.b"} = 1; }`, "a.b", true},
		{`{ ${"x" + "y"} = 1; }`, "", false},
	} {
		p, err := ParseString(test.src)
		assert.NoError(t, err, test.src)
		path := p.Result.Nodes[0].Nodes[0].Nodes
		name, ok := staticName(p, path[0])
		assert.Equal(t, test.ok, ok, test.src)
		if test.ok {
			assert.Equal(t, test.name, name, test.src)
		}
	}
}
