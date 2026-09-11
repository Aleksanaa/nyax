package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksanaa/nyax/pkg/nixhash"
	"github.com/aleksanaa/nyax/pkg/parser"
)

// evalFile parses and evaluates a file, printing the result deeply.
func evalFile(t *testing.T, path string) string {
	t.Helper()
	pr, err := parser.ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	val, err := Eval(pr)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	s, err := Print(val, -1)
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	return s
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestImport(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "main.nix"), `let f = import ./foo.nix; in { a = f.a; bx = f.b.x; }`)
	write(t, filepath.Join(dir, "foo.nix"), `{ a = 42; b = import ./sub/bar.nix; }`)
	write(t, filepath.Join(dir, "sub", "bar.nix"), `{ x = 1; }`)
	write(t, filepath.Join(dir, "pkg", "default.nix"), `{ fromDefaultNix = true; }`)
	write(t, filepath.Join(dir, "dir.nix"), `let d = import ./pkg; in { d = d.fromDefaultNix; }`)
	write(t, filepath.Join(dir, "greet.nix"), `"${greeting}, ${name}!"`)
	write(t, filepath.Join(dir, "scoped.nix"), `builtins.scopedImport { greeting = "hi"; name = "world"; } ./greet.nix`)

	if got, want := evalFile(t, filepath.Join(dir, "main.nix")), `{ a = 42; bx = 1; }`; got != want {
		t.Errorf("import = %s, want %s", got, want)
	}
	if got, want := evalFile(t, filepath.Join(dir, "dir.nix")), `{ d = true; }`; got != want {
		t.Errorf("default.nix import = %s, want %s", got, want)
	}
	if got, want := evalFile(t, filepath.Join(dir, "scoped.nix")), `"hi, world!"`; got != want {
		t.Errorf("scopedImport = %s, want %s", got, want)
	}

	// The same file imported twice is the same value, not a fresh evaluation.
	write(t, filepath.Join(dir, "twice.nix"), `let a = import ./foo.nix; b = import ./foo.nix; in a == b`)
	if got := evalFile(t, filepath.Join(dir, "twice.nix")); got != "true" {
		t.Errorf("import memoization = %s, want true", got)
	}
}

// TestImportRecursive pins that a file importing itself reports the cycle
// rather than recursing.
func TestImportRecursive(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "cycle.nix"), `import ./cycle.nix`)
	pr, err := parser.ParseFile(filepath.Join(dir, "cycle.nix"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Eval(pr); err == nil {
		t.Fatal("expected a recursive import to fail")
	}
}

func TestSourceAccess(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "data.txt"), "hello world\n")
	write(t, filepath.Join(dir, "sub", "a.txt"), "")
	write(t, filepath.Join(dir, "sub", "b.txt"), "")

	write(t, filepath.Join(dir, "main.nix"), `{
	  content = builtins.readFile ./data.txt;
	  dir = builtins.readDir ./sub;
	  exists = builtins.pathExists ./data.txt;
	  missing = builtins.pathExists ./nope;
	  base = builtins.baseNameOf ./data.txt;
	  directory = toString (builtins.dirOf ./data.txt);
	}`)

	got := evalFile(t, filepath.Join(dir, "main.nix"))
	want := `{ base = "data.txt"; content = "hello world\n"; dir = { "a.txt" = "regular"; "b.txt" = "regular"; }; directory = "` + dir + `"; exists = true; missing = false; }`
	if got != want {
		t.Errorf("source access = %s\nwant %s", got, want)
	}
}

// TestBuiltinsPath pins that builtins.path and filterSource hash a source
// directory the way the store would copy it.
func TestBuiltinsPath(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "src", "a.txt"), "aaa")
	write(t, filepath.Join(dir, "src", "b.txt"), "bb")

	write(t, filepath.Join(dir, "main.nix"), `{
	  path = builtins.path { path = ./src; };
	  filtered = builtins.filterSource (n: t: t != "regular" || builtins.baseNameOf n != "a.txt") ./src;
	}`)

	pr, err := parser.ParseFile(filepath.Join(dir, "main.nix"))
	if err != nil {
		t.Fatal(err)
	}
	val, err := Eval(pr)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Print(val, -1)
	if err != nil {
		t.Fatal(err)
	}

	wantPath, _ := nixhash.StorePathFiltered(filepath.Join(dir, "src"), "src", nil)
	wantFiltered, _ := nixhash.StorePathFiltered(filepath.Join(dir, "src"), "src", func(p, typ string) bool {
		return typ != "regular" || filepath.Base(p) != "a.txt"
	})
	want := `{ filtered = "` + wantFiltered + `"; path = "` + wantPath + `"; }`
	if s != want {
		t.Errorf("builtins.path = %s\nwant %s", s, want)
	}
}
