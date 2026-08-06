package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// notJira answers everything with an HTML 404, which is what a site URL missing
// its context path looks like: a web server, not an API.
func notJira(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><title>HTTP Status 404</title></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// credentialed is an isolated session with a credential in the environment, so
// a command gets far enough to fail at the endpoint rather than before it.
func credentialed(t *testing.T) map[string]string {
	t.Helper()
	env := session(t)
	env["JIRA_API_TOKEN"] = "not-a-real-token"
	env["JIRA_EMAIL"] = "ada@acme.invalid"
	return env
}

// TestAnEndpointErrorNamesWhereTheSiteCameFrom is the whole card. Three things
// can supply a site and which one won is visible nowhere else, so "the site is
// not reachable" used to cost a second command — `jr context show` — before it
// could be acted on at all.
func TestAnEndpointErrorNamesWhereTheSiteCameFrom(t *testing.T) {
	url := notJira(t)

	t.Run("from a context", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url)

		got := run(t, env, "issue", "list")
		assertNamesOrigin(t, got, `the site came from context "work"`)
	})

	t.Run("from the flag", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", "https://elsewhere.invalid")

		got := run(t, env, "issue", "list", "--site", url)
		assertNamesOrigin(t, got, "the site came from --site")
	})

	t.Run("from the environment", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", "https://elsewhere.invalid")
		env["JIRA_SITE"] = url

		got := run(t, env, "issue", "list")
		assertNamesOrigin(t, got, "the site came from JIRA_SITE")
	})
}

// assertNamesOrigin checks the failure explains itself without a second
// command, and that it still says what actually went wrong.
func assertNamesOrigin(t *testing.T, got result, origin string) {
	t.Helper()
	if got.exit == exitcode.OK {
		t.Fatalf("the command succeeded against a server that is not Jira:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, origin) {
		t.Errorf("the error does not say where the site came from; want %q in:\n%s",
			origin, got.stderr)
	}
	// The origin is an addition, not a replacement. The endpoint that failed is
	// still the first thing a reader needs.
	if !strings.Contains(got.stderr, "NO_SUCH_ENDPOINT") {
		t.Errorf("the original diagnosis was lost:\n%s", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("a failing command wrote to stdout:\n%s", got.stdout)
	}
}

// TestOnlySiteErrorsCarryTheOrigin keeps the addition from becoming noise.
// "which site was that" is the next question for a connection failure and not
// for a mistyped flag, and an error that explains everything explains nothing.
func TestOnlySiteErrorsCarryTheOrigin(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "https://jira.acme.invalid")

	got := run(t, env, "issue", "list", "--limit", "nonsense")
	if got.exit == exitcode.OK {
		t.Fatal("a bad --limit was accepted")
	}
	if strings.Contains(got.stderr, "the site came from") {
		t.Errorf("a usage error was told where the site came from:\n%s", got.stderr)
	}
}
