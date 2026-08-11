//go:build write && admin

package sprint_test

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/sprint"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestCloseReadsBeforeItWrites is the shape of every refusal in this tool: the
// sprint is fetched first, so a state that cannot be closed costs one read and
// no mutation.
func TestCloseReadsBeforeItWrites(t *testing.T) {
	doc, replayer := runClose(t, "close", []string{"100"}, false)

	if id, _ := doc.Record.AttrValue("id"); id != "100" {
		t.Errorf("id = %q", id)
	}
	if state, _ := doc.Record.AttrValue("state"); state != "closed" {
		t.Errorf("state = %q", state)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "closed" {
		t.Errorf("action = %q", action)
	}
	// The name comes from the read, which is why the read is worth doing: the
	// acknowledgement says which sprint ended rather than echoing back the id
	// the caller already had.
	name, ok := doc.Record.ChildNamed("name")
	if !ok || name.Text != "ENG Sprint 4" {
		t.Errorf("name = %+v, want the sprint the read reported", name)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the close did not make both calls: %v", unplayed)
	}
}

// TestASprintThatHasNotStartedIsNotClosed covers the precondition. The cassette
// carries no POST, so a close that went ahead anyway fails to match rather than
// passing quietly.
func TestASprintThatHasNotStartedIsNotClosed(t *testing.T) {
	cmd, _ := registry.Lookup("sprint.close")
	conn, replayer := replayConn(t, "closefuture.datacenter.json")
	flags := registry.NewFlags()
	flags.SetBool("yes", true)

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: []string{"99"},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("a future sprint was closed")
	}
	e := errs.Coerce(err)
	if e.Code != "SPRINT_NOT_ACTIVE" {
		t.Fatalf("code = %q, want SPRINT_NOT_ACTIVE", e.Code)
	}
	// The two wrong states need different remedies: one has not happened yet
	// and the other already has.
	if !strings.Contains(e.Remedy, "started") {
		t.Errorf("remedy = %q, want it to say the sprint has not started", e.Remedy)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made past the refusal: %v", unmatched)
	}
}

// TestDryRunPrintsTheRequestAndSendsNothing covers the flag on every mutating
// verb. The read still happens, so a dry run also tells you whether the close
// would be allowed.
func TestDryRunPrintsTheRequestAndSendsNothing(t *testing.T) {
	doc, replayer := runClose(t, "close", []string{"100"}, true)

	if doc.Kind != registry.DryRunOutput().Kind {
		t.Errorf("kind = %q, want the dry-run kind", doc.Kind)
	}
	// The POST is still in the cassette and was never played, which is the
	// assertion: nothing was sent.
	unplayed := replayer.Unplayed()
	if len(unplayed) != 1 || !strings.HasPrefix(unplayed[0], "POST ") {
		t.Errorf("unplayed = %v, want exactly the POST", unplayed)
	}
	// The body it prints is the body it would have sent, not a description of
	// one — a second rendering of the request would drift from the first.
	// The preview is a list even when it holds one request, so a consumer never
	// has to branch on the count.
	if len(doc.Record.Children) != 1 {
		t.Fatalf("got %d requests, want the one this sends", len(doc.Record.Children))
	}
	body, ok := doc.Record.Children[0].ChildNamed("body")
	if !ok {
		t.Fatal("the dry run printed no body")
	}
	if body.Text != `{"state":"closed"}` {
		t.Errorf("body = %q, want the bytes that would have gone out", body.Text)
	}
}

// TestTheRequestIsAPartialUpdate pins the method. A PUT to a sprint is a full
// replacement, and every field left out of it — the name, the goal, the dates —
// would be cleared.
func TestTheRequestIsAPartialUpdate(t *testing.T) {
	conn, _ := replayConn(t, "close.datacenter.json")
	client := &sprint.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	req, err := client.CloseRequest("100")
	if err != nil {
		t.Fatalf("close request: %v", err)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST: a PUT would clear every field it omits",
			req.Method)
	}
	if req.Path != "/rest/agile/1.0/sprint/100" {
		t.Errorf("path = %q, want the agile API", req.Path)
	}
	if got := string(req.Body); got != `{"state":"closed"}` {
		t.Errorf("body = %s, want only the field that changes", got)
	}
}

// TestCloseIsDeclaredForWhatItDoes is the declaration the CLI enforces from:
// read-only mode, --yes, and the tag gate all come from here.
func TestCloseIsDeclaredForWhatItDoes(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.close")
	if !ok {
		t.Fatal("sprint close is not registered")
	}
	if !cmd.Mutating {
		t.Error("sprint close is not marked mutating, so read-only would not refuse it")
	}
	if !cmd.Destructive {
		t.Error("sprint close is not marked destructive, so --yes would not be required")
	}
	// Both tags, and for different reasons: write because it changes Jira,
	// admin because of what it changes. An agent build has write and not admin.
	for _, tag := range []string{"write", "admin"} {
		if !slices.Contains(cmd.RequiresTags, tag) {
			t.Errorf("sprint close does not require %q; it requires %v",
				tag, cmd.RequiresTags)
		}
	}
}

// runClose runs `sprint close 100` against the close cassette, with --yes so
// the confirmation gate — which lives in the CLI layer, not here — is not what
// is being tested.
func runClose(
	t *testing.T, fixture string, args []string, dryRun bool,
) (*render.Doc, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("sprint.close")
	if !ok {
		t.Fatal("sprint close is not registered")
	}
	conn, replayer := replayConn(t, fixture+".datacenter.json")
	flags := registry.NewFlags()
	flags.SetBool("yes", true)
	flags.SetBool("dry-run", dryRun)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: args,
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	return doc, replayer
}

// TestTheRecordedCloseIsAConversationAServerHad is the last verb in this tree
// to have been run against a real Jira, and it was run exactly once.
//
// Closing a sprint cannot be undone — the API offers no way to reopen one — so
// this recording cost the sandbox's only sprint, and re-recording it means
// somebody making a new one in the UI, since `jr` has no `sprint start`. That is
// the reason it stayed unrecorded through four sessions of gate-building while
// everything around it was covered.
//
// What it establishes over close.datacenter.json beside it: that the two-step
// this command performs is the two-step Jira expects, and that the POST answers
// **200 with a body** rather than the 204 the other agile writes give. The
// constructed fixture had that right, which is worth saying out loud — three
// others in this tree did not, and nothing but a recording could tell them
// apart.
func TestTheRecordedCloseIsAConversationAServerHad(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "close-recorded.cloud.json"),
	)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cassette.Evidence() {
		t.Fatal("close-recorded.cloud.json is not a recording, so replaying it " +
			"establishes nothing about the API")
	}

	cmd, ok := registry.Lookup("sprint.close")
	if !ok {
		t.Fatal("sprint close is not registered")
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	flags := registry.NewFlags()
	flags.SetBool("yes", true)
	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud}, Args: []string{"1"},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the requests this code builds are not the ones the server "+
			"answered: %v", err)
	}

	if state, _ := doc.Record.AttrValue("state"); state != "closed" {
		t.Errorf("state = %q, want closed", state)
	}
	if action, _ := doc.Record.AttrValue("action"); action != "closed" {
		t.Errorf("action = %q, want closed", action)
	}
	// The name comes from the read, which is the point of reading first: the
	// acknowledgement names the iteration that ended rather than echoing the id
	// back at the caller who typed it.
	name, ok := doc.Record.ChildNamed("name")
	if !ok || name.Text != "AGL Sprint 1" {
		t.Errorf("name = %+v, want the sprint the read reported", name)
	}
	// Both interactions, in order: a close that skipped the precondition read
	// would leave the GET unplayed.
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the close did not make both calls: %v", unplayed)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request went somewhere the cassette does not answer: %v",
			unmatched)
	}
}

// TestTheCloseAnswersTwoHundredWithABody pins the divergence a caller would
// otherwise meet at runtime.
//
// Every other agile write here answers 204 and nothing, and `send` is written
// for that: it reads the status and discards the body. This one answers 200 and
// a sprint, so a future change that started parsing the response of an agile
// write would find one endpoint behaving differently from its neighbours. The
// recording is where that fact lives.
func TestTheCloseAnswersTwoHundredWithABody(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "close-recorded.cloud.json"),
	)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cassette.Interactions) != 2 {
		t.Fatalf("got %d interactions, want the read and the write",
			len(cassette.Interactions))
	}

	write := cassette.Interactions[1]
	if write.Request.Method != transport.MethodPost {
		t.Errorf("method = %q, want POST", write.Request.Method)
	}
	if write.Response.Status != 200 {
		t.Errorf("status = %d, want 200 — this one is not a 204 like its "+
			"neighbours", write.Response.Status)
	}
	if !strings.Contains(write.Response.Body, "completeDate") {
		t.Errorf("the response carries no completeDate, so the 200 body is not "+
			"the sprint Jira sent back: %s", write.Response.Body)
	}
}
