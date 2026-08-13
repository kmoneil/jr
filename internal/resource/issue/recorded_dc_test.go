package issue_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
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

// TestTheServerRefusesAnUnknownStatusAndNotAnUnknownLabel is the evidence
// behind leaving a comma in a repeatable filter alone.
//
// `--status` and `--label` bind as string arrays, so neither splits on a comma
// and `--status Done,Closed` asks for one status whose name contains one. That
// is the right default, and the reason is no longer a supposition: a label
// containing a comma was created on Jira Cloud on 2026-08-13 and read back as
// one label, so splitting would make a value the server genuinely stores
// unaskable. The argument still rests on what happens next, which is what the
// two cassettes below asked the server.
//
// It answers differently for the two fields, and the difference is the whole
// point. Jira validates a status name against the ones that exist and refuses
// an unknown one with its own wording, so the mistake arrives as a loud error a
// round trip away. It does not validate a label, so an unknown one is legal JQL
// that matches nothing and comes back complete, empty, and exit 0 — which is
// indistinguishable from an honest answer, and is the shape this project exists
// to refuse.
//
// Recorded against Jira Software Data Center 9.12.38 rather than the 10.4.0
// every other recording here came from. Two lines behaving alike is a stronger
// claim than one, and this is the LTS a lot of customers are still on.
func TestTheServerRefusesAnUnknownStatusAndNotAnUnknownLabel(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	t.Run("an unknown status is refused by the server", func(t *testing.T) {
		conn, replayer := recordedConn(t, "unknown-status-recorded.datacenter.json")
		flags := registry.NewFlags()
		flags.SetString("status", "NOSUCHSTATUS")

		_, err := runRecordedList(t, cmd, conn, flags)
		if err == nil {
			t.Fatal("a status no project defines was accepted")
		}
		// The server's own sentence, carried rather than paraphrased: it names
		// the value and the field, which is more than this tool knows.
		detail := errs.Coerce(err).Detail
		if !strings.Contains(detail, "'NOSUCHSTATUS' does not exist for the field 'status'") {
			t.Errorf("detail = %q, want Jira's own wording about the status", detail)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("a recorded exchange was never requested: %v", unplayed)
		}
	})

	t.Run("an unknown label is answered with nothing", func(t *testing.T) {
		conn, replayer := recordedConn(t, "comma-label-recorded.datacenter.json")
		flags := registry.NewFlags()
		flags.SetString("label", "a,b")

		out, err := runRecordedList(t, cmd, conn, flags)
		if err != nil {
			t.Fatalf("a label that matches nothing was an error: %v", err)
		}
		// A header and no rows, reported complete. The tool is not wrong here —
		// the server was asked a question with no matches and answered it — and
		// that is exactly why the comma cannot be split silently: this is what
		// the mistake looks like.
		if out != "key\tstatus\tassignee\tupdated\tsummary\n" {
			t.Errorf("listing =\n%q\nwant a header and no rows", out)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("a recorded exchange was never requested: %v", unplayed)
		}
	})
}

// TestACommaInALabelIsPartOfTheLabel is what a future "fix" for the case above
// would break, so it is asserted rather than left to the comment.
//
// The obvious remedy for `--label a,b` answering emptily is to split on the
// comma and ask for two labels. It was the middle option on the card and it is
// wrong: `a,b` is a label Jira accepts and stores, measured against Cloud on
// 2026-08-13 by creating an issue with it and reading it back as one label. A
// tool that split would have no spelling for that label at all, which is the
// silent alteration §5.2 forbids, and it would do so to guess at an intent the
// caller can state exactly by repeating the flag.
//
// So this pins both ends: one value reaches JQL, and one value comes back out
// of the TSV writer with its comma escaped rather than read as a separator.
func TestACommaInALabelIsPartOfTheLabel(t *testing.T) {
	built := jql.New().In("labels", "a,b").String()
	if built != `labels = "a,b"` {
		t.Errorf("built %s, want one label and not two", built)
	}

	one := render.JoinList([]string{"a,b"})
	two := render.JoinList([]string{"a", "b"})
	if one == two {
		t.Errorf("one label %q and two labels %q render identically, so a "+
			"consumer splitting the cell reads the one as two", one, two)
	}
	if one != `a\,b` {
		t.Errorf("JoinList = %q, want the comma escaped", one)
	}
}

// runRecordedList drives `issue list` against a recording, scoped to the
// stub's project the way a context scopes it in production. That is the
// invocation both cassettes above were recorded with.
func runRecordedList(
	t *testing.T, cmd *registry.Command, conn *transport.Client, flags registry.Flags,
) (string, error) {
	t.Helper()

	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Flags: flags, Limit: registry.Limit{N: 50},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		return "", err
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
		return "", err
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !result.Complete {
		t.Error("an exhausted listing was reported incomplete")
	}
	return buf.String(), nil
}
