package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The body somebody has to pay for. Long enough that its presence or absence is
// unambiguous in a byte count, and holding the fenced code the reporter of
// issue 156 said their first call came back full of.
const bulkyComment = "Here is the whole investigation.\n\n" +
	"```go\nfunc main() {\n\tfor i := range 100 {\n\t\tprintln(i)\n\t}\n}\n```\n\n" +
	"and a second paragraph nobody reading a feed of timestamps wanted."

// activityJira serves one issue with one comment and one transition, so a feed
// has an event that carries a body and an event that does not.
func activityJira(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-09-22T12:00:00.000+0000"}`))
		default:
			body, _ := jsonQuote(bulkyComment)
			_, _ = fmt.Fprintf(w, `{"startAt":0,"maxResults":50,"total":1,"issues":[
			{"id":"1","key":"ENG-1","fields":{
			  "summary":"an issue somebody worked on",
			  "status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},
			  "created":"2026-09-20T00:00:00.000+0000",
			  "updated":"2026-09-22T00:00:00.000+0000",
			  "comment":{"startAt":0,"maxResults":20,"total":1,"comments":[
			    {"id":"9","author":{"accountId":"acc-1","displayName":"Ada Lovelace"},
			     "created":"2026-09-22T09:00:00.000+0000",
			     "updated":"2026-09-22T09:00:00.000+0000","body":%s}]}},
			 "changelog":{"startAt":0,"maxResults":40,"total":1,"histories":[
			   {"id":"5","created":"2026-09-22T10:00:00.000+0000",
			    "author":{"accountId":"acc-1","displayName":"Ada Lovelace"},
			    "items":[{"field":"status","fromString":"To Do","toString":"In Progress"}]}]}}
			]}`, body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// jsonQuote is enough of a JSON string encoder for one fixture.
func jsonQuote(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// TestNoBodyDropsTheBodyAndKeepsTheEvent is the defect issue 156 reported.
//
// "What did I touch yesterday" needs the issue key, the kind, and the
// timestamp. --kind filters which events come back and not how much of each, so
// the comment bodies were unavoidable, and on a busy day they dominate the
// answer. The skill tells a caller to ask for the columns they need, on a
// command that had no way to do it.
func TestNoBodyDropsTheBodyAndKeepsTheEvent(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	full := mustRun(t, env, "issue", "activity", "--since", "-7d")
	if !strings.Contains(full.stdout, "whole investigation") {
		t.Fatalf("the fixture's body never reached the default output:\n%s", full.stdout)
	}

	lean := mustRun(t, env, "issue", "activity", "--since", "-7d", "--no-body")

	if strings.Contains(lean.stdout, "whole investigation") {
		t.Errorf("--no-body still emitted the body:\n%s", lean.stdout)
	}
	if strings.Contains(lean.stdout, "func main") {
		t.Errorf("--no-body still emitted the fenced code:\n%s", lean.stdout)
	}
	// The event itself is the answer and must survive. Dropping the comment
	// event along with its text would answer a different question.
	if !strings.Contains(lean.stdout, "comment") {
		t.Errorf("--no-body dropped the comment event, not just its body:\n%s", lean.stdout)
	}
	if !strings.Contains(lean.stdout, "ENG-1") {
		t.Errorf("--no-body dropped the issue key:\n%s", lean.stdout)
	}
	if len(lean.stdout) >= len(full.stdout) {
		t.Errorf("--no-body did not get smaller: %d vs %d", len(lean.stdout), len(full.stdout))
	}
}

// TestNoBodyDropsTheColumnInTSV is the half a caller actually sees, TSV being
// the default for a collection.
func TestNoBodyDropsTheColumnInTSV(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := mustRun(t, env, "issue", "activity", "--since", "-7d", "--no-body", "--format", "tsv")

	header := strings.Split(strings.Split(got.stdout, "\n")[0], "\t")
	for _, h := range header {
		if h == "body" {
			t.Errorf("--no-body left the body column in place: %v", header)
		}
	}
	// Every other column stays, and in the same order: this drops a column, it
	// does not redesign the row.
	want := []string{"at", "issue", "kind", "author", "field", "time-spent", "from", "to"}
	if strings.Join(header, ",") != strings.Join(want, ",") {
		t.Errorf("columns = %v, want %v", header, want)
	}
	// And the rows are not ragged, which is what a column set and a row shape
	// disagreeing looks like.
	for line := range strings.SplitSeq(strings.TrimRight(got.stdout, "\n"), "\n") {
		if n := len(strings.Split(line, "\t")); n != len(want) {
			t.Errorf("row has %d cells, header has %d: %q", n, len(want), line)
		}
	}
}
