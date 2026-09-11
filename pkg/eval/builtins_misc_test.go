package eval

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMiscBuiltins(t *testing.T) {
	for _, test := range [][2]string{
		{`builtins.mapAttrs (n: v: v * 10) { a = 1; b = 2; }`, `{ a = 10; b = 20; }`},
		{`builtins.concatMap (x: [ x x ]) [ 1 2 ]`, `[ 1 1 2 2 ]`},
		{`builtins.zipAttrsWith (n: v: { ${n} = v; }) [ { a = "x"; } { a = "y"; b = "z"; } ]`,
			`{ a = { a = [ "x" "y" ]; }; b = { b = [ "z" ]; }; }`},
		{`builtins.parseDrvName "nix-0.12pre12876"`, `{ name = "nix"; version = "0.12pre12876"; }`},
		{`builtins.splitVersion "1.2.3pre"`, `[ "1" "2" "3" "pre" ]`},
		{`builtins.storeDir`, `"/nix/store"`},
		{`builtins.hashString "sha256" "hello"`, `"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"`},
		{`builtins.convertHash { hash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="; toHashFormat = "nix32"; }`,
			`"0000000000000000000000000000000000000000000000000000"`},
		{`builtins.convertHash { hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"; toHashFormat = "sri"; }`,
			`"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="`},
		// A set with a __functor attribute is callable.
		{`let f = { __functor = self: x: x * 2; }; in f 21`, `42`},
		// A null attribute name skips the binding.
		{`{ ${null} = 1; ${"y"} = 2; }`, `{ y = 2; }`},
	} {
		got, err := evalPrint(t, test[0])
		if err != nil {
			t.Errorf("%s: %v", test[0], err)
			continue
		}
		if got != test[1] {
			t.Errorf("%s: got %s, want %s", test[0], got, test[1])
		}
	}
}

func TestGenericClosure(t *testing.T) {
	got, err := evalPrint(t, `builtins.genericClosure {
	  startSet = [ { key = 5; } ];
	  operator = item: [ { key = if (item.key / 2) * 2 == item.key then item.key / 2 else 3 * item.key + 1; } ];
	}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `[ { key = 5; } { key = 16; } { key = 8; } { key = 4; } { key = 2; } { key = 1; } ]`
	if got != want {
		t.Errorf("genericClosure = %s, want %s", got, want)
	}
}

func TestStringContextBuiltins(t *testing.T) {
	// A derivation's outPath carries a built-output reference, which
	// getContext reports and appendContext can reproduce.
	src := `builtins.derivation { name = "ctx"; builder = "/bin/sh"; system = "x86_64-linux"; }`
	got, err := evalPrint(t, `let d = `+src+`; in builtins.getContext d.outPath`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{ "/nix/store/n96mls8jja99bb70ghnlxk8mdb5b51i9-ctx.drv" = { outputs = [ "out" ]; }; }`
	if got != want {
		t.Errorf("getContext = %s, want %s", got, want)
	}

	got, err = evalPrint(t, `let d = `+src+`; in builtins.appendContext "plain" (builtins.getContext d.outPath)`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"plain"` {
		t.Errorf("appendContext = %s, want %s", got, want)
	}
}

// TestToJSON checks that JSON is rendered the way Nix's writer does: floats
// keep Nix's formatting and Go's HTML escaping is not applied.
func TestToJSON(t *testing.T) {
	for _, test := range [][2]string{
		{`builtins.toJSON 1.0`, `"1.0"`},
		{`builtins.toJSON 1000000.0`, `"1000000.0"`},
		{`builtins.toJSON 0.000001`, `"1e-06"`},
		{`builtins.toJSON 1.0e20`, `"1e+20"`},
		{`builtins.toJSON "<>&"`, `"\"<>&\""`},
		{`builtins.toJSON { b = 1; a = 2; }`, `"{\"a\":2,\"b\":1}"`},
		{`builtins.toJSON { outPath = "/x"; }`, `"\"/x\""`},
	} {
		got, err := evalPrint(t, test[0])
		if err != nil {
			t.Errorf("%s: %v", test[0], err)
			continue
		}
		if got != test[1] {
			t.Errorf("%s\n got: %s\nwant: %s", test[0], got, test[1])
		}
	}
}

// TestToJSONContext pins that toJSON keeps the string context of the value it
// serialises. nixpkgs's lib.generators.toLua serialises a derivation with
// toJSON "${drv}", so the references have to survive for the generated file to
// depend on the store paths it mentions.
func TestToJSONContext(t *testing.T) {
	src := `let d = builtins.derivation { name = "ctx"; builder = "/bin/sh"; system = "x86_64-linux"; };` +
		` in builtins.getContext (builtins.toJSON "x ${d}")`
	got, err := evalPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	want := `{ "/nix/store/n96mls8jja99bb70ghnlxk8mdb5b51i9-ctx.drv" = { outputs = [ "out" ]; }; }`
	if got != want {
		t.Errorf("toJSON context = %s, want %s", got, want)
	}
}

// TestMapAttrsLaziness pins that mapAttrs and zipAttrsWith defer applying their
// callback until an attribute is read, as Nix's mkApp does. The callback may
// take fewer arguments than the builtin passes, in which case the evaluator has
// to enter its body to find the next function; doing that while the result set
// is being built forces every value and, for a callback that reads the set
// being defined, recurses.
func TestMapAttrsLaziness(t *testing.T) {
	for _, test := range [][2]string{
		// Reading only the names must not apply the callback.
		{`builtins.attrNames (builtins.mapAttrs (name: 1) { a = 1; b = 2; })`, `[ "a" "b" ]`},
		{`builtins.attrNames (builtins.zipAttrsWith (name: 1) [ { a = 1; } { a = 2; } ])`, `[ "a" ]`},
		// The presence check does not force the value either.
		{`builtins.mapAttrs (name: 1) { a = 1; } ? a`, `true`},
		{`builtins.zipAttrsWith (name: 1) [ { a = 1; } ] ? a`, `true`},
		// A callback that reads the set being defined must not be run until
		// the set exists; attrNames only looks at the names.
		{`let x = builtins.mapAttrs (name: x.a) { a = 1; b = 2; }; in builtins.attrNames x`, `[ "a" "b" ]`},
	} {
		got, err := evalPrint(t, test[0])
		if err != nil {
			t.Errorf("%s: %v", test[0], err)
			continue
		}
		if got != test[1] {
			t.Errorf("%s: got %s, want %s", test[0], got, test[1])
		}
	}

	// The application is still made when the value is forced, so a callback
	// that does not return a function for its second argument fails then.
	if _, err := EvalString(`(builtins.mapAttrs (name: 1) { a = 1; }).a`); err == nil {
		t.Error("forcing .a: expected an error, got nil")
	}
}

func TestUnsafeGetAttrPosSource(t *testing.T) {
	// Nix only reports a position when it comes from a file. An expression
	// parsed from text has positions, but they are treated as sourceless.
	if got, err := evalPrint(t, `builtins.unsafeGetAttrPos "a" { a = 1; }`); err != nil || got != "null" {
		t.Fatalf("string source: got %q, err %v; want null", got, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "pos.nix")
	write(t, path, `builtins.unsafeGetAttrPos "a" { a = 1; }`)
	if got := evalFile(t, path); !strings.Contains(got, path) {
		t.Fatalf("file source: got %q, want it to mention %q", got, path)
	}
}
