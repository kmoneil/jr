package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// stream runs a command that emits its collection incrementally.
//
// The format decides whether rows reach stdout as they arrive or wait for the
// last page; the command is written the same way either way. Progress goes to
// stderr and only when a human is there to read it.
func (a *app) stream(ctx context.Context, rc *registry.Command, inv *registry.Invocation) error {
	format, err := a.resolveFormat(collectionShaped)
	if err != nil {
		return err
	}

	spec := streamSpec(rc, inv)
	out, err := render.NewStream(a.stdout, format, spec)
	if err != nil {
		return err
	}

	reporter := a.progress(rc.CollectionName)
	inv.Progress = reporter
	defer reporter.Done()

	result, runErr := rc.Stream(ctx, inv, out)
	// Clear the progress line before anything else reaches stderr, so a
	// diagnostic never lands on top of a half-rewritten counter.
	reporter.Done()
	if runErr != nil {
		return withPartialStdout(runErr, out.Emitted())
	}

	if err := out.Close(result.Complete, result.NextPageToken); err != nil {
		return withPartialStdout(err, out.Emitted())
	}
	if result.Complete {
		return nil
	}

	// A truncated result is data plus a structured warning plus exit 3. TSV
	// carries no envelope, so the warning and the code are the whole signal.
	if err := render.WriteStreamTruncation(a.stderr, spec.Kind, out.Count(),
		result.NextPageToken, result.PartialElement, format); err != nil {
		return err
	}
	a.exit = exitcode.Partial
	return nil
}

// withPartialStdout tells a mid-stream failure what it left behind.
//
// docs/invariants.md says a failing command writes nothing at all to stdout, and
// a streamed collection is the one place that stops being true: TSV puts a row
// on the wire the moment it arrives, so a failure on the fortieth row cannot
// unwrite the thirty-nine before it. The same document, refused for the same
// reason, writes nothing under XML, JSON, and YAML, which is the output contract
// splitting along --format, one layer out from the value check that closed it
// for what a value may contain.
//
// Closing it the same way would mean buffering every format, and buffering is
// the one thing streaming exists not to do: a collection that has to be complete
// before it is emitted cannot be piped into anything while it runs. So the split
// is stated rather than hidden. A consumer that reads only stdout was always
// going to be misled here, and this is the sentence that lets one reading stderr
// know what the bytes it already has are worth.
//
// It says nothing when nothing went out, which is every buffered format and
// every failure before the first row.
func withPartialStdout(err error, rows int) error {
	if err == nil || rows == 0 {
		return err
	}
	e, ok := errs.AsError(err)
	if !ok {
		return err
	}

	noun := "rows"
	if rows == 1 {
		noun = "row"
	}
	note := fmt.Sprintf("%d %s of this collection reached stdout before it failed: "+
		"TSV streams, so a row is bytes the moment it is written. What is there is "+
		"the answer up to the failure and not a complete one.", rows, noun)

	// Annotated in place, and the caller's own error is what goes back: an
	// *Error reached through a wrapper is still the thing carrying the remedy,
	// and returning it bare would drop whatever wrapped it.
	switch {
	case e.Remedy == "":
		e.Remedy = note
	case strings.HasSuffix(e.Remedy, "."):
		e.Remedy += " " + note
	default:
		// A remedy is one line of prose and most are written without a full
		// stop, so joining on a space alone runs the two sentences together.
		e.Remedy += ". " + note
	}
	return err
}

// streamSpec builds the collection description from the command's declaration.
//
// Columns can depend on the invocation — a caller asking for an extra field
// wants a column for it — and they are needed before the first page, because
// the header goes out ahead of the rows.
func streamSpec(rc *registry.Command, inv *registry.Invocation) render.StreamSpec {
	columns := rc.Columns
	if rc.ColumnsFor != nil {
		columns = rc.ColumnsFor(inv)
	}
	return render.StreamSpec{
		Kind:    rc.Kind(),
		Version: rc.KindVersion(),
		Name:    rc.CollectionName,
		Columns: columns,
		Site:    siteOf(inv),
		Scope:   func() (string, string) { return scopeOf(inv) },
	}
}

// collectionShaped stands in for a document that has not been built yet, so the
// per-content default resolves to the collection format rather than the record
// one. A streaming command always emits a collection.
var collectionShaped = &render.Doc{Collection: &render.Collection{}}
