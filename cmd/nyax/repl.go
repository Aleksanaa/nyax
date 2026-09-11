// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package main

// The read-evaluate-print loop.
//
// Line editing, history and the keyboard live in lineedit.go; what is here is
// the part that knows about Nix: how an entry is continued over several
// lines, what the colon commands do, and what a name can be completed to.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/alecthomas/kingpin"
	"github.com/aleksanaa/nyax/pkg/eval"
	p "github.com/aleksanaa/nyax/pkg/parser"
)

var replCmd = kingpin.Command("repl", "Read, evaluate and print loop.")

const (
	prompt     = "nix-repl> "
	contPrompt = "          " // as wide as the prompt, so entries line up
)

// repl is one session: the names it has bound, and the files it was told to
// load, which :r reloads.
type repl struct {
	ed      *editor
	out     io.Writer
	session *eval.Session
	files   []string
}

var replMain = register("repl", func() {
	r := &repl{}
	r.reset()
	hist := openHistory(historyPath())
	defer hist.Close()
	r.ed = newEditor(hist, r.completeAt)
	defer r.ed.Close()
	r.out = r.ed.out
	r.run()
})

// reset returns the session to the bindings it started with. A session binds
// into one frame that stays where it is, so a name bound later is visible to a
// thunk made earlier, which is what makes definitions in a REPL behave like a
// `let`.
func (r *repl) reset() {
	r.session = eval.NewSession(eval.DefaultScope)
}

// bind gives a name a value for the rest of the session.
func (r *repl) bind(sym eval.Sym, x *eval.Expression) {
	if !r.session.Bind(sym, x) {
		fmt.Fprintln(r.out, "error: too many variables in this session")
	}
}

func (r *repl) run() {
	var entry []string // the lines of an entry still being continued
	for {
		at := prompt
		if len(entry) > 0 {
			at = contPrompt
		}
		line, err := r.ed.Read(at)
		if errors.Is(err, errInterrupt) {
			entry = nil // ^C abandons the entry, however many lines it is
			continue
		}
		if err != nil {
			// End of input, or a terminal that stopped reading: either way
			// the session is over. The newline leaves the shell prompt on a
			// line of its own, as it would be after :q.
			fmt.Fprintln(r.out)
			return
		}
		if strings.TrimSpace(line) == "" && len(entry) == 0 {
			continue
		}
		entry = append(entry, line)
		src := strings.Join(entry, "\n")
		blank, partial := classify(src)
		if partial {
			continue
		}
		entry = nil
		if blank {
			continue
		}
		if !r.eval(src) {
			return
		}
	}
}

// eval runs one entry, reporting whether the session continues. An error in
// an entry is printed and the session goes on; only :q ends it.
func (r *repl) eval(src string) bool {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, ":") {
		return r.command(src)
	}
	if name, rest, ok := binding(src); ok {
		pr, err := p.ParseString(rest)
		if err != nil {
			r.fail(err)
			return true
		}
		r.bind(eval.Intern(name), eval.Delay(r.session.Scope(), pr))
		return true
	}
	if val, ok := r.value(src); ok {
		r.print(val, 1)
	}
	return true
}

// value parses and evaluates an expression, reporting failures itself.
func (r *repl) value(src string) (eval.NixValue, bool) {
	pr, err := p.ParseString(src)
	if err != nil {
		r.fail(err)
		return eval.NixValue{}, false
	}
	val, err := eval.EvalIn(r.session.Scope(), pr)
	if err != nil {
		r.fail(err)
		return eval.NixValue{}, false
	}
	return val, true
}

// fail reports an error. Parse and evaluation failures already render
// themselves the way Nix does, message first and then a source excerpt; the
// prefix is added to anything else, which is an I/O error from the operating
// system.
func (r *repl) fail(err error) {
	s := err.Error()
	if !strings.HasPrefix(s, "error:") {
		s = "error: " + s
	}
	fmt.Fprintln(r.out, s)
}

// print renders a value. Printing forces it, so it can fail where evaluating
// the outermost value did not.
func (r *repl) print(val eval.NixValue, depth int) {
	s, err := eval.Print(val, depth)
	if err != nil {
		r.fail(err)
		return
	}
	fmt.Fprintln(r.out, s)
}

const replHelp = `  <expr>             Evaluate and print an expression
  <x> = <expr>       Bind an expression to a name
  :a, :add <expr>    Add the attributes of a set to the scope
  :d, :doc <expr>    Show the documentation of a builtin
  :l, :load <path>   Evaluate a file and add its attributes to the scope
  :p, :print <expr>  Evaluate and print an expression in full
  :r, :reload        Reload the files loaded with :l
  :t, :type <expr>   Show the type of an expression
  :q, :quit          Exit
  :?, :help          Show this help

  Tab completes names and attributes, ^R searches the history, and an
  unfinished expression is continued on the next line.`

// command runs a colon command, reporting whether the session continues.
func (r *repl) command(src string) bool {
	name, arg, _ := strings.Cut(src, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case ":q", ":quit":
		return false
	case ":?", ":help":
		fmt.Fprintln(r.out, replHelp)
	case ":t", ":type":
		if val, ok := r.value(arg); ok {
			fmt.Fprintln(r.out, eval.TypeName(val))
		}
	case ":p", ":print":
		if val, ok := r.value(arg); ok {
			r.print(val, -1) // a negative depth never reaches zero
		}
	case ":d", ":doc":
		r.doc(arg)
	case ":a", ":add":
		if val, ok := r.value(arg); ok {
			r.add(val, arg)
		}
	case ":l", ":load":
		if r.load(arg) {
			r.files = append(r.files, arg)
		}
	case ":r", ":reload":
		files := r.files
		r.reset()
		r.files = nil
		for _, f := range files {
			if r.load(f) {
				r.files = append(r.files, f)
			}
		}
	default:
		fmt.Fprintf(r.out, "error: unknown command '%s'; :? for help\n", name)
	}
	return true
}

// add brings the attributes of a set into scope, the way `with` would.
func (r *repl) add(val eval.NixValue, what string) {
	set, ok := val.AsSet()
	if !ok {
		fmt.Fprintf(r.out, "error: '%s' is %s, not a set\n", what, eval.TypeName(val))
		return
	}
	names := set.Keys()
	for _, sym := range names {
		x, _ := set.Get(sym)
		r.bind(sym, x)
	}
	fmt.Fprintf(r.out, "Added %d variables.\n", len(names))
}

// load evaluates a file and adds its attributes, reporting whether it worked.
func (r *repl) load(path string) bool {
	if path == "" {
		fmt.Fprintln(r.out, "error: :load needs a path")
		return false
	}
	pr, err := p.ParseFile(expandPath(path))
	if err != nil {
		r.fail(err)
		return false
	}
	val, err := eval.EvalIn(r.session.Scope(), pr)
	if err != nil {
		r.fail(err)
		return false
	}
	r.add(val, path)
	return true
}

// doc shows what a builtin is for. Only builtins carry documentation; a
// function written in Nix has none to show.
func (r *repl) doc(arg string) {
	val, ok := r.value(arg)
	if !ok {
		return
	}
	op, ok := val.AsPrimop()
	if !ok {
		fmt.Fprintf(r.out, "error: '%s' is %s, which has no documentation\n", arg, eval.TypeName(val))
		return
	}
	fmt.Fprintf(r.out, "Synopsis: builtins.%s%s\n\n  %s\n", op.Sym, strings.Repeat(" e", op.ArgNum), op.Doc)
}

// binding recognises `name = expr`, the REPL's way of defining a name. The
// `=` of an `==` is not one, and neither is the `=` inside a `{ a = 1; }`,
// which does not start with a bare name.
func binding(src string) (name, expr string, ok bool) {
	i := 0
	for i < len(src) && isName(rune(src[i])) {
		i++
	}
	if i == 0 || (src[0] >= '0' && src[0] <= '9') {
		return "", "", false
	}
	name = src[:i]
	rest := strings.TrimLeft(src[i:], " \t")
	if !strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "==") {
		return "", "", false
	}
	return name, rest[1:], true
}

func isName(r rune) bool {
	return r == '_' || r == '-' || r == '\'' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// classify sorts out what has been typed so far.
//
// partial means the entry is an expression the user is in the middle of, so
// the REPL asks for another line instead of reporting a syntax error. Only a
// failure at the end of the input counts: anything else is a real mistake, and
// waiting for more would hide it.
//
// blank means the entry holds no tokens at all, however much whitespace and
// comment it contains. That is not an unfinished expression, and pasting a
// comment should neither hang the prompt nor raise an error.
func classify(src string) (blank, partial bool) {
	pr, err := p.ParseString(src)
	if pr != nil && pr.Last() < 0 {
		return true, false
	}
	if err == nil {
		return false, false
	}
	switch e := err.(type) {
	case *p.LexerError:
		// An unterminated string, comment or bracket.
		return false, strings.Contains(e.Desc, "is not terminated")
	case *p.ParserError:
		return false, atEnd(e)
	case p.ParserErrors:
		return false, len(e) > 0 && atEnd(e[len(e)-1])
	}
	return false, false
}

func atEnd(e *p.ParserError) bool { return strings.Contains(e.Desc, "$end") }

// expandPath resolves a leading ~, which a shell would have done had the path
// not been typed at our own prompt.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

// completeAt proposes completions for the word ending at pos: a command after
// a leading colon, a path after :load, an attribute after a dot, and a name in
// scope otherwise.
//
// Only a command is followed by a space once it is settled. A name is not: in
// Nix what usually comes next is a `.` or a `)`, and a space that has to be
// deleted again is worse than one that has to be typed.
func (r *repl) completeAt(line string, pos int) (start int, options []string, done string) {
	// Completing evaluates whatever has been typed so far, and half an
	// expression can reach corners of the evaluator that a whole one does not.
	// A bug found that way should cost a completion, not the session.
	defer func() {
		if recover() != nil {
			start, options, done = pos, nil, ""
		}
	}()
	head := line[:pos]
	if cmd, rest, ok := strings.Cut(head, " "); ok && strings.HasPrefix(cmd, ":") {
		if cmd == ":l" || cmd == ":load" {
			arg := strings.TrimLeft(rest, " \t")
			return pos - len(arg), completePath(arg), ""
		}
	} else if strings.HasPrefix(head, ":") {
		return 0, matching(replCommands, head), " "
	}
	start = pos
	for start > 0 && (isName(rune(head[start-1])) || head[start-1] == '.') {
		start--
	}
	word := head[start:pos]
	dot := strings.LastIndex(word, ".")
	if dot < 0 {
		return start, matching(r.session.Names(), word), ""
	}
	// `a.b.c` completes against the attributes of `a.b`, which has to be
	// evaluated to find out what they are.
	set, ok := r.set(word[:dot])
	if !ok {
		return pos, nil, ""
	}
	var names []string
	for _, sym := range set.Keys() {
		names = append(names, word[:dot+1]+sym.String())
	}
	return start, matching(names, word), ""
}

// set evaluates a prefix of an attribute path to the set it names. Failures
// are silent: a completion is not the place to report that an expression the
// user is still typing does not evaluate.
func (r *repl) set(src string) (eval.NixSet, bool) {
	pr, err := p.ParseString(src)
	if err != nil {
		return nil, false
	}
	val, err := eval.EvalIn(r.session.Scope(), pr)
	if err != nil {
		return nil, false
	}
	return val.AsSet()
}

var replCommands = []string{
	":add", ":doc", ":help", ":load", ":print", ":quit", ":reload", ":type",
}

func matching(names []string, prefix string) []string {
	var found []string
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

// completePath completes a filename, with a trailing slash on directories so
// that another Tab descends into them. The directory is kept as it was typed,
// since that is the text being completed.
func completePath(prefix string) []string {
	dir, file := filepath.Split(prefix)
	read := expandPath(dir)
	if read == "" {
		read = "."
	}
	entries, err := os.ReadDir(read)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, file) {
			continue
		}
		if file == "" && strings.HasPrefix(name, ".") {
			continue // a bare Tab does not offer dotfiles
		}
		if e.IsDir() {
			name += "/"
		}
		found = append(found, dir+name)
	}
	sort.Strings(found)
	return found
}
