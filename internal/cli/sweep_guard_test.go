package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// sweptJira is a site whose candidate set is as large as a test says, and which
// counts the two kinds of search it is sent. A count is a POST, to the zero-row
// search on Data Center and to approximate-count on Cloud; a page is a GET. So
// "refused before the sweep" is a number a test can read: one count, no pages.
type sweptJira struct {
	url    string
	counts atomic.Int32
	pages  atomic.Int32
}

// sweepJira serves total candidates, generated from the offset asked for so an
// offset walk's overlap row is always the row the page before it ended on. The
// issues carry no events, because the guard is about what the sweep costs and
// not about what it finds.
func sweepJira(t *testing.T, deployment string, total int) *sweptJira {
	t.Helper()
	s := &sweptJira{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = fmt.Fprintf(w, `{"version":"10.4.0","deploymentType":%q,`+
				`"serverTime":"2026-09-30T12:00:00.000+0000"}`, deployment)
		case r.Method == http.MethodPost &&
			strings.HasSuffix(r.URL.Path, "/search/approximate-count"):
			s.counts.Add(1)
			_, _ = fmt.Fprintf(w, `{"count":%d}`, total)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/search"):
			s.counts.Add(1)
			_, _ = fmt.Fprintf(w, `{"startAt":0,"maxResults":0,"total":%d,"issues":[]}`, total)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/search"):
			s.pages.Add(1)
			startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
			want, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
			var rows []string
			for i := startAt; i < min(startAt+want, total); i++ {
				n := total - i
				rows = append(rows, fmt.Sprintf(`{"id":"%d","key":"ENG-%d","fields":{`+
					`"summary":"row %d","status":{"name":"Open",`+
					`"statusCategory":{"key":"new","name":"To Do"}},`+
					`"created":"2026-09-01T00:00:00.000+0000",`+
					`"updated":"2026-09-29T00:00:00.000+0000"}}`, n, n, n))
			}
			_, _ = fmt.Fprintf(w, `{"startAt":%d,"maxResults":%d,"total":%d,"issues":[%s]}`,
				startAt, want, total, strings.Join(rows, ","))
		default:
			t.Errorf("the sweep guard test was sent a request it did not expect: %s %s",
				r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// TestTheReportedSweepIsRefusedBeforeAPage is issue 149.
//
// The reported invocation ran past five minutes on Data Center and was killed
// with nothing on stdout: every issue updated anywhere that week was a
// candidate, about sixty pages of changelog, to report 31 events. --user and
// --limit narrowed nothing the server was asked, because both are applied after
// the page arrives. One count says how big the sweep is before any of it is
// paid for.
//
// The floor is the offset walk's, which is the walk --all-projects makes on
// Data Center: 100 rows on the first page and 99 new ones on each page after
// it, since every later page asks again for the row the last one ended on. That
// is 60 pages for 5873 candidates, where dividing by the page size says 59.
func TestTheReportedSweepIsRefusedBeforeAPage(t *testing.T) {
	for _, tc := range []struct {
		deployment, count, floor string
	}{
		{deployment: "Server", count: "5873", floor: "at least 60 search requests"},
		// Cloud walks by cursor, so every page is new rows, and its only count
		// is an estimate, which the refusal says rather than quoting as exact.
		{deployment: "Cloud", count: "about 5873", floor: "at least 59 search requests"},
	} {
		t.Run(tc.deployment, func(t *testing.T) {
			jira := sweepJira(t, tc.deployment, 5873)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

			got := run(t, env, "issue", "activity", "--since", "-7d", "--user", "currentUser",
				"--limit", "all", "--all-projects", "--format", "xml")

			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want 2 (USAGE)\nstdout: %s\nstderr: %s",
					got.exit, got.stdout, got.stderr)
			}
			if code := errorCodeIn(got.stderr); code != "SWEEP_TOO_LARGE" {
				t.Errorf("code = %q, want SWEEP_TOO_LARGE\n%s", code, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.count+" candidate issues") {
				t.Errorf("the refusal does not name the count %q:\n%s", tc.count, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.floor) {
				t.Errorf("the refusal does not name the floor %q:\n%s", tc.floor, got.stderr)
			}
			if n := jira.counts.Load(); n != 1 {
				t.Errorf("sent %d counts, want exactly 1", n)
			}
			if n := jira.pages.Load(); n != 0 {
				t.Errorf("sent %d pages of the sweep it refused", n)
			}
		})
	}
}

// TestTheNumberTheRefusalNamesIsEnough holds the remedy to its word.
//
// --max-requests is how a caller accepts the cost, and the refusal names the
// value to pass. That number has to buy the whole sweep, because a sweep the
// budget cuts short cannot be resumed, and one fewer has to be refused for the
// same reason rather than run to a partial feed.
//
// Thirty candidates at two a page is fifteen pages, over the ceiling of ten.
// The first run warms the deployment cache, so the probe it spends is not
// spent again and the second refusal names the number every later run needs.
func TestTheNumberTheRefusalNamesIsEnough(t *testing.T) {
	jira := sweepJira(t, "Server", 30)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")
	args := []string{"issue", "activity", "--since", "-7d", "--all-projects", "--page-size", "2"}

	_ = run(t, env, args...)
	refused := run(t, env, args...)
	if code := errorCodeIn(refused.stderr); code != "SWEEP_TOO_LARGE" {
		t.Fatalf("fifteen pages were not refused: code = %q\n%s", code, refused.stderr)
	}
	enough := namedBudget(t, refused.stderr)

	short := run(t, env, append([]string{"--max-requests", strconv.Itoa(enough - 1)}, args...)...)
	if code := errorCodeIn(short.stderr); code != "SWEEP_TOO_LARGE" {
		t.Errorf("--max-requests %d, one short of the floor, was not refused: "+
			"exit = %v\n%s", enough-1, short.exit, short.stderr)
	}
	if n := jira.pages.Load(); n != 0 {
		t.Fatalf("a refused sweep sent %d pages", n)
	}

	accepted := run(t, env, append([]string{"--max-requests", strconv.Itoa(enough)}, args...)...)
	if accepted.exit != exitcode.OK {
		t.Fatalf("--max-requests %d, the number the refusal named, did not buy "+
			"the sweep: exit = %v\n%s", enough, accepted.exit, accepted.stderr)
	}
	if n := jira.pages.Load(); n != 15 {
		t.Errorf("the accepted sweep sent %d pages, want 15", n)
	}
}

// namedBudget reads the --max-requests value out of the refusal's remedy.
func namedBudget(t *testing.T, stderr string) int {
	t.Helper()
	m := regexp.MustCompile(`--max-requests (\d+) or more`).FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("the refusal names no --max-requests value:\n%s", stderr)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// TestASweepUnderTheCeilingCostsOneCountAndNothingElse is the price of the
// guard, asserted so it stays one request.
//
// Five candidates at two a page is three pages, and it was three pages before
// the guard existed. The count is the only request added, and nothing about
// the sweep behind it moves.
func TestASweepUnderTheCeilingCostsOneCountAndNothingElse(t *testing.T) {
	jira := sweepJira(t, "Server", 5)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

	got := run(t, env, "issue", "activity", "--since", "-7d", "--all-projects",
		"--page-size", "2")

	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, want 0\n%s", got.exit, got.stderr)
	}
	if n := jira.counts.Load(); n != 1 {
		t.Errorf("sent %d counts, want exactly 1", n)
	}
	if n := jira.pages.Load(); n != 3 {
		t.Errorf("sent %d pages, want the 3 the sweep always took", n)
	}
}
