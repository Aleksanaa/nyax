# Towers of Hanoi counting only the moves it would make, so that the timing
# measures function calls and integer arithmetic rather than list building.
#
#   nix-instantiate --eval examples/hanoi-calls.nix
#   nyax eval -f examples/hanoi-calls.nix
#
# `n` is on its own line so that examples/bench.sh can rewrite it.
let
  n = 20;

  count = k: if k == 0 then 0 else count (k - 1) + 1 + count (k - 1);
in
"n=${toString n} moves=${toString (count n)}"
