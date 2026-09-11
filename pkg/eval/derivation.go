package eval

import (
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/aleksanaa/nyax/pkg/nixhash"
	"github.com/aleksanaa/nyax/pkg/nixjson"
)

// Derivations.
//
// A derivation is built from an attribute set by builtins.derivationStrict and
// wrapped by builtins.derivation. Its store path and the store paths of its
// outputs are pure functions of the attribute set, so they are computed here
// and kept in memory: no store is written, which is Nix's read-only mode.
// The one piece of state the hashing needs is a derivation's hash modulo its
// own output paths, which is recorded as each derivation is built and read
// back when a later one names it as an input.

// stringContext is one reference a string carries to a derivation or a store
// path. It is what a derivation's inputs are collected from: every string an
// attribute is coerced to carries the references it picked up along the way.
//
// A reference is described by the store path it names — a derivation path for
// a built output or a deep reference, a plain path for a source — rather than
// by an in-memory derivation, so that getContext and appendContext can build
// and read it without the derivation object at hand.
type stringContext struct {
	drvPath string // derivation path, for a built output or a deep reference
	out     string // output name, when this is a built output
	path    string // store path, for a plain source reference
	deep    bool   // drvPath reference: the whole derivation
}

// Derivation is a built derivation: the nixhash.Derivation being serialised,
// the output names in the order they were declared, and the store paths
// computed for it.
type Derivation struct {
	drv       *nixhash.Derivation
	outputs   []string
	drvPath   string
	outPaths  map[string]string // output name -> store path
	inputHash string            // hash modulo, base-16, for other derivations to resolve
}

// derivationRegistry is the derivation hash modulo keyed by derivation path,
// which is how a derivation resolves the hashes of the derivations it names as
// inputs. It is the in-memory counterpart of Nix's derivation::masked::hashes,
// and like it is shared by every worker.
var derivationRegistry sync.Map // drvPath string -> *Derivation

func lookupDerivation(drvPath string) *Derivation {
	if v, ok := derivationRegistry.Load(drvPath); ok {
		return v.(*Derivation)
	}
	return nil
}

// DerivationOf returns the derivation a value names, or nil when it is not
// one. It is how a caller outside the package — the CLI — turns an evaluated
// derivation back into the object whose store paths and JSON it can read.
//
// The drvPath of a builtins.derivation result is lazy, so reading it builds
// the derivation; a value that cannot produce one is simply not a derivation.
func DerivationOf(val NixValue) *Derivation {
	if val.Kind() != KindSet {
		return nil
	}
	x, ok := val.Set().Get(symDrvPath)
	if !ok {
		return nil
	}
	v, err := catching(func() NixValue { return x.Eval(mainWorker) })
	if err != nil {
		return nil
	}
	if v.Kind() == KindString {
		return lookupDerivation(v.Str().Content)
	}
	return nil
}

// DrvPath is the derivation's store path.
func (d *Derivation) DrvPath() string { return d.drvPath }

// JSON serialises the derivation the way `nix derivation show` does.
func (d *Derivation) JSON() ([]byte, error) { return d.drv.JSON() }

// stringWithContext makes a string that carries derivation references.
func stringWithContext(content string, ctx ...stringContext) *NixString {
	s := newString(content)
	if len(ctx) > 0 {
		s.extra = &stringExtra{Context: ctx}
	}
	return s
}

// drvPathString is a derivation's drvPath as a string carrying the deep
// reference that makes whatever interpolates it depend on the whole
// derivation.
func drvPathString(d *Derivation) *NixString {
	return stringWithContext(d.drvPath, stringContext{drvPath: d.drvPath, deep: true})
}

// outputString is one output's store path as a string carrying the built
// reference to that output.
func outputString(d *Derivation, out string) *NixString {
	return stringWithContext(d.outPaths[out], stringContext{drvPath: d.drvPath, out: out})
}

// derivationStrictInternal builds a derivation from an attribute set and
// computes its store paths. It is the work of both builtins.derivationStrict
// and builtins.derivation, which differ only in how they package the result.
func derivationStrictInternal(w *worker, attrs *AttrSet) *Derivation {
	name := derivationAttr(w, attrs, symName)

	// __structuredAttrs and __ignoreNulls are read first, as in Nix. Both are
	// required to be booleans; __ignoreNulls is skipped when the attributes
	// are folded into JSON, and __structuredAttrs is skipped when they are not.
	structuredAttrs := false
	if attr, ok := attrs.Get(symStructuredAttrs); ok {
		structuredAttrs = assertBool(w, attr.Eval(w))
	}
	ignoreNulls := false
	if attr, ok := attrs.Get(symIgnoreNulls); ok {
		ignoreNulls = assertBool(w, attr.Eval(w))
	}

	drv := &nixhash.Derivation{
		Name:      name,
		Outputs:   map[string]nixhash.Output{},
		InputDrvs: map[string][]string{},
		Env:       map[string]string{},
	}
	var outputs []string
	var context []stringContext
	var outputHash, outputHashAlgo, outputHashMode string
	hasOutputHash := false
	var jsonAttrs map[string]any
	if structuredAttrs {
		jsonAttrs = make(map[string]any, len(attrs.attrs))
	}

	for _, a := range attrs.attrs {
		sym := a.sym
		key := sym.String()
		// __ignoreNulls never reaches the environment. __structuredAttrs only
		// does in the non-structured case, where it is coerced like any other
		// attribute; when it is true every attribute is folded into __json.
		if sym == symIgnoreNulls {
			continue
		}
		if sym == symStructuredAttrs && structuredAttrs {
			continue
		}
		val := a.x.Eval(w)
		if ignoreNulls && val.Kind() == KindNull {
			continue
		}
		if sym == symArgs {
			list := assertList(w, val)
			for _, el := range list {
				str := CoerceEnv(w, el.Eval(w))
				drv.Args = append(drv.Args, str.Content)
				context = appendStringContext(context, str)
			}
			continue
		}
		// With structured attributes every attribute is folded into one JSON
		// value instead of becoming an environment variable of its own; its
		// references are still collected for the derivation's inputs.
		if structuredAttrs {
			jsonAttrs[key] = valueToNative(w, val, &context)
		} else {
			str := CoerceEnv(w, val)
			drv.Env[key] = str.Content
			context = appendStringContext(context, str)
		}
		// The attributes that name the derivation's own fields are read out
		// in either mode. Only these are coerced to a string.
		switch sym {
		case symBuilder:
			drv.Builder = ToString(w, val).Content
		case symSystem:
			drv.System = ToString(w, val).Content
		case symOutputs:
			outputs = strings.Fields(ToString(w, val).Content)
		case symOutputHash:
			outputHash, hasOutputHash = ToString(w, val).Content, true
		case symOutputHashAlgo:
			outputHashAlgo = ToString(w, val).Content
		case symOutputHashMode:
			outputHashMode = ToString(w, val).Content
		}
	}

	if structuredAttrs {
		js, err := nixjson.Marshal(jsonAttrs)
		if err != nil {
			w.throwf(ErrEval, "cannot serialise structured attributes: %s", err)
		}
		drv.Env["__json"] = string(js)
	}

	if outputs == nil {
		outputs = []string{"out"}
	}
	// Derivations cannot be named after drvPath, cannot have duplicates, and
	// cannot have no outputs at all.
	seen := map[string]bool{}
	for _, o := range outputs {
		if o == "drvPath" {
			w.throwf(ErrEval, "invalid derivation output name 'drvPath'")
		}
		if seen[o] {
			w.throwf(ErrEval, "duplicate derivation output '%s'", o)
		}
		seen[o] = true
	}

	if drv.Builder == "" {
		w.throwf(ErrEval, "required attribute 'builder' missing")
	}
	if drv.System == "" {
		w.throwf(ErrEval, "required attribute 'system' missing")
	}

	// The references carried by the coerced strings become the derivation's
	// inputs.
	addInputs(w, drv, context)

	// A fixed-output derivation names the hash of its one output, so its
	// output path is computed directly from that hash rather than from the
	// hash modulo the derivation. See Nix's prim_derivationStrict.
	if hasOutputHash {
		if len(outputs) != 1 || outputs[0] != "out" {
			w.throwf(ErrEval, "multiple outputs are not supported in fixed-output derivations")
		}
		method := "flat"
		switch outputHashMode {
		case "", "flat":
		case "recursive":
			method = "recursive"
		case "git":
			method = "git"
		case "text":
			w.throwf(ErrEval, "text-hashed derivations are not supported")
		default:
			w.throwf(ErrEval, "invalid value '%s' for 'outputHashMode' attribute", outputHashMode)
		}
		hashBase16, hashAlgo, err := nixhash.ParseHash(outputHash, outputHashAlgo)
		if err != nil {
			w.throwf(ErrEval, "invalid output hash '%s': %s", outputHash, err)
		}
		outPath := nixhash.FixedOutputPath(name, method, hashAlgo, hashBase16)
		drv.Outputs["out"] = nixhash.Output{
			Path:     outPath,
			HashAlgo: nixhash.OutputHashAlgo(method, hashAlgo),
			Hash:     hashBase16,
			Method:   method,
		}
		drv.Env["out"] = outPath

		d := &Derivation{drv: drv, outputs: outputs, outPaths: map[string]string{"out": outPath}}
		d.drvPath = drv.DrvPath()
		d.inputHash = nixhash.FixedOutputInputHash(method, hashAlgo, hashBase16, outPath)
		derivationRegistry.Store(d.drvPath, d)
		return d
	}

	// Blank the environment variables named after the outputs, so the hash of
	// the derivation does not depend on its own output paths.
	for _, o := range outputs {
		drv.Outputs[o] = nixhash.Output{}
		drv.Env[o] = ""
	}

	resolve := func(drvPath string) (string, bool) {
		d := lookupDerivation(drvPath)
		if d == nil {
			w.throwf(ErrEval, "derivation input '%s' is not known", drvPath)
		}
		return d.inputHash, true
	}

	// The output paths come from the hash modulo the derivation's own outputs.
	hashModulo, ok := drv.OutputHash(resolve)
	if !ok {
		w.throwf(ErrEval, "derivation '%s' has an input whose hash is not known", name)
	}

	d := &Derivation{drv: drv, outputs: outputs, outPaths: map[string]string{}}
	for _, o := range outputs {
		path := drv.OutputPath(o, hashModulo)
		d.outPaths[o] = path
		drv.Outputs[o] = nixhash.Output{Path: path}
		drv.Env[o] = path
	}

	d.drvPath = drv.DrvPath()

	inputHash, ok := drv.InputHash(resolve)
	if !ok {
		w.throwf(ErrEval, "derivation '%s' has an input whose hash is not known", name)
	}
	d.inputHash = inputHash.String(16)

	derivationRegistry.Store(d.drvPath, d)
	return d
}

// derivationAttr reads a required attribute as a string, which is how Nix
// reads the name: only a string is accepted, and its context is discarded.
func derivationAttr(w *worker, attrs *AttrSet, sym Sym) string {
	x, ok := attrs.Get(sym)
	if !ok {
		w.throwf(ErrMissingAttribute, "attribute '%s' missing", sym)
	}
	return assertString(w, x.Eval(w)).Content
}

// appendStringContext collects the references a coerced string carries.
func appendStringContext(dst []stringContext, s *NixString) []stringContext {
	if s.extra != nil && len(s.extra.Context) != 0 {
		dst = append(dst, s.extra.Context...)
	}
	return dst
}

// addInputs turns the references carried by the derivation's strings into its
// inputs: built outputs become input derivations, plain paths become sources,
// and a deep reference becomes the whole closure of the derivation it names.
func addInputs(w *worker, drv *nixhash.Derivation, context []stringContext) {
	addDrv := func(drvPath, out string) {
		drv.InputDrvs[drvPath] = append(drv.InputDrvs[drvPath], out)
	}
	addSrc := func(path string) {
		drv.InputSrcs = append(drv.InputSrcs, path)
	}
	for _, c := range context {
		switch {
		case c.deep:
			for _, p := range derivationClosure(c.drvPath) {
				addSrc(p)
				if d := lookupDerivation(p); d != nil {
					for _, o := range d.outputs {
						addDrv(p, o)
					}
				}
			}
		case c.drvPath != "":
			addDrv(c.drvPath, c.out)
		case c.path != "":
			addSrc(c.path)
		}
	}
	// Inputs are sets: the output names of each derivation and the source
	// paths are kept sorted and unique.
	for drvPath, outs := range drv.InputDrvs {
		sort.Strings(outs)
		drv.InputDrvs[drvPath] = slices.Compact(outs)
	}
	sort.Strings(drv.InputSrcs)
	drv.InputSrcs = slices.Compact(drv.InputSrcs)
}

// derivationClosure returns every store path reachable from a derivation, in
// ascending order: its own path and, transitively, the paths it names as
// inputs.
func derivationClosure(drvPath string) []string {
	seen := map[string]bool{}
	var out []string
	var visit func(string)
	visit = func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
		if next := lookupDerivation(p); next != nil {
			for _, src := range next.drv.InputSrcs {
				visit(src)
			}
			for drvPath := range next.drv.InputDrvs {
				visit(drvPath)
			}
		}
	}
	visit(drvPath)
	sort.Strings(out)
	return out
}

// derivationStrictSet is the result of builtins.derivationStrict: drvPath plus
// one attribute per output naming its store path.
func derivationStrictSet(w *worker, d *Derivation) NixValue {
	set := NewSet(len(d.outputs) + 1)
	set.Bind1(symDrvPath, value(w, StrValue(drvPathString(d))))
	for _, o := range d.outputs {
		set.Bind1(Intern(o), value(w, StrValue(outputString(d, o))))
	}
	return SetValue(set.finish(w))
}

// bDerivationStrict implements builtins.derivationStrict.
func bDerivationStrict(w *worker, args ...*Expression) NixValue {
	attrs := assertSet(w, args[0].Eval(w))
	return derivationStrictSet(w, derivationStrictInternal(w, attrs))
}

// bDerivation implements builtins.derivation, which derivation.nix wraps
// around derivationStrict. The result is an ordinary-looking set: every
// attribute the caller passed in, plus type, drvPath, outPath and outputName,
// plus one attribute per output and an all list. As in derivation.nix, the
// derivation itself is only built when an output path is read, so evaluating
// the set or looking at its attribute names must not force every attribute.
func bDerivation(w *worker, args ...*Expression) NixValue {
	attrs := assertSet(w, args[0].Eval(w))
	outputs := derivationOutputNames(w, attrs)

	// strict stands for `derivationStrict attrs`: it is forced at most once,
	// and only when an output path is read.
	strict := thunk(w, func(w *worker) NixValue {
		return derivationStrictSet(w, derivationStrictInternal(w, attrs))
	})
	strictAttr := func(w *worker, sym Sym) NixValue {
		x, ok := strict.Eval(w).Set().Get(sym)
		if !ok {
			w.throwf(ErrEval, "attribute '%s' missing", sym)
		}
		return x.Eval(w)
	}

	// The per-output sets refer to each other and to the common attributes, so
	// they are filled in after the common set is finished, and read through
	// the closures below only once forced.
	elem := make([]*AttrSet, len(outputs))

	common := NewSet(len(attrs.attrs) + len(outputs) + 2)
	for _, a := range attrs.attrs {
		common.Bind1(a.sym, a.x)
	}
	// The output names and the all/drvAttrs attributes are what `//` adds on
	// top of drvAttrs, so they override an input attribute of the same name.
	for i := range outputs {
		idx := i
		common.Set(Intern(outputs[i]), thunk(w, func(w *worker) NixValue {
			return SetValue(elem[idx])
		}))
	}
	common.Set(symAll, thunk(w, func(w *worker) NixValue {
		list := make(NixList, len(elem))
		for i := range elem {
			list[i] = value(w, SetValue(elem[i]))
		}
		return ListValue(list)
	}))
	common.Set(symDrvAttrs, value(w, SetValue(attrs)))
	common.finish(w)

	for i, o := range outputs {
		out := o
		extra := NewSet(4)
		extra.Bind1(symOutPath, thunk(w, func(w *worker) NixValue {
			return strictAttr(w, Intern(out))
		}))
		extra.Bind1(symDrvPath, thunk(w, func(w *worker) NixValue {
			return strictAttr(w, symDrvPath)
		}))
		extra.Bind1(symType, value(w, String("derivation")))
		extra.Bind1(symOutputName, value(w, String(out)))
		elem[i] = common.Update(extra.finish(w))
	}

	return SetValue(elem[0])
}

// derivationOutputNames reads the output names the way derivation.nix does,
// straight from the `outputs` attribute and defaulting to a single "out".
func derivationOutputNames(w *worker, attrs *AttrSet) []string {
	x, ok := attrs.Get(symOutputs)
	if !ok {
		return []string{"out"}
	}
	list := assertList(w, x.Eval(w))
	if len(list) == 0 {
		w.throwf(ErrEval, "derivation cannot have an empty set of outputs")
	}
	outputs := make([]string, len(list))
	for i, el := range list {
		outputs[i] = assertString(w, el.Eval(w)).Content
	}
	return outputs
}
