package eval

import (
	p "github.com/aleksanaa/nyax/pkg/parser"
)

// A builtin is a primitive operation exposed through the `builtins` set.
//
// Arity is the number of arguments the implementation takes at once; Nix
// functions are curried, so applying fewer yields a partial application.
// Global is set for the handful of builtins Nix also exposes unprefixed, such
// as `map` and `throw`; the rest are reachable as `builtins.name` and as the
// underscored `__name`.
type builtin struct {
	arity  int
	fn     func(*worker, ...*Expression) NixValue
	doc    string
	global bool
}

var builtins = map[string]builtin{
	// Arithmetic and comparison.
	"add":      {2, arithOp(p.OpAddNode), "Add two numbers.", false},
	"sub":      {2, arithOp(p.OpReduceNode), "Subtract the second number from the first.", false},
	"mul":      {2, arithOp(p.OpMultiplyNode), "Multiply two numbers.", false},
	"div":      {2, arithOp(p.OpDivideNode), "Divide the first number by the second.", false},
	"lessThan": {2, bLessThan, "Compare two numbers or strings.", false},
	"bitAnd":   {2, bitOp(func(a, b int64) int64 { return a & b }), "Bitwise AND of two integers.", false},
	"bitOr":    {2, bitOp(func(a, b int64) int64 { return a | b }), "Bitwise OR of two integers.", false},
	"bitXor":   {2, bitOp(func(a, b int64) int64 { return a ^ b }), "Bitwise XOR of two integers.", false},
	"ceil":     {1, bCeil, "Round a number up to an integer.", false},
	"floor":    {1, bFloor, "Round a number down to an integer.", false},
	"compareVersions": {2, bCompareVersions,
		"Compare two version strings; -1, 0 or 1 as the first sorts before, with, or after the second.", false},

	// Types.
	"typeOf":     {1, bTypeOf, "Name of the type of a value.", false},
	"isAttrs":    {1, bIsAttrs, "Whether a value is a set.", false},
	"isBool":     {1, bIsBool, "Whether a value is a Boolean.", false},
	"isFloat":    {1, bIsFloat, "Whether a value is a float.", false},
	"isFunction": {1, bIsFunction, "Whether a value is a function.", false},
	"isInt":      {1, bIsInt, "Whether a value is an integer.", false},
	"isList":     {1, bIsList, "Whether a value is a list.", false},
	"isNull":     {1, bIsNull, "Whether a value is null.", true},
	"isPath":     {1, bIsPath, "Whether a value is a path.", false},
	"isString":   {1, bIsString, "Whether a value is a string.", false},

	// Evaluation control.
	"seq":     {2, bSeq, "Evaluate the first argument, then return the second.", false},
	"deepSeq": {2, bDeepSeq, "Evaluate the first argument recursively, then return the second.", false},
	"throw":   {1, bThrow, "Fail evaluation with a message.", true},
	"abort":   {1, bAbort, "Abort evaluation with a message; not catchable by tryEval.", true},
	"tryEval": {1, bTryEval, "Evaluate a value, reporting whether it threw.", false},
	"trace":   {2, bTrace, "Print the first argument to stderr, then return the second.", false},

	// Lists.
	"all":         {2, bAll, "Whether a predicate holds for every element.", false},
	"any":         {2, bAny, "Whether a predicate holds for any element.", false},
	"concatLists": {1, bConcatLists, "Concatenate a list of lists.", false},
	"concatMap":   {2, bConcatMap, "Concatenate the result of mapping a function over a list.", false},
	"elem":        {2, bElem, "Whether a value occurs in a list.", false},
	"elemAt":      {2, bElemAt, "Element of a list at an index, counting from zero.", false},
	"filter":      {2, bFilter, "Elements of a list satisfying a predicate.", false},
	"foldl'":      {3, bFoldl, "Fold a list from the left, forcing each accumulator.", false},
	"genList":     {2, bGenList, "List of n values built from their indices.", false},
	"genericClosure": {1, bGenericClosure,
		"The transitive closure of a start set under an operator, keyed by a key attribute.", false},
	"head":      {1, bHead, "First element of a list.", false},
	"length":    {1, bLength, "Number of elements in a list.", false},
	"map":       {2, bMap, "Apply a function to every element of a list.", true},
	"partition": {2, bPartition, "Split a list into elements that do and do not satisfy a predicate.", false},
	"sort":      {2, bSort, "Sort a list with a strict less-than comparator.", false},
	"tail":      {1, bTail, "All but the first element of a list.", false},

	// Sets.
	"attrNames":    {1, bAttrNames, "Names of the attributes of a set, sorted.", false},
	"attrValues":   {1, bAttrValues, "Values of the attributes of a set, ordered by name.", false},
	"catAttrs":     {2, bCatAttrs, "Values of an attribute across a list of sets, skipping those without it.", false},
	"functionArgs": {1, bFunctionArgs, "Formal arguments of a function, mapped to whether they have a default.", false},
	"getAttr":      {2, bGetAttr, "Value of a named attribute of a set.", false},
	"groupBy":      {2, bGroupBy, "Group list elements into a set by a key function.", false},
	"hasAttr":      {2, bHasAttr, "Whether a set has a named attribute.", false},
	"intersectAttrs": {2, bIntersectAttrs,
		"Attributes of the second set whose names also occur in the first.", false},
	"listToAttrs": {1, bListToAttrs, "Build a set from a list of { name, value } sets.", false},
	"mapAttrs":    {2, bMapAttrs, "Apply a function to each attribute's name and value.", false},
	"removeAttrs": {2, bRemoveAttrs, "A set without the named attributes.", true},
	"zipAttrsWith": {2, bZipAttrsWith,
		"Transpose a list of sets into a set of lists, then apply a function.", false},

	// Strings.
	"concatStringsSep": {2, bConcatStringsSep, "Join a list of strings with a separator.", false},
	"match":            {2, bMatch, "Match a string against an anchored POSIX regular expression.", false},
	"parseDrvName":     {1, bParseDrvName, "Split a name into the package name and version.", false},
	"replaceStrings":   {3, bReplaceStrings, "Replace occurrences of each string with its replacement.", false},
	"split":            {2, bSplit, "Split a string on a POSIX regular expression.", false},
	"splitVersion":     {1, bSplitVersion, "Split a version into its components.", false},
	"stringLength":     {1, bStringLength, "Length of a string in bytes.", false},
	"substring":        {3, bSubstring, "Substring of a string, by start offset and length.", false},
	"toString":         {1, bToString, "Coerce a value to a string.", true},

	// Strings with context.
	"hasContext":                 {1, bHasContext, "Whether a string refers to any derivation or path.", false},
	"getContext":                 {1, bGetContext, "The references a string carries.", false},
	"appendContext":              {2, bAppendContext, "Add references to a string.", false},
	"unsafeDiscardStringContext": {1, bUnsafeDiscardStringContext, "A string without the derivations it refers to.", false},
	"addDrvOutputDependencies":   {1, bAddDrvOutputDependencies, "Widen a derivation-path reference to the whole derivation.", false},
	"unsafeDiscardOutputDependency": {1, bUnsafeDiscardOutputDependency,
		"Narrow a whole-derivation reference to its path.", false},

	// Paths and hashing.
	"baseNameOf":   {1, bBaseNameOf, "The part of a path after the last slash.", true},
	"dirOf":        {1, bDirOf, "The part of a path before the last slash.", true},
	"hashString":   {2, bHashString, "The base-16 digest of a string.", false},
	"hashFile":     {2, bHashFile, "The base-16 digest of a file.", false},
	"toFile":       {2, bToFile, "Store a string in a file and return its path.", false},
	"pathExists":   {1, bPathExists, "Whether a path exists.", false},
	"readFile":     {1, bReadFile, "The contents of a file as a string.", false},
	"readDir":      {1, bReadDir, "A directory's entries mapped to their types.", false},
	"readFileType": {1, bReadFileType, "The type of a path.", false},
	"path":         {1, bPath, "The store path of a source path.", false},
	"filterSource": {2, bFilterSource, "The store path of a source path, filtered.", false},
	"storeDir":     {0, bStoreDir, "The Nix store directory.", false},
	"storePath":    {1, bStorePath, "An existing store path.", false},

	// Evaluation context.
	"addErrorContext":  {2, bAddErrorContext, "Annotate an error with extra context.", false},
	"unsafeGetAttrPos": {2, bUnsafeGetAttrPos, "The source position of an attribute.", false},
	"currentSystem":    {0, bCurrentSystem, "The host platform.", false},
	"currentTime":      {0, bCurrentTime, "The current time in seconds.", false},
	"nixVersion":       {0, bNixVersion, "The Nix version.", false},
	"langVersion":      {0, bLangVersion, "The Nix language version.", false},
	"getEnv":           {1, bGetEnv, "The value of an environment variable.", false},
	"warn":             {2, bWarn, "Print a warning, then return the value.", false},
	"traceVerbose":     {2, bTraceVerbose, "Print a value with --trace-verbose.", false},

	// Flakes and fetchers, recognised but not yet implemented.
	"parseFlakeRef":    {1, unimplemented("parseFlakeRef"), "Parse a flake reference.", false},
	"flakeRefToString": {1, unimplemented("flakeRefToString"), "Render a flake reference.", false},
	"fetchurl":         {1, unimplemented("fetchurl"), "Fetch a URL.", false},
	"fetchTarball":     {2, unimplemented("fetchTarball"), "Fetch and unpack a tarball.", true},
	"fetchGit":         {1, unimplemented("fetchGit"), "Fetch a Git repository.", true},
	"fetchTree":        {1, unimplemented("fetchTree"), "Fetch a source tree.", true},
	"fetch":            {1, unimplemented("fetch"), "Fetch a flake input.", false},
	"getFlake":         {1, unimplemented("getFlake"), "Fetch a flake.", false},
	"nixPath":          {1, unimplemented("nixPath"), "The Nix search path.", false},
	"fetchClosure":     {1, unimplemented("fetchClosure"), "Fetch a closure from a binary cache.", false},

	// Files.
	"import":       {1, bImport, "Load and evaluate a Nix file.", true},
	"scopedImport": {2, bScopedImport, "Import a Nix file with an alternate scope.", true},

	// Serialisation.
	"fromJSON":    {1, bFromJSON, "Parse a JSON string into a Nix value.", false},
	"toJSON":      {1, bToJSON, "Render a value as JSON.", false},
	"fromTOML":    {1, bFromTOML, "Parse a TOML string into a Nix value.", true},
	"toXML":       {1, bToXML, "Render a value as XML.", false},
	"convertHash": {1, bConvertHash, "Re-encode a hash between formats.", false},

	// Derivations.
	"derivation":       {1, bDerivation, "Build a derivation from an attribute set.", true},
	"derivationStrict": {1, bDerivationStrict, "Build a derivation, returning only its paths.", true},
	"placeholder":      {1, bPlaceholder, "The placeholder string an output is known by before it is built.", true},
}

// globals are the non-function names Nix predefines.
var globals = map[string]NixValue{
	"true":  True,
	"false": False,
	"null":  Null,
}

// DefaultScope is the scope every evaluation starts in: the globals, the
// underscored builtins, and `builtins` itself.
var DefaultScope = newDefaultScope(mainWorker)

func newDefaultScope(w *worker) *Scope {
	builtinsSet := NewSet(len(builtins) + len(globals) + 1)
	mainSet := NewSet(2*len(builtins) + len(globals) + 1)

	for name, b := range builtins {
		sym := Intern(name)
		// A builtin of arity zero is a constant, not a function: it evaluates
		// to its value when it is reached, as Nix's nullary primops do.
		var val NixValue
		if b.arity == 0 {
			val = b.fn(w)
		} else {
			op := &NixPrimop{Func: b.fn, ArgNum: b.arity, Doc: b.doc, Sym: sym}
			val = LambdaValue(w, op)
		}
		builtinsSet.Bind1(sym, value(w, val))
		if b.global {
			mainSet.Bind1(sym, value(w, val))
		}
		mainSet.Bind1(Intern("__"+name), value(w, val))
	}
	for name, val := range globals {
		sym := Intern(name)
		builtinsSet.Bind1(sym, value(w, val))
		mainSet.Bind1(sym, value(w, val))
	}

	builtinsSet.Bind1(symBuiltins, value(w, SetValue(builtinsSet)))
	mainSet.Bind1(symBuiltins, value(w, SetValue(builtinsSet)))
	main := mainSet.finish(w)
	// The resolver needs the same names, in the same slots, to settle what a
	// file's free names refer to before it is evaluated.
	baseFrame = frameOfSet(main)
	// The default scope is where an import evaluates its file. The worker
	// carries it so that a builtin need not name it before it exists.
	env := &Env{vals: make([]*Expression, len(main.attrs))}
	for i, a := range main.attrs {
		env.vals[i] = a.x
	}
	s := &Scope{env: env, frame: baseFrame}
	w.base = s
	return s
}
