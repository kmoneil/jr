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
		return a.warnEmptyResult(rc, inv, spec, out.Count(), format)
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

// EmptyResultCode is the warning a complete collection carries when it holds no
// rows.
const EmptyResultCode = "EMPTY_RESULT"

// warnEmptyResult says what bounds a zero-row answer was computed over.
//
// Zero rows is the one result whose frame cannot be inferred from the result.
// A populated collection shows its own scope: the rows name their projects and
// their dates, so a caller who narrowed the question by accident can see it in
// what came back. An empty one is bytes-identical whatever the frame was, and
// `internal/resource/issue/scopewarn.go` already spells out where that leads,
// for the one case where the caller typed the contradiction themselves.
// SCOPE_MISMATCH fires when a raw --jql names a project the scope excludes.
// This is the same defect where the caller named nothing at all, so there was
// nothing to compare and nothing was said.
//
// XML, JSON and YAML already carry the scope on the envelope, and
// TestTheEnvelopeNamesTheScopeTheAnswerWasComputedOver pins it against an empty
// result for exactly this reason. TSV has no envelope and is the default, so
// for the format most callers get, this line is the whole signal. It is written
// in every format regardless: the resolved bounds an EmptyFrame contributes are
// on no envelope at all, and a warning that appeared under some formats and not
// others would be a second rule for a reader to learn.
//
// Exit stays 0. The query was legal, the answer is honest, and an empty result
// is not a failure. Not being able to tell what it was empty *about* is what
// was missing.
func (a *app) warnEmptyResult(
	rc *registry.Command,
	inv *registry.Invocation,
	spec render.StreamSpec,
	count int,
	format render.Format,
) error {
	if count > 0 || a.stderr == nil {
		return nil
	}

	notes := []string{fmt.Sprintf("0 %s", spec.Name)}
	// The scope goes through the same reader the envelope uses, so the warning
	// and the document cannot disagree about what the answer covered, and
	// --all-projects reports no project here for the same reason it reports
	// none there.
	if project, board := scopeOf(inv); project != "" || board != "" {
		if project != "" {
			notes = append(notes, "project="+project)
		}
		if board != "" {
			notes = append(notes, "board="+board)
		}
	} else if inv.Jira != nil {
		// Said out loud rather than left to the absence of a project= note. An
		// unscoped sweep and a sweep whose scope nobody reported look the same
		// from a missing key, and they are opposite answers.
		notes = append(notes, "scope=none")
	}
	if rc.EmptyFrame != nil {
		notes = append(notes, rc.EmptyFrame(inv)...)
	}

	return render.WriteWarning(a.stderr, EmptyResultCode,
		strings.Join(notes, ", "), format)
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
