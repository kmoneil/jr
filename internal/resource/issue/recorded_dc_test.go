package issue_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// recordedConn is replayConn plus the check that makes a recording worth
// keeping separately: a cassette that has quietly become hand-written replays
// exactly like a recorded one and would assert nothing about the API.
func recordedConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	if !cassette.Evidence() {
		t.Fatalf("%s is not a recording, so replaying it establishes nothing "+
			"about the API", fixture)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// TestTheRecordedDataCenterListingIsAConversationAServerHad is what every
// constructed Data Center fixture beside it cannot be.
//
// Those are worth keeping: keyset.datacenter.json resumes with
// `issuekey < ENG-1000` across a boundary and keyset-misordered.datacenter.json
// returns a page the server ordered wrong, shapes no sandbox produces on
// request. What they cannot establish is that the request is one Data Center
// accepts, because their author decided both halves of the exchange — and a
// hand-written cassette proves a response is handled, never that a request was
// accepted.
//
// This establishes exactly that. The replayer matches on path and query, so a
// rendered row means the endpoint, the field list, the `maxResults` the limit
// becomes, and the `ORDER BY issuekey DESC` this code appends are the ones a
// real Jira Software Data Center 10.4.0 answered.
func TestTheRecordedDataCenterListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	conn, replayer := recordedConn(t, "list-recorded.datacenter.json")

	// The invocation that produced the recording: `issue list --project ENG
	// --limit 5`, the project coming from the context the way it does in
	// production.
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: site.DataCenter, project: "ENG",
		},
		Flags: registry.NewFlags(), Limit: registry.Limit{N: 5},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !result.Complete {
		t.Error("a listing that exhausted the project was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded page was never requested: %v", unplayed)
	}

	// Every value below is one the server sent. Descending by key is what the
	// ORDER BY asked for, and the server agreed.
	want := "key\tstatus\tassignee\tupdated\tsummary\n" +
		"ENG-5\tTo Do\tAda Lovelace\t2026-08-11T16:37:32Z\tRefuse an unrecognised deployment type\n" +
		"ENG-4\tTo Do\tAda Lovelace\t2026-08-11T16:37:31Z\tA truncated result reports itself complete\n" +
		"ENG-3\tTo Do\tGrace Hopper\t2026-08-11T16:37:31Z\tQuote a JQL value exactly once\n" +
		"ENG-2\tTo Do\tAda Lovelace\t2026-08-11T16:37:31Z\tPage a search by keyset\n" +
		"ENG-1\tTo Do\tAda Lovelace\t2026-08-11T16:37:31Z\tRecord every Data Center conversation\n"
	if buf.String() != want {
		t.Errorf("listing =\n%s\nwant\n%s", buf.String(), want)
	}
}

// TestTheRecordedDataCenterIssueIsAConversationAServerHad covers the read by
// key against a real instance.
//
// get.datacenter.json beside it is constructed, and holds what a sandbox will
// not serve on request — a description carrying a fenced code block and a
// literal ]]>. What it cannot say is that `/rest/api/2/issue/ENG-1` with this
// tool's fifteen-field `fields` parameter is a request Data Center answers
// rather than rejects; a constructed cassette answers whatever it is asked.
// This replays the exchange that happened, so a change to the path, the field
// list, or the key as a path segment stops matching and fails here.
func TestTheRecordedDataCenterIssueIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("issue.get")
	if !ok {
		t.Fatal("issue get is not registered")
	}
	conn, replayer := recordedConn(t, "issue-recorded.datacenter.json")

	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: site.DataCenter, project: "ENG",
		},
		Args: []string{"ENG-1"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if !cmd.Emits(doc.Kind, doc.Version) {
		t.Errorf("emitted %s v%d, which the command does not declare",
			doc.Kind, doc.Version)
	}
	if err := doc.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the issue was never read: %v", unplayed)
	}

	// Attributes the server sent, including the id it minted — 10000 is the
	// first issue of a fresh instance, not a number this project chose.
	for _, tc := range []struct{ attr, want string }{
		{"key", "ENG-1"},
		{"id", "10000"},
		{"type", "Epic"},
		{"priority", "Medium"},
		{"project", "ENG"},
	} {
		if got, _ := doc.Record.AttrValue(tc.attr); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.attr, got, tc.want)
		}
	}

	summary, ok := doc.Record.ChildNamed("summary")
	if !ok {
		t.Fatal("the issue carries no summary")
	}
	if summary.Text != "Record every Data Center conversation" {
		t.Errorf("summary = %q, want the recorded one", summary.Text)
	}

	// Data Center sends a status category key of "new", which this code
	// normalizes to to-do. Nothing but a recording says the server spells it
	// that way.
	status, ok := doc.Record.ChildNamed("status")
	if !ok {
		t.Fatal("the issue carries no status")
	}
	if status.Text != "To Do" {
		t.Errorf("status = %q, want To Do", status.Text)
	}
	if category, _ := status.AttrValue("category"); category != "to-do" {
		t.Errorf("category = %q, want to-do", category)
	}

	// Data Center identifies a user by name, not by accountId, so the id here
	// is the username the server returned rather than a Cloud-shaped opaque id.
	assignee, ok := doc.Record.ChildNamed("assignee")
	if !ok {
		t.Fatal("the issue carries no assignee")
	}
	if id, _ := assignee.AttrValue("id"); id != "ada" {
		t.Errorf("assignee id = %q, want the Data Center username", id)
	}
	if display, _ := assignee.AttrValue("display"); display != "Ada Lovelace" {
		t.Errorf("assignee display = %q, want Ada Lovelace", display)
	}
	reporter, ok := doc.Record.ChildNamed("reporter")
	if !ok {
		t.Fatal("the issue carries no reporter")
	}
	if display, _ := reporter.AttrValue("display"); display != "Ada Lovelace" {
		t.Errorf("reporter display = %q, want Ada Lovelace", display)
	}
}
