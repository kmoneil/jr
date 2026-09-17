package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// endlessJira answers every search with a full page out of a set far larger
// than any walk here will finish, so the only thing that can stop a walk is a
// bound the caller set.
//
// Rows are generated from the offset asked for, which keeps an offset walk's
// overlap row consistent: the page starting at n always begins with the row the
// page before it ended on.
func endlessJira(t *testing.T) string {
	t.Helper()
	const total = 500

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
			return
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-08-28T12:00:00.000+0000"}`))
			return
		}

		startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		want, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
		if err != nil || want <= 0 {
			want = 50
		}

		var rows []string
		for i := range want {
			n := total - (startAt + i)
			rows = append(rows, fmt.Sprintf(`{"id":"%d","key":"ENG-%d","fields":{`+
				`"summary":"row %d","status":{"name":"Open",`+
				`"statusCategory":{"key":"new","name":"To Do"}},`+
				`"created":"2026-01-01T00:00:00.000+0000",`+
				`"updated":"2026-09-17T00:00:00.000+0000"}}`, n, n, n))
		}
		_, _ = fmt.Fprintf(w, `{"startAt":%d,"maxResults":%d,"total":%d,"issues":[%s]}`,
			startAt, want, total, strings.Join(rows, ","))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestABudgetCutNamesTheBudgetAndNotTheLimit is the defect this file exists for.
//
// A walk stopped by --max-requests was told to raise --limit, with --limit all
// already on the command line, because the warning picked its remedy from
// whether a resume token existed and nothing told it what had actually stopped
// the walk. On `issue activity` that is the whole remedy and none of it can be
// followed: the limit is already lifted and the command has no --page-token at
// all.
func TestABudgetCutNamesTheBudgetAndNotTheLimit(t *testing.T) {
	url := endlessJira(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			// This one can resume, so the token stays in the remedy.
			name: "issue list",
			args: []string{
				"issue", "list", "--all-projects", "--limit", "all",
				"--page-size", "2",
			},
		},
		{
			// This one cannot. An event feed is merged from three projections
			// across a page of issues, so there is no position to resume from,
			// which is what makes a wrong remedy unfollowable rather than
			// merely unhelpful.
			name: "issue activity",
			args: []string{
				"issue", "activity", "--since", "-1d", "--all-projects",
				"--limit", "all", "--page-size", "2",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

			args := append([]string{"--max-requests", "4"}, tc.args...)
			got := run(t, env, args...)

			if got.exit != exitcode.Partial {
				t.Fatalf("exit = %v, want 3 (PARTIAL)\n%s", got.exit, got.stderr)
			}
			if !strings.Contains(got.stderr, "RESULT_TRUNCATED") {
				t.Fatalf("no truncation warning:\n%s", got.stderr)
			}
			remedy := remedyLine(t, got.stderr)
			if !strings.Contains(remedy, "--max-requests") {
				t.Errorf("the budget stopped this walk and the remedy does not "+
					"name --max-requests: %q", remedy)
			}
			// The caller already passed `--limit all`. Offering it back is the
			// dead end with a helpful tone that the truncation warning's own
			// comment warns about.
			if strings.Contains(remedy, "--limit") {
				t.Errorf("the remedy offers a bound the caller already lifted: %q", remedy)
			}
		})
	}
}

// TestALimitCutStillNamesTheLimit is the converse, and the reason the fix is a
// reason rather than a rewording: the ordinary case must not move.
func TestALimitCutStillNamesTheLimit(t *testing.T) {
	url := endlessJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "list", "--limit", "3")
	if got.exit != exitcode.Partial {
		t.Fatalf("exit = %v, want 3 (PARTIAL)\n%s", got.exit, got.stderr)
	}
	remedy := remedyLine(t, got.stderr)
	if !strings.Contains(remedy, "--limit") {
		t.Errorf("a walk stopped by --limit does not name it: %q", remedy)
	}
	if strings.Contains(remedy, "--max-requests") {
		t.Errorf("a walk stopped by --limit names the budget: %q", remedy)
	}
}

// remedyLine pulls the remedy out of the TSV warning, which is what a caller
// reading stderr sees by default.
func remedyLine(t *testing.T, stderr string) string {
	t.Helper()
	for line := range strings.SplitSeq(stderr, "\n") {
		if name, value, ok := strings.Cut(line, "\t"); ok && name == "remedy" {
			return value
		}
	}
	t.Fatalf("no remedy in the warning:\n%s", stderr)
	return ""
}
