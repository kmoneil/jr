package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// brokenProbe is the instance this flag exists for: something in front of Jira
// mangles serverInfo, so the deployment cannot be detected and every command
// fails before it starts. The issue endpoint works fine.
func brokenProbe(t *testing.T, probes *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/serverInfo") {
			probes.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html><title>Proxy Error</title></html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"maxResults":0,"total":0,"issues":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestADeclaredVersionSkipsTheProbe is the card. A site whose serverInfo cannot
// be read had no way in at all: the probe runs before anything else, so the
// failure arrives before the override could have helped — which is why this
// skips the probe rather than overriding its answer.
func TestADeclaredVersionSkipsTheProbe(t *testing.T) {
	var probes atomic.Int32
	url := brokenProbe(t, &probes)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url)

	// Without it, the probe is what fails.
	failed := run(t, env, "issue", "list")
	if failed.exit == exitcode.OK {
		t.Fatal("a site with a broken probe worked without the override")
	}
	if probes.Load() == 0 {
		t.Fatal("the probe was never attempted")
	}

	before := probes.Load()
	got := run(t, env, "issue", "list", "--api-version", "2")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v with the override\n%s", got.exit, got.stderr)
	}
	if probes.Load() != before {
		t.Error("the override still probed; the point is that it cannot")
	}
}

// TestTheDeclaredVersionPicksTheEndpoints covers what "forces the base path"
// means in practice, and the card's other use: exercising the deployment you
// are not on, deliberately.
func TestTheDeclaredVersionPicksTheEndpoints(t *testing.T) {
	var paths atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"maxResults":0,"total":0,"issues":[]}`))
	}))
	defer srv.Close()

	for version, want := range map[string]string{"2": "/rest/api/2/", "3": "/rest/api/3/"} {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", srv.URL)

		if got := run(t, env, "issue", "list", "--api-version", version); got.exit != exitcode.OK {
			t.Fatalf("--api-version %s: exit = %v\n%s", version, got.exit, got.stderr)
		}
		if path, _ := paths.Load().(string); !strings.HasPrefix(path, want) {
			t.Errorf("--api-version %s hit %q, want a %s path", version, path, want)
		}
	}
}

// TestAnOverrideIsVisibleRatherThanMysterious is the second half of the card.
// A forced version changes which endpoints every command uses, and a setting
// with that reach must not be invisible.
func TestAnOverrideIsVisibleRatherThanMysterious(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "jira.acme.invalid")

	forced := mustRun(t, env, "context", "show", "--api-version", "2")
	if !strings.Contains(forced.stdout, `api-version="2"`) {
		t.Errorf("context show does not report the override:\n%s", forced.stdout)
	}

	// Absent when nothing forced one: the probe deciding is the case worth
	// saying nothing about.
	quiet := mustRun(t, env, "context", "show")
	if strings.Contains(quiet.stdout, "api-version") {
		t.Errorf("context show reports an override nobody asked for:\n%s", quiet.stdout)
	}

	// The environment works too, because a broken proxy means every command in
	// the shell needs it and repeating a flag forever is not a fix.
	env["JIRA_API_VERSION"] = "3"
	fromEnv := mustRun(t, env, "context", "show")
	if !strings.Contains(fromEnv.stdout, `api-version="3"`) {
		t.Errorf("JIRA_API_VERSION was ignored:\n%s", fromEnv.stdout)
	}
}

// TestOnlyTwoAndThreeAreVersions covers the values that are not.
func TestOnlyTwoAndThreeAreVersions(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "jira.acme.invalid")

	for _, bad := range []string{"9", "0", "-1", "v2", "two", "2.0"} {
		got := run(t, env, "context", "show", "--api-version", bad)
		if got.exit != exitcode.Usage {
			t.Errorf("--api-version %q: exit = %v, want %v", bad, got.exit, exitcode.Usage)
			continue
		}
		if !strings.Contains(got.stderr, "INVALID_API_VERSION") {
			t.Errorf("--api-version %q: %s", bad, got.stderr)
		}
	}
}
