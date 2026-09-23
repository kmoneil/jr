//go:build write

package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// markupJira is a Data Center that accepts a create, so the warning can be
// observed on a run that otherwise succeeds.
//
// kind is the deployment: Data Center stores wiki markup, Cloud stores an ADF
// document where a brace is a brace.
func markupJira(t *testing.T, kind string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"10.4.0","deploymentType":"` + kind + `",` +
				`"serverTime":"2026-09-23T12:00:00.000+0000"}`))
		case strings.Contains(r.URL.Path, "/createmeta"):
			_, _ = w.Write([]byte(`{"projects":[{"key":"ENG","issuetypes":[` +
				`{"id":"1","name":"Task","fields":{}}]}]}`))
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"1","key":"ENG-1",` +
				`"self":"http://example.invalid/rest/api/2/issue/1"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"1","key":"ENG-1","fields":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The construct from issue 161, which its reporter rewrote rather than send
// because they could not tell how it would render.
const ambiguousMarkup = `{{/subjects/{subject\}}}, {{/subjects/{subject\}/versions}}`

// TestAmbiguousWikiMarkupWarnsAndStillSends is the whole of issue 161.
//
// Nothing here is known to be wrong, so the write goes through and the exit
// stays 0. What the caller gets is the thing they could not get by reading the
// text: that it has more than one reading.
func TestAmbiguousWikiMarkupWarnsAndStillSends(t *testing.T) {
	url := markupJira(t, "Server")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "create", "--type", "Task",
		"--summary", "a ticket", "--description", ambiguousMarkup)

	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, want 0: nothing here is known to be wrong\n%s",
			got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "AMBIGUOUS_WIKI_MARKUP") {
		t.Errorf("no warning for the reported construct:\n%s", got.stderr)
	}
	// stdout is data only, always.
	if strings.Contains(got.stdout, "AMBIGUOUS_WIKI_MARKUP") {
		t.Errorf("the warning reached stdout:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "ENG-1") {
		t.Errorf("the issue was not created:\n%s", got.stdout)
	}
}

// TestCloudDoesNotWarnAboutWikiMarkup is the deployment guard. Cloud stores an
// ADF document, so a brace is a brace and there is nothing ambiguous to report.
// Warning there would be a false positive on every Cloud user forever.
func TestCloudDoesNotWarnAboutWikiMarkup(t *testing.T) {
	url := markupJira(t, "Cloud")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "create", "--type", "Task",
		"--summary", "a ticket", "--description", ambiguousMarkup)

	if strings.Contains(got.stderr, "AMBIGUOUS_WIKI_MARKUP") {
		t.Errorf("Cloud was warned about wiki markup:\n%s", got.stderr)
	}
}

// TestOrdinaryMarkupIsNotWarnedAbout is the one that keeps the channel worth
// reading. A warning that fires on valid markup teaches the reader to skip it.
func TestOrdinaryMarkupIsNotWarnedAbout(t *testing.T) {
	url := markupJira(t, "Server")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	for _, body := range []string{
		"h3. Background\n\nThe importer drops rows.\n\n* one\n* two",
		"run {{make test}} and then {{make lint}}",
		"{code:go}\nif (x) {{{ y }}}\n{code}\nprose after the block",
	} {
		got := run(t, env, "issue", "create", "--type", "Task",
			"--summary", "a ticket", "--description", body)
		if strings.Contains(got.stderr, "AMBIGUOUS_WIKI_MARKUP") {
			t.Errorf("warned about ordinary markup %q:\n%s", body, got.stderr)
		}
	}
}
