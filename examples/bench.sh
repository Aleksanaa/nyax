#!/usr/bin/env bash
# Compare Nyax against nix on the example workloads.
#
#   examples/bench.sh                          # every workload, at its own size
#   examples/bench.sh hanoi.nix 10 18          # one workload, swept over n
#   NYAX=./result/bin/nyax examples/bench.sh   # time a binary you already have
#   REPEATS=5 examples/bench.sh                # more runs per measurement
#
# Each workload is a self-contained Nix expression that both evaluators accept,
# and each isolates a different part of the evaluator — see the comment at the
# top of each file. Both evaluators are asked for the same value and their
# answers are compared, so a timing is only reported once the two agree.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(dirname "$here")
repeats=${REPEATS:-3}

nix=${NIX:-$(command -v nix-instantiate || true)}
if [ -z "$nix" ]; then
  echo "nix-instantiate not found" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

nyax=${NYAX:-$work/nyax}
if [ ! -x "$nyax" ]; then
  echo "building nyax..." >&2
  go build -C "$root" -o "$nyax" ./cmd/nyax
fi

# best runs a command `repeats` times and echoes the shortest wall time in ms.
best() {
  local ms start end best=""
  for ((i = 0; i < repeats; i++)); do
    start=${EPOCHREALTIME/./}
    "$@" >/dev/null 2>&1
    end=${EPOCHREALTIME/./}
    ms=$(((end - start) / 1000))
    if [ -z "$best" ] || [ "$ms" -lt "$best" ]; then best=$ms; fi
  done
  echo "$best"
}

# ratio divides the two evaluation times with process startup taken out of
# both, since nix spends more than ten times as long as Nyax getting started and
# would otherwise look better than it is on the shorter workloads.
ratio() {
  awk -v g="$1" -v x="$2" -v bg="$basenyax" -v bx="$basenix" 'BEGIN {
    g -= bg; x -= bx
    if (g < 1) g = 1
    if (x < 1) x = 1
    printf "%.1f", g / x
  }'
}

# compare times one file and prints a row. $1 is the file, $2 the label.
compare() {
  local src=$1 label=$2 a b g x
  a=$("$nyax" eval -f "$src")
  b=$("$nix" --eval "$src")
  if [ "$a" != "$b" ]; then
    printf '%-16s  MISMATCH\n  nyax: %s\n  nix: %s\n' "$label" "$a" "$b"
    return 1
  fi
  g=$(best "$nyax" eval -f "$src")
  x=$(best "$nix" --eval "$src")
  printf '%-16s  %8d  %8d  %7sx\n' "$label" "$g" "$x" "$(ratio "$g" "$x")"
}

header() {
  printf '\n%-16s  %8s  %8s  %8s\n' "$1" nyax nix slower
  printf '%-16s  %8s  %8s  %8s\n' ---------------- -------- -------- --------
}

# The floor each evaluator pays before it evaluates anything.
basenyax=$(best "$nyax" eval 1)
basenix=$(best "$nix" --eval -E 1)

if [ $# -eq 0 ]; then
  header "workload"
  for src in "$here"/*.nix; do
    compare "$src" "$(basename "$src" .nix)"
  done
else
  file=$1
  from=${2:-10}
  to=${3:-18}
  header "$file (n)"
  for ((n = from; n <= to; n++)); do
    src=$work/$n.nix
    sed "s/^  n = .*/  n = $n;/" "$here/$file" > "$src"
    compare "$src" "$n"
  done
fi

printf '\nbest of %d runs, wall time in ms including startup, which is %sms for\n' "$repeats" "$basenyax"
printf 'Nyax and %sms for nix. The ratio has both subtracted.\n' "$basenix"
