//go:build write

package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

const (
	kindHalfApplied    = "test.half-applied"
	versionHalfApplied = 1
)

// halfApplied registers a mutating command that applies one thing, fails on the
// next, and returns both halves.
//
// It is a fake rather than `epic add` on a cassette because what is under test
// is the CLI layer: whether a document carried by an error reaches stdout, and
// whether the exit is the cause's. Driving a real verb would test the verb.
func halfApplied(doc *render.Doc, cause error) *registry.Registry {
	r := registry.New()
	r.Register(&registry.Command{
		Path:    []string{"probe", "half"},
		Summary: "Apply some of it and stop",
		Example: "jr probe half",
		Flags: []registry.Flag{
			{Name: "dry-run", Type: registry.TypeBool, Usage: "print the request"},
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: nil,
		Outputs:      []registry.Output{{Kind: kindHalfApplied, Version: versionHalfApplied}},
		ExitCodes:    []exitcode.Code{exitcode.Blocked, exitcode.Permission},
		Run: func(_ context.Context, _ *registry.Invocation) (*render.Doc, error) {
			return nil, &registry.PartiallyApplied{Doc: doc, Cause: cause}
		},
	})
	return r
}

// TestAHalfAppliedWriteReachesStdoutAndStillFails is the layer a user actually
// invokes, which is where this had to be tested.
//
// The workflow package can assert that `epic add` returns a PartiallyApplied.
// It cannot assert that anything writes it: runDocument returned on any error,
// so the document existed and went nowhere, and every test below the CLI would
// have passed anyway. That is the mcp.Serve lesson — test the wrapper, not the
// thing it wraps.
func TestAHalfAppliedWriteReachesStdoutAndStillFails(t *testing.T) {
	doc := render.Record(kindHalfApplied, versionHalfApplied,
		render.El("epic").
			Attr("id", "ENG-42").
			Attr("action", "added").
			Attr("requested", "2").
			Attr("applied", "1").
			Child(render.ListEl(
				"issues", "issue",
				render.El("issue").Attr("key", "ENG-101").Attr("status", "moved"),
				render.El("issue").Attr("key", "ENG-102").Attr("status", "failed"),
			)))
	cause := errs.Permission("FORBIDDEN", "not allowed to edit that project")

	got := runGated(t, halfApplied(doc, cause), nil, "probe", "half")

	// The exit is the cause's. Not 0, which would report a failure as success,
	// and not 3, which means a truncated result set and would send a caller
	// looking for a page token.
	if got.exit != exitcode.Permission {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Permission)
	}
	// And the document is there, which is the whole point: a caller told only
	// FORBIDDEN would assume ENG-101 did not move, and it did.
	if !strings.Contains(got.stdout, `key="ENG-101" status="moved"`) {
		t.Errorf("stdout does not say what was applied:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, `applied="1"`) {
		t.Errorf("stdout carries no applied count:\n%s", got.stdout)
	}
	// The failure still goes to stderr, structured, like every other failure.
	if !strings.Contains(got.stderr, "FORBIDDEN") {
		t.Errorf("stderr does not carry the failure:\n%s", got.stderr)
	}
}

// TestAnOrdinaryFailureStillWritesNothingToStdout is the other half, and the
// one that keeps the exception narrow. Widening it would be invisible from the
// test above, which only ever asserts the exception's own case.
func TestAnOrdinaryFailureStillWritesNothingToStdout(t *testing.T) {
	cause := errs.Permission("FORBIDDEN", "not allowed to edit that project")
	got := runGated(t, halfApplied(nil, cause), nil, "probe", "half")

	if got.exit != exitcode.Permission {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Permission)
	}
	if got.stdout != "" {
		t.Errorf("a failure that applied nothing wrote to stdout:\n%s", got.stdout)
	}
}
