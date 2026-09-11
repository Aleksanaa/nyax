// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package main

// A line editor with the bindings a readline user reaches for.
//
// golang.org/x/term draws the line and already handles the arrow keys, the
// emacs motion bindings, ^D, ^L, ^T, history recall and bracketed paste. What
// it leaves out is what this file adds: completion, reverse search, a kill
// ring and the meta-key word bindings.
//
// Those extras go through the one hook x/term offers, AutoCompleteCallback,
// which is called for every key it does not handle itself. Two kinds of key
// never reach it, so keyReader rewrites them on their way in:
//
//   - ^C, which x/term reports as io.EOF just like ^D, so an unmodified
//     session would exit on it instead of abandoning the line;
//   - Alt-<key>, and the kills x/term performs itself, which it would consume
//     before the hook could see them.
//
// Both become runes from the Unicode private use area, which no keyboard
// produces and which the callback below dispatches on.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

// errInterrupt is returned by editor.Read for a line abandoned with ^C.
var errInterrupt = errors.New("interrupted")

// Keys smuggled past x/term as private use runes. metaKey covers Alt-<r>,
// ctrlKey the control bytes x/term would otherwise act on itself, and
// searchKey every key typed while reverse search is up, since search owns the
// whole keyboard while it is running.
const (
	metaBase   = 0xE000
	ctrlBase   = 0xE100
	searchBase = 0xE200
)

func metaKey(r rune) rune   { return metaBase + r }
func ctrlKey(b byte) rune   { return ctrlBase + rune(b) }
func searchKey(r rune) rune { return searchBase + r }

// Control bytes taken over from x/term. It implements ^K, ^U and ^W itself
// but cannot tell us what they removed, and a kill that does not fill the
// kill ring is a kill ^Y cannot put back.
const (
	ctrlC = 0x03
	ctrlE = 0x05
	ctrlK = 0x0b
	ctrlR = 0x12
	ctrlU = 0x15
	ctrlW = 0x17
	ctrlY = 0x19
	esc   = 0x1b
	del   = 0x7f
	tab   = 0x09
	cr    = 0x0d
)

var stolen = map[byte]bool{ctrlK: true, ctrlU: true, ctrlW: true}

// keyReader sits between the terminal and x/term's key decoder.
type keyReader struct {
	e   *editor
	r   io.Reader
	buf []byte // translated input not yet handed over
	esc bool   // a lone ESC held back until the rest of its sequence arrives

	interrupted bool // ^C was seen; the line about to be returned is void
}

// Read waits for the next keys. Waiting is the one moment the screen is
// settled, which is where a completion menu goes up; the key that ends the
// wait takes it down again, whatever that key turns out to do.
func (k *keyReader) Read(p []byte) (int, error) {
	for len(k.buf) == 0 {
		var in [256]byte
		k.e.showMenu()
		n, err := k.r.Read(in[:])
		k.e.hideMenu()
		if n > 0 {
			k.translate(in[:n])
		}
		if len(k.buf) == 0 && err != nil {
			return 0, err
		}
	}
	n := copy(p, k.buf)
	k.buf = k.buf[n:]
	return n, nil
}

// press feeds a key back as if it had just been typed. Reverse search uses it
// both to press Enter on the user's behalf once a match is accepted, and to
// return a key that ended the search so that it acts on the line it found.
func (k *keyReader) press(b byte) { k.translate([]byte{b}) }

func (k *keyReader) emit(b ...byte) { k.buf = append(k.buf, b...) }

func (k *keyReader) emitRune(r rune) {
	var b [4]byte
	k.buf = append(k.buf, b[:utf8.EncodeRune(b[:], r)]...)
}

func (k *keyReader) translate(in []byte) {
	if k.esc {
		in = append([]byte{esc}, in...)
		k.esc = false
	}
	for i := 0; i < len(in); {
		b := in[i]
		switch {
		case b == esc && i+1 == len(in):
			// The rest of the sequence has not arrived yet. A lone ESC does
			// nothing on its own, so there is nothing to lose by waiting.
			k.esc = true
			i++
		case b == esc && (in[i+1] == '[' || in[i+1] == 'O'):
			// A cursor or function key, which x/term decodes itself. During a
			// search, though, moving the cursor ends the search, as it does
			// in readline.
			if k.e.searching {
				k.emitRune(searchKey(esc))
				i += 2
				continue
			}
			k.emit(b)
			i++
		case b == esc:
			k.emitRune(metaKey(rune(in[i+1])))
			i += 2
		case k.e.searching && (b < 0x20 || b == del):
			k.emitRune(searchKey(rune(b)))
			i++
		case b == ctrlC:
			// x/term cannot report ^C apart from ^D, which would end the
			// session. Instead the line is marked and submitted, leaving the
			// abandoned text on screen with a ^C after it and the next prompt
			// below, which is how readline leaves it. The flag tells Read to
			// throw the line away.
			k.interrupted = true
			k.emit(ctrlE, '^', 'C', cr)
			i++
		case stolen[b]:
			k.emitRune(ctrlKey(b))
			i++
		default:
			k.emit(b)
			i++
		}
	}
}

// completer proposes replacements for the word ending at pos. It returns the
// offset the word starts at, the candidates, which include that word as their
// common prefix, and what to add once one of them has been settled on, such
// as the space that separates a command from its argument.
type completer func(line string, pos int) (start int, options []string, done string)

// editor reads lines from the terminal. When stdin is not a terminal it falls
// back to reading them plainly, so that a piped script still works.
type editor struct {
	t     *term.Terminal
	in    *keyReader
	out   io.Writer // where the REPL prints, which is the terminal itself
	tty   io.Writer // the screen, for sequences x/term must not know about
	fd    int
	state *term.State
	hist  *history

	complete completer
	kill     string // the last killed text, for ^Y

	searching bool
	search    searchState
	menu      menu
	prompt    string
}

func newEditor(hist *history, complete completer) *editor {
	e := &editor{
		out: os.Stdout, tty: os.Stdout, fd: int(os.Stdin.Fd()),
		hist: hist, complete: complete,
	}
	if !term.IsTerminal(e.fd) {
		return e
	}
	state, err := term.MakeRaw(e.fd)
	if err != nil {
		return e // no raw mode: read lines plainly rather than garble them
	}
	e.state = state
	e.in = &keyReader{e: e, r: os.Stdin}
	e.t = term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{e.in, os.Stdout}, "")
	e.t.History = termHistory{e}
	e.t.AutoCompleteCallback = e.onKey
	e.out = e.t
	// A terminal that does not know its own size keeps x/term's default of
	// 80 columns; taking a zero from it would wrap the line at every column.
	if w, h, err := term.GetSize(e.fd); err == nil && w > 0 && h > 0 {
		e.t.SetSize(w, h)
	}
	return e
}

// Close restores the terminal. It must run before the process exits, or the
// shell is left in raw mode.
func (e *editor) Close() {
	if e.state != nil {
		term.Restore(e.fd, e.state)
		e.state = nil
	}
}

// Read prompts for one line. It returns errInterrupt if the line was
// abandoned with ^C and io.EOF at the end of the input.
func (e *editor) Read(prompt string) (string, error) {
	if e.t == nil {
		return e.readPlain(prompt)
	}
	e.prompt = prompt
	e.t.SetPrompt(prompt)
	line, err := e.t.ReadLine()
	if e.in.interrupted {
		e.in.interrupted = false
		return "", errInterrupt
	}
	return line, err
}

func (e *editor) readPlain(prompt string) (string, error) {
	fmt.Fprint(e.out, prompt)
	var b []byte
	var one [1]byte
	for {
		n, err := os.Stdin.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return string(b), nil
			}
			b = append(b, one[0])
			continue
		}
		if err != nil {
			if len(b) > 0 {
				return string(b), nil
			}
			return "", err
		}
	}
}

// width and height are the terminal's, used to lay completions out in columns
// and to keep the list from being longer than the screen. They are read afresh
// rather than remembered, so that a resized window is followed.
func (e *editor) width() int {
	if w, _, err := term.GetSize(e.fd); err == nil && w > 20 {
		return w
	}
	return 80
}

func (e *editor) height() int {
	if _, h, err := term.GetSize(e.fd); err == nil && h > 2 {
		return h
	}
	return 24
}

// onKey handles the keys x/term does not. It returns the line to replace the
// current one with; ok is false to let x/term deal with the key after all,
// which is the case for ordinary typing.
func (e *editor) onKey(line string, pos int, key rune) (string, int, bool) {
	if e.searching {
		return e.onSearchKey(line, pos, key)
	}
	switch key {
	case tab:
		return e.completeAt(line, pos)
	case ctrlR:
		return e.startSearch(line, pos)
	case ctrlY:
		return line[:pos] + e.kill + line[pos:], pos + len(e.kill), true
	case ctrlKey(ctrlK):
		e.kill = line[pos:]
		return line[:pos], pos, true
	case ctrlKey(ctrlU):
		e.kill = line[:pos]
		return line[pos:], 0, true
	case ctrlKey(ctrlW), metaKey(del), metaKey(0x08):
		start := wordStart(line, pos)
		e.kill = line[start:pos]
		return line[:start] + line[pos:], start, true
	case metaKey('b'), metaKey('B'):
		return line, wordStart(line, pos), true
	case metaKey('f'), metaKey('F'):
		return line, wordEnd(line, pos), true
	case metaKey('d'), metaKey('D'):
		end := wordEnd(line, pos)
		e.kill = line[pos:end]
		return line[:pos] + line[end:], pos, true
	}
	if key >= metaBase {
		return line, pos, true // an unbound meta key: swallow it, do not type it
	}
	return line, pos, false
}

// completeAt replaces the word before the cursor. A single candidate is
// inserted outright; several are extended as far as they agree and then
// listed, as readline does.
func (e *editor) completeAt(line string, pos int) (string, int, bool) {
	start, options, done := e.complete(line, pos)
	if len(options) == 0 {
		e.raw("\a")
		return line, pos, true
	}
	word := commonPrefix(options)
	if len(options) > 1 && word == line[start:pos] {
		e.queueMenu(options, line, pos)
		return line, pos, true
	}
	if len(options) == 1 {
		word = options[0] + done
	}
	return line[:start] + word + line[pos:], start + len(word), true
}

// menu is a list of completions shown under the input line.
//
// It is drawn once the terminal has gone quiet rather than when the key is
// handled: x/term repaints the line after the callback returns, so only at the
// moment it settles down to wait for the next key is the cursor where the
// geometry below says it is. The next key takes the menu down again, so that a
// second Tab replaces the list rather than adding to it.
type menu struct {
	rows  []string // laid out, waiting to be drawn
	shown int      // rows on screen
	below int      // rows of input between the cursor and the last of them
	col   int      // the column the cursor sits in, to return it to
}

// queueMenu lays the candidates out and records where the input line ends, so
// that showMenu can put them under it.
func (e *editor) queueMenu(options []string, line string, pos int) {
	w := e.width()
	at := utf8.RuneCountInString(e.prompt + line[:pos])
	end := utf8.RuneCountInString(e.prompt + line)
	e.menu = menu{rows: e.layout(options, w), below: end/w - at/w, col: at % w}
}

// showMenu draws the pending list under the input and puts the cursor back.
func (e *editor) showMenu() {
	if len(e.menu.rows) == 0 {
		return
	}
	var b strings.Builder
	down(&b, e.menu.below)
	for _, row := range e.menu.rows {
		b.WriteString("\r\n") // a real newline, so the screen scrolls if it must
		b.WriteString(row)
	}
	// Moving back is relative, which is what makes it survive that scroll.
	up(&b, e.menu.below+len(e.menu.rows))
	b.WriteString("\r")
	right(&b, e.menu.col)
	e.raw(b.String())
	e.menu.shown, e.menu.rows = len(e.menu.rows), nil
}

// hideMenu erases the list, leaving the cursor where it found it.
func (e *editor) hideMenu() {
	if e.menu.shown == 0 {
		return
	}
	var b strings.Builder
	down(&b, e.menu.below+1)
	b.WriteString("\r\x1b[J") // erase from here to the bottom of the screen
	up(&b, e.menu.below+1)
	b.WriteString("\r")
	right(&b, e.menu.col)
	e.raw(b.String())
	e.menu.shown = 0
}

// raw writes control sequences straight to the terminal, behind x/term's
// back. It is safe only while x/term is waiting for a key, with everything it
// has drawn already flushed, and only for sequences that leave the cursor
// where they found it.
func (e *editor) raw(s string) { io.WriteString(e.tty, s) }

func down(b *strings.Builder, n int) {
	if n > 0 {
		fmt.Fprintf(b, "\x1b[%dB", n)
	}
}

func up(b *strings.Builder, n int) {
	if n > 0 {
		fmt.Fprintf(b, "\x1b[%dA", n)
	}
}

func right(b *strings.Builder, n int) {
	if n > 0 {
		fmt.Fprintf(b, "\x1b[%dC", n)
	}
}

// maxMenuRows caps the completion list. Every row costs a row of scroll when
// the prompt is near the bottom of the screen, which is where a prompt spends
// most of its life, so a long list is cut short and counted instead. Narrowing
// it by typing another character is quicker than reading it anyway.
const maxMenuRows = 10

// layout arranges candidates into rows of columns that fit the width. What is
// shown is only the part being completed: the candidates have to carry the
// whole word, since one of them will replace it, but a column of
// `builtins.` repeated forty times says nothing.
func (e *editor) layout(options []string, width int) []string {
	shown := make([]string, len(options))
	cut := len(stem(commonPrefix(options)))
	w := 0
	for i, o := range options {
		shown[i] = o[cut:]
		w = max(w, utf8.RuneCountInString(shown[i]))
	}
	options = shown
	w += 2
	cols := max(width/w, 1)
	total := len(options)
	if room := min(e.height()-2, maxMenuRows); room > 0 && (total+cols-1)/cols > room {
		options = options[:(room-1)*cols]
	}
	var out []string
	for i := 0; i < len(options); i += cols {
		var b strings.Builder
		row := options[i:min(i+cols, len(options))]
		for j, o := range row {
			if j == len(row)-1 {
				b.WriteString(o)
			} else {
				fmt.Fprintf(&b, "%-*s", w, o)
			}
		}
		out = append(out, b.String())
	}
	if n := total - len(options); n > 0 {
		out = append(out, fmt.Sprintf("… %d more", n))
	}
	return out
}

// searchState is an incremental reverse search over the history.
type searchState struct {
	query string
	found string // the entry currently shown
	index int    // where it was found, or -1
	line  string // the line search started from, restored if it is abandoned
	pos   int
}

func (e *editor) startSearch(line string, pos int) (string, int, bool) {
	e.searching = true
	e.search = searchState{line: line, pos: pos, index: -1}
	e.drawSearch()
	return line, pos, true
}

// searchKey handles a key typed during reverse search. Every key arrives
// here, since keyReader hands the whole keyboard over while search is up.
func (e *editor) onSearchKey(line string, pos int, key rune) (string, int, bool) {
	s := &e.search
	switch key {
	case searchKey(ctrlR):
		e.step()
	case searchKey(del), searchKey(0x08):
		if s.query != "" {
			s.query = s.query[:len(s.query)-1]
			s.index = -1
			e.step()
		}
	case searchKey(ctrlC), searchKey(0x07): // ^C, ^G: abandon and restore
		e.endSearch()
		return s.line, s.pos, true
	case searchKey(cr), searchKey('\n'):
		// readline runs the entry it found, so Enter is pressed for us once
		// the search prompt is out of the way.
		e.endSearch()
		e.in.press(cr)
		return e.searchLine()
	case searchKey(esc):
		e.endSearch()
		return e.searchLine()
	default:
		if key >= searchBase {
			// Another control key ends the search and then does its usual
			// job on the line it found, which is what readline does. It is
			// pressed again rather than dispatched here, so that the keys
			// x/term handles itself still reach it.
			e.endSearch()
			e.in.press(byte(key - searchBase))
			return e.searchLine()
		}
		if !unicode.IsPrint(key) {
			return line, pos, true
		}
		s.query += string(key)
		s.index = -1
		e.step()
	}
	if !e.searching {
		return line, pos, true
	}
	e.drawSearch()
	return e.searchLine()
}

// step looks for the next older entry containing the query.
func (e *editor) step() {
	s := &e.search
	for i := s.index + 1; i < e.hist.Len(); i++ {
		if strings.Contains(e.hist.At(i), s.query) {
			s.index, s.found = i, e.hist.At(i)
			return
		}
	}
	fmt.Fprint(e.out, "\a") // no more matches; keep the one we have
}

// searchLine is the line to show: the match, with the cursor on it.
func (e *editor) searchLine() (string, int, bool) {
	s := &e.search
	if s.index < 0 {
		return s.line, s.pos, true
	}
	return s.found, strings.Index(s.found, s.query) + len(s.query), true
}

func (e *editor) drawSearch() {
	s := &e.search
	failed := ""
	if s.index < 0 && s.query != "" {
		failed = "failed "
	}
	e.t.SetPrompt(fmt.Sprintf("(%sreverse-i-search)`%s': ", failed, s.query))
	e.t.Write(nil) // repaint the line under the new prompt
}

func (e *editor) endSearch() {
	e.searching = false
	e.t.SetPrompt(e.prompt)
	e.t.Write(nil)
}

// wordStart is the beginning of the word before pos, skipping any separators
// between the two.
func wordStart(line string, pos int) int {
	i := pos
	for i > 0 && !isWord(rune(line[i-1])) {
		i--
	}
	for i > 0 && isWord(rune(line[i-1])) {
		i--
	}
	return i
}

// wordEnd is the end of the word after pos.
func wordEnd(line string, pos int) int {
	i := pos
	for i < len(line) && !isWord(rune(line[i])) {
		i++
	}
	for i < len(line) && isWord(rune(line[i])) {
		i++
	}
	return i
}

func isWord(r rune) bool {
	return r == '_' || r == '-' || r == '\'' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// stem is the part of a shared prefix that every candidate is agreed to be
// under: everything up to and including its last separator. `builtins.attr`
// stems to `builtins.`, and `examples/li` to `examples/`.
func stem(prefix string) string {
	if i := strings.LastIndexAny(prefix, "./"); i >= 0 {
		return prefix[:i+1]
	}
	return ""
}

func commonPrefix(options []string) string {
	if len(options) == 0 {
		return ""
	}
	prefix := options[0]
	for _, o := range options[1:] {
		for !strings.HasPrefix(o, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

// termHistory is the session history as x/term wants it, minus the line an
// interrupt leaves behind: ^C reaches x/term as a submitted line, so that the
// prompt starts afresh, but an abandoned line is not one to recall.
type termHistory struct{ e *editor }

func (h termHistory) Add(entry string) {
	if !h.e.in.interrupted {
		h.e.hist.Add(entry)
	}
}

func (h termHistory) Len() int { return h.e.hist.Len() }

func (h termHistory) At(i int) string { return h.e.hist.At(i) }

// history is the command history, kept across sessions in a file the way a
// shell keeps one. It implements term.History, which is what makes the up and
// down arrows walk it.
//
// Index 0 is the most recent entry, as that interface requires; the file is
// in the opposite order, oldest first, so that appending to it is enough to
// record a new entry.
type history struct {
	path    string
	entries []string // most recent last
	limit   int
	f       *os.File
}

const historyLimit = 1000

// openHistory loads the history file, creating its directory if need be. A
// history that cannot be read or written is not worth failing over, so
// errors leave the session with an in-memory history instead.
func openHistory(path string) *history {
	h := &history{path: path, limit: historyLimit}
	if path == "" {
		return h
	}
	if b, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if line != "" {
				h.entries = append(h.entries, line)
			}
		}
		if n := len(h.entries) - h.limit; n > 0 {
			h.entries = h.entries[n:]
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return h
	}
	h.f, _ = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	return h
}

func (h *history) Close() {
	if h.f != nil {
		h.f.Close()
		h.f = nil
	}
}

// Add records an entry. Blank lines and a repeat of the entry before are
// dropped, which is what a shell's ignoredups does and what keeps ^C, whose
// line arrives here empty, out of the history.
func (h *history) Add(entry string) {
	entry = strings.TrimRight(entry, " \t")
	if entry == "" || strings.ContainsAny(entry, "\n\r") {
		return
	}
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == entry {
		return
	}
	h.entries = append(h.entries, entry)
	if n := len(h.entries) - h.limit; n > 0 {
		h.entries = h.entries[n:]
	}
	if h.f != nil {
		fmt.Fprintln(h.f, entry)
	}
}

func (h *history) Len() int { return len(h.entries) }

func (h *history) At(i int) string { return h.entries[len(h.entries)-1-i] }

// historyPath is where the history file lives, following the XDG layout Nix
// uses for its own REPL history.
func historyPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "nyax", "repl-history")
}
