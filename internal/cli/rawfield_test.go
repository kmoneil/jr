package cli_test

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// rawFieldJira serves one issue whose fields are exactly the JSON given, and
// a two-entry catalogue so a name resolves the way --field resolves one.
func rawFieldJira(t *testing.T, fields string) string {
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
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[` +
				`{"id":"description","name":"Description","custom":false},` +
				`{"id":"customfield_10042","name":"Story Points","custom":true}]`))
		case strings.Contains(r.URL.Path, "/issue/"):
			_, _ = w.Write([]byte(`{"id":"1","key":"ENG-101","fields":{` + fields + `}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRawFieldWritesTheStoredBytesExactly is the feature: what Jira holds,
// byte for byte, no envelope, no added newline. The value here carries CRLF
// line endings, a non-ASCII rune, wiki braces, and a trailing newline,
// because each of those is something a lesser path has eaten.
func TestRawFieldWritesTheStoredBytesExactly(t *testing.T) {
	url := rawFieldJira(t,
		`"description":"h3. Ren\u00e9e\r\n{{count: 1}}\r\nlast line\n"`)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "get", "ENG-101", "--raw-field", "description")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	want := "h3. Renée\r\n{{count: 1}}\r\nlast line\n"
	if got.stdout != want {
		t.Errorf("stdout is not the stored bytes:\ngot  %q\nwant %q", got.stdout, want)
	}
}

// TestRawFieldResolvesANameLikeFieldDoes: 'Story Points' and its id are one
// request, through the same catalogue --field consults.
func TestRawFieldResolvesANameLikeFieldDoes(t *testing.T) {
	url := rawFieldJira(t, `"customfield_10042":"5"`)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "get", "ENG-101", "--raw-field", "Story Points")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if got.stdout != "5" {
		t.Errorf("stdout = %q, want the stored string 5", got.stdout)
	}
}

// TestRawFieldRefusesWhatItCannotWrite: an unset value and a structured one
// are refusals, not zero bytes and not an invented serialization.
func TestRawFieldRefusesWhatItCannotWrite(t *testing.T) {
	t.Run("unset is not zero bytes", func(t *testing.T) {
		url := rawFieldJira(t, `"description":null`)
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "get", "ENG-101", "--raw-field", "description")
		if got.exit != exitcode.NotFound {
			t.Fatalf("exit = %v, want 5 (NOT_FOUND); stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stderr, "UNSET_FIELD") {
			t.Errorf("stderr does not carry UNSET_FIELD:\n%s", got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("a refusal wrote to stdout: %q", got.stdout)
		}
	})

	t.Run("a document is not text", func(t *testing.T) {
		url := rawFieldJira(t,
			`"description":{"type":"doc","version":1,"content":[]}`)
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

		got := run(t, env, "issue", "get", "ENG-101", "--raw-field", "description")
		if got.exit != exitcode.Usage {
			t.Fatalf("exit = %v, want 2 (USAGE); stderr = %s", got.exit, got.stderr)
		}
		for _, want := range []string{"FIELD_NOT_TEXT", "object", "raw-body"} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr does not carry %q:\n%s", want, got.stderr)
			}
		}
	})
}

// TestRawFieldRefusesASecondOutput: an explicit --format names a second
// output, and the flags that shape the document have nothing to act on. The
// environment variable is not grounds for the format refusal, because it is
// set once for a whole shell.
func TestRawFieldRefusesASecondOutput(t *testing.T) {
	url := rawFieldJira(t, `"description":"plain"`)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	t.Run("an explicit --format refuses", func(t *testing.T) {
		got := run(t, env, "issue", "get", "ENG-101",
			"--raw-field", "description", "--format", "json")
		if got.exit != exitcode.Usage {
			t.Fatalf("exit = %v, want 2 (USAGE); stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stderr, "RAW_AND_FORMAT") {
			t.Errorf("stderr does not carry RAW_AND_FORMAT:\n%s", got.stderr)
		}
	})

	t.Run("JIRA_FORMAT alone does not", func(t *testing.T) {
		withEnv := map[string]string{}
		maps.Copy(withEnv, env)
		withEnv["JIRA_FORMAT"] = "json"
		got := run(t, withEnv, "issue", "get", "ENG-101", "--raw-field", "description")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if got.stdout != "plain" {
			t.Errorf("stdout = %q, want the stored bytes", got.stdout)
		}
	})

	t.Run("a document-shaping flag refuses", func(t *testing.T) {
		got := run(t, env, "issue", "get", "ENG-101",
			"--raw-field", "description", "--url")
		if got.exit != exitcode.Usage {
			t.Fatalf("exit = %v, want 2 (USAGE); stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stderr, "RAW_FIELD_ALONE") {
			t.Errorf("stderr does not carry RAW_FIELD_ALONE:\n%s", got.stderr)
		}
	})
}
