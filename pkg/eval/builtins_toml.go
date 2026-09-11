package eval

import (
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// bFromTOML implements builtins.fromTOML: parse a TOML 1.0.0 document into a
// Nix value. Nix parses with toml11 and maps the result in prim_fromTOML, so
// the same shape is produced here: tables become sets, arrays become lists,
// and — unlike JSON — an integer stays an integer while a float stays a float.
func bFromTOML(w *worker, args ...*Expression) NixValue {
	str := assertString(w, args[0].Eval(w))
	var parsed map[string]any
	if _, err := toml.Decode(str.Content, &parsed); err != nil {
		w.throwf(ErrEval, "while parsing TOML: %s", err)
	}
	return tomlToNix(w, parsed)
}

// tomlToNix converts a value decoded by the TOML library, mirroring the switch
// in prim_fromTOML. Dates and times are refused because Nix only accepts them
// behind the experimental ParseTomlTimestamps feature, which Nyax does not
// implement.
func tomlToNix(w *worker, x any) NixValue {
	switch t := x.(type) {
	case map[string]any:
		s := NewSet(len(t))
		for key, val := range t {
			checkNoNullByte(w, key)
			s.Bind1(Intern(key), value(w, tomlToNix(w, val)))
		}
		return SetValue(s.keepFirst())
	case []any:
		list := make(NixList, len(t))
		for i, val := range t {
			list[i] = value(w, tomlToNix(w, val))
		}
		return ListValue(list)
	case []map[string]any:
		list := make(NixList, len(t))
		for i, val := range t {
			list[i] = value(w, tomlToNix(w, val))
		}
		return ListValue(list)
	case string:
		checkNoNullByte(w, t)
		return String(t)
	case int64:
		return Int(t)
	case float64:
		return Float(t)
	case bool:
		return Bool(t)
	case time.Time:
		w.throwf(ErrEval, "while parsing TOML: Dates and times are not supported")
	}
	w.throwf(ErrType, "cannot convert a TOML value of type %T to a Nix value", x)
	return NixValue{}
}

// checkNoNullByte mirrors forceNoNullByte: a TOML string or key may not carry
// a U+0000, which Nix strings cannot represent.
func checkNoNullByte(w *worker, s string) {
	if strings.IndexByte(s, 0) >= 0 {
		w.throwf(ErrEval,
			"while parsing TOML: input string %q cannot be represented as Nix string because it contains null bytes", s)
	}
}
