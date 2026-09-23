package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// probeThenDeafJira answers the probes context setup may make, and fails the
// test on any request once armed. --explain's whole claim is that it sends
// nothing, so a test that let a request slide would be checking the output of
// a command while ignoring its promise.
func probeThenDeafJira(t *testing.T, armed *atomic.Bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() {
			t.Errorf("--explain reached the server: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unreachable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-08-28T12:00:00.000+0000"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// textOf pulls one element's text out of a rendered document. The tests here
// compare query strings, and a substring match on the whole document would
// pass on a query that leaked into the wrong element.
func textOf(t *testing.T, doc, element string) string {
	t.Helper()
	openTag, closeTag := "<"+element+">", "</"+element+">"
	i := strings.Index(doc, openTag)
	j := strings.Index(doc, closeTag)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("document has no <%s> element:\n%s", element, doc)
	}
	return doc[i+len(openTag) : j]
}

// TestExplainPrintsTheQueryWithoutSending is the core of the global flag: the
// composed JQL on stdout, as a jql.explain document, with the server never
// consulted.
func TestExplainPrintsTheQueryWithoutSending(t *testing.T) {
	var armed atomic.Bool
	url := probeThenDeafJira(t, &armed)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
	armed.Store(true)

	t.Run("issue list composes from its flags", func(t *testing.T) {
		got := run(t, env, "issue", "list", "--status", "Open",
			"--label", "retry", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, `kind="jql.explain"`) {
			t.Errorf("stdout is not a jql.explain document:\n%s", got.stdout)
		}
		query := textOf(t, got.stdout, "query")
		for _, want := range []string{"ENG", "Open", "retry", "ORDER BY issuekey"} {
			if !strings.Contains(query, want) {
				t.Errorf("query does not carry %q: %s", want, query)
			}
		}
	})

	t.Run("a raw fragment is reported and wrapped", func(t *testing.T) {
		got := run(t, env, "issue", "list", "--jql", "labels = retry", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if fragment := textOf(t, got.stdout, "fragment"); fragment != "labels = retry" {
			t.Errorf("fragment = %q", fragment)
		}
		if query := textOf(t, got.stdout, "query"); !strings.Contains(query, "(labels = retry)") {
			t.Errorf("query does not carry the wrapped fragment: %s", query)
		}
	})

	t.Run("issue activity explains its window", func(t *testing.T) {
		got := run(t, env, "issue", "activity", "--since", "-7d", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if query := textOf(t, got.stdout, "query"); !strings.Contains(query, "-7d") {
			t.Errorf("query does not carry the --since bound: %s", query)
		}
	})

	t.Run("issue changes names its floor as unresolved", func(t *testing.T) {
		got := run(t, env, "issue", "changes", "--since", "-7d", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, `<filter flag="since">-7d</filter>`) {
			t.Errorf("unresolved does not name --since:\n%s", got.stdout)
		}
	})
}

// TestExplainNamesWhatItDidNotResolve is the v2 element. A typed name is
// resolved to an account id only at send time, so the explained query carries
// it as typed and says so; the forms JQL itself has words for are complete
// and are not listed.
func TestExplainNamesWhatItDidNotResolve(t *testing.T) {
	var armed atomic.Bool
	url := probeThenDeafJira(t, &armed)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
	armed.Store(true)

	t.Run("a typed name is listed", func(t *testing.T) {
		got := run(t, env, "issue", "list", "--assignee", "Ada Lovelace", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, `<filter flag="assignee">Ada Lovelace</filter>`) {
			t.Errorf("unresolved does not name the assignee:\n%s", got.stdout)
		}
		if query := textOf(t, got.stdout, "query"); !strings.Contains(query, "Ada Lovelace") {
			t.Errorf("query does not carry the typed value: %s", query)
		}
	})

	t.Run("currentUser is complete as typed", func(t *testing.T) {
		got := run(t, env, "issue", "list", "--assignee", "currentUser", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if strings.Contains(got.stdout, "<unresolved") {
			t.Errorf("currentUser was reported unresolved:\n%s", got.stdout)
		}
	})

	t.Run("unassigned is complete as typed", func(t *testing.T) {
		got := run(t, env, "issue", "list", "--assignee", "unassigned", "--explain")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if strings.Contains(got.stdout, "<unresolved") {
			t.Errorf("the sentinel was reported unresolved:\n%s", got.stdout)
		}
	})
}

// TestExplainMatchesWhatTheCommandSends is the drift gate. The explanation
// composes through the same funnel the command sends through, and this is
// the assertion that keeps it true: the same flags, explained and then run,
// produce byte-identical JQL.
func TestExplainMatchesWhatTheCommandSends(t *testing.T) {
	var jql atomic.Value
	url := framedJira(t, &jql, false)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	args := []string{
		"issue", "list", "--status", "Open", "--label", "retry",
		"--assignee", "currentUser", "--jql", "labels = retry",
	}
	explained := run(t, env, append(args, "--explain")...)
	if explained.exit != exitcode.OK {
		t.Fatalf("explain exit = %v, stderr = %s", explained.exit, explained.stderr)
	}
	sent := run(t, env, args...)
	if sent.exit != exitcode.OK {
		t.Fatalf("list exit = %v, stderr = %s", sent.exit, sent.stderr)
	}

	want, _ := jql.Load().(string)
	if want == "" {
		t.Fatal("the list run sent no JQL")
	}
	if got := textOf(t, explained.stdout, "query"); got != want {
		t.Errorf("explained query differs from the sent one:\nexplained: %s\nsent:      %s",
			got, want)
	}
}

// TestExplainOnACommandWithNoQueryRefuses: running as if the flag had not
// been given would be the incumbent's habit. The refusal is knowable from
// the declaration alone, so no context is needed to hear it.
func TestExplainOnACommandWithNoQueryRefuses(t *testing.T) {
	env := credentialed(t)
	got := run(t, env, "issue", "get", "ENG-1", "--explain")
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want usage; stderr = %s", got.exit, got.stderr)
	}
	for _, want := range []string{"INVALID_USAGE", "composes no query"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
		}
	}
	if got.stdout != "" {
		t.Errorf("a refusal wrote to stdout:\n%s", got.stdout)
	}
}

// TestDescribeAndExplainRefuses: the two flags name two different outputs,
// and honoring both would mean ignoring one.
func TestDescribeAndExplainRefuses(t *testing.T) {
	env := credentialed(t)
	got := run(t, env, "issue", "list", "--describe", "--explain")
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want usage; stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "DESCRIBE_AND_EXPLAIN") {
		t.Errorf("stderr does not carry the code:\n%s", got.stderr)
	}
}
