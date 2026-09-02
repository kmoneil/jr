package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// framedJira answers the probe, the clock and the account, and returns either
// no rows or one row depending on rows.
//
// It records the JQL of the last search, because half the assertions here are
// about the frame the warning reports and the other half are about the query
// that frame claims to describe. A warning naming a scope the query did not
// apply would be worse than no warning: it is a statement about the answer that
// nothing checked.
func framedJira(t *testing.T, jql *atomic.Value, rows bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-08-28T12:00:00.000+0000"}`))
		default:
			if q := r.URL.Query().Get("jql"); q != "" {
				jql.Store(q)
			}
			if !rows {
				_, _ = w.Write([]byte(
					`{"startAt":0,"maxResults":50,"total":0,"isLast":true,"issues":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":1,` +
				`"isLast":true,"issues":[{"id":"1","key":"ENG-1","fields":{` +
				`"summary":"probe","status":{"name":"Open",` +
				`"statusCategory":{"key":"new","name":"To Do"}},` +
				`"created":"2026-01-01T00:00:00.000+0000",` +
				`"updated":"2026-01-01T00:00:00.000+0000"}}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestAnEmptyCollectionNamesTheFrameItWasEmptyIn is issue #117.
//
// A populated collection shows its own frame: the rows name their projects and
// their dates, so a caller who narrowed the question by accident can see it in
// what came back. Zero rows is the one result with no data left to infer the
// frame from, and it is bytes-identical whatever the frame was.
func TestAnEmptyCollectionNamesTheFrameItWasEmptyIn(t *testing.T) {
	var jql atomic.Value
	url := framedJira(t, &jql, false)

	t.Run("a scoped empty result names the scope", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "list")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		for _, want := range []string{"EMPTY_RESULT", "0 issues", "project=ENG"} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
			}
		}
	})

	t.Run("stdout stays data-only", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "list")
		// The warning is a frame for the answer, not part of it. A consumer
		// piping stdout into a parser must see exactly what it saw before.
		if strings.Contains(got.stdout, "EMPTY_RESULT") {
			t.Errorf("the warning reached stdout:\n%q", got.stdout)
		}
		if lines := strings.Count(strings.TrimSpace(got.stdout), "\n"); lines != 0 {
			t.Errorf("stdout should be the header row alone, got:\n%q", got.stdout)
		}
	})

	t.Run("--all-projects says the scope was lifted", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "list", "--all-projects", "--limit", "all")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		// Not merely the absence of project=. An unscoped sweep and a sweep
		// whose scope nobody reported are opposite answers and a missing key
		// cannot tell them apart.
		if !strings.Contains(got.stderr, "scope=none") {
			t.Errorf("a lifted scope is not reported as one:\n%s", got.stderr)
		}
		if strings.Contains(got.stderr, "project=") {
			t.Errorf("--all-projects lifted the scope and the warning named "+
				"one anyway:\n%s", got.stderr)
		}
	})

	t.Run("activity reports the bounds it resolved", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		// --kind is passed so the assertion below has something it could
		// catch. Without it the flag is empty, nothing could put `kind=` in
		// the frame, and a check that cannot fail reads exactly like one that
		// passes: staging the red is what found that, not reading it.
		got := run(t, env, "issue", "activity", "--since", "2026-08-01",
			"--user", "currentUser", "--kind", "comment")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		// The instant, not the flag. A bare date is read in the Jira account's
		// timezone rather than the caller's, and the resolved value is on no
		// envelope in any format, so this line is the only place it appears.
		if !strings.Contains(got.stderr, "since=2026-08-01T00:00:00Z") {
			t.Errorf("the resolved --since instant is not reported:\n%s", got.stderr)
		}
		// currentUser resolved to an account nothing else echoes back.
		if !strings.Contains(got.stderr, "user=acc-1") {
			t.Errorf("the resolved --user is not reported:\n%s", got.stderr)
		}
		// --kind is the caller's own word, still on their command line.
		// Repeating input at somebody says nothing they did not know.
		if strings.Contains(got.stderr, "kind=") {
			t.Errorf("a flag the caller typed was echoed back:\n%s", got.stderr)
		}
	})
}

// TestAPopulatedCollectionIsNotFramed is the other half, and the half that
// keeps the warning from becoming noise.
//
// A warning on every result is a warning nobody reads. This fires on the one
// case that cannot describe itself.
func TestAPopulatedCollectionIsNotFramed(t *testing.T) {
	var jql atomic.Value
	url := framedJira(t, &jql, true)

	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "list")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, "ENG-1") {
		t.Fatalf("the fixture returned no rows, so this proves nothing:\n%s",
			got.stdout)
	}
	if strings.Contains(got.stderr, "EMPTY_RESULT") {
		t.Errorf("a collection with rows in it was framed as empty:\n%s", got.stderr)
	}
}

// TestActivitySearchesEveryProjectWithAllProjects is issue #116.
//
// The bug was not that the answer was wrong. It was that the narrow answer and
// the wide one are the same bytes, so the command built to answer "what
// happened" could not be asked about more than one project and could not say
// that it had not been.
func TestActivitySearchesEveryProjectWithAllProjects(t *testing.T) {
	t.Run("the context scopes it by default", func(t *testing.T) {
		var jql atomic.Value
		url := framedJira(t, &jql, false)

		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "activity", "--since", "-1d", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		sent, _ := jql.Load().(string)
		if !strings.Contains(sent, "project = ") {
			t.Errorf("the candidate search was not scoped to the context:\n%s", sent)
		}
		if !strings.Contains(got.stdout, `project="ENG"`) {
			t.Errorf("the envelope does not name the scope:\n%s", got.stdout)
		}
	})

	t.Run("--all-projects lifts it, in the query and in the envelope", func(t *testing.T) {
		var jql atomic.Value
		url := framedJira(t, &jql, false)

		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "activity", "--since", "-1d",
			"--all-projects", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		// Both halves, because either one alone is a lie. A query that dropped
		// the clause while the envelope still named ENG would describe the
		// answer wrongly; an envelope that named none over a query that kept
		// the clause would describe it wrongly the other way.
		sent, _ := jql.Load().(string)
		if strings.Contains(sent, "project = ") {
			t.Errorf("--all-projects left the project clause on the query:\n%s", sent)
		}
		if strings.Contains(got.stdout, `project=`) {
			t.Errorf("--all-projects lifted the scope and the envelope named "+
				"one anyway:\n%s", got.stdout)
		}
	})
}
