//go:build write

package issue_test

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// screenConn is a Data Center connection whose only answer is the edit screen
// of key, holding ids. For a test about something an edit carries rather than
// about the screen, which a Data Center edit now reads before it builds a
// preview.
func screenConn(key string, ids ...string) *transport.Client {
	fields := map[string]any{}
	for _, id := range ids {
		fields[id] = map[string]any{"name": id, "key": id, "operations": []string{"set"}}
	}
	body, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		panic(err)
	}
	replayer := transport.NewReplayer(&transport.Cassette{
		Deployment: transport.DataCenter, Source: transport.Constructed,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: "GET", Path: "/rest/api/2/issue/" + key + "/editmeta",
			},
			Response: transport.RecordedResponse{Status: 200, Body: string(body)},
		}},
	})
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		panic(err)
	}
	return conn
}

// everyWritableField is a screen holding every field writeCatalogueJSON names
// and every field a typed flag on issue edit sets.
func everyWritableField() []string {
	var catalogue []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(writeCatalogueJSON), &catalogue); err != nil {
		panic(err)
	}
	ids := []string{"summary", "description", "priority", "labels", "assignee", "parent"}
	for _, f := range catalogue {
		ids = append(ids, f.ID)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// offScreenEdit runs issue 291's command against a cassette: a new summary and
// one --field the issue's edit screen does not hold.
func offScreenEdit(
	t *testing.T, kind site.Kind, fixture, field string, dryRun bool,
) (*render.Doc, *transport.Replayer, error) {
	t.Helper()
	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	conn, replayer := replayConn(t, fixture)
	flags := registry.NewFlags()
	flags.SetString("summary", "New summary")
	flags.SetString("field", field)
	flags.SetBool("dry-run", dryRun)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: writeCatalogueJSON}, conn: conn, kind: kind,
		},
		Args: []string{"ENG-101"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	doc, err := cmd.Run(t.Context(), inv)
	return doc, replayer, err
}

// putSent reports whether a run sent the edit, read off what it left unplayed.
func putSent(replayer *transport.Replayer) bool {
	for _, u := range replayer.Unplayed() {
		if strings.HasPrefix(u, "PUT") {
			return false
		}
	}
	return true
}

// TestAFieldOffTheEditScreenIsRefusedBeforeSending is issue 291 as filed. The
// cassette's PUT is answered with the refusal Data Center gives a field that
// is not on the edit screen, so code that sends it runs to the end and is
// wrong: BAD_REQUEST, the summary dropped with it, and no word on whether
// anything landed.
func TestAFieldOffTheEditScreenIsRefusedBeforeSending(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		_, replayer, err := offScreenEdit(t, site.DataCenter,
			"edit-offscreen.datacenter.json", "Acceptance Criteria=* criterion one", dryRun)
		if err == nil {
			t.Fatalf("dry-run=%v: a field the edit screen does not hold was accepted", dryRun)
		}
		e := errs.Coerce(err)
		if e.Code != "FIELD_NOT_ON_SCREEN" {
			t.Fatalf("dry-run=%v: code = %q, want FIELD_NOT_ON_SCREEN: %v", dryRun, e.Code, err)
		}
		if e.Exit != exitcode.Usage {
			t.Errorf("dry-run=%v: exit = %d, want %d", dryRun, e.Exit, exitcode.Usage)
		}
		if putSent(replayer) {
			t.Errorf("dry-run=%v: the edit was sent", dryRun)
		}
		if !strings.Contains(e.Message, "customfield_13256 (Acceptance Criteria)") ||
			!strings.Contains(e.Message, "ENG-101") {
			t.Errorf("dry-run=%v: message = %q, want the field by id and name, and the issue",
				dryRun, e.Message)
		}
		// The screen it does hold, so the caller can see what an edit can set
		// without a second command.
		if !strings.Contains(e.Detail, "summary") || !strings.Contains(e.Detail, "labels") {
			t.Errorf("dry-run=%v: detail = %q, want the fields the screen holds", dryRun, e.Detail)
		}
		if !strings.Contains(e.Remedy, "nothing was sent") ||
			!strings.Contains(e.Remedy, "customfield_13256") {
			t.Errorf("dry-run=%v: remedy = %q, want it to say nothing was sent and what to drop",
				dryRun, e.Remedy)
		}
	}
}

// TestAnEditOnTheScreenStillSends is the other side: the screen read is a
// check, not a toll, and an edit it holds goes out as before.
func TestAnEditOnTheScreenStillSends(t *testing.T) {
	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	conn, replayer := replayConn(t, "edit.datacenter.json")
	flags := registry.NewFlags()
	flags.SetString("summary", "a better summary")
	inv := &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter},
		Args: []string{"ENG-101"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := cmd.Run(t.Context(), inv); err != nil {
		t.Fatalf("run: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the edit never asked for: %v", unplayed)
	}
}

// TestCloudEditsAreNotHeldToEditmeta is the measurement that kept the check
// off Cloud. On the sandbox on 2026-11-10 a PUT set and stored fields the
// issue's editmeta did not list, so a Cloud edit held to editmeta would be
// refused a write Cloud makes. The cassette holds no editmeta: reading one
// here is a FIXTURE_MISS, and refusing is the wrong code. A plain number
// field, because that is one of the kinds measured.
func TestCloudEditsAreNotHeldToEditmeta(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		_, replayer, err := offScreenEdit(t, site.Cloud,
			"edit-offscreen.cloud.json", "Story Points=5", dryRun)
		if err != nil {
			t.Fatalf("dry-run=%v: %v", dryRun, err)
		}
		if got := putSent(replayer); got == dryRun {
			t.Errorf("dry-run=%v: sent = %v", dryRun, got)
		}
	}
}

// TestAnEmptyEditScreenSaysSo is the screen a status that allows no editing
// answers with. Every field is off it, and the refusal says the screen is
// empty rather than listing nothing after "holds".
func TestAnEmptyEditScreenSaysSo(t *testing.T) {
	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("summary", "a better summary")
	flags.SetBool("dry-run", true)
	inv := &registry.Invocation{
		Jira: &stubSession{conn: screenConn("ENG-101"), kind: site.DataCenter},
		Args: []string{"ENG-101"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	_, err := cmd.Run(t.Context(), inv)
	if err == nil {
		t.Fatal("an edit against an empty edit screen was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "FIELD_NOT_ON_SCREEN" {
		t.Fatalf("code = %q, want FIELD_NOT_ON_SCREEN: %v", e.Code, err)
	}
	if !strings.Contains(e.Detail, "holds no fields at all") {
		t.Errorf("detail = %q", e.Detail)
	}
}
