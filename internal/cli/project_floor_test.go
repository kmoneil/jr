package cli_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// cloudWithNothing answers like a Cloud site whose searches match nothing,
// which is what Cloud says of a project it has never heard of and of one that
// is merely empty. The parse endpoint is where the two differ, and it says of
// NOPE what Cloud says of a key it does not know.
func cloudWithNothing(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","displayName":"Ada Lovelace",` +
				`"timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"1001.0.0","deploymentType":"Cloud",` +
				`"serverTime":"2026-10-12T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/jql/parse"):
			asked, _ := io.ReadAll(r.Body)
			if strings.Contains(string(asked), "NOPE") {
				_, _ = w.Write([]byte(`{"queries":[{"query":"x","errors":` +
					`["The value 'NOPE' does not exist for the field 'project'."]}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"queries":[{"query":"x","errors":[],"warnings":[]}]}`))
		case strings.HasSuffix(r.URL.Path, "/approximate-count"):
			_, _ = w.Write([]byte(`{"count":0}`))
		default:
			_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestAnUnknownProjectIsNotAnEmptyResult is the card, from the command line.
//
// `jr issue list --project NOPE` against Cloud answered exit 0, no rows, and
// EMPTY_RESULT naming the key, which is the document a real and empty project
// gives. The refusal has to arrive with stdout untouched in every format, TSV
// included, and it has to arrive wherever the key came from: a context whose
// project has gone is the likeliest source of one, and nobody typed that.
func TestAnUnknownProjectIsNotAnEmptyResult(t *testing.T) {
	url := cloudWithNothing(t)

	refused := func(t *testing.T, got result) {
		t.Helper()
		if got.exit != exitcode.NotFound {
			t.Fatalf("exit = %v, want %v\nstdout: %s\nstderr: %s",
				got.exit, exitcode.NotFound, got.stdout, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("a refused command wrote to stdout:\n%q", got.stdout)
		}
		for _, want := range []string{
			"UNKNOWN_PROJECT", "The value 'NOPE' does not exist for the field 'project'.",
		} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
			}
		}
		if strings.Contains(got.stderr, "EMPTY_RESULT") {
			t.Errorf("the refusal came with a note calling the answer empty:\n%s", got.stderr)
		}
	}

	for _, format := range []string{"tsv", "xml", "json", "yaml"} {
		t.Run("from the flag, as "+format, func(t *testing.T) {
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
			refused(t, run(t, env, "--project", "NOPE", "issue", "list", "--format", format))
		})
	}

	t.Run("from the context", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "NOPE")
		refused(t, run(t, env, "issue", "list"))
	})

	t.Run("from the environment", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
		env["JIRA_PROJECT"] = "NOPE"
		refused(t, run(t, env, "issue", "list"))
	})

	for _, args := range [][]string{
		{"issue", "activity", "--since", "-1d"},
		{"issue", "changes", "--since", "-1d"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
			refused(t, run(t, env, append([]string{"--project", "NOPE"}, args...)...))
		})
	}

	// The control, and the reason this is a question put to Jira and not a
	// rule about empty answers: a project that is there and holds nothing is
	// still exit 0, a header, and the note saying what was empty.
	t.Run("a project Jira knows is still an empty result", func(t *testing.T) {
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "list", "--format", "tsv")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.HasPrefix(got.stdout, "key\t") {
			t.Errorf("an empty result has no header:\n%q", got.stdout)
		}
		if !strings.Contains(got.stderr, "EMPTY_RESULT") ||
			!strings.Contains(got.stderr, "project=ENG") {
			t.Errorf("the empty result does not say what it was empty about:\n%s", got.stderr)
		}
	})
}
