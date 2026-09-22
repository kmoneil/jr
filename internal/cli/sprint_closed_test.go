//go:build write

package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// closedSprintJira refuses a move the way Jira 10.4.0 does, measured on the
// rig: a 400 whose message names the state and not a field.
//
// `state` is served from the sprint endpoint so the refusal can say which state
// the sprint is actually in, rather than reading it out of the error text.
func closedSprintJira(t *testing.T, state string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"10.4.0","deploymentType":"Server",` +
				`"serverTime":"2026-09-22T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/issue"):
			// The move. Jira's own words, from the rig.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorMessages":` +
				`["You must specify a sprint which has not been completed."],` +
				`"errors":{}}`))
		default:
			// The sprint itself.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"name":"ENG Sprint 1","state":"` + state + `",` +
				`"originBoardId":1}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestAddingToAClosedSprintSaysTheSprintIsClosed is the surviving half of issue
// 155. The reporter said a stale sprint id "fails silently"; measured against a
// real Data Center it fails loudly, and the issue does not move. What was wrong
// is the shape of the refusal.
//
// It arrived as a generic BAD_REQUEST at exit 2, with a remedy reading "the
// detail below comes from Jira and names the offending field" when nothing is
// wrong with a field. `sprint start` and `sprint close` both have typed
// refusals naming the state; `sprint add` had none.
func TestAddingToAClosedSprintSaysTheSprintIsClosed(t *testing.T) {
	url := closedSprintJira(t, "closed")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "sprint", "add", "1", "ENG-3")

	if got.exit != exitcode.Conflict {
		t.Errorf("exit = %v, want 7 (CONFLICT): a closed sprint is a state "+
			"conflict, not a usage error\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "SPRINT_CLOSED") {
		t.Errorf("no typed refusal:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "ENG Sprint 1") {
		t.Errorf("the refusal does not name the sprint:\n%s", got.stderr)
	}
	// The old remedy sent the reader looking for a bad field. The new one has
	// to point at the sprint.
	if strings.Contains(got.stderr, "offending field") {
		t.Errorf("the remedy still blames a field:\n%s", got.stderr)
	}
}

// TestAnUnexplainedRefusalStaysGeneric is the guard. Diagnosing one condition
// must not turn every other 400 into a confident wrong answer: a sprint that is
// open when the move is refused means the refusal was about something else.
func TestAnUnexplainedRefusalStaysGeneric(t *testing.T) {
	url := closedSprintJira(t, "active")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "sprint", "add", "1", "ENG-3")

	if got.exit == exitcode.OK {
		t.Fatalf("a refused move reported success:\n%s", got.stdout)
	}
	if strings.Contains(got.stderr, "SPRINT_CLOSED") {
		t.Errorf("an active sprint was reported closed:\n%s", got.stderr)
	}
	// Jira's own sentence still reaches the caller, which is the whole value of
	// the generic path.
	if !strings.Contains(got.stderr, "has not been completed") {
		t.Errorf("Jira's explanation was lost:\n%s", got.stderr)
	}
}
