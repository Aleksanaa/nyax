# Variable resolution: every iteration below resolves names ten scopes up, and
# two more through a `with`, which both evaluators have to search dynamically.
#
# Nix resolves lexical names to a position at parse time and indexes straight
# into the environment; Nyax walks a chain of scopes. This is the workload that
# tells the two apart.
#
# `n` is on its own line so that examples/bench.sh can rewrite it.
let
  n = 700000;
in
let l1 = 1; in let l2 = 2; in let l3 = 3; in let l4 = 4; in let l5 = 5; in
let l6 = 6; in let l7 = 7; in let l8 = 8; in let l9 = 9; in let l10 = 10; in
with { w1 = 100; w2 = 200; };
let
  step = acc: _: acc + l1 + l10 + w1 + w2;
  total = builtins.foldl' step 0 (builtins.genList (i: i) n);
in
"n=${toString n} total=${toString total}"
