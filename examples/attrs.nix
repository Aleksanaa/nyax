# Attribute sets: building them, merging them, and looking names up.
#
# This is the shape most Nix code has, and the one where the two evaluators
# differ most in representation — Nix keeps a set as a sorted array of
# name/value pairs, Nyax keeps it as a Go map.
#
# `n` is on its own line so that examples/bench.sh can rewrite it.
let
  n = 50000;

  entry = i: { name = "k${toString i}"; value = i; };

  a = builtins.listToAttrs (builtins.genList entry n);
  # Overlapping halfway, so the merge has both new and shadowed names.
  b = builtins.listToAttrs (builtins.genList (i: entry (i + n / 2)) n);
  merged = a // b;

  names = builtins.attrNames merged;
  total = builtins.foldl' (acc: name: acc + builtins.getAttr name merged) 0 names;
in
"n=${toString n} attrs=${toString (builtins.length names)} total=${toString total}"
