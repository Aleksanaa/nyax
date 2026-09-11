# Lowering the cost of evaluation

Notes on a round of optimization against `examples/bench.sh`, continuing the
work in the git history (thunk flattening, slabs, the static node cache).

## Method

`examples/bench.sh` times whole processes and is noisy at its default three
repeats; a fair A/B interleaves the two binaries and takes the best of nine.
The Go benchmarks in `pkg/eval/bench_test.go` run the same workloads in
process, but their CPU profiles lie: `b.Loop` collects between rounds, and
with `-benchmem` so does `ReadMemStats`, so the profile fills with `gcDrain`
however GOGC is set. The profiles below come from the binary itself
(`nyax --profile cpu`, run under `GOGC=off`), which is what bench.sh times.

Baseline (bench.sh sizes, Nyax vs nix, startup subtracted): Nyax ran 1.5–2.1x
slower than nix on every workload.

## What the profiles said

- **lists**: `newPartial` was 26% of the run. `builtins.bitAnd x 3` gathers
  its whole spine at the ApplyNode, then applied it one argument at a time
  anyway: build a partial primop, force it, take it apart, call. 
- **attrs**: `Intern` was 38% cumulative. `attrNames` turned symbols back
  into strings, and every `getAttr` over the result hashed and looked the
  same string up in the symbol table again.
- **hanoi-calls**: `resolve`/`force` flat, ~40% together — the fixed cost of
  the force loop. Literal operands (`k - 1`, `k == 0`) went through a stack
  thunk, an evaluation frame and the whole resolve switch to read a cached
  number.
- **strings**: `bindArgs` allocated a slice of scopes per call, bypassing the
  scope slab every other scope comes from.
- With GC on, the collector is ~15% of wall time on most workloads
  (GOGC=off A/B); the changes below shrink it indirectly by allocating less.

## Changes

1. **Direct primop calls in `applySpine`** (lambda.go). When the spine holds
   at least as many arguments as the builtin takes, the `nativeCall` is built
   outright — the expression carries on with it in place, no partial primop
   object, no intermediate force. Curried use (more args than the builtin
   takes) still goes through its own expression, since x is mid-force.

2. **Symbols cached on strings** (string.go, builtins_set.go, scope.go).
   `NixString` gained a `sym` field and an `intern` method: the content is
   hashed at most once per string object. `attrNames` seeds it — the strings
   it hands out already know their symbol — so the `attrNames`/`getAttr`/
   `.${name}` pair that attribute-heavy code is made of never re-enters the
   symbol table. All builtins that take a name (`getAttr`, `hasAttr`,
   `catAttrs`, `groupBy`, `removeAttrs`, `listToAttrs`) and `attrSym` now go
   through `intern`. Cost: NixString grows 24→32 bytes.

3. **Literal fast path in `Scope.evalNode`** (scope.go, static.go). A number,
   path or URI operand is read straight out of the node's static cache — no
   thunk, no evaluation frame, no force. The per-type parsers moved out of
   `resolve` into named functions (intLiteral &co.) that both paths share, so
   `resolve` keeps its behaviour and the literal is still computed once.

4. **`bindArgs` uses the scope slab** (lambda.go). A multi-argument call took
   a fresh `make([]Scope, n)` per call; now each scope comes from the slab,
   which amortizes to one allocation per 256 scopes and matches how every
   other scope is allocated. The single-argument special case folded into the
   loop — `newScope` from a slab is the same cost.

5. **The evaluation frame is built inline in `force`** (expr.go). It was
   assembled through the `scope()`, `node()` and `native()` accessors, each
   re-testing a kind the loop had just tested; the loop already knows what an
   unforced expression holds, and this is the hottest code in the evaluator.

## Results

Go benchmarks (`go test ./pkg/eval -run x -bench . -benchmem`, best of 3,
ns/op before → after):

    HanoiCalls   7.20M → 5.83M   (-19%)
    Strings      4.91M → 3.54M   (-28%)
    Lookup       4.04M → 3.48M   (-14%)
    Hanoi        5.31M → 4.66M   (-12%)
    Fix          4.39M → 3.87M   (-12%)
    Attrs        2.58M → 2.37M   (-8%)
    Lists        7.01M → 7.79M   (noise; allocs 80252 → 13816)
    Lazy         7.07M → 3.05M   (noisy both ways)
    Eval (test)    53K →   28K   (-47%, allocs 307 → 108)

bench.sh sizes, old vs new binary, best of 9, GC on (GOGC=off in parens):

    attrs        1.17x (1.20x)     lists        1.18x (1.31x)
    fix          1.13x (1.23x)     lookup       0.96x (1.06x)
    hanoi-calls  1.22x (1.21x)     strings      0.98x (1.21x)
    hanoi        1.06x (1.03x)     lazy         1.01x (1.03x)

Against nix (bench.sh, GC on): hanoi-calls went 1.5x → 1.2x slower, the rest
are 1.5–2.0x. `go test ./...` passes; both binaries agree on every workload's
output, which bench.sh checks before timing.

The GC-on and GC-off columns differ by 10–20% on the allocation-heavy
workloads: what is left of the gap to nix is now substantially the collector.

## Not done / next

- The GC itself. A batch evaluator can trade memory for the collector's share
  (a higher GOGC or a GOMEMLIMIT ballast in cmd/nyax, respecting the user's
  own settings); rejected for now as a runtime-policy change rather than an
  evaluator improvement.
- `strings` still allocates ~6 objects per fold step: two NixStrings, two
  nativeCalls, the Builder's buffer, FormatInt's string. A nativeCall slab is
  the obvious next step; merging the Builder buffer and the NixString into
  one allocation is possible but wants unsafe.
- `lookup`: names under a `with` (static hops < 0) walk the whole scope chain
  on every evaluation; Nix flattens `with` sets at entry instead.
- `lazy`: the genList index block and the per-element thunks are the live
  heap; shrinking Expression below 32 bytes would need giving up `num`
  (int64 payloads) or the blame byte.
- `hanoi`: `memclrNoHeapPointers` ~20% under GOGC=off — fresh slab blocks and
  Concat copies; a slab free-list would reintroduce retention questions.
