//go:build write

package workflow_test

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
	"github.com/kmoneil/jira-cli/internal/workflow"
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

// runRecorded drives a verb against a Data Center recording. The site kind is
// what selects the batched endpoint these cassettes hold, so it is not a
// detail: on Cloud the same two epic verbs take the per-issue parent path and
// would never reach this conversation at all.
func runRecorded(
	t *testing.T, command, fixture string, args []string,
) (*render.Doc, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	conn, replayer := recordedConn(t, fixture)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: args,
		Flags: registry.NewFlags(), Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded request was never sent: %v", unplayed)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request went somewhere the cassette does not answer: %v",
			unmatched)
	}
	return doc, replayer
}

// TestTheRecordedSprintMoveIsAConversationAServerHad is what
// sprintadd.datacenter.json beside it cannot be.
//
// That one is constructed, so it pins the request against change and nothing
// more: its author decided both halves, and a batched POST that a real Data
// Center refuses replays green forever. This one is the exchange a Jira
// Software Data Center 10.4.0 actually had — the replayer matches method, path
// and canonicalised body, so a rendered acknowledgement means
// POST /rest/agile/1.0/sprint/1/issue with {"issues":["ENG-2"]} is a request
// that instance accepted, and answered 204.
func TestTheRecordedSprintMoveIsAConversationAServerHad(t *testing.T) {
	doc, _ := runRecorded(t, "sprint.add", "sprintadd-recorded.datacenter.json",
		[]string{"1", "ENG-2"})

	if doc.Record.Name != "sprint" {
		t.Errorf("element = %q, want sprint", doc.Record.Name)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "1" {
		t.Errorf("id = %q, want 1 — the sprint the recording moved into", id)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "added" {
		t.Errorf("action = %q, want added", action)
	}
	issues, ok := doc.Record.ChildNamed("issues")
	if !ok {
		t.Fatal("the document named no issues")
	}
	if len(issues.Children) != 1 {
		t.Fatalf("got %d issues, want the 1 the recording carries", len(issues.Children))
	}
	if key, _ := issues.Children[0].AttrValue("key"); key != "ENG-2" {
		t.Errorf("key = %q, want ENG-2", key)
	}
}

// TestTheRecordedEpicMoveIsAConversationAServerHad covers the endpoint this
// project had most reason to doubt.
//
// POST /rest/agile/1.0/epic/{ref}/issue was believed good on Cloud for two
// releases on the strength of a constructed cassette, and Cloud refuses it for
// team-managed projects; the code now takes the parent path there and keeps the
// batched one only for Data Center. Nothing had ever shown that the surviving
// half was accepted either. This is that: a real 10.4.0 took the epic key in
// the path and the issue key in the body, and answered 204.
func TestTheRecordedEpicMoveIsAConversationAServerHad(t *testing.T) {
	doc, _ := runRecorded(t, "epic.add", "epicadd-recorded.datacenter.json",
		[]string{"ENG-1", "ENG-3"})

	if doc.Record.Name != "epic" {
		t.Errorf("element = %q, want epic", doc.Record.Name)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "ENG-1" {
		t.Errorf("id = %q, want ENG-1 — the epic the recording moved into", id)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "added" {
		t.Errorf("action = %q, want added", action)
	}
	if got, _ := doc.Record.AttrValue("requested"); got != "1" {
		t.Errorf("requested = %q, want 1", got)
	}
	if got, _ := doc.Record.AttrValue("applied"); got != "1" {
		t.Errorf("applied = %q, want 1 — one 204 covered the whole batch", got)
	}
	issues, ok := doc.Record.ChildNamed("issues")
	if !ok {
		t.Fatal("the document named no issues")
	}
	if len(issues.Children) != 1 {
		t.Fatalf("got %d issues, want the 1 the recording carries", len(issues.Children))
	}
	key, _ := issues.Children[0].AttrValue("key")
	status, _ := issues.Children[0].AttrValue("status")
	if key != "ENG-3" || status != workflow.StatusMoved {
		t.Errorf("issue = %s/%s, want ENG-3/%s", key, status, workflow.StatusMoved)
	}
}

// TestTheRecordedEpicRemovalIsAConversationAServerHad is the one spelling no
// constructed fixture could have vouched for.
//
// Leaving an epic is written as joining `none` — a literal in the path, not a
// key, and not something the API documents in a way that settles it. A
// hand-written cassette answers 204 to whatever word its author chose, so
// epicremove.datacenter.json would replay green if the word were wrong. This
// records a real 10.4.0 accepting POST /rest/agile/1.0/epic/none/issue, which
// is the only evidence that the sentinel is the one the server knows.
func TestTheRecordedEpicRemovalIsAConversationAServerHad(t *testing.T) {
	doc, _ := runRecorded(t, "epic.remove", "epicremove-recorded.datacenter.json",
		[]string{"ENG-3"})

	if doc.Record.Name != "epic" {
		t.Errorf("element = %q, want epic", doc.Record.Name)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "none" {
		t.Errorf("id = %q, want none — the removal names no epic", id)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "removed" {
		t.Errorf("action = %q, want removed", action)
	}
	if got, _ := doc.Record.AttrValue("requested"); got != "1" {
		t.Errorf("requested = %q, want 1", got)
	}
	if got, _ := doc.Record.AttrValue("applied"); got != "1" {
		t.Errorf("applied = %q, want 1", got)
	}
	issues, ok := doc.Record.ChildNamed("issues")
	if !ok {
		t.Fatal("the document named no issues")
	}
	if len(issues.Children) != 1 {
		t.Fatalf("got %d issues, want the 1 the recording carries", len(issues.Children))
	}
	key, _ := issues.Children[0].AttrValue("key")
	status, _ := issues.Children[0].AttrValue("status")
	if key != "ENG-3" || status != workflow.StatusMoved {
		t.Errorf("issue = %s/%s, want ENG-3/%s", key, status, workflow.StatusMoved)
	}
}
