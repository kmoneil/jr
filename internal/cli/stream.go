package cli

import (
	"context"

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

	reporter := a.progress()
	inv.Progress = reporter
	defer reporter.Done()

	result, runErr := rc.Stream(ctx, inv, out)
	// Clear the progress line before anything else reaches stderr, so a
	// diagnostic never lands on top of a half-rewritten counter.
	reporter.Done()
	if runErr != nil {
		return runErr
	}

	if err := out.Close(result.Complete, result.NextPageToken); err != nil {
		return err
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
	}
}

// collectionShaped stands in for a document that has not been built yet, so the
// per-content default resolves to the collection format rather than the record
// one. A streaming command always emits a collection.
var collectionShaped = &render.Doc{Collection: &render.Collection{}}
