package board_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedDataCenterListingIsAConversationAServerHad is the half of the
// board listing that had no evidence behind it until a Data Center existed to
// answer it.
//
// boards.datacenter.json is worth keeping — three boards out of numeric order,
// every one of them located on a project, a shape chosen to catch a text sort —
// but its author wrote both halves of the exchange, so it establishes that a
// response is handled and never that Jira accepts the request. The replayer
// matches on path and query, so a rendered row here means /rest/agile/1.0/board
// with startAt and maxResults is what a 10.4.0 instance answered, rather than
// what this code assumed it would.
//
// The recording also carries what the constructed fixture chose not to: a board
// with no location at all. Data Center returns one for a board on a person, and
// the empty project column below is the server's answer, not a stub's.
func TestTheRecordedDataCenterListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("board.list")
	if !ok {
		t.Fatal("board list is not registered")
	}
	conn, replayer := recordedConn(t, "boards-recorded.datacenter.json")

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter},
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

	// One board, and it is the only one the instance had. Asserting three
	// would be asserting a sandbox nobody recorded.
	want := "id\tname\ttype\tproject\n" +
		"1\tENG board\tscrum\t\n"
	if buf.String() != want {
		t.Errorf("listing =\n%s\nwant\n%s", buf.String(), want)
	}
}

// TestTheRecordedDataCenterBoardIsAConversationAServerHad covers the read by
// id against a real Data Center.
//
// board.datacenter.json answers /rest/agile/1.0/board/99 because its author
// wrote that path, which is exactly the thing under test: the agile base and
// the platform base differ, and a fixture that agrees with the code by
// construction cannot tell you which one the server honours. This one can.
//
// It asserts no project element, because the recorded board carries no
// location. That is the honest reading of the response rather than an invented
// key, and it is the same absence userboard.datacenter.json was hand-written to
// describe — now with a server behind it.
func TestTheRecordedDataCenterBoardIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("board.get")
	if !ok {
		t.Fatal("board get is not registered")
	}
	conn, replayer := recordedConn(t, "board-recorded.datacenter.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter},
		Args: []string{"1"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "1" {
		t.Errorf("id = %q, want 1", id)
	}
	if kind, _ := doc.Record.AttrValue("type"); kind != "scrum" {
		t.Errorf("type = %q, want scrum", kind)
	}
	name, ok := doc.Record.ChildNamed("name")
	if !ok {
		t.Fatal("the board had no name")
	}
	if name.Text != "ENG board" {
		t.Errorf("name = %q, want ENG board", name.Text)
	}
	if _, ok := doc.Record.ChildNamed("project"); ok {
		t.Error("a board the server placed nowhere was rendered with a project")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the board was never read: %v", unplayed)
	}
}
