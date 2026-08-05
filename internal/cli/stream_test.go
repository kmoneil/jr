package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// fakeStream registers a streaming command that emits rows without any Jira, so
// the CLI's streaming path can be tested on its own.
func fakeStream(t *testing.T, rows int, complete bool, token string) *registry.Registry {
	t.Helper()
	r := registry.New()
	r.Register(&registry.Command{
		Path:           []string{"fake", "list"},
		Summary:        "Emit rows for a test",
		Example:        "jr fake list",
		Paginated:      true,
		CollectionName: "things",
		Columns: []render.Column{
			{Header: "key", Path: "@key"},
			{Header: "name", Path: "name"},
		},
		Outputs:   []registry.Output{{Kind: "fake.list", Version: 1}},
		ExitCodes: []exitcode.Code{exitcode.Partial},
		Stream: func(
			_ context.Context, inv *registry.Invocation, out *render.Stream,
		) (registry.StreamResult, error) {
			for i := range rows {
				node := render.El("thing").
					Attr("key", "K-"+string(rune('A'+i))).
					Leaf("name", "row")
				if err := out.Write(node); err != nil {
					return registry.StreamResult{}, err
				}
				inv.Progress.Update(out.Count(), rows)
			}
			return registry.StreamResult{Complete: complete, NextPageToken: token}, nil
		},
	})
	return r
}

func runStream(t *testing.T, reg *registry.Registry, args ...string) result {
	t.Helper()
	var out, errOut strings.Builder
	env := isolate(t, nil)
	code := cli.Main(context.Background(), args, cli.Options{
		Registry: reg,
		Stdout:   &out,
		Stderr:   &errOut,
		Getenv:   func(k string) string { return env[k] },
	})
	return result{exit: code, stdout: out.String(), stderr: errOut.String()}
}

// TestStreamingCommandRendersEveryFormat asserts the caller writes rows the same
// way regardless, and each format still produces its own shape.
func TestStreamingCommandRendersEveryFormat(t *testing.T) {
	want := map[string]string{
		"tsv":  "key\tname\n",
		"xml":  `<result kind="fake.list" v="1">`,
		"json": `"kind": "fake.list"`,
		"yaml": "kind: fake.list",
	}
	for format, marker := range want {
		got := runStream(t, fakeStream(t, 3, true, ""), "fake", "list", "--format", format)
		if got.exit != exitcode.OK {
			t.Fatalf("%s: exit = %v\nstderr: %s", format, got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, marker) {
			t.Errorf("%s output does not look like %s:\n%s", format, format, got.stdout)
		}
		if got.stderr != "" {
			t.Errorf("%s: a complete result wrote to stderr:\n%s", format, got.stderr)
		}
	}
}

// TestStreamingDefaultsToTSV keeps the per-content default: a streaming command
// always emits a collection.
func TestStreamingDefaultsToTSV(t *testing.T) {
	got := runStream(t, fakeStream(t, 2, true, ""), "fake", "list")
	if strings.HasPrefix(got.stdout, "<") || strings.HasPrefix(got.stdout, "{") {
		t.Errorf("a streamed collection did not default to TSV:\n%s", got.stdout)
	}
	if lines := strings.Count(strings.TrimRight(got.stdout, "\n"), "\n"); lines != 2 {
		t.Errorf("got %d data rows, want 2:\n%s", lines, got.stdout)
	}
}

// TestStreamedTruncationStillExitsPartial is the contract holding across the
// streaming path: rows on stdout, a structured warning on stderr, exit 3.
func TestStreamedTruncationStillExitsPartial(t *testing.T) {
	for _, format := range []string{"tsv", "xml", "json", "yaml"} {
		got := runStream(t, fakeStream(t, 2, false, "TOKEN123"),
			"fake", "list", "--format", format)
		if got.exit != exitcode.Partial {
			t.Errorf("%s: exit = %v, want %v", format, got.exit, exitcode.Partial)
		}
		if !strings.Contains(got.stderr, "RESULT_TRUNCATED") {
			t.Errorf("%s: no truncation warning:\n%s", format, got.stderr)
		}
		if !strings.Contains(got.stderr, "TOKEN123") {
			t.Errorf("%s: the warning carries no resume token:\n%s", format, got.stderr)
		}
		// The rows still went out, whatever the format.
		if got.stdout == "" {
			t.Errorf("%s: a truncated result produced no data", format)
		}
	}
}

// TestProgressIsSilentOnAPipe is what keeps a progress indicator compatible with
// "stderr carries only structured diagnostics": on anything but a terminal,
// nothing is emitted at all.
func TestProgressIsSilentOnAPipe(t *testing.T) {
	got := runStream(t, fakeStream(t, 50, true, ""), "fake", "list")
	if got.stderr != "" {
		t.Errorf("progress reached a non-terminal stderr:\n%q", got.stderr)
	}
	if got.exit != exitcode.OK {
		t.Errorf("exit = %v", got.exit)
	}
}

// TestEmptyStreamedResultStillHasAHeader covers a query that matched nothing.
func TestEmptyStreamedResultStillHasAHeader(t *testing.T) {
	got := runStream(t, fakeStream(t, 0, true, ""), "fake", "list")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	if !strings.HasPrefix(got.stdout, "key\tname") {
		t.Errorf("an empty result produced no header:\n%q", got.stdout)
	}
}

// TestValidateRunsBeforeAnyOutput is what makes a streaming command able to
// reject a flag at all. The header goes out before the body runs, so a check
// that happened inside the command would arrive after bytes were already on
// stdout.
func TestValidateRunsBeforeAnyOutput(t *testing.T) {
	r := registry.New()
	r.Register(&registry.Command{
		Path:           []string{"fake", "list"},
		Summary:        "Refuse before writing",
		Example:        "jr fake list",
		Paginated:      true,
		CollectionName: "things",
		Columns:        []render.Column{{Header: "key", Path: "@key"}},
		Outputs:        []registry.Output{{Kind: "fake.list", Version: 1}},
		ExitCodes:      []exitcode.Code{exitcode.Partial},
		Validate: func(context.Context, *registry.Invocation) error {
			return errs.Usage("REFUSED", "not today")
		},
		Stream: func(
			_ context.Context, _ *registry.Invocation, out *render.Stream,
		) (registry.StreamResult, error) {
			_ = out.Write(render.El("thing").Attr("key", "K-1"))
			return registry.StreamResult{Complete: true}, nil
		},
	})

	got := runStream(t, r, "fake", "list")
	if got.exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Usage)
	}
	if got.stdout != "" {
		t.Errorf("output was written before the command was refused:\n%q", got.stdout)
	}
	if !strings.Contains(got.stderr, "REFUSED") {
		t.Errorf("stderr does not carry the refusal:\n%s", got.stderr)
	}
}

// TestStdoutOwnerEmitsNoDocument covers the layer the MCP protocol tests
// missed.
//
// mcp.Serve was tested directly and wrote only frames, but the command wrapper
// above it then returned a result document, which the CLI rendered onto the
// same stream. The bug was invisible until a live session was run by hand: the
// test verified the wrong layer.
func TestStdoutOwnerEmitsNoDocument(t *testing.T) {
	r := registry.New()
	r.Register(&registry.Command{
		Path:       []string{"proto", "serve"},
		Summary:    "Own the output stream",
		Example:    "jr proto serve",
		OwnsStdout: true,
		Run: func(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
			// A protocol server writes its own frames and returns nothing.
			return render.Record("proto.serve", 1, render.El("leftover")), nil
		},
	})

	got := runStream(t, r, "proto", "serve")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("a document was rendered onto a stream the command owns:\n%s", got.stdout)
	}
}
