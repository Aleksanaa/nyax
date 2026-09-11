package eval

import (
	"fmt"
	"slices"
	"strings"
	"unsafe"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// AttrSet is an attribute set: the names it binds, each with the expression
// that produces its value.
//
// Nix keeps a set as a sorted array of name/value pairs, and so does this. A
// set is one object rather than a hash table, a name is found by comparing
// four-byte symbols, and `//` and the set builtins are merges of two ordered
// runs rather than rehashing.
type AttrSet struct {
	attrs []attr
	// sorted says the attributes are in symbol order, which is what a lookup
	// binary searches. A set being built is not: names are appended and the
	// whole thing is put in order once, when the set is finished.
	sorted bool
}

// NixSet is how a set is passed around: always by pointer, so that a value
// holding one costs a word, and a scope shares the set it binds rather than
// copying it.
type NixSet = *AttrSet

// attr is one name and the expression that produces its value.
type attr struct {
	sym Sym
	x   *Expression
	// pos is where the attribute was written, for builtins.unsafeGetAttrPos.
	// It is nil for an attribute a builtin constructed, whose source position
	// there is no way to name.
	pos *p.LexPosition
}

// NewSet returns an empty attribute set with room for n attributes.
func NewSet(n int) NixSet { return &AttrSet{attrs: make([]attr, 0, n)} }

// setOf returns a set of attributes already in symbol order.
func setOf(attrs []attr) NixSet { return &AttrSet{attrs: attrs, sorted: true} }

// Len is how many attributes the set has.
func (s *AttrSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.attrs)
}

// cmpSym orders attributes by symbol, which is the order they are kept in.
// Symbols are interned in first-seen order, so this is not alphabetical; what
// has to be read alphabetically asks Keys.
func cmpSym(a attr, sym Sym) int {
	switch {
	case a.sym < sym:
		return -1
	case a.sym > sym:
		return 1
	}
	return 0
}

// scanMax is how large a set may be before a lookup binary searches it rather
// than scanning it. Most sets are small — the bindings of a `let`, the scope a
// call introduces — and a run of four-byte compares beats a search that cannot
// be inlined until there are a good many of them.
const scanMax = 8

// Get returns the attribute named sym, unevaluated.
func (s *AttrSet) Get(sym Sym) (*Expression, bool) {
	if s == nil {
		return nil, false
	}
	// A set still being built is scanned whatever its size: binding groups
	// large enough for that to matter do not look names up in themselves.
	if len(s.attrs) <= scanMax || !s.sorted {
		for i := range s.attrs {
			if s.attrs[i].sym == sym {
				return s.attrs[i].x, true
			}
		}
		return nil, false
	}
	// A plain search over four-byte symbols, rather than slices.BinarySearch
	// with a comparison function it cannot inline.
	lo, hi := 0, len(s.attrs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if s.attrs[mid].sym < sym {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == len(s.attrs) || s.attrs[lo].sym != sym {
		return nil, false
	}
	return s.attrs[lo].x, true
}

// Has reports whether the set binds sym.
func (s *AttrSet) Has(sym Sym) bool {
	_, ok := s.Get(sym)
	return ok
}

// Keys returns the attribute names in the order Nix presents them:
// alphabetically, so that printing and builtins.attrNames are deterministic.
func (s *AttrSet) Keys() []Sym {
	keys := make([]Sym, 0, s.Len())
	for _, a := range s.attrs {
		keys = append(keys, a.sym)
	}
	SortSym(keys)
	return keys
}

func (s *AttrSet) Print(w *worker, recurse int) string {
	if recurse == 0 {
		return "{ ... }"
	}
	if w.seen != nil {
		p := unsafe.Pointer(s)
		if w.seen[p] {
			return "«repeated»"
		}
		w.seen[p] = true
	}
	parts := make([]string, 0, s.Len()+2)
	parts = append(parts, "{")
	for _, key := range s.Keys() {
		x, _ := s.Get(key)
		parts = append(parts, fmt.Sprintf("%s = %s;", printAttrName(key), x.Eval(w).Print(w, recurse-1)))
	}
	return strings.Join(append(parts, "}"), " ")
}

// Bind1 adds a single attribute. A redefinition is caught when the set is
// finished, which is also when the names are put in order: checking here
// instead would mean searching the set once per binding.
func (s *AttrSet) Bind1(sym Sym, x *Expression) {
	s.attrs = append(s.attrs, attr{sym: sym, x: x})
	s.sorted = false
}

// setPos records the source position of the attribute just bound under sym,
// which is what builtins.unsafeGetAttrPos reads back.
func (s *AttrSet) setPos(sym Sym, pos *p.LexPosition) {
	if pos == nil {
		return
	}
	for i := len(s.attrs) - 1; i >= 0; i-- {
		if s.attrs[i].sym == sym {
			s.attrs[i].pos = pos
			return
		}
	}
}

// Bind adds an attribute under a path, creating the intermediate sets that
// `a.b.c = v;` implies. It returns the set the final name is bound in, so the
// caller can note its position.
func (s *AttrSet) Bind(w *worker, syms []Sym, x *Expression) *AttrSet {
	last := len(syms) - 1
	for i, sym := range syms[:last] {
		y, ok := s.Get(sym)
		if !ok {
			sub := NewSet(1)
			s.Bind1(sym, value(w, SetValue(sub)))
			s = sub
			continue
		}
		// The intermediate set must be one this binding group created; merging
		// into an attribute that is already a value would not be lazy.
		val := y.Val()
		if val.Kind() != KindSet {
			w.throwf(ErrEval, "attribute '%s' already defined", strings.Join(symNames(syms[:i+1]), "."))
		}
		s = val.Set()
	}
	s.Bind1(syms[last], x)
	return s
}

// finishAll puts this set and the ones nested inside it in order, which is
// what a binding group does once it is complete.
func (s *AttrSet) finishAll(w *worker) {
	for _, a := range s.attrs {
		if val := a.x.Val(); val.Kind() == KindSet && !val.Set().sorted {
			val.Set().finishAll(w)
		}
	}
	s.finish(w)
}

// finish puts a set that was built by appending in order, and reports a name
// bound twice. Every set reaches this before anything reads it.
func (s *AttrSet) finish(w *worker) NixSet {
	if s.sorted {
		return s
	}
	// Stable, so that a name bound twice keeps the first of the two and the
	// error below names the same one however the sort moved them.
	slices.SortStableFunc(s.attrs, func(a, b attr) int { return cmpSym(a, b.sym) })
	for i := 1; i < len(s.attrs); i++ {
		if s.attrs[i].sym == s.attrs[i-1].sym {
			w.throwf(ErrEval, "attribute '%s' already defined", s.attrs[i].sym)
		}
	}
	s.sorted = true
	return s
}

// keepFirst puts a set in order, dropping a repeated name in favour of the
// first that was added. It is what builtins.listToAttrs does with a name that
// appears twice, where a binding group would fail.
func (s *AttrSet) keepFirst() NixSet {
	slices.SortStableFunc(s.attrs, func(a, b attr) int { return cmpSym(a, b.sym) })
	s.attrs = slices.CompactFunc(s.attrs, func(a, b attr) bool { return a.sym == b.sym })
	s.sorted = true
	return s
}

// Update returns `s // other`: a merge of two runs already in order, so the
// result is in order without sorting anything.
func (s *AttrSet) Update(other NixSet) NixSet {
	merged := make([]attr, 0, len(s.attrs)+len(other.attrs))
	i, j := 0, 0
	for i < len(s.attrs) && j < len(other.attrs) {
		switch a, b := s.attrs[i], other.attrs[j]; {
		case a.sym < b.sym:
			merged, i = append(merged, a), i+1
		case a.sym > b.sym:
			merged, j = append(merged, b), j+1
		default:
			// The right-hand side wins, which is what `//` means.
			merged, i, j = append(merged, b), i+1, j+1
		}
	}
	merged = append(merged, s.attrs[i:]...)
	merged = append(merged, other.attrs[j:]...)
	return setOf(merged)
}

func (s *AttrSet) Compare(w *worker, other NixSet) bool {
	// Two derivations are equal when they build the same thing, which their
	// output path already says. Nix compares them that way and so must this:
	// a derivation carries functions — `override`, `overrideAttrs` — and a
	// function is equal to nothing, not even itself, so comparing the two
	// structurally would call every derivation different from every other.
	// nixpkgs leans on this: `drv.pythonModule != python` is how a Python
	// package checks it was built for the interpreter it is being used with.
	// Both have to say so before either output path is looked at, let alone
	// forced: a set that is not a derivation is compared as it stands.
	if s.derivationLike(w) && other.derivationLike(w) {
		a, aok := s.Get(symOutPath)
		b, bok := other.Get(symOutPath)
		if aok && bok {
			return a.Eval(w).Compare(w, b.Eval(w))
		}
	}
	if len(s.attrs) != len(other.attrs) {
		return false
	}
	for i, a := range s.attrs {
		b := other.attrs[i]
		if a.sym != b.sym {
			return false
		}
		if !sameThunk(a.x, b.x) && !a.x.Eval(w).Compare(w, b.x.Eval(w)) {
			return false
		}
	}
	return true
}

// derivationLike reports whether this set says it is a derivation, which is
// what its `type` being the string "derivation" says. Only `type` is forced:
// whether the other set is one decides whether anything else is looked at.
func (s *AttrSet) derivationLike(w *worker) bool {
	t, ok := s.Get(symType)
	if !ok {
		return false
	}
	v := t.Eval(w)
	return v.Kind() == KindString && v.Str().Content == "derivation"
}

func symNames(syms []Sym) []string {
	names := make([]string, len(syms))
	for i, sym := range syms {
		names[i] = sym.String()
	}
	return names
}

// Set binds sym, replacing whatever was there. It is Bind1 without the
// redefinition check, for the REPL, where rebinding a name is the point.
func (s *AttrSet) Set(sym Sym, x *Expression) {
	if s.sorted {
		if i, ok := slices.BinarySearchFunc(s.attrs, sym, cmpSym); ok {
			s.attrs[i].x = x
			return
		} else {
			s.attrs = slices.Insert(s.attrs, i, attr{sym: sym, x: x})
			return
		}
	}
	for i := range s.attrs {
		if s.attrs[i].sym == sym {
			s.attrs[i].x = x
			return
		}
	}
	s.Bind1(sym, x)
}

// pair is a set of two known attributes, which is the shape of the answer a
// handful of builtins give.
func pair(w *worker, sym1 Sym, val1 NixValue, sym2 Sym, val2 NixValue) NixValue {
	s := NewSet(2)
	s.Bind1(sym1, value(w, val1))
	s.Bind1(sym2, value(w, val2))
	return SetValue(s.finish(w))
}

// printAttrName is how a name is written in a printed set. Nix quotes one that
// could not be written as an identifier, so that what it prints reads back as
// the same set: the dot in `{ "a.b" = 1; }` is part of the name, and printing
// it bare would say the set held a nested `a`.
func printAttrName(sym Sym) string {
	s := sym.String()
	if !isIdentName(s) {
		return (&NixString{Content: s}).Print()
	}
	return s
}

// isIdentName reports whether a name can be written without quotes, which is
// the identifier the lexer accepts: a letter or underscore, then letters,
// digits, underscores, apostrophes and dashes.
func isIdentName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case i > 0 && (c >= '0' && c <= '9' || c == '\'' || c == '-'):
		default:
			return false
		}
	}
	return true
}
