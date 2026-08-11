//go:build write

package registry

import "github.com/kmoneil/jr/internal/render"

// PartiallyApplied is what a mutating command returns when it did some of what
// it was asked and then stopped.
//
// It carries both halves, because neither is sufficient on its own: the
// document names which items were applied and has to reach stdout, and the
// cause decides the exit.
//
// "A failing command writes nothing at all to stdout" is right for a command
// that did nothing and wrong for one that did half the work. A caller told only
// PERMISSION has no way to learn which issues already moved, and the safe
// assumption — that nothing happened — is the false one. So this is the one
// shape that writes a result and exits non-zero, and it is deliberately narrow:
// it exists for an operation Jira offers no atomic spelling of.
//
// The exit is the cause's own, not a new code and not exitcode.Partial. Partial
// means a truncated *result set*, which a write does not have, and reusing it
// would make one code mean two things.
type PartiallyApplied struct {
	// Doc names what was applied and what was not. It is a complete document:
	// nothing about it is truncated, so it carries no complete="false".
	Doc *render.Doc
	// Cause is the failure that stopped the run, and the exit the caller gets.
	Cause error
}

func (e *PartiallyApplied) Error() string { return e.Cause.Error() }

func (e *PartiallyApplied) Unwrap() error { return e.Cause }
