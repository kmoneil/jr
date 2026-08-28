package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// emptyJira answers the deployment probe and then every search with no rows,
// recording the JQL each search carried.
//
// No rows on purpose: this test is about the envelope, and a result with no
// rows is exactly the case the attribute exists for. An agent's 195-row pull
// looked right; the empty one it also got looked like an honest "nothing
// matched" and was a query that could not match.
func emptyJira(t *testing.T, jql *atomic.Value) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			// The account's timezone, which `issue changes` needs before it
			// will read a date at all. A date is evaluated by Jira in the
			// account's zone, so the feed refuses NO_ACCOUNT_TIMEZONE rather
			// than substituting this process's.
			_, _ = w.Write([]byte(
				`{"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			// serverTime as well as the version, because `issue changes` bounds
			// its window with the server's clock and refuses NO_SERVER_TIME
			// without one. A fixture that answers the probe and not the clock
			// covers `issue list` and quietly excludes the feed.
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-08-28T12:00:00.000+0000"}`))
		default:
			if q := r.URL.Query().Get("jql"); q != "" {
				jql.Store(q)
			}
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":0,"issues":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestTheEnvelopeNamesTheScopeTheAnswerWasComputedOver is the card.
//
// `complete="true"` means no bound cut the result short. It has never meant
// "this is the answer", and until now there was no field in which the
// difference could be stated: two runs of one command under two contexts
// produced different rows and two envelopes identical in every attribute that
// describes the result.
func TestTheEnvelopeNamesTheScopeTheAnswerWasComputedOver(t *testing.T) {
	var jql atomic.Value
	url := emptyJira(t, &jql)

	t.Run("a scoped query names its scope", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "list", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, `project="ENG"`) {
			t.Errorf("the envelope does not name the scope the rows came from:\n%s",
				got.stdout)
		}
	})

	t.Run("--all-projects names none", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "list", "--all-projects", "--limit", "all",
			"--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		// This is the assertion that makes the attribute worth having rather
		// than merely present. A boundary that re-read the context would stamp
		// project="ENG" onto a document whose rows came from every project on
		// the site, which is the defect the attribute exists to fix, backwards.
		if strings.Contains(got.stdout, `project=`) {
			t.Errorf("--all-projects lifted the scope and the envelope named "+
				"one anyway:\n%s", got.stdout)
		}
	})

	t.Run("the flag overrides the context and the envelope says so", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "--project", "OPS", "issue", "list", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, `project="OPS"`) {
			t.Errorf("the envelope names the context's project rather than the "+
				"effective one:\n%s", got.stdout)
		}
	})

	t.Run("a command that reads no scope names none", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "version", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if strings.Contains(got.stdout, `project=`) {
			t.Errorf("`version` named a project scope:\n%s", got.stdout)
		}
	})
}

// TestTheScopeAttributeMatchesTheQueryThatWasSent is the guard against the
// attribute being decorative.
//
// An envelope saying project="ENG" beside a query that did not scope to ENG
// would be worse than no attribute at all: it is a claim about the answer that
// nothing checked. So this reads both — the JQL the server actually received,
// and the attribute the document carries — and requires them to agree.
func TestTheScopeAttributeMatchesTheQueryThatWasSent(t *testing.T) {
	var jql atomic.Value
	url := emptyJira(t, &jql)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "list", "--format", "xml")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	sent, _ := jql.Load().(string)
	if sent == "" {
		t.Fatal("no query reached the server, so this proves nothing")
	}
	if !strings.Contains(sent, "ENG") {
		t.Fatalf("the query did not scope to ENG, so the attribute has "+
			"nothing to agree with: %q", sent)
	}
	if !strings.Contains(got.stdout, `project="ENG"`) {
		t.Errorf("the query sent %q and the envelope named no scope:\n%s",
			sent, got.stdout)
	}
}
