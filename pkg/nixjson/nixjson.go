// Package nixjson renders values the way Nix's JSON writer does.
//
// Go's encoding/json is close, but differs in two ways Nix users can see: it
// HTML-escapes by default, turning ">" into "\u003e", and it lays floats out
// differently. This package wraps it with Nix's choices, which are
// nlohmann::json's: Nix serialises with that library, so matching it is what
// makes Nyax's JSON byte-for-byte the same.
package nixjson

import (
	"bytes"
	stdjson "encoding/json"
	"math"
	"strconv"
	"strings"
)

// Marshal renders v as compact JSON, with map keys sorted and without the HTML
// escaping encoding/json applies by default, which would turn ">" into
// "\u003e" where Nix writes ">" verbatim.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := stdjson.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode terminates the value with a newline; Nix's writer does not.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Float is a float that marshals the way Nix's JSON writer does, which differs
// from Go's: an integral float keeps a ".0", and the switch to exponent
// notation happens at a different magnitude. It implements
// [stdjson.Marshaler].
type Float float64

func (f Float) MarshalJSON() ([]byte, error) {
	// nlohmann::json writes non-finite numbers as null, since JSON has no
	// way to represent them.
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
		return []byte("null"), nil
	}
	return []byte(FormatFloat(float64(f))), nil
}

// FormatFloat is nlohmann's to_chars: the shortest round-trip digits from Go,
// laid out by nlohmann's format_buffer rules (fixed for a decimal point in
// (-4, 15], scientific otherwise).
func FormatFloat(f float64) string {
	sign := ""
	if math.Signbit(f) {
		sign, f = "-", -f
	}
	if f == 0 {
		return sign + "0.0"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	// value = digits * 10^decExp, with the point after decExp more digits.
	decExp := exp - (len(digits) - 1)
	return sign + formatDecimal(digits, decExp)
}

// formatDecimal is nlohmann's format_buffer with min_exp -4 and max_exp 15.
func formatDecimal(digits string, decExp int) string {
	k := len(digits)
	n := k + decExp
	switch {
	case k <= n && n <= 15:
		// 123 -> 123000.0
		return digits + strings.Repeat("0", n-k) + ".0"
	case 0 < n && n <= 15:
		// 123456 -> 123.456
		return digits[:n] + "." + digits[n:]
	case -4 < n && n <= 0:
		// 0.00123
		return "0." + strings.Repeat("0", -n) + digits
	}
	mant := digits
	if k > 1 {
		mant = digits[:1] + "." + digits[1:]
	}
	return mant + "e" + formatExponent(n-1)
}

func formatExponent(e int) string {
	sign := "+"
	if e < 0 {
		sign, e = "-", -e
	}
	// nlohmann prints at least two exponent digits, for printf("%g") compat.
	s := strconv.Itoa(e)
	if len(s) < 2 {
		s = "0" + s
	}
	return sign + s
}
