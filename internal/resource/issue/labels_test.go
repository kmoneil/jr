package issue_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// suggestDoer answers the label suggestion route from a fixed set and counts
// what it was asked, so a test can assert the cost as well as the verdict.
type suggestDoer struct {
	labels []string
	asked  []string
}

func (d *suggestDoer) Do(
	_ context.Context, r transport.Request,
) (*transport.Response, error) {
	if !strings.Contains(r.Path, site.LabelSuggestPath) {
		return nil, errNotStubbed
	}
	prefix := r.Query.Get("fieldValue")
	d.asked = append(d.asked, prefix)

	var results []string
	for _, l := range d.labels {
		if strings.HasPrefix(l, prefix) {
			results = append(results, `{"value":"`+l+`"}`)
		}
	}
	return &transport.Response{
		Status: 200,
		Body:   []byte(`{"results":[` + strings.Join(results, ",") + `]}`),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

var errNotStubbed = errString("no stub for that path")

type errString string

func (e errString) Error() string { return string(e) }

// listWithLabels drives `issue list` validation only, which is where the check
// runs: a streaming command writes its header before its body, so a diagnostic
// from inside the command would arrive after bytes are on stdout.
func listWithLabels(
	t *testing.T, doer *suggestDoer, stderr io.Writer, label string,
) error {
	t.Helper()

	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("label", label)

	return cmd.Validate(t.Context(), &registry.Invocation{
		Jira:   &stubSession{metaClient: doer, project: "ENG"},
		Flags:  flags,
		Limit:  registry.Limit{N: 50},
		Format: render.TSV,
		Stderr: stderr,
	})
}

func TestAnUnknownLabelIsWarnedAboutAndTheQueryStillRuns(t *testing.T) {
	doer := &suggestDoer{labels: []string{"retry", "transport"}}
	var stderr strings.Builder

	if err := listWithLabels(t, doer, &stderr, "retyr"); err != nil {
		t.Fatalf("validate refused a legal query: %v", err)
	}
	if !strings.Contains(stderr.String(), "UNKNOWN_LABEL") {
		t.Errorf("stderr =\n%q\nwant UNKNOWN_LABEL", stderr.String())
	}
	if !strings.Contains(stderr.String(), "retyr") {
		t.Errorf("stderr =\n%q\nwant the label it is about", stderr.String())
	}
}

func TestAKnownLabelIsNotWarnedAboutAndCostsOneRequest(t *testing.T) {
	doer := &suggestDoer{labels: []string{"retry", "transport"}}
	var stderr strings.Builder

	if err := listWithLabels(t, doer, &stderr, "retry"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if stderr.String() != "" {
		t.Errorf("stderr =\n%q\nwant nothing said about a label that exists", stderr.String())
	}
	if len(doer.asked) != 1 {
		t.Errorf("asked %v, want one lookup and no liveness check", doer.asked)
	}
}

// TestADiscardedWarningIsNotWorthARequest is the cost half of the guard rather
// than the behaviour half.
//
// An MCP tool call sets Stderr to io.Discard on purpose: a diagnostic there has
// nowhere to go but the protocol stream, which it must not reach. Checking the
// label anyway would spend one request per label to write into a hole.
func TestADiscardedWarningIsNotWorthARequest(t *testing.T) {
	doer := &suggestDoer{labels: []string{"retry"}}

	if err := listWithLabels(t, doer, io.Discard, "retyr"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(doer.asked) != 0 {
		t.Errorf("asked %v with nowhere to report it", doer.asked)
	}
}
