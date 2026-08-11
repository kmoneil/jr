package epic_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedDataCenterListingIsAConversationAServerHad establishes that the
// endpoint and the paging parameters this code builds against a Data Center
// deployment are the ones a Jira Software 10.4.0 instance answered.
//
// epics.datacenter.json beside it is worth keeping — it holds three epics whose
// ids and keys are chosen so a text sort of the key fails the ordering test, and
// no sandbox produces that arrangement on request. What it cannot establish is
// that `/rest/agile/1.0/board/1/epic?maxResults=50&startAt=0` is a request Data
// Center accepts, because its author wrote both halves of the exchange: a typo
// in the path or a Cloud-only paging parameter would replay exactly as happily.
//
// This is also the deployment where the whole listing arrives in one response
// with isLast true and no nextPage, so the recording pins the caller's view of
// an offset-paged API that answered in a single page.
func TestTheRecordedDataCenterListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("epic.list")
	if !ok {
		t.Fatal("epic list is not registered")
	}
	conn, replayer := recordedConn(t, "epics-recorded.datacenter.json")

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

	want := "key\tname\tsummary\tdone\n" +
		"ENG-1\tRecording the API\tRecord every Data Center conversation\tfalse\n"
	if buf.String() != want {
		t.Errorf("listing =\n%q\nwant\n%q", buf.String(), want)
	}
}

// TestTheRecordedDataCenterEpicIsAConversationAServerHad covers the read by key
// against the deployment whose agile endpoint this project had only hand-written
// evidence for.
//
// epic.datacenter.json proves the response is handled: it was written to carry a
// name and a summary that differ, so a reader collapsing the two fields fails.
// It cannot prove `/rest/agile/1.0/epic/ENG-1` is a path Data Center serves, or
// that a bare issue key — rather than the numeric id — is an address it accepts
// there. This recording is a server saying yes to both.
func TestTheRecordedDataCenterEpicIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("epic.get")
	if !ok {
		t.Fatal("epic get is not registered")
	}
	conn, replayer := recordedConn(t, "epic-recorded.datacenter.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter},
		Args: []string{"ENG-1"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if key, _ := doc.Record.AttrValue("key"); key != "ENG-1" {
		t.Errorf("key = %q, want ENG-1", key)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "10000" {
		t.Errorf("id = %q, want 10000", id)
	}
	if done, _ := doc.Record.AttrValue("done"); done != "false" {
		t.Errorf("done = %q, want false", done)
	}
	name, ok := doc.Record.ChildNamed("name")
	if !ok {
		t.Fatal("the epic carried no name")
	}
	if name.Text != "Recording the API" {
		t.Errorf("name = %q", name.Text)
	}
	summary, ok := doc.Record.ChildNamed("summary")
	if !ok {
		t.Fatal("the epic carried no summary")
	}
	if summary.Text != "Record every Data Center conversation" {
		t.Errorf("summary = %q", summary.Text)
	}
	// The name and the summary are different fields, and this server returned
	// different values for them.
	if name.Text == summary.Text {
		t.Error("name and summary collapsed into one value")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the epic was never read: %v", unplayed)
	}
}
