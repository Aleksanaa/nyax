package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleksanaa/nyax/pkg/nixhash"
)

// The store paths below were produced by Nix for the same expressions, so the
// test pins that the derivation hashing stays byte-for-byte compatible.

func TestDerivationPaths(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"simple drvPath",
			`(builtins.derivation { name = "foo"; builder = "/bin/sh"; system = "x86_64-linux"; }).drvPath`,
			`"/nix/store/9d794zbx10n43fa6zaqf33pfymdrm4r2-foo.drv"`,
		},
		{
			"simple outPath",
			`(builtins.derivation { name = "foo"; builder = "/bin/sh"; system = "x86_64-linux"; }).outPath`,
			`"/nix/store/xpcvxsx5sw4rbq666blz6sxqlmsqphmr-foo"`,
		},
		{
			"input derivation",
			`let a = builtins.derivation { name = "dep"; builder = "/bin/true"; system = "x86_64-linux"; };
			 in (builtins.derivation { name = "top"; builder = "/bin/sh"; system = "x86_64-linux"; dep = a.drvPath; }).drvPath`,
			`"/nix/store/w2v5c9ba4slr5jdvzhnqlh5sccf84xy4-top.drv"`,
		},
		{
			"input by outPath",
			`let a = builtins.derivation { name = "dep"; builder = "/bin/true"; system = "x86_64-linux"; };
			 in (builtins.derivation { name = "top"; builder = "/bin/sh"; system = "x86_64-linux"; dep = a.outPath; }).drvPath`,
			`"/nix/store/9za4y6zmcy6kdpg9958ir48xkar20w5a-top.drv"`,
		},
		{
			"multi-output out",
			`let a = builtins.derivation { name = "dep"; builder = "/bin/true"; system = "x86_64-linux"; };
			 in (builtins.derivation { name = "top"; builder = "/bin/sh"; system = "x86_64-linux"; outputs = [ "out" "dev" ]; dep = a.drvPath; }).outPath`,
			`"/nix/store/6pvma8zhy82ibikh4mjrjkmr3aszjfhs-top"`,
		},
		{
			"multi-output dev",
			`let a = builtins.derivation { name = "dep"; builder = "/bin/true"; system = "x86_64-linux"; };
			 in (builtins.derivation { name = "top"; builder = "/bin/sh"; system = "x86_64-linux"; outputs = [ "out" "dev" ]; dep = a.drvPath; }).dev.outPath`,
			`"/nix/store/7ra3lknchpdc1xlkxdsmd0n8pnm9yqid-top-dev"`,
		},
		{
			"fixed-output drvPath",
			`(builtins.derivation { name = "fod"; builder = "/bin/sh"; system = "x86_64-linux";
			   outputHash = "0000000000000000000000000000000000000000000000000000"; outputHashAlgo = "sha256"; outputHashMode = "flat"; }).drvPath`,
			`"/nix/store/c96xwi096xizcwkc2j8n5v7gicy73hr5-fod.drv"`,
		},
		{
			"fixed-output outPath",
			`(builtins.derivation { name = "fod"; builder = "/bin/sh"; system = "x86_64-linux";
			   outputHash = "0000000000000000000000000000000000000000000000000000"; outputHashAlgo = "sha256"; outputHashMode = "flat"; }).outPath`,
			`"/nix/store/59209gx1b93k17d049isa072bka4iv8w-fod"`,
		},
		{
			// A recursive hash is written with the ingestion prefix in the
			// .drv, "r:sha256", which is what makes the derivation path match.
			"recursive fixed-output drvPath",
			`(builtins.derivation { name = "fod"; builder = "x"; system = "x86_64-linux";
			   outputHashMode = "recursive"; outputHashAlgo = "sha256";
			   outputHash = "sha256-UNoyb2teqH26VM7YoOcazyqZ0AlDae045aWc31ZHFdw="; }).drvPath`,
			`"/nix/store/8qv0zjjg8jksrjvdsr3nj3chjwg9r4jp-fod.drv"`,
		},
		{
			"recursive fixed-output outPath",
			`(builtins.derivation { name = "fod"; builder = "x"; system = "x86_64-linux";
			   outputHashMode = "recursive"; outputHashAlgo = "sha256";
			   outputHash = "sha256-UNoyb2teqH26VM7YoOcazyqZ0AlDae045aWc31ZHFdw="; }).outPath`,
			`"/nix/store/adxqb5x4k92giw80xfiyfz154v1w4i1i-fod"`,
		},
		{
			"structured-attrs drvPath",
			`(builtins.derivation { name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
			   __structuredAttrs = true; foo = "bar"; num = 3; list = [ 1 2 ]; nested = { x = 1; }; }).drvPath`,
			`"/nix/store/hr0lqcjinhk4jwyy85yilgaa44fw4z93-sa.drv"`,
		},
		{
			"structured-attrs outPath",
			`(builtins.derivation { name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
			   __structuredAttrs = true; foo = "bar"; num = 3; list = [ 1 2 ]; nested = { x = 1; }; }).outPath`,
			`"/nix/store/prp16lg0jg0rz4zfkhrshg4acnffw82z-sa"`,
		},
		{
			// With more than one input the hash-modulo serialisation names each
			// input by its hash and orders by that hash, not by derivation path
			// (Nix's maskInputDrvs). A fixed-output input's hash sorts unlike
			// its path, so reordering changes the output path.
			"multi-input hash ordering",
			`let
			   reg = builtins.derivation { name = "reg"; builder = "/bin/sh"; system = "x86_64-linux"; };
			   fod = builtins.derivation { name = "fod"; builder = "/bin/sh"; system = "x86_64-linux";
			     outputHash = "0000000000000000000000000000000000000000000000000000"; outputHashAlgo = "sha256"; outputHashMode = "flat"; };
			 in (builtins.derivation { name = "top"; builder = "/bin/sh"; system = "x86_64-linux"; x = reg; y = fod; }).outPath`,
			`"/nix/store/wm20nsm23i56rs6gw15df1589lii6qj7-top"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := evalPrint(t, test.src)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestDerivationJSON(t *testing.T) {
	val, err := EvalString(`let a = builtins.derivation { name = "dep"; builder = "/bin/true"; system = "x86_64-linux"; };
		in builtins.derivation { name = "top"; builder = "/bin/sh"; system = "x86_64-linux"; outputs = [ "out" "dev" ]; args = [ "-c" "echo hello\nworld" ]; dep = a.drvPath; }`)
	if err != nil {
		t.Fatal(err)
	}
	d := DerivationOf(val)
	if d == nil {
		t.Fatal("value is not a derivation")
	}
	if want := "/nix/store/qzikiiliib9fs3y3l08j5wbxnf3pw1x4-top.drv"; d.DrvPath() != want {
		t.Errorf("drvPath = %s, want %s", d.DrvPath(), want)
	}
	got, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"args":["-c","echo hello\nworld"],"builder":"/bin/sh","env":{"builder":"/bin/sh","dep":"/nix/store/v9ak4484q7m2q36wrjh2rvpks78h1156-dep.drv","dev":"/nix/store/lmdhqcfyd6n20ghz42bkjb9zhwkndww8-top-dev","name":"top","out":"/nix/store/w2hnnycf7gbizkn40kbg7qpnx7fmsyv8-top","outputs":"out dev","system":"x86_64-linux"},"inputs":{"drvs":{"v9ak4484q7m2q36wrjh2rvpks78h1156-dep.drv":{"dynamicOutputs":{},"outputs":["out"]}},"srcs":["v9ak4484q7m2q36wrjh2rvpks78h1156-dep.drv"]},"name":"top","outputs":{"dev":{"path":"lmdhqcfyd6n20ghz42bkjb9zhwkndww8-top-dev"},"out":{"path":"w2hnnycf7gbizkn40kbg7qpnx7fmsyv8-top"}},"system":"x86_64-linux","version":4}`
	if string(got) != want {
		t.Errorf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestFixedOutputJSON covers how a fixed output is described in derivation
// JSON: Nix names the method by its content-address name, so a recursive hash
// is a "nar" rather than the outputHashMode spelling "recursive".
func TestFixedOutputJSON(t *testing.T) {
	val, err := EvalString(`builtins.derivation { name = "fod"; builder = "x"; system = "x86_64-linux";
		outputHashMode = "recursive"; outputHashAlgo = "sha256";
		outputHash = "sha256-UNoyb2teqH26VM7YoOcazyqZ0AlDae045aWc31ZHFdw="; }`)
	if err != nil {
		t.Fatal(err)
	}
	d := DerivationOf(val)
	if d == nil {
		t.Fatal("value is not a derivation")
	}
	got, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"method":"nar"`,
		`"hash":"sha256-UNoyb2teqH26VM7YoOcazyqZ0AlDae045aWc31ZHFdw="`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("JSON %s missing %s", got, want)
		}
	}
}

func TestDerivationStrict(t *testing.T) {
	got, err := evalPrint(t, `builtins.derivationStrict { name = "foo"; builder = "/bin/sh"; system = "x86_64-linux"; }`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{ drvPath = "/nix/store/9d794zbx10n43fa6zaqf33pfymdrm4r2-foo.drv"; out = "/nix/store/xpcvxsx5sw4rbq666blz6sxqlmsqphmr-foo"; }`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDerivationBuiltins(t *testing.T) {
	for _, test := range [][2]string{
		{`builtins.placeholder "out"`, `"/1rz4g4znpzjwh1xymhjpm42vipw92pr73vdgl6xs1hycac8kf2n9"`},
		{`builtins.toFile "foo.txt" "hello"`, `"/nix/store/m0iyz1rzcgxw68h6skk8b9sg0p0napwc-foo.txt"`},
		{`builtins.hashString "sha256" "hello"`, `"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"`},
		{`builtins.baseNameOf "a/b/c/"`, `"c"`},
		{`builtins.dirOf "a/b/c"`, `"a/b"`},
		{`builtins.dirOf "a"`, `"."`},
		{`builtins.dirOf "/a"`, `"/"`},
		{`builtins.unsafeDiscardStringContext (builtins.derivation { name = "foo"; builder = "/bin/sh"; system = "x86_64-linux"; }).outPath`,
			`"/nix/store/xpcvxsx5sw4rbq666blz6sxqlmsqphmr-foo"`},
		{`builtins.hasContext (builtins.derivation { name = "foo"; builder = "/bin/sh"; system = "x86_64-linux"; }).outPath`, `true`},
		{`builtins.hasContext "plain"`, `false`},
	} {
		got, err := evalPrint(t, test[0])
		if err != nil {
			t.Errorf("%s: %v", test[0], err)
			continue
		}
		if got != test[1] {
			t.Errorf("%s: got %s, want %s", test[0], got, test[1])
		}
	}
}

// TestDerivationSelfReference pins that a derivation's self-referential
// structure — out refers back to the derivation — does not make strict
// printing or deepSeq recurse forever.
func TestDerivationSelfReference(t *testing.T) {
	src := `builtins.derivation { name = "foo"; builder = "/bin/sh"; system = "x86_64-linux"; }`
	if got, err := evalPrint(t, `builtins.deepSeq (`+src+`) 1`); err != nil || got != "1" {
		t.Errorf("deepSeq = %q, %v", got, err)
	}
	val, err := EvalString(src)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Print(val, -1); err != nil || s == "" {
		t.Errorf("strict print = %q, %v", s, err)
	}
}

// TestStructuredAttrs covers __structuredAttrs. When it is set, every ordinary
// attribute is folded into a single __json environment variable instead of
// becoming one of its own, while the attributes that name the derivation's own
// fields are still read out and references are still collected as inputs. The
// store paths and __json strings below were produced by Nix.
func TestStructuredAttrs(t *testing.T) {
	build := func(t *testing.T, src string) *Derivation {
		t.Helper()
		val, err := EvalString(src)
		if err != nil {
			t.Fatal(err)
		}
		d := DerivationOf(val)
		if d == nil {
			t.Fatal("value is not a derivation")
		}
		return d
	}

	t.Run("folds attributes into __json", func(t *testing.T) {
		d := build(t, `builtins.derivation {
			name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
			__structuredAttrs = true;
			foo = "bar"; num = 3; list = [ 1 2 ]; nested = { x = 1; };
		}`)
		if want := "/nix/store/hr0lqcjinhk4jwyy85yilgaa44fw4z93-sa.drv"; d.DrvPath() != want {
			t.Errorf("drvPath = %s, want %s", d.DrvPath(), want)
		}
		want := `{"builder":"/bin/sh","foo":"bar","list":[1,2],"name":"sa","nested":{"x":1},"num":3,"system":"x86_64-linux"}`
		if got := d.drv.Env["__json"]; got != want {
			t.Errorf("__json = %s, want %s", got, want)
		}
		if len(d.drv.Env) != 2 {
			t.Errorf("env = %v, want only __json and out", d.drv.Env)
		}
		if _, ok := d.drv.Env["foo"]; ok {
			t.Error("attribute foo leaked into the environment")
		}
		if d.drv.Builder != "/bin/sh" || d.drv.System != "x86_64-linux" {
			t.Errorf("builder/system = %q/%q, want /bin/sh/x86_64-linux", d.drv.Builder, d.drv.System)
		}
	})

	t.Run("reads outputs from the attributes", func(t *testing.T) {
		d := build(t, `builtins.derivation {
			name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
			__structuredAttrs = true; outputs = [ "out" "dev" ];
		}`)
		if want := "/nix/store/r557nlfsyjs0kawngz5pmkl4m5n6p7df-sa.drv"; d.DrvPath() != want {
			t.Errorf("drvPath = %s, want %s", d.DrvPath(), want)
		}
		if _, ok := d.drv.Outputs["dev"]; !ok {
			t.Error("dev output missing")
		}
		want := `{"builder":"/bin/sh","name":"sa","outputs":["out","dev"],"system":"x86_64-linux"}`
		if got := d.drv.Env["__json"]; got != want {
			t.Errorf("__json = %s, want %s", got, want)
		}
	})

	t.Run("ignore nulls drops null attributes", func(t *testing.T) {
		d := build(t, `builtins.derivation {
			name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
			__structuredAttrs = true; __ignoreNulls = true;
			keep = "yes"; drop = null;
		}`)
		if want := "/nix/store/1zvxkrrwnji67ifjbcaiw2r0wkq5cy9d-sa.drv"; d.DrvPath() != want {
			t.Errorf("drvPath = %s, want %s", d.DrvPath(), want)
		}
		want := `{"builder":"/bin/sh","keep":"yes","name":"sa","system":"x86_64-linux"}`
		if got := d.drv.Env["__json"]; got != want {
			t.Errorf("__json = %s, want %s", got, want)
		}
	})

	t.Run("collects references as inputs", func(t *testing.T) {
		d := build(t, `let dep = builtins.derivation { name = "dep"; builder = "/bin/true"; system = "x86_64-linux"; };
			in builtins.derivation {
				name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
				__structuredAttrs = true;
				text = "ref ${dep}"; direct = dep;
			}`)
		if want := "/nix/store/3d3zn8l3p0a0xc912kv34dx4qagn2plz-sa.drv"; d.DrvPath() != want {
			t.Errorf("drvPath = %s, want %s", d.DrvPath(), want)
		}
		const dep = "/nix/store/v9ak4484q7m2q36wrjh2rvpks78h1156-dep.drv"
		if got := d.drv.InputDrvs[dep]; len(got) != 1 || got[0] != "out" {
			t.Errorf("input derivations = %v, want %s -> [out]", d.drv.InputDrvs, dep)
		}
		want := `{"builder":"/bin/sh","direct":"/nix/store/q29279kjjvm86j19gld6rsclr0khshzm-dep","name":"sa","system":"x86_64-linux","text":"ref /nix/store/q29279kjjvm86j19gld6rsclr0khshzm-dep"}`
		if got := d.drv.Env["__json"]; got != want {
			t.Errorf("__json = %s, want %s", got, want)
		}
	})

	t.Run("false is not structured", func(t *testing.T) {
		d := build(t, `builtins.derivation {
			name = "sa"; builder = "/bin/sh"; system = "x86_64-linux";
			__structuredAttrs = false; foo = "bar";
		}`)
		if want := "/nix/store/4bq2xmck19614whad6s5fyl6ddhl4ncr-sa.drv"; d.DrvPath() != want {
			t.Errorf("drvPath = %s, want %s", d.DrvPath(), want)
		}
		if _, ok := d.drv.Env["__json"]; ok {
			t.Error("__json set without __structuredAttrs")
		}
		if got := d.drv.Env["foo"]; got != "bar" {
			t.Errorf("env.foo = %q, want %q", got, "bar")
		}
		// Nix still writes __structuredAttrs to the environment when it is
		// false, coerced to the empty string.
		if got := d.drv.Env["__structuredAttrs"]; got != "" {
			t.Errorf("env.__structuredAttrs = %q, want %q", got, "")
		}
	})

	t.Run("non-bool __structuredAttrs is rejected", func(t *testing.T) {
		_, err := EvalString(`builtins.derivationStrict {
			name = "sa"; builder = "/bin/sh"; system = "x86_64-linux"; __structuredAttrs = 1;
		}`)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "Boolean") {
			t.Errorf("error = %v, want a Boolean type error", err)
		}
	})

	t.Run("non-bool __ignoreNulls is rejected", func(t *testing.T) {
		_, err := EvalString(`builtins.derivationStrict {
			name = "sa"; builder = "/bin/sh"; system = "x86_64-linux"; __ignoreNulls = 1;
		}`)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "Boolean") {
			t.Errorf("error = %v, want a Boolean type error", err)
		}
	})
}

// TestStructuredPathContext covers a path literal inside structured attributes:
// it must be copied to the store, and the reference to that store path must be
// collected so the path becomes an input of the derivation.
func TestStructuredPathContext(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := nixhash.StorePath(p, "")

	val, err := EvalString(`builtins.derivation { name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; __structuredAttrs = true; src = ` + p + `; }`)
	if err != nil {
		t.Fatal(err)
	}
	d := DerivationOf(val)
	if d == nil {
		t.Fatal("value is not a derivation")
	}
	if js := d.drv.Env["__json"]; !strings.Contains(js, `"src":"`+sp+`"`) {
		t.Errorf("__json = %s, want src %s", js, sp)
	}
	inInputs := false
	for _, src := range d.drv.InputSrcs {
		if src == sp {
			inInputs = true
		}
	}
	if !inInputs {
		t.Errorf("drv inputs %v do not include %s", d.drv.InputSrcs, sp)
	}
}

// TestDerivationLaziness pins that builtins.derivation, like derivation.nix,
// only builds the derivation when an output path is read. Evaluating the result
// set or reading a plain attribute must not force the attributes that go into
// the derivation, while reading an output path must.
func TestDerivationLaziness(t *testing.T) {
	const thrown = `builtins.derivation { name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; fixupPhase = throw "boom"; }`

	for _, test := range [][2]string{
		{`builtins.isAttrs (` + thrown + `)`, `true`},
		{`(` + thrown + `).name`, `"x"`},
		{`builtins.length (` + thrown + `).all`, `1`},
		{`builtins.attrNames (` + thrown + `)`,
			`[ "all" "builder" "drvAttrs" "drvPath" "fixupPhase" "name" "out" "outPath" "outputName" "system" "type" ]`},
	} {
		got, err := evalPrint(t, test[0])
		if err != nil {
			t.Errorf("%s: %v", test[0], err)
			continue
		}
		if got != test[1] {
			t.Errorf("%s: got %s, want %s", test[0], got, test[1])
		}
	}

	// Reading an output path does build the derivation, so the throwing
	// attribute is forced and the error surfaces.
	for _, src := range []string{
		`(` + thrown + `).drvPath`,
		`(` + thrown + `).outPath`,
		`(` + thrown + `).out.outPath`,
	} {
		if _, err := EvalString(src); err == nil {
			t.Errorf("%s: expected an error, got nil", src)
		}
	}

	// derivationStrict is a primop and stays eager.
	if _, err := EvalString(`builtins.derivationStrict { name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; fixupPhase = throw "boom"; }`); err == nil {
		t.Error("derivationStrict: expected an error, got nil")
	}
}

// TestDerivationOutputs covers the output names derivation.nix reads straight
// from the `outputs` attribute, and the rejection of an empty set.
func TestDerivationOutputs(t *testing.T) {
	got, err := evalPrint(t, `builtins.attrNames (builtins.derivation {
		name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; outputs = [ "out" "dev" ];
	})`)
	if err != nil {
		t.Fatal(err)
	}
	want := `[ "all" "builder" "dev" "drvAttrs" "drvPath" "name" "out" "outPath" "outputName" "outputs" "system" "type" ]`
	if got != want {
		t.Errorf("attrNames = %s, want %s", got, want)
	}

	got, err = evalPrint(t, `(builtins.derivation {
		name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; outputs = [ "out" "dev" ];
	}).dev.outPath`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"/nix/store/3qb5aqi3wxx9cipz0418a9w1nvqjsx4m-x-dev"`; got != want {
		t.Errorf("dev.outPath = %s, want %s", got, want)
	}

	if _, err := EvalString(`builtins.derivation { name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; outputs = []; }`); err == nil {
		t.Error("empty outputs: expected an error, got nil")
	}
}

// TestMakeOverridableLazyDerivation pins that accessing .override on a
// makeOverridable package does not build the base derivation.
// makeOverridable forces `result // { override = ...; }`, so the base set must
// not force its attributes; the derivation.nix wrapper is what makes that hold.
func TestMakeOverridableLazyDerivation(t *testing.T) {
	got, err := evalPrint(t, `
		let
			makeOverridable = f: origArgs:
				let result = f origArgs;
				in result // { override = newArgs: makeOverridable f (origArgs // newArgs); };
			f = { runtimeShell }: builtins.derivation {
				name = "x"; builder = "/bin/sh"; system = "x86_64-linux"; fixupPhase = runtimeShell;
			};
			base = makeOverridable f { runtimeShell = throw "base"; };
		in (base.override { runtimeShell = "overridden"; }).drvPath`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"/nix/store/kx4gsxgmw028pwsk3qf08vv298r2wa0i-x.drv"`; got != want {
		t.Errorf("drvPath = %s, want %s", got, want)
	}
}
