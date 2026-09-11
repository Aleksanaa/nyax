package eval

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// The example workloads double as benchmarks, so that a change can be measured
// against the same expressions examples/bench.sh times against Nix. Every file
// takes its size from a single `n` on its own line, which is rewritten here to
// something a benchmark can run many times over — a fraction of the size for
// the linear workloads, a few disks fewer for the two that double with n.
//
//	go test ./pkg/eval -run x -bench . -benchmem
//	go test ./pkg/eval -run x -bench Attrs -cpuprofile cpu.out
var benchSize = map[string]int{
	"attrs":       2500,
	"fix":         3000,
	"hanoi":       13,
	"hanoi-calls": 15,
	"lazy":        150000,
	"lists":       12500,
	"lookup":      35000,
	"strings":     12500,
}

var benchN = regexp.MustCompile(`(?m)^  n = \d+;$`)

// benchSource reads an example and rewrites its `n` to the benchmark size.
func benchSource(tb testing.TB, name string) string {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", name+".nix"))
	if err != nil {
		tb.Skipf("no example: %v", err)
	}
	if !benchN.MatchString(string(data)) {
		tb.Fatalf("%s: no size to rewrite", name)
	}
	src := benchN.ReplaceAllLiteralString(string(data), "  n = "+strconv.Itoa(benchSize[name])+";")
	return src
}

// benchExample times parsing, evaluating and printing one example, which is
// what bench.sh times a whole process doing.
func benchExample(b *testing.B, name string) {
	src := benchSource(b, name)
	b.ReportAllocs()
	for b.Loop() {
		pr, err := p.ParseString(src)
		if err != nil {
			b.Fatal(err)
		}
		val, err := Eval(pr)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := Print(val, -1); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAttrs(b *testing.B)      { benchExample(b, "attrs") }
func BenchmarkFix(b *testing.B)        { benchExample(b, "fix") }
func BenchmarkHanoi(b *testing.B)      { benchExample(b, "hanoi") }
func BenchmarkHanoiCalls(b *testing.B) { benchExample(b, "hanoi-calls") }
func BenchmarkLazy(b *testing.B)       { benchExample(b, "lazy") }
func BenchmarkLists(b *testing.B)      { benchExample(b, "lists") }
func BenchmarkLookup(b *testing.B)     { benchExample(b, "lookup") }
func BenchmarkStrings(b *testing.B)    { benchExample(b, "strings") }
