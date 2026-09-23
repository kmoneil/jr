//go:build write

package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// editCaptureJira accepts one edit and records the description it was sent.
func editCaptureJira(t *testing.T, sent *atomic.Value) string {
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
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/issue/"):
			var body struct {
				Fields map[string]json.RawMessage `json:"fields"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				var text string
				_ = json.Unmarshal(body.Fields["description"], &text)
				sent.Store(text)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestDescriptionFileSendsTheExactBytes is the write half of the raw round
// trip: the file's bytes reach the request untouched. The content carries
// CRLF endings and a trailing newline, which is exactly what
// --description "$(cat f)" eats.
func TestDescriptionFileSendsTheExactBytes(t *testing.T) {
	var sent atomic.Value
	url := editCaptureJira(t, &sent)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	want := "h3. Title\r\n\r\n* one\r\n* two\n"
	path := filepath.Join(t.TempDir(), "description.wiki")
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	got := run(t, env, "issue", "edit", "ENG-101", "--description-file", path)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if sentText, _ := sent.Load().(string); sentText != want {
		t.Errorf("the request did not carry the file's bytes:\ngot  %q\nwant %q",
			sentText, want)
	}
}

// TestDescriptionFileReadsStdinForDash: `-` is stdin, so the read half can
// pipe straight into the write half with no file in between.
func TestDescriptionFileReadsStdinForDash(t *testing.T) {
	var sent atomic.Value
	url := editCaptureJira(t, &sent)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	want := "piped body\nsecond line\n"
	got := runWithStdin(t, env, strings.NewReader(want),
		"issue", "edit", "ENG-101", "--description-file", "-")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if sentText, _ := sent.Load().(string); sentText != want {
		t.Errorf("the request did not carry stdin's bytes:\ngot  %q\nwant %q",
			sentText, want)
	}
}

// TestDescriptionFileStillLintsWikiMarkup: the file path feeds the same
// chokepoint --description feeds, so the ambiguity scanner is not skipped by
// arriving as bytes.
func TestDescriptionFileStillLintsWikiMarkup(t *testing.T) {
	var sent atomic.Value
	url := editCaptureJira(t, &sent)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	path := filepath.Join(t.TempDir(), "description.wiki")
	if err := os.WriteFile(path, []byte("{{/subjects/{subject\\}}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := run(t, env, "issue", "edit", "ENG-101", "--description-file", path)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "AMBIGUOUS_WIKI_MARKUP") {
		t.Errorf("the scanner did not see the file's markup:\n%s", got.stderr)
	}
}

// TestDescriptionFileRefusals: both flags at once, an empty file, and a
// missing file each refuse before any request is spent.
func TestDescriptionFileRefusals(t *testing.T) {
	var sent atomic.Value
	url := editCaptureJira(t, &sent)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	dir := t.TempDir()
	full := filepath.Join(dir, "full.wiki")
	if err := os.WriteFile(full, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.wiki")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, code string
		args       []string
	}{
		{
			"both flags name one description", "DESCRIPTION_AND_FILE",
			[]string{"--description", "x", "--description-file", full},
		},
		{
			"an empty file is not an edit", "EMPTY_DESCRIPTION_FILE",
			[]string{"--description-file", empty},
		},
		{
			"a missing file refuses", "DESCRIPTION_FILE_UNREADABLE",
			[]string{"--description-file", filepath.Join(dir, "absent.wiki")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, env, append([]string{"issue", "edit", "ENG-101"}, tc.args...)...)
			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want 2 (USAGE); stderr = %s", got.exit, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.code) {
				t.Errorf("stderr does not carry %s:\n%s", tc.code, got.stderr)
			}
		})
	}
	if sent.Load() != nil {
		t.Errorf("a refused edit reached the server with %q", sent.Load())
	}
}
