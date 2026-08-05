package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// fakeJira serves the two endpoints `auth login` verifies against.
func fakeJira(t *testing.T, deployment string, myself func(http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.7","deploymentType":"` + deployment + `"}`))
		case strings.HasSuffix(r.URL.Path, "/myself"):
			myself(w)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html><title>HTTP Status 404</title></html>"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func okAccount(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"name":"ada","displayName":"Ada Lovelace","active":true}`))
}

// TestLoginVerifiesTheCredential is the point of verifying at all: a login that
// only writes a file reports success for a token that does not work, and every
// later command fails for reasons that look unrelated to it.
func TestLoginVerifiesTheCredential(t *testing.T) {
	url := fakeJira(t, "Server", okAccount)
	env := session(t)

	got := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", url, "--token-stdin")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	// The account is reported, so the caller knows which identity was stored.
	if !strings.Contains(got.stdout, `display="Ada Lovelace"`) {
		t.Errorf("login did not report who it authenticated as:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, theToken) {
		t.Fatalf("login echoed the token:\n%s", got.stdout)
	}
}

// TestLoginRefusesABadCredential asserts nothing is written when the check
// fails. Storing a rejected credential would leave the caller believing they
// are logged in.
func TestLoginRefusesABadCredential(t *testing.T) {
	url := fakeJira(t, "Server", func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorMessages":["Client must be authenticated"]}`))
	})
	env := session(t)

	got := runWithStdin(t, env, strings.NewReader("wrong-token"),
		"auth", "login", "--site", url, "--token-stdin")
	if got.exit != exitcode.Auth {
		t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Auth, got.stderr)
	}

	// Nothing stored, and no context invented for a site that rejected us.
	status := mustRun(t, env, "auth", "status", "--site", url)
	if !strings.Contains(status.stdout, `authenticated="false"`) {
		t.Errorf("a rejected credential was stored anyway:\n%s", status.stdout)
	}
	list := mustRun(t, env, "context", "list")
	if strings.Contains(list.stdout, "http") {
		t.Errorf("a context was created for a site that rejected the credential:\n%s", list.stdout)
	}
}

// TestLoginRefusesAWrongSite is the failure this was built for: a host that
// answers but is not the Jira API, which is what a missing context path looks
// like.
func TestLoginRefusesAWrongSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<html><head><title>HTTP Status 404</title></head></html>`))
	}))
	defer srv.Close()

	env := session(t)
	got := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", srv.URL, "--token-stdin")
	if got.exit == exitcode.OK {
		t.Fatal("a site that serves web pages was accepted as Jira")
	}
	if !strings.Contains(got.stderr, "NO_SUCH_ENDPOINT") {
		t.Errorf("stderr does not name the problem:\n%s", got.stderr)
	}
	// The remedy is the one that would have saved the round trip.
	if !strings.Contains(got.stderr, "context path") {
		t.Errorf("the remedy does not mention a context path:\n%s", got.stderr)
	}
}

// TestNoVerifySkipsTheCheck keeps configuring a machine offline possible.
func TestNoVerifySkipsTheCheck(t *testing.T) {
	env := session(t)
	got := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--no-verify", "--site", "unreachable.invalid", "--token-stdin")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	if strings.Contains(got.stdout, "display=") {
		t.Error("--no-verify reported an account it never fetched")
	}
}

// TestVerifiedLoginUsesTheRightAPIVersion checks the probe decides the path.
// Cloud serves /rest/api/3/myself and Data Center /rest/api/2/myself, and
// asking the wrong one is a 404 that reads like a missing user.
func TestVerifiedLoginUsesTheRightAPIVersion(t *testing.T) {
	for deployment, want := range map[string]string{
		"Cloud":  "/rest/api/3/myself",
		"Server": "/rest/api/2/myself",
	} {
		t.Run(deployment, func(t *testing.T) {
			var asked string
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.HasSuffix(r.URL.Path, "/serverInfo") {
						_, _ = w.Write([]byte(
							`{"version":"1","deploymentType":"` + deployment + `"}`,
						))
						return
					}
					asked = r.URL.Path
					okAccount(w)
				},
			))
			defer srv.Close()

			env := session(t)
			got := runWithStdin(t, env, strings.NewReader(theToken),
				"auth", "login", "--site", srv.URL, "--token-stdin")
			if got.exit != exitcode.OK {
				t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
			}
			if asked != want {
				t.Errorf("asked %q, want %q", asked, want)
			}
		})
	}
}
