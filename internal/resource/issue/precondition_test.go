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

// TestIssueGetAlwaysReportsAPrecondition is the half of the shape decision that
// takes no flag.
//
// A caller cannot ask for a baseline they do not yet know they need: the
// decision to write comes after the read. So get mints one every time, and it
// costs no request, because the issue is already here. `issue list` mints one
// per row only with --precondition, for the byte reason rather than a
// correctness one, and TestTheListingMintsNoBaselineUnlessAsked holds that end.
//
// This asserts the token from the output rather than from the schema, because
// the schema is what `make golden` already pins.
func TestIssueGetAlwaysReportsAPrecondition(t *testing.T) {
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

// TestTheClientMintsNoPreconditionOfItsOwn keeps the flag as the only door.
//
// Stamping a listing is a command-layer decision, made in stampPage from
// --precondition, and Client.List has no opinion about it. That split is what
// makes the flag mean anything: if the client minted tokens on its own, every
// caller of List would carry them whatever the command asked for, and §2.4
// would be decided somewhere no flag can reach.
//
// This used to assert that a listed row could never carry one, on the reasoning
// that issue.list did not declare the attribute. It does declare it as of v8,
// so the claim worth holding moved down a layer rather than disappearing.
func TestTheClientMintsNoPreconditionOfItsOwn(t *testing.T) {
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
			t.Errorf("%s carries a precondition the command never asked for, "+
				"so --precondition is no longer what decides", i.Key)
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
		token, err := issue.EncodePrecondition(info, "ENG-101", spelling, issue.PrecisionMillisecond)
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
	token, err := issue.EncodePrecondition(site.Info{Kind: site.Cloud}, "ENG-101", "", issue.PrecisionMillisecond)
	if err != nil {
		t.Fatalf("an absent timestamp is not an error: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want none", token)
	}
}
