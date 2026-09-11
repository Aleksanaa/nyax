package eval

import (
	"path"
	"strings"
	"sync"

	"github.com/aleksanaa/nyax/pkg/nixhash"
)

// CoerceToString converts a value to a string the way interpolation and
// attribute names do: only strings, paths and sets with an outPath or a
// __toString attribute are accepted. A path is copied to the store, which is
// what Nix's coerceToString does by default.
func CoerceToString(w *worker, val NixValue) *NixString {
	return coerceToString(w, val, false, true)
}

// CoerceNoCopy is CoerceToString without copying a path to the store. It is
// what builtins.baseNameOf and builtins.dirOf use, which look at the source
// path itself.
func CoerceNoCopy(w *worker, val NixValue) *NixString {
	return coerceToString(w, val, false, false)
}

// ToString converts a value to a string the way builtins.toString does, which
// additionally accepts numbers, Booleans, null and lists, and leaves a path
// as the source path.
func ToString(w *worker, val NixValue) *NixString {
	return coerceToString(w, val, true, false)
}

// CoerceEnv converts a value the way a derivation attribute is: like
// builtins.toString but copying paths to the store, which is what makes them
// inputs of the derivation.
func CoerceEnv(w *worker, val NixValue) *NixString {
	return coerceToString(w, val, true, true)
}

func coerceToString(w *worker, val NixValue, more, copy bool) *NixString {
	switch val.Kind() {
	case KindString:
		return val.Str()
	case KindPath:
		if copy {
			return copyPathToStore(w, val.Path().String())
		}
		return newString(val.Path().String())
	case KindSet:
		return val.Set().coerceToString(w, more, copy)
	}
	if !more {
		w.throwf(ErrType, "cannot coerce %s to a string", anTypeName(val))
	}
	switch val.Kind() {
	case KindInt:
		return newString(printInt(val.Int()))
	case KindFloat:
		return newString(printFloat(val.Float()))
	case KindBool:
		// Nix renders true as "1" and false as the empty string.
		if val.Bool() {
			return newString("1")
		}
		return newString("")
	case KindNull:
		return newString("")
	case KindList:
		return coerceListToString(w, val.List(), more, copy)
	}
	w.throwf(ErrType, "cannot coerce %s to a string", anTypeName(val))
	return nil
}

// copyPathToStore returns the store path a source path is copied to, hashed in
// memory and cached the way Nix caches addToStore.
func copyPathToStore(w *worker, p string) *NixString {
	if v, ok := storePathCache.Load(p); ok {
		sp := v.(string)
		return stringWithContext(sp, stringContext{path: sp})
	}
	sp, err := nixhash.StorePathFiltered(p, path.Base(p), nil)
	if err != nil {
		w.throwf(ErrEval, "cannot add path '%s' to the store: %s", p, err)
	}
	storePathCache.Store(p, sp)
	return stringWithContext(sp, stringContext{path: sp})
}

// storePathCache maps a source path to the store path it is copied to, so a
// path an expression names many times is only hashed once.
var storePathCache sync.Map

// coerceToString on a set uses __toString if present, else outPath, which is
// what makes a derivation usable inside a string.
func (s *AttrSet) coerceToString(w *worker, more, copy bool) *NixString {
	if x, ok := s.Get(symToString); ok {
		val := x.Eval(w)
		if !val.IsLambda() {
			w.throwf(ErrType, "value of the __toString attribute is %s while a function was expected",
				anTypeName(val))
		}
		return coerceToString(w, applyToValue(w, val.Lambda(), SetValue(s)).Eval(w), more, copy)
	}
	if x, ok := s.Get(symOutPath); ok {
		return coerceToString(w, x.Eval(w), more, copy)
	}
	w.throwf(ErrType, "cannot coerce a set to a string: it has neither a __toString nor an outPath attribute")
	return nil
}

// coerceListToString joins the coerced elements with spaces.
func coerceListToString(w *worker, l NixList, more, copy bool) *NixString {
	result := &NixString{}
	parts := make([]string, len(l))
	for i, x := range l {
		str := coerceToString(w, x.Eval(w), more, copy)
		parts[i] = str.Content
		result.absorb(str)
	}
	result.Content = strings.Join(parts, " ")
	return result
}
