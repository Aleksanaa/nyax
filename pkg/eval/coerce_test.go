package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksanaa/nyax/pkg/nixhash"
)

// TestPathCoercion covers which coercions copy a path literal into the store.
// Nix's coerceToString does so by default, which is what makes a path an input
// of a derivation; builtins.toString, builtins.baseNameOf, builtins.dirOf and
// a path on the left of `+` do not.
func TestPathCoercion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := nixhash.StorePath(p, "")

	// Interpolating a path copies it to the store, and so does a string on the
	// left of `+`.
	if got, err := evalPrint(t, `"${`+p+`}"`); err != nil || got != `"`+sp+`"` {
		t.Errorf("interpolation = %s, %v; want %q", got, err, sp)
	}
	if got, err := evalPrint(t, `"/x" + `+p); err != nil || got != `"/x`+sp+`"` {
		t.Errorf("string + path = %s, %v; want %q", got, err, "/x"+sp)
	}

	// These look at the source path itself.
	if got, err := evalPrint(t, `builtins.toString `+p); err != nil || got != `"`+p+`"` {
		t.Errorf("toString = %s, %v; want %q", got, err, p)
	}
	if got, err := evalPrint(t, `builtins.baseNameOf (`+p+` + "/b")`); err != nil || got != `"b"` {
		t.Errorf("baseNameOf (path + string) = %s, %v; want \"b\"", got, err)
	}

	// A derivation attribute copies the path and records it as a source input.
	val, err := EvalString(`builtins.derivation { name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; src = ` + p + `; }`)
	if err != nil {
		t.Fatal(err)
	}
	d := DerivationOf(val)
	if d == nil {
		t.Fatal("value is not a derivation")
	}
	if got := d.drv.Env["src"]; got != sp {
		t.Errorf("drv env src = %s, want %s", got, sp)
	}
	inInputs := false
	for _, src := range d.drv.InputSrcs {
		if src == sp {
			inInputs = true
		}
	}
	if !inInputs {
		t.Errorf("drv inputs %v do not include %s", d.drv.InputSrcs, sp)
	}
}
