package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// activeSprintsJira serves a board whose active sprints are the ones given,
// and asserts the listing actually narrowed to active: a `sprint current`
// that fetched every sprint and filtered here would pay for the whole
// history on boards that have one.
func activeSprintsJira(t *testing.T, sprints string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"10.4.0","deploymentType":"Server",` +
				`"serverTime":"2026-09-23T12:00:00.000+0000"}`))
		case strings.Contains(r.URL.Path, "/board/") && strings.HasSuffix(r.URL.Path, "/sprint"):
			if got := r.URL.Query().Get("state"); got != "active" {
				t.Errorf("the sprint listing was not narrowed to active: state=%q", got)
			}
			_, _ = w.Write([]byte(`{"isLast":true,"values":[` + sprints + `]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestSprintCurrentAnswersTheOneActiveSprint is the feature: the id an agent
// should re-derive rather than remember, as one request.
func TestSprintCurrentAnswersTheOneActiveSprint(t *testing.T) {
	url := activeSprintsJira(t,
		`{"id":31,"name":"ENG Iteration 7","state":"active","originBoardId":9}`)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "--board", "9", "sprint", "current")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, `kind="sprint.get"`) {
		t.Errorf("the answer is not a sprint.get document:\n%s", got.stdout)
	}
	for _, want := range []string{`id="31"`, "ENG Iteration 7", `state="active"`} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout does not carry %s:\n%s", want, got.stdout)
		}
	}
}

// TestSprintCurrentRefusesABoardWithNoActiveSprint: zero is a refusal that
// names the repair, not an empty answer a script would have to detect.
func TestSprintCurrentRefusesABoardWithNoActiveSprint(t *testing.T) {
	url := activeSprintsJira(t, ``)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "--board", "9", "sprint", "current")
	if got.exit != exitcode.NotFound {
		t.Fatalf("exit = %v, want 5 (NOT_FOUND); stderr = %s", got.exit, got.stderr)
	}
	for _, want := range []string{"NO_ACTIVE_SPRINT", "board 9", "sprint start"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
		}
	}
	if got.stdout != "" {
		t.Errorf("a refusal wrote to stdout:\n%s", got.stdout)
	}
}

// TestSprintCurrentRefusesParallelActiveSprints: some boards run several
// active sprints at once, and picking one of them without saying so would be
// a guess dressed as an answer. The refusal names every candidate so the
// caller does not pay a second request to see them.
func TestSprintCurrentRefusesParallelActiveSprints(t *testing.T) {
	url := activeSprintsJira(t,
		`{"id":31,"name":"ENG Iteration 7","state":"active","originBoardId":9},`+
			`{"id":32,"name":"ENG Hardening","state":"active","originBoardId":9}`)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "--board", "9", "sprint", "current")
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want 2 (USAGE); stderr = %s", got.exit, got.stderr)
	}
	for _, want := range []string{
		"AMBIGUOUS_SPRINT", "31 ENG Iteration 7", "32 ENG Hardening",
		"sprint list --state active",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
		}
	}
}

// TestSprintCurrentNeedsABoard: with no board from any source the refusal is
// the same NO_BOARD every board-scoped command gives, naming all three
// sources, before any request is spent.
func TestSprintCurrentNeedsABoard(t *testing.T) {
	url := activeSprintsJira(t, ``)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "sprint", "current")
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want 2 (USAGE); stderr = %s", got.exit, got.stderr)
	}
	for _, want := range []string{"NO_BOARD", "JIRA_BOARD"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
		}
	}
}
