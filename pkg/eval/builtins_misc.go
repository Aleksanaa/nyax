package eval

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/aleksanaa/nyax/pkg/nixhash"
	p "github.com/aleksanaa/nyax/pkg/parser"
	"github.com/aleksanaa/nyax/pkg/source"
)

// The remaining builtins nixpkgs leans on that are not big enough for a file
// of their own: platform and version constants, environment access, the
// derivation-name helpers, and genericClosure.

// bCurrentSystem implements builtins.currentSystem: the host platform.
func bCurrentSystem(w *worker, args ...*Expression) NixValue {
	return String(currentSystem())
}

func currentSystem() string {
	switch runtime.GOARCH {
	case "amd64":
		switch runtime.GOOS {
		case "darwin":
			return "x86_64-darwin"
		default:
			return "x86_64-linux"
		}
	case "arm64":
		switch runtime.GOOS {
		case "darwin":
			return "aarch64-darwin"
		default:
			return "aarch64-linux"
		}
	case "386":
		return "i686-linux"
	}
	return runtime.GOARCH + "-" + runtime.GOOS
}

// bNixVersion implements builtins.nixVersion.
func bNixVersion(w *worker, args ...*Expression) NixValue { return String("2.35.2") }

// bGetEnv implements builtins.getEnv.
func bGetEnv(w *worker, args ...*Expression) NixValue {
	return String(os.Getenv(assertString(w, args[0].Eval(w)).Content))
}

// bWarn implements builtins.warn: print a warning, then return the value.
func bWarn(w *worker, args ...*Expression) NixValue {
	fmt.Fprintln(os.Stderr, "warning:", assertString(w, args[0].Eval(w)).Content)
	return args[1].Eval(w)
}

// bTraceVerbose implements builtins.traceVerbose: without --trace-verbose it
// is the identity on the second argument.
func bTraceVerbose(w *worker, args ...*Expression) NixValue {
	return args[1].Eval(w)
}

// bLangVersion implements builtins.langVersion.
func bLangVersion(w *worker, args ...*Expression) NixValue { return Int(6) }

// bCurrentTime implements builtins.currentTime.
func bCurrentTime(w *worker, args ...*Expression) NixValue {
	return Int(time.Now().Unix())
}

// bReadFileType implements builtins.readFileType.
func bReadFileType(w *worker, args ...*Expression) NixValue {
	return String(source.FileType(coerceToPath(w, args[0].Eval(w))))
}

// bStorePath implements builtins.storePath: an existing store path, taken as
// is rather than copied.
func bStorePath(w *worker, args ...*Expression) NixValue {
	p := coerceToPath(w, args[0].Eval(w))
	if !strings.HasPrefix(p, "/nix/store/") {
		w.throwf(ErrEval, "path '%s' is not in the Nix store", p)
	}
	return StrValue(stringWithContext(p, stringContext{path: p}))
}

// bParseDrvName implements builtins.parseDrvName: split a name into the part
// before the first version dash and the version after it.
func bParseDrvName(w *worker, args ...*Expression) NixValue {
	name, version := parseDrvName(assertString(w, args[0].Eval(w)).Content)
	s := NewSet(2)
	s.Bind1(symName, value(w, String(name)))
	s.Bind1(Intern("version"), value(w, String(version)))
	return SetValue(s.finish(w))
}

func parseDrvName(s string) (name, version string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '-' && i+1 < len(s) && !isAlpha(s[i+1]) {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

func isAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// bSplitVersion implements builtins.splitVersion: the components of a version,
// alternating runs of digits and of letters.
func bSplitVersion(w *worker, args ...*Expression) NixValue {
	comps := splitVersion(assertString(w, args[0].Eval(w)).Content)
	list := make(NixList, len(comps))
	for i, c := range comps {
		list[i] = value(w, String(c))
	}
	return ListValue(list)
}

// bConvertHash implements builtins.convertHash: re-encode a hash between
// formats.
func bConvertHash(w *worker, args ...*Expression) NixValue {
	attrs := assertSet(w, args[0].Eval(w))
	hash := requiredAttr(w, attrs, symHash).Str().Content
	algo := ""
	if x, ok := attrs.Get(symHashAlgo); ok {
		algo = assertString(w, x.Eval(w)).Content
	}
	format := assertString(w, requiredAttr(w, attrs, symToHashFormat)).Content
	out, err := nixhash.ConvertHash(hash, algo, format)
	if err != nil {
		w.throwf(ErrEval, "%s", err)
	}
	return String(out)
}

// bToXML implements builtins.toXML.
func bToXML(w *worker, args ...*Expression) NixValue {
	w.throwf(ErrEval, "builtins.toXML is not implemented")
	return Null
}

// bGenericClosure implements builtins.genericClosure: the fixpoint of startSet
// under operator, deduplicated by each element's key.
func bGenericClosure(w *worker, args ...*Expression) NixValue {
	attrs := assertSet(w, args[0].Eval(w))
	startExpr, ok := attrs.Get(symStartSet)
	if !ok {
		w.throwf(ErrMissingAttribute, "attribute 'startSet' missing in genericClosure")
	}
	startSet := assertList(w, startExpr.Eval(w))
	opExpr, ok := attrs.Get(symOperator)
	if !ok {
		w.throwf(ErrMissingAttribute, "attribute 'operator' missing in genericClosure")
	}
	op := assertLambda(w, opExpr.Eval(w))

	work := make([]*Expression, len(startSet))
	copy(work, startSet)
	var result []*Expression
	var keys []NixValue

	for len(work) > 0 {
		e := work[0]
		work = work[1:]
		set := assertSet(w, e.Eval(w))
		keyExpr, ok := set.Get(symKey)
		if !ok {
			w.throwf(ErrEval, "attribute 'key' missing in a genericClosure element")
		}
		key := keyExpr.Eval(w)
		seen := false
		for _, k := range keys {
			if k.Compare(w, key) {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		keys = append(keys, key)
		result = append(result, e)
		next := assertList(w, op.Apply(w, e).Eval(w))
		work = append(work, next...)
	}
	return ListValue(result)
}

// bAddErrorContext implements builtins.addErrorContext: on failure, annotate
// the error with the first argument before it propagates.
func bAddErrorContext(w *worker, args ...*Expression) (result NixValue) {
	defer func() {
		if r := recover(); r != nil {
			if e := asEvalError(r); e != nil {
				msg := CoerceToString(w, args[0].Eval(w)).Content
				e.Trace = append(e.Trace, Frame{Desc: msg})
				panic(e)
			}
			panic(r)
		}
	}()
	return args[1].Eval(w)
}

// bUnsafeGetAttrPos implements builtins.unsafeGetAttrPos: the source position
// of an attribute, or null when the attribute has none recorded.
func bUnsafeGetAttrPos(w *worker, args ...*Expression) NixValue {
	name := assertString(w, args[0].Eval(w)).intern()
	set := assertSet(w, args[1].Eval(w))
	var pos *p.LexPosition
	for i := range set.attrs {
		if set.attrs[i].sym == name {
			pos = set.attrs[i].pos
			break
		}
	}
	if pos == nil || pos.Filename == p.StringPath {
		return Null
	}
	s := NewSet(3)
	s.Bind1(symFile, value(w, String(pos.Filename)))
	s.Bind1(symLine, value(w, Int(int64(pos.Line))))
	s.Bind1(symColumn, value(w, Int(int64(pos.Column))))
	return SetValue(s.finish(w))
}
