# Towers of Hanoi, written to evaluate identically under `nix` and under `nyax`.
#
#   nix-instantiate --eval examples/hanoi.nix
#   nyax eval -f examples/hanoi.nix
#
# Exercises list building and set construction: solving n disks builds a list
# of 2^n - 1 moves, so the cost doubles with every disk.
#
# `n` is on its own line so that examples/bench.sh can rewrite it.
let
  n = 17;

  # Move k disks from peg `from` to peg `to`, using `via` as the spare.
  # Returns the moves in order, each one { disk, from, to }.
  solve = k: from: to: via:
    if k == 0 then
      [ ]
    else
      solve (k - 1) from via to
      ++ [ { disk = k; inherit from to; } ]
      ++ solve (k - 1) via to from;

  moves = solve n "a" "c" "b";

  # Forcing every move keeps the evaluator from leaving the list as thunks,
  # and the sum stays far below the integer range for any n worth timing.
  weight = builtins.foldl' (acc: move: acc + move.disk) 0 moves;

  # The first and last move of a correct solution are always of disk 1, and
  # every peg name survives the recursion, so this catches a wrong answer.
  first = builtins.head moves;
  last = builtins.elemAt moves (builtins.length moves - 1);
in
"n=${toString n}"
+ " moves=${toString (builtins.length moves)}"
+ " weight=${toString weight}"
+ " first=${first.from}${first.to}"
+ " last=${last.from}${last.to}"
