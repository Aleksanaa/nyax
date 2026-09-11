package eval

import "testing"

// TestFromTOML checks builtins.fromTOML against values produced by Nix. Each
// case compares builtins.toJSON of the parsed document with the JSON Nix emits
// for the same input, which pins the integer/float distinction (TOML has both,
// JSON only one) and Nix's null rendering of non-finite floats.
func TestFromTOML(t *testing.T) {
	for _, test := range []struct{ toml, wantJSON string }{
		{`x=1
`, `{"x":1}`},
		{`f=1.0
`, `{"f":1.0}`},
		{`e=1e2
`, `{"e":100.0}`},
		{`small=0.000001
`, `{"small":1e-06}`},
		{`big=1.0e20
`, `{"big":1e+20}`},
		{`neg=-3
`, `{"neg":-3}`},
		{`plus=+99
`, `{"plus":99}`},
		{`hex=0xdeadBEEF
`, `{"hex":3735928559}`},
		{`oct=0o755
`, `{"oct":493}`},
		{`bin=0b1010
`, `{"bin":10}`},
		{`u=1_000_000
`, `{"u":1000000}`},
		{`b=true
n=false
`, `{"b":true,"n":false}`},
		{`s="a"
`, `{"s":"a"}`},
		{`lit='c:\path'
`, `{"lit":"c:\\path"}`},
		{`frac=0.5
`, `{"frac":0.5}`},
		{`arr=[1,2,3]
`, `{"arr":[1,2,3]}`},
		{`mixed=[1,"x",true]
`, `{"mixed":[1,"x",true]}`},
		{`nested=[[1,2],[3]]
`, `{"nested":[[1,2],[3]]}`},
		{`[t]
y=2
`, `{"t":{"y":2}}`},
		{`a.b.c=1
`, `{"a":{"b":{"c":1}}}`},
		{`inline={b=1,c={d=2}}
`, `{"inline":{"b":1,"c":{"d":2}}}`},
		{`[[aot]]
x=1
[[aot]]
x=2
`, `{"aot":[{"x":1},{"x":2}]}`},
		{`inf=inf
ninf=-inf
nan=nan
`, `{"inf":null,"nan":null,"ninf":null}`},
		{`under=1e-400
`, `{"under":0.0}`},
		{`deep={a={b={c=[{d=1}]}}}
`, `{"deep":{"a":{"b":{"c":[{"d":1}]}}}}`},
		{`quoted."key with space"=1
`, `{"quoted":{"key with space":1}}`},
		{`"a b"=1
`, `{"a b":1}`},
		{`ml="""x
y"""
`, `{"ml":"x\ny"}`},
		{`esc="a\tb\nc\"d\\e"
`, `{"esc":"a\tb\nc\"d\\e"}`},
		{`quote="a\"b"
`, `{"quote":"a\"b"}`},
		{``, `{}`},
	} {
		src := "builtins.toJSON (builtins.fromTOML " + nixIndented(test.toml) + ") == " + nixIndented(test.wantJSON)
		got, err := evalPrint(t, src)
		if err != nil {
			t.Errorf("%q: %v", test.toml, err)
			continue
		}
		if got != "true" {
			t.Errorf("%q: JSON differs from Nix; want %s", test.toml, test.wantJSON)
		}
	}
}

// TestFromTOMLErrors checks the inputs Nix rejects. Dates and times are only
// accepted behind an experimental feature Nyax does not implement, and a null
// byte cannot be represented as a Nix string in either a value or a key.
func TestFromTOMLErrors(t *testing.T) {
	for _, toml := range []string{
		`d=1979-05-27T07:32:00Z
`,
		`d=1979-05-27
`,
		`d=07:32:00
`,
		`s="a\u0000b"
`,
		`"\u0000"=1
`,
		`a=9223372036854775808
`,
		`a=01
`,
		`a=1
a=2
`,
		`a=
`,
		`a=1e400
`,
		`[a]
b=1
[a]
c=2
`,
		`a=[1,2
`,
	} {
		if _, err := EvalString("builtins.fromTOML " + nixIndented(toml)); err == nil {
			t.Errorf("%q: expected an error, got nil", toml)
		}
	}
}

// nixIndented wraps text in a Nix indented string, which passes backslashes
// through unchanged so that TOML escapes reach the parser intact.
func nixIndented(s string) string { return "''\n" + s + "''" }
