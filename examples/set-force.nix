# Forcing every value of a wide attribute set: the shape a package's dependency
# closure has, where forcing one derivation forces all of its inputs.
#
# `deepSeq` forces the whole set, and `listToAttrs` builds it so that every
# value is a thunk until then. The values are strict folds so they are real
# work, not already-evaluated constants.
#
#   nix-instantiate --eval examples/set-force.nix
#   nyax eval -f examples/set-force.nix
#
# `n` is on its own line so that examples/bench.sh can rewrite it.
let
  n = 50000;

  entry = i: {
    name = "k${toString i}";
    value = builtins.foldl' (a: x: a + x) 0 (builtins.genList (j: i + j) 50);
  };

  set = builtins.listToAttrs (builtins.genList entry n);
in builtins.deepSeq set (builtins.length (builtins.attrNames set))
