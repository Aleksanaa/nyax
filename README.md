# go-nix

## Intro

Experimental project; performance still lagging behind official Nix (expect 1.6~2.2x). We are seeking to
add parallel evaluation but still experimenting.

What we have implemented (Expect bugs)

**Language and evaluation**

- [x] Lexer and parser for the Nix language (`pkg/parser`)
- [x] Lazy evaluator with memoised thunks and statically resolved scopes
- [x] `let`, `rec`, `with`, `inherit`, destructuring arguments with defaults and
      `...`, `@`-patterns, `or`, `?`, `//`, `seq`, `deepSeq`, interpolation and
      indented strings
- [x] Paths and search paths (`NIX_PATH`, `<nixpkgs>`), `import`/`scopedImport`
- [x] Nix-style errors with backtraces

**Builtins**

- [x] Arithmetic, booleans, comparisons, `typeOf` and the `is*` predicates
- [x] Lists, sets, strings and regex (`match`, `split`, `replaceStrings`)
- [x] Versions, hashing (`hashString`, `hashFile`, `convertHash`) and string
      context (`getContext`, `appendContext`, `unsafeDiscardStringContext`)
- [x] `toJSON`, `fromJSON`, `fromTOML`
- [x] `derivation`, `derivationStrict`, `placeholder`
- [x] `readFile`, `readDir`, `path`, `filterSource`, `storePath`, `toFile`,
      `pathExists`, `getEnv`, `currentSystem`, `genericClosure`, and more
- [ ] Fetchers and flakes (`fetchurl`, `fetchTarball`, `fetchGit`, `fetchTree`,
      `getFlake`, `builtins.nixPath`, …) are recognised but throw
- [ ] `builtins.toXML`

**Derivations and store**

- [x] Input-addressed derivations and fixed-output derivations (`flat`,
      `recursive`, `git`)
- [x] `__structuredAttrs`, string context and references
- [x] Nix-compatible store-path and derivation hashing, and
      `nix derivation show`-compatible JSON
- [x] `lib.evalModules`, enough to evaluate full NixOS systems and ISOs
- [ ] Content-addressed derivations
- [ ] Writing to the store (a `pkg/daemon` client exists but is not wired up)

**Parity and tooling**

- [x] Hash parity with official Nix across nixpkgs — `hello`, `stdenv`,
      `neovim`, `emacs`, `ffmpeg`, `llvm`, the GNOME graphical ISO, and others
      all produce the same derivation paths
- [x] CLI: `gon eval`, `gon parse`, `gon hash`, `gon derivation`, `gon repl`
- [ ] Parallel evaluation

## Acknowledgement

1. I initially wanted to implement another Nix implementation, on top of Orivej
   Desh's go-nix. At that time I implemented a somewhat working evaluator and
   ~50 builtin functions (that are not related to derivation and filesystems)
   myself, but later, for some reason, I forgot about it. Now I want to try to
   pick it up and use it as an experiment field to exercise some ideas about Nix
   from the community. I decided to use AI because it has much more patience
   with random behaviour and also more experience with optimizations. In this
   project, Claude Opus 5 and DeepSeek v4.1 Flash are mainly used. I
   intentionally left the AI-written comments in the code because this may
   prevent them from breaking the project in undesired ways. **For people who
   believe that AI-assisted programming is AI slop, yes this is AI slop and you
   should generally avoid it.**
2. This project has reached a state where evaluation of a GNOME ISO from
   nixpkgs succeeds. We (well, mainly I and the blue fat fish) fixed many bugs
   (or, to put it another way, also emulated a lot of bugs) along the way, to
   make this possible.
3. Great thanks to the Nix development team for carrying such an excellent
   project. The Nix code is heavily referenced and copied (adapted to Go, of
   course), so the acknowledgement is a necessity.
4. Great thanks to Orivej for writing this project. This helped me learn parsers
   and interpreters two years ago, and it is still a good place to do some
   random stuff.

## License

go-nix is licensed under the GNU Lesser General Public License, version 2.1 or
later. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
