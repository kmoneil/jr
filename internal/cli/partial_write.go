//go:build write

package cli

import (
	"errors"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// writeAppliedSoFar writes the document a half-applied write carries, and
// reports whether err was one.
//
// This is the single exception to "a failing command writes nothing at all to
// stdout", and it is narrow on purpose. The rule is right for a command that
// did nothing: a result document alongside a failure would let a caller act on
// data that describes a state Jira is not in. It is wrong for a command that
// applied some of what it was asked, because then the failure alone is
// misleading in the other direction — the caller assumes nothing happened, and
// something did.
//
// The exit is still the cause's. errs.Coerce traverses Unwrap, so the caller
// gets NOT_FOUND or PERMISSION or whatever stopped the run, and reads the
// document to learn how far it got.
func (a *app) writeAppliedSoFar(
	rc *registry.Command, inv *registry.Invocation, err error,
) (bool, error) {
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		return false, nil
	}
	if partial.Doc == nil {
		// Nothing to write is not an error worth failing over: the cause still
		// reaches the caller, which is the important half.
		return true, nil
	}
	if !rc.EmitsDocumentFor(inv) {
		return true, nil
	}
	if !rc.Emits(partial.Doc.Kind, partial.Doc.Version) {
		return true, errs.Runtime("UNDECLARED_KIND",
			"command %s emitted kind %q v%d, which it does not declare",
			rc.Name(), partial.Doc.Kind, partial.Doc.Version)
	}

	format, ferr := a.resolveFormat(partial.Doc)
	if ferr != nil {
		return true, ferr
	}
	return true, render.Write(a.stdout, partial.Doc, format)
}
