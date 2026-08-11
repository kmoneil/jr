package issue_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// TestIssueGetReportsAPreconditionAndIssueListDoesNot is the shape decision.
//
// A row nobody read is not a baseline, so the token is on the record and not on
// the listing — and because the two deliberately share Schema(), putting it in
// the wrong place would have widened issue.list's shape without changing its
// version. This asserts the split from the output rather than from the schema,
// because the schema is what `make golden` already pins.
func TestIssueGetReportsAPreconditionAndIssueListDoesNot(t *testing.T) {
	cmd, ok := registry.Lookup("issue.get")
	if !ok {
		t.Fatal("issue get is not registered")
	}
	conn, _ := replayConn(t, "get.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:   &stubSession{conn: conn, kind: site.Cloud},
		Args:   []string{"ENG-101"},
		Flags:  registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	token, ok := doc.Record.AttrValue("precondition")
	if !ok || token == "" {
		t.Fatal("issue get reported no precondition, so --if-unchanged has nothing to take")
	}

	// Opaque to a caller, but this test is inside the contract and checks that
	// what is wrapped is the millisecond value and not the published one.
	var decoded struct {
		Deployment string `json:"d"`
		Key        string `json:"k"`
		Updated    string `json:"u"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("the token is not base64: %v", err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the token is not JSON: %v", err)
	}
	if decoded.Key != "ENG-101" {
		t.Errorf("token names issue %q, want ENG-101", decoded.Key)
	}
	if decoded.Deployment != string(site.Cloud) {
		t.Errorf("token names deployment %q, want cloud", decoded.Deployment)
	}
	// The fixture's updated is 2026-08-04T11:32:07.000+0000. The published
	// element rounds to the second; the token must not, or two edits inside one
	// second are indistinguishable.
	if !strings.Contains(decoded.Updated, ".000") {
		t.Errorf("token version = %q, want milliseconds — the published "+
			"`updated` is already second-granularity, and wrapping that value "+
			"would buy nothing", decoded.Updated)
	}

	updated, ok := doc.Record.ChildNamed("updated")
	if !ok {
		t.Fatal("the record carries no updated element")
	}
	if strings.Contains(updated.Text, ".") {
		t.Errorf("updated = %q; this change must not widen the published "+
			"timestamp, which would move every golden carrying one", updated.Text)
	}
}

// TestAListedRowCarriesNoPrecondition is the other half, asserted through the
// listing rather than through issue get's absence of one.
func TestAListedRowCarriesNoPrecondition(t *testing.T) {
	client, _ := replayClient(t, site.Cloud)
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Issues) == 0 {
		t.Fatal("the fixture returned no rows")
	}
	for _, i := range result.Issues {
		if _, has := i.Node().AttrValue("precondition"); has {
			t.Errorf("%s carries a precondition; issue.list does not declare one, "+
				"so this row would fail validation", i.Key)
		}
	}
}

// TestAPreconditionSurvivesATimestampReformatting is the false-stale case.
//
// Both sides of the comparison are canonicalized, so an instant Jira spelled
// `+0000` on one read and a proxy normalized to `Z` on the next is the same
// version. A refusal to write an untouched issue is as wrong as a silent
// overwrite, and it is the failure a string comparison would have shipped.
func TestAPreconditionSurvivesATimestampReformatting(t *testing.T) {
	info := site.Info{Kind: site.Cloud}
	spellings := []string{
		"2026-08-04T11:32:07.412+0000",
		"2026-08-04T11:32:07.412Z",
		"2026-08-04T12:32:07.412+0100",
	}

	var first string
	for i, spelling := range spellings {
		token, err := issue.EncodePrecondition(info, "ENG-101", spelling)
		if err != nil {
			t.Fatalf("%q: %v", spelling, err)
		}
		if i == 0 {
			first = token
			continue
		}
		if token != first {
			t.Errorf("%q minted a different token from %q; the same instant "+
				"spelled two ways would read as a stale write",
				spelling, spellings[0])
		}
	}
}

// TestAnIssueWithNoUpdatedGetsNoPrecondition is why the attribute is optional.
//
// A token asserting a version of nothing would compare equal to the next one
// minted the same way, which is a check that always passes — worse than no
// check, because the caller believes they have one.
func TestAnIssueWithNoUpdatedGetsNoPrecondition(t *testing.T) {
	token, err := issue.EncodePrecondition(site.Info{Kind: site.Cloud}, "ENG-101", "")
	if err != nil {
		t.Fatalf("an absent timestamp is not an error: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want none", token)
	}
}
