package board_test

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

// TestTheRecordedListingIsAConversationAServerHad is what the constructed
// fixtures beside it cannot be.
//
// Those are worth keeping: boards.cloud.json returns three boards out of
// numeric order and pages, shapes chosen to catch a text sort and a dropped
// page, and no sandbox produces them on request. What they cannot establish is
// that the request is one Jira accepts, because their author decided both
// halves of the exchange.
//
// This establishes that and little else. The replayer matches on path and
// query, so a rendered row means the endpoint and the paging parameters this
// code builds are the ones a Cloud instance answered.
func TestTheRecordedListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("board.list")
	if !ok {
		t.Fatal("board list is not registered")
	}
	conn, replayer := recordedConn(t, "boards-recorded.cloud.json")

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.Cloud},
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

	want := "id\tname\ttype\tproject\n" +
		"1\tOPS board\tsimple\tOPS\n" +
		"2\tENG board\tsimple\tENG\n" +
		"3\tAGL board\tscrum\tAGL\n"
	if buf.String() != want {
		t.Errorf("listing =\n%s\nwant\n%s", buf.String(), want)
	}
}

// TestTheRecordedBoardIsAConversationAServerHad covers the read by id, and the
// board type this project had no recording of at all. Every Cloud board in the
// constructed fixtures is simple, because until a scrum board existed in the
// sandbox there was nothing else to read.
func TestTheRecordedBoardIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("board.get")
	if !ok {
		t.Fatal("board get is not registered")
	}
	conn, replayer := recordedConn(t, "board-recorded.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud},
		Args: []string{"3"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "3" {
		t.Errorf("id = %q, want 3", id)
	}
	if kind, _ := doc.Record.AttrValue("type"); kind != "scrum" {
		t.Errorf("type = %q, want scrum", kind)
	}
	project, ok := doc.Record.ChildNamed("project")
	if !ok {
		t.Fatal("the board named no project")
	}
	if project.Text != "AGL" {
		t.Errorf("project = %q, want AGL", project.Text)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the board was never read: %v", unplayed)
	}
}
