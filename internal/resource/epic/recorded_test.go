package epic_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
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

// TestTheRecordedListingIsAConversationAServerHad establishes that the endpoint
// and the paging parameters this code builds are the ones a Cloud instance
// answered. The constructed fixtures beside it are worth keeping and cannot do
// that: their author decided both halves of the exchange.
//
// The empty name column is the server's answer and not a gap in the fixture. An
// epic created through the REST API on a company-managed Cloud project has no
// Epic Name set, and this tool has no flag that sets one — so the column a
// caller sees for a freshly created epic is empty, which is worth pinning
// because it looks like a rendering bug and is not one.
func TestTheRecordedListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("epic.list")
	if !ok {
		t.Fatal("epic list is not registered")
	}
	conn, replayer := recordedConn(t, "epics-recorded.cloud.json")

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.Cloud, board: "3"},
		Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}, stream)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !result.Complete {
		t.Error("an exhausted listing was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded page was never requested: %v", unplayed)
	}

	want := "key\tname\tsummary\tdone\n" +
		"AGL-1\t\tAgile fixture epic\tfalse\n"
	if buf.String() != want {
		t.Errorf("listing =\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestTheRecordedEpicIsAConversationAServerHad covers the read by key.
func TestTheRecordedEpicIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("epic.get")
	if !ok {
		t.Fatal("epic get is not registered")
	}
	conn, replayer := recordedConn(t, "epic-recorded.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud},
		Args: []string{"AGL-1"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if key, _ := doc.Record.AttrValue("key"); key != "AGL-1" {
		t.Errorf("key = %q, want AGL-1", key)
	}
	if done, _ := doc.Record.AttrValue("done"); done != "false" {
		t.Errorf("done = %q, want false", done)
	}
	summary, ok := doc.Record.ChildNamed("summary")
	if !ok {
		t.Fatal("the epic carried no summary")
	}
	if summary.Text != "Agile fixture epic" {
		t.Errorf("summary = %q", summary.Text)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the epic was never read: %v", unplayed)
	}
}
