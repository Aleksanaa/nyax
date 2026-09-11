package eval

// Builtins that are recognised so that the `inherit (builtins) …` blocks in
// nixpkgs resolve, but that Nyax does not yet implement: the flakes and the
// fetchers, which do nothing but fail loudly when they are actually called.

func unimplemented(name string) func(*worker, ...*Expression) NixValue {
	return func(w *worker, args ...*Expression) NixValue {
		w.throwf(ErrEval, "builtins.%s is not implemented", name)
		return Null
	}
}
