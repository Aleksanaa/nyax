package eval

import (
	"fmt"
	"strings"

	p "github.com/aleksanaa/nyax/pkg/parser"
)

// ErrorKind classifies an evaluation failure. It decides both the wording of
// the message and whether builtins.tryEval is allowed to catch it.
type ErrorKind int

const (
	ErrEval ErrorKind = iota // generic evaluation error
	ErrType
	ErrUndefinedVariable
	ErrMissingAttribute
	ErrInfiniteRecursion
	ErrSyntax
	ErrAssertion // assert cond; ...
	ErrThrown    // builtins.throw
	ErrAborted   // builtins.abort
)

// Catchable reports whether builtins.tryEval swallows this kind of error.
// Nix only lets tryEval catch assertions and explicit throws; type errors and
// aborts always propagate to the top.
func (k ErrorKind) Catchable() bool {
	return k == ErrAssertion || k == ErrThrown
}

// Frame is one entry of an evaluation backtrace: a source position plus a
// description of what was being evaluated there.
type Frame struct {
	Pos  *p.LexPosition
	Line string // source text of Pos.Line, kept so the error outlives the parser
	Desc string
}

// EvalError is an evaluation failure carrying the position it occurred at and
// the chain of evaluations that led to it.
//
// Trace is ordered innermost first: Trace[0] is the expression closest to the
// failure and the last frame is the outermost one, which is how Error renders
// it.
type EvalError struct {
	Kind  ErrorKind
	Msg   string
	Pos   *p.LexPosition
	Line  string
	Trace []Frame
}

func (e *EvalError) Error() string {
	var b strings.Builder
	b.WriteString("error: ")
	b.WriteString(e.Msg)
	if s := p.Snippet(e.Pos, e.Line, "       "); s != "" {
		b.WriteString("\n\n")
		b.WriteString(s)
	}
	for _, f := range e.Trace {
		b.WriteString("\n\n       … ")
		b.WriteString(f.Desc)
		if s := p.Snippet(f.Pos, f.Line, "         "); s != "" {
			b.WriteString("\n")
			b.WriteString(s)
		}
	}
	return b.String()
}

// maxTraceFrames bounds the backtrace so that runaway recursion reports a
// readable error instead of megabytes of repeated frames.
const maxTraceFrames = 64

// throwf raises an evaluation error, describing where it happened from the
// expressions currently being evaluated.
func (w *worker) throwf(kind ErrorKind, format string, args ...any) {
	w.raise(&EvalError{Kind: kind, Msg: fmt.Sprintf(format, args...)})
}

// throwAt raises an evaluation error at a node, for a failure found while
// evaluating something that has no expression of its own to be reported
// from — reading a name, which goes straight to the binding.
func (w *worker) throwAt(env *Env, n *p.Node, kind ErrorKind, format string, args ...any) {
	err := &EvalError{Kind: kind, Msg: fmt.Sprintf(format, args...)}
	if pr := env.parser(); pr != nil {
		if pos := pr.NodePos(n); pos != nil {
			err.Pos, err.Line = pos, pr.SourceLine(pos)
		}
	}
	w.raise(err)
}

// raise records where the failure happened on the evaluation stack and
// panics with it. The panic is what unwinds the evaluation, so it never
// returns.
func (w *worker) raise(err *EvalError) {
	err.capture(w.stack[:w.depth])
	panic(err)
}

// capture records where the failure happened: the position of the innermost
// expression that has one, and a frame for every enclosing expression that is
// worth naming.
func (e *EvalError) capture(stack []evalFrame) {
	for i := len(stack) - 1; i >= 0; i-- {
		f := stack[i]
		if e.Pos == nil {
			if pos := f.pos(); pos != nil {
				e.Pos, e.Line = pos, f.parser().SourceLine(pos)
			}
		}
		if f.blame == blameNone {
			continue
		}
		if len(e.Trace) == maxTraceFrames {
			break
		}
		e.Trace = append(e.Trace, f.traceFrame())
	}
}

// asEvalError converts a recovered panic value into an *EvalError, or returns
// nil if the panic came from somewhere else and should keep unwinding.
func asEvalError(v any) *EvalError {
	if err, ok := v.(*EvalError); ok {
		return err
	}
	return nil
}
