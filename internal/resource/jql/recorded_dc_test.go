package jql_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	jqlcmd "github.com/kmoneil/jr/internal/resource/jql"
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

// TestTheRecordedDataCenterCheckIsAConversationAServerHad is what
// search.datacenter.json beside it cannot be.
//
// That fixture is worth keeping — it says a zero-row search means valid — but
// its author wrote both halves, so it proves the response is handled and never
// that the request is one Jira accepts. The whole reason this path exists is
// that Data Center has no parse endpoint and the substitute is a search whose
// body has to be exactly right: `validateQuery` is a boolean here and Cloud's
// `"strict"` is a deserialization error that would arrive as valid="false".
//
// The replayer matches on method, path, and canonicalized body, so a rendered
// verdict means a real Jira Software Data Center 10.4.0 answered the POST to
// /rest/api/2/search that this code builds, with maxResults=0 and
// validateQuery=true, and returned total=5 with no issues attached.
func TestTheRecordedDataCenterCheckIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("jql.validate")
	if !ok {
		t.Fatal("jql validate is not registered")
	}
	conn, replayer := recordedConn(t, "validate-recorded.datacenter.json")

	const query = "project = ENG ORDER BY key DESC"
	flags := registry.NewFlags()
	flags.SetString("jql", query)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if valid, _ := doc.Record.AttrValue("valid"); valid != "true" {
		t.Errorf("valid = %q, want true — the server parsed this query", valid)
	}
	// Reported, because "valid" from a zero-row search and "valid" from a parse
	// are not quite the same claim.
	if method, _ := doc.Record.AttrValue("method"); method != jqlcmd.MethodSearch {
		t.Errorf("method = %q, want %q", method, jqlcmd.MethodSearch)
	}
	echoed, ok := doc.Record.ChildNamed("jql")
	if !ok {
		t.Fatal("the verdict did not echo the query it was about")
	}
	if echoed.Text != query {
		t.Errorf("jql = %q, want %q", echoed.Text, query)
	}
	if problem, ok := doc.Record.ChildNamed("error"); ok {
		t.Errorf("a query the server accepted carried an error: %q", problem.Text)
	}
	// The recorded response sent warningMessages: null, so a warning here would
	// be this tool's invention rather than Jira's.
	if warning, ok := doc.Record.ChildNamed("warning"); ok {
		t.Errorf("warning = %q, and the server sent none", warning.Text)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the check was never sent: %v", unplayed)
	}
}

// TestTheRecordedDataCenterRefusalIsAConversationAServerHad pins the 400 path to
// a refusal a server actually issued.
//
// invalid.datacenter.json beside it is a syntax error, and errorskey.datacenter.json
// is the errors.jql shape some versions use; both were written by hand, which
// means the decision to read a 400 as an answer rather than a failure was only
// ever tested against a body this project chose. This one is what 10.4.0 sends
// for a project that does not exist: status 400, the complaint in
// errorMessages, and errors empty — a semantic refusal rather than a parse
// failure, which no constructed fixture here covered.
//
// It establishes that the exchange happened and that the command still exits 0
// with the server's own wording, which is the product: an agent checking a
// query before it acts needs the reason, and an exit code cannot carry one.
func TestTheRecordedDataCenterRefusalIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("jql.validate")
	if !ok {
		t.Fatal("jql validate is not registered")
	}
	conn, replayer := recordedConn(t, "invalid-recorded.datacenter.json")

	const query = "project = NOSUCHPROJECT"
	flags := registry.NewFlags()
	flags.SetString("jql", query)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("a refusal the server sent failed the command instead of "+
			"answering it: %v", err)
	}
	if valid, _ := doc.Record.AttrValue("valid"); valid != "false" {
		t.Errorf("valid = %q, want false — the server refused this query", valid)
	}
	// The refusal came from the server, not from the local lexer: this query
	// lexes and balances, so a "local" verdict would mean no request went out.
	if method, _ := doc.Record.AttrValue("method"); method != jqlcmd.MethodSearch {
		t.Errorf("method = %q, want %q", method, jqlcmd.MethodSearch)
	}
	problem, ok := doc.Record.ChildNamed("error")
	if !ok {
		t.Fatal("invalid with no reason given")
	}
	// Jira's message, unedited. Rewriting it into this tool's own words would
	// lose the one thing worth having.
	const want = "The value 'NOSUCHPROJECT' does not exist for the field 'project'."
	if problem.Text != want {
		t.Errorf("error = %q, want Jira's own wording %q", problem.Text, want)
	}
	if strings.Contains(problem.Text, "readable reason") {
		t.Error("the recorded body was not understood, so the fallback text won")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the refused query was never sent: %v", unplayed)
	}
}
