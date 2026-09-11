package eval

import (
	"path"
	"sync"

	"github.com/aleksanaa/nyax/pkg/nixhash"
	"github.com/aleksanaa/nyax/pkg/parser"
	"github.com/aleksanaa/nyax/pkg/source"
)

// import and the source-access builtins: loading and evaluating files, and
// reading the file system. There is no store yet, so a path is a source path
// on the file system, and builtins.path hashes one in memory the way the store
// would copy it.

// importCache memoizes an import: the same file, imported again, is the same
// value. It maps the resolved file to the thunk that evaluates it, which also
// makes a file that imports itself report the cycle rather than recurse.
var importCache sync.Map // file path -> *Expression

// coerceToPath is how a value becomes a path: a path is itself, a string is
// taken to name one, and anything else is a type error.
func coerceToPath(w *worker, val NixValue) string {
	switch val.Kind() {
	case KindPath:
		return val.Path().Path
	case KindString:
		return source.Resolve(".", val.Str().Content)
	case KindSet:
		w.throwf(ErrEval, "import from a derivation is not supported without a store")
	}
	w.throwf(ErrType, "value is %s while a path was expected", anTypeName(val))
	return ""
}

// resolveImportFile turns an import argument into the file to evaluate: a
// directory names its default.nix.
func resolveImportFile(w *worker, p string) string {
	if !source.Exists(p) {
		w.throwf(ErrEval, "cannot import '%s': path does not exist", p)
	}
	if source.IsDir(p) {
		p = path.Join(p, "default.nix")
	}
	return p
}

// importFile parses and evaluates the file at p in scope. The parse is
// memoized against the key it was reached at, so the same file imported again
// is the same value, and a file that imports itself reports the cycle rather
// than recursing.
func importFile(w *worker, p string, scope *Scope) NixValue {
	// An embedded core package, reached through <nix/...>, is parsed from
	// memory and keyed by its <nix/...> name; anything else is a file on the
	// file system, keyed by its resolved path.
	content, embedded := corepkgContent(p)
	if !embedded {
		p = resolveImportFile(w, p)
	}
	if x, ok := importCache.Load(p); ok {
		return x.(*Expression).Eval(w)
	}
	var pr *parser.Parser
	var err error
	if embedded {
		pr, err = parser.ParseString(content)
	} else {
		pr, err = parser.ParseFile(p)
	}
	if err != nil {
		w.throwf(ErrEval, "%s", err)
	}
	x := delay(w, scope, pr)
	importCache.Store(p, x)
	return x.Eval(w)
}

// bImport implements builtins.import.
func bImport(w *worker, args ...*Expression) NixValue {
	return importFile(w, coerceToPath(w, args[0].Eval(w)), w.base)
}

// bScopedImport implements builtins.scopedImport: import with the attributes
// of a set as the base scope. It is not memoized.
func bScopedImport(w *worker, args ...*Expression) NixValue {
	scope := assertSet(w, args[0].Eval(w))
	p := coerceToPath(w, args[1].Eval(w))
	file := resolveImportFile(w, p)
	pr, err := parser.ParseFile(file)
	if err != nil {
		w.throwf(ErrEval, "%s", err)
	}
	x := delay(w, w.base.Subscope(w, scope), pr)
	return x.Eval(w)
}

// bReadFile implements builtins.readFile.
func bReadFile(w *worker, args ...*Expression) NixValue {
	p := coerceToPath(w, args[0].Eval(w))
	data, err := source.ReadFile(p)
	if err != nil {
		w.throwf(ErrEval, "cannot read '%s': %s", p, err)
	}
	return String(string(data))
}

// bReadDir implements builtins.readDir: a set of the directory's entries,
// each mapped to its type.
func bReadDir(w *worker, args ...*Expression) NixValue {
	p := coerceToPath(w, args[0].Eval(w))
	entries, err := source.ReadDir(p)
	if err != nil {
		w.throwf(ErrEval, "cannot list directory '%s': %s", p, err)
	}
	set := NewSet(len(entries))
	for _, e := range entries {
		set.Bind1(Intern(e.Name), value(w, String(e.Type)))
	}
	return SetValue(set.finish(w))
}

// bPathExists implements builtins.pathExists.
func bPathExists(w *worker, args ...*Expression) NixValue {
	return Bool(source.Exists(coerceToPath(w, args[0].Eval(w))))
}

// bStoreDir implements builtins.storeDir.
func bStoreDir(w *worker, args ...*Expression) NixValue { return String("/nix/store") }

// bPath implements builtins.path: the store path of a source path, hashed in
// memory. The result is a store path, not a source path.
func bPath(w *worker, args ...*Expression) NixValue {
	attrs := assertSet(w, args[0].Eval(w))
	p := coerceToPath(w, requiredAttr(w, attrs, symPath))
	name := path.Base(p)
	if x, ok := attrs.Get(symName); ok {
		name = assertString(w, x.Eval(w)).Content
	}
	var filter *Expression
	if x, ok := attrs.Get(symFilter); ok {
		filter = x
	}
	return addPath(w, name, p, filter)
}

// bFilterSource implements builtins.filterSource: builtins.path with a filter
// function and the path's base name.
func bFilterSource(w *worker, args ...*Expression) NixValue {
	filter := assertLambda(w, args[0].Eval(w))
	p := coerceToPath(w, args[1].Eval(w))
	expr := thunk(w, func(w *worker) NixValue { return LambdaValue(w, filter) })
	return addPath(w, path.Base(p), p, expr)
}

// addPath hashes a source path in memory, filtering its entries through the
// Nix function filter, and returns the store path the file would be copied to.
func addPath(w *worker, name, p string, filter *Expression) NixValue {
	var pf func(pth, typ string) bool
	if filter != nil {
		pf = func(pth, typ string) bool {
			fn := assertLambda(w, filter.Eval(w))
			return assertBool(w, apply2(w, fn, value(w, String(pth)), value(w, String(typ))).Eval(w))
		}
	}
	if err := nixhash.CheckStoreName(name); err != nil {
		w.throwf(ErrEval, "illegal name '%s' in path", name)
	}
	sp, err := nixhash.StorePathFiltered(p, name, pf)
	if err != nil {
		w.throwf(ErrEval, "cannot add path '%s': %s", p, err)
	}
	return StrValue(stringWithContext(sp, stringContext{path: sp}))
}

// requiredAttr reads a required attribute, which is how the path argument is
// read.
func requiredAttr(w *worker, attrs *AttrSet, sym Sym) NixValue {
	x, ok := attrs.Get(sym)
	if !ok {
		w.throwf(ErrMissingAttribute, "attribute '%s' missing", sym)
	}
	return x.Eval(w)
}
