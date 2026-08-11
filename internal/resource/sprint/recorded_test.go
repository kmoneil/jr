package sprint_test

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

// TestTheRecordedListingIsAConversationAServerHad establishes that the endpoint
// and the paging parameters this code builds are the ones a Cloud instance
// answered. The constructed fixtures beside it decide both halves of the
// exchange and so can establish only that the request is unchanged.
//
// The empty start and end columns are the point of recording this one in
// particular. A future sprint has not been started, so the server sends neither
// date, and the two columns have to come out empty rather than filled with a
// zero time — the shape a constructed fixture is least likely to contain,
// because whoever writes one writes a sprint that looks finished.
func TestTheRecordedListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.list")
	if !ok {
		t.Fatal("sprint list is not registered")
	}
	conn, replayer := recordedConn(t, "sprints-recorded.cloud.json")

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

	want := "id\tname\tstate\tstart\tend\n" +
		"1\tAGL Sprint 1\tfuture\t\t\n"
	if buf.String() != want {
		t.Errorf("listing =\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestTheRecordedSprintIsAConversationAServerHad covers the read by id, which
// takes no board: a sprint id addresses a sprint on its own.
func TestTheRecordedSprintIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.get")
	if !ok {
		t.Fatal("sprint get is not registered")
	}
	conn, replayer := recordedConn(t, "sprint-recorded.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud},
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
	if state, _ := doc.Record.AttrValue("state"); state != "future" {
		t.Errorf("state = %q, want future", state)
	}
	// The board the sprint came from, which the listing request never needed.
	if board, _ := doc.Record.AttrValue("board"); board != "3" {
		t.Errorf("board = %q, want 3", board)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the sprint was never read: %v", unplayed)
	}
}

// TestTheRecordedActiveSprintListIsAConversationAServerHad is the other half of
// the listing pair.
//
// sprints-recorded.cloud.json holds a `future` sprint, which carries neither
// date, and this one holds the same sprint started. Between them both
// renderings of the start and end columns are pinned against a real server —
// empty and populated — which one recording could not do and which the
// constructed fixtures get to decide for themselves.
func TestTheRecordedActiveSprintListIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.list")
	if !ok {
		t.Fatal("sprint list is not registered")
	}
	conn, replayer := recordedConn(t, "sprints-active-recorded.cloud.json")

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
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded page was never requested: %v", unplayed)
	}

	// Jira sends the dates with milliseconds and a Z; the renderer normalizes
	// to RFC 3339 seconds, and this is the only place that conversion is
	// checked against bytes a server actually sent.
	want := "id\tname\tstate\tstart\tend\n" +
		"1\tAGL Sprint 1\tactive\t2026-08-10T15:12:18Z\t2026-08-24T15:12:13Z\n"
	if buf.String() != want {
		t.Errorf("listing =\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestTheRecordedActiveSprintIsAConversationAServerHad covers the read by id
// with both dates set.
func TestTheRecordedActiveSprintIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.get")
	if !ok {
		t.Fatal("sprint get is not registered")
	}
	conn, replayer := recordedConn(t, "sprint-active-recorded.cloud.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud},
		Args: []string{"1"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "active" {
		t.Errorf("state = %q, want active", state)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the sprint was never read: %v", unplayed)
	}
}
