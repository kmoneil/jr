//go:build write

package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// planVerbsRecorder counts what reached the server, keyed by issue.
type planVerbsRecorder struct {
	mu      sync.Mutex
	moves   map[string]string // key -> transition id sent
	assigns map[string]string // key -> assignee value sent
}

// planVerbsJira serves three issues whose workflows differ: ENG-1 and ENG-3
// can Done, under different ids, and ENG-2 cannot. That difference is the
// whole reason a move plan resolves per row.
func planVerbsJira(t *testing.T, rec *planVerbsRecorder) string {
	t.Helper()
	const updated = `"2026-09-23T10:00:00.000+0000"`
	transitions := map[string]string{
		"ENG-1": `{"transitions":[{"id":"31","name":"Done","to":{"id":"6","name":"Done","statusCategory":{"key":"done","name":"Done"}}}]}`,
		"ENG-2": `{"transitions":[{"id":"21","name":"Start Progress","to":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate","name":"In Progress"}}}]}`,
		"ENG-3": `{"transitions":[{"id":"33","name":"Done","to":{"id":"6","name":"Done","statusCategory":{"key":"done","name":"Done"}}}]}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := ""
		if i := strings.Index(r.URL.Path, "/issue/"); i >= 0 {
			key = strings.SplitN(r.URL.Path[i+len("/issue/"):], "/", 2)[0]
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"10.4.0","deploymentType":"Server",` +
				`"serverTime":"2026-09-23T12:00:00.000+0000"}`))
		case strings.Contains(r.URL.Path, "/user/"):
			_, _ = w.Write([]byte(`[{"name":"ada","displayName":"Ada Lovelace",` +
				`"active":true}]`))
		case strings.HasSuffix(r.URL.Path, "/search"):
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":3,"issues":[` +
				`{"id":"1","key":"ENG-1","fields":{"updated":` + updated + `}},` +
				`{"id":"2","key":"ENG-2","fields":{"updated":` + updated + `}},` +
				`{"id":"3","key":"ENG-3","fields":{"updated":` + updated + `}}]}`))
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(transitions[key]))
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == http.MethodPost:
			var body struct {
				Transition struct {
					ID string `json:"id"`
				} `json:"transition"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.mu.Lock()
			rec.moves[key] = body.Transition.ID
			rec.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/assignee") && r.Method == http.MethodPut:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.mu.Lock()
			rec.assigns[key], _ = body["name"].(string)
			rec.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case key != "" && r.Method == http.MethodGet:
			// The apply's own staleness check reads the issue back.
			_, _ = w.Write([]byte(`{"id":"9","key":"` + key +
				`","fields":{"updated":` + updated + `}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestMovePlanResolvesEachWorkflowsOwnTransition is the card's done-when: a
// plan built from issue move records the id each row's workflow resolved, or
// the reason it could not, and sends nothing mutating while doing it.
func TestMovePlanResolvesEachWorkflowsOwnTransition(t *testing.T) {
	rec := &planVerbsRecorder{moves: map[string]string{}, assigns: map[string]string{}}
	url := planVerbsJira(t, rec)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
	path := filepath.Join(t.TempDir(), "move.plan.xml")

	got := run(t, env, "issue", "move", "ENG-1", "ENG-2", "ENG-3", "Done",
		"--plan-out", path)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	for _, want := range []string{
		`kind="issue.plan" v="2"`, `verb="issue.move"`,
		"<transition>Done</transition>",
		`transition="31"`, `transition="33"`,
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the plan does not carry %s:\n%s", want, got.stdout)
		}
	}
	if n := strings.Count(got.stdout, `blocked="`); n != 1 {
		t.Errorf("blocked rows = %d, want exactly ENG-2:\n%s", n, got.stdout)
	}
	if len(rec.moves) != 0 {
		t.Errorf("planning sent a transition: %v", rec.moves)
	}
}

// TestMoveApplyMovesWhatItCanAndOnlyOnce: the movable rows move, the blocked
// row fails without a request, and a second run replays nothing.
func TestMoveApplyMovesWhatItCanAndOnlyOnce(t *testing.T) {
	rec := &planVerbsRecorder{moves: map[string]string{}, assigns: map[string]string{}}
	url := planVerbsJira(t, rec)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
	path := filepath.Join(t.TempDir(), "move.plan.xml")
	mustRun(t, env, "issue", "move", "ENG-1", "ENG-2", "ENG-3", "Done",
		"--plan-out", path)

	got := run(t, env, "issue", "move", "--apply", path)
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want 2: the blocked row is the cause; stderr = %s",
			got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, `kind="issue.apply" v="2"`) {
		t.Errorf("the outcome is not an issue.apply v2 document:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "TRANSITION_UNAVAILABLE") {
		t.Errorf("the cause does not name the blocked row:\n%s", got.stderr)
	}
	if rec.moves["ENG-1"] != "31" || rec.moves["ENG-3"] != "33" {
		t.Errorf("moves sent = %v, want ENG-1:31 and ENG-3:33", rec.moves)
	}
	if _, sent := rec.moves["ENG-2"]; sent {
		t.Errorf("the blocked row reached the server")
	}

	before := len(rec.moves)
	again := run(t, env, "issue", "move", "--apply", path)
	if again.exit != exitcode.Usage {
		t.Fatalf("re-run exit = %v, want 2; stderr = %s", again.exit, again.stderr)
	}
	if n := strings.Count(again.stdout, `outcome="skipped"`); n != 2 {
		t.Errorf("re-run skipped %d rows, want the 2 that landed:\n%s", n, again.stdout)
	}
	if len(rec.moves) != before {
		t.Errorf("the re-run sent a transition again: %v", rec.moves)
	}
}

// TestAssignPlanRecordsTheResolvedPerson: the plan carries the id the
// directory resolved, not the name typed, so the same plan means the same
// person on every day it is applied.
func TestAssignPlanRecordsTheResolvedPerson(t *testing.T) {
	rec := &planVerbsRecorder{moves: map[string]string{}, assigns: map[string]string{}}
	url := planVerbsJira(t, rec)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
	path := filepath.Join(t.TempDir(), "assign.plan.xml")

	got := run(t, env, "issue", "assign", "ENG-1", "ENG-2", "Ada Lovelace",
		"--plan-out", path)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	for _, want := range []string{`verb="issue.assign"`, "<assignee>ada</assignee>"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the plan does not carry %s:\n%s", want, got.stdout)
		}
	}

	applied := run(t, env, "issue", "assign", "--apply", path)
	if applied.exit != exitcode.OK {
		t.Fatalf("apply exit = %v, stderr = %s", applied.exit, applied.stderr)
	}
	if rec.assigns["ENG-1"] != "ada" || rec.assigns["ENG-2"] != "ada" {
		t.Errorf("assignments sent = %v, want ada on both", rec.assigns)
	}
}

// TestApplyRefusesAPlanForAnotherVerb: a move plan is not an edit, and
// reinterpreting its change set would be the incumbent's habit.
func TestApplyRefusesAPlanForAnotherVerb(t *testing.T) {
	rec := &planVerbsRecorder{moves: map[string]string{}, assigns: map[string]string{}}
	url := planVerbsJira(t, rec)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
	path := filepath.Join(t.TempDir(), "move.plan.xml")
	mustRun(t, env, "issue", "move", "ENG-1", "ENG-2", "ENG-3", "Done",
		"--plan-out", path)

	got := run(t, env, "issue", "edit", "--apply", path)
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want 2; stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "not for") {
		t.Errorf("the refusal does not name the verb mismatch:\n%s", got.stderr)
	}
	if len(rec.moves) != 0 {
		t.Errorf("a refused apply reached the server: %v", rec.moves)
	}
}
