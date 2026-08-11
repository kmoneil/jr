package sprint_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
)

// The Data Center half of recorded_test.go. recordedConn is declared there and
// reused here: the refusal of a cassette that is not `Evidence()` is the whole
// reason these files are separate from the constructed ones, and it should have
// exactly one implementation.

// TestTheRecordedDataCenterListingIsAConversationAServerHad establishes that the
// endpoint and the offset paging parameters this code builds against Data Center
// are the ones a Jira Software 10.4.0 instance answered.
//
// sprints.datacenter.json beside it cannot establish that. Its author wrote both
// halves of the exchange, so it holds `maxResults=50&startAt=0` because this code
// sends `maxResults=50&startAt=0` — it proves the response is handled and never
// that the request is accepted. Data Center is where that gap matters most: the
// deployment takes offsets rather than a cursor, and a query the server ignores
// looks identical to one it honours until the second page.
//
// The rendered row is what the server actually returned: a `future` sprint with
// neither date, so both time columns have to come out empty rather than filled
// with a zero time.
func TestTheRecordedDataCenterListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.list")
	if !ok {
		t.Fatal("sprint list is not registered")
	}
	conn, replayer := recordedConn(t, "sprints-recorded.datacenter.json")

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter, board: "1"},
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
		"1\tENG Sprint 1\tfuture\t\t\n"
	if buf.String() != want {
		t.Errorf("listing =\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestTheRecordedDataCenterSprintIsAConversationAServerHad covers the read by id
// on Data Center, which takes no board: a sprint id addresses a sprint on its
// own, and the board comes back on the record from `originBoardId`.
//
// What sprint.datacenter.json beside it cannot show is which fields a real 10.4.0
// sends. The constructed one carries a `goal` and two dates because somebody
// wrote a sprint that looks finished; the server sent an unstarted sprint with no
// dates and no goal at all, plus `synced` and `autoStartStop` — Data Center-only
// fields nobody had thought to construct. Decoding has to tolerate both.
func TestTheRecordedDataCenterSprintIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.get")
	if !ok {
		t.Fatal("sprint get is not registered")
	}
	conn, replayer := recordedConn(t, "sprint-recorded.datacenter.json")

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
	name, ok := doc.Record.ChildNamed("name")
	if !ok {
		t.Fatal("the sprint carried no name")
	}
	if name.Text != "ENG Sprint 1" {
		t.Errorf("name = %q, want ENG Sprint 1", name.Text)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "future" {
		t.Errorf("state = %q, want future", state)
	}
	// originBoardId, which the read by id never had to be told.
	if board, _ := doc.Record.AttrValue("board"); board != "1" {
		t.Errorf("board = %q, want 1", board)
	}
	// The server sent no dates for an unstarted sprint, so the record carries
	// none: an absent date is absent, not a zero time.
	if start, ok := doc.Record.ChildNamed("start"); ok {
		t.Errorf("start = %q, want no start element at all", start.Text)
	}
	if end, ok := doc.Record.ChildNamed("end"); ok {
		t.Errorf("end = %q, want no end element at all", end.Text)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the sprint was never read: %v", unplayed)
	}
}
