package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		case strings.HasSuffix(r.URL.Path, "/user/search"):
			// One person per deployment, each with the id that deployment
			// puts in JQL: an accountId on Cloud, and on Data Center a
			// username holding the one character a shell-quoted clause
			// has to escape.
			if deployment == "Cloud" {
				_, _ = w.Write([]byte(`[{"accountId":"712020:8f3a",` +
					`"displayName":"Ada Lovelace","emailAddress":"ada@example.invalid",` +
					`"active":true}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"name":"o'brien","key":"JIRAUSER10100",` +
				`"displayName":"Grace O'Brien","emailAddress":"grace@example.invalid",` +
				`"active":true}]`))
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

// TestTheRecordedRefusalReadsAsASentence is the refusal the sandbox gave when
// its count was recorded, four candidates and one request allowed, which is
// where a floor of one first met a message written for many: "at least 1
// search requests".
func TestTheRecordedRefusalReadsAsASentence(t *testing.T) {
	jira := sweepJira(t, "Cloud", 4)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

	// Two requests: the probe on a cold cache, and the count.
	got := run(t, env, "--max-requests", "2", "issue", "activity", "--since", "-30d",
		"--all-projects")

	const want = "issue activity would read about 4 candidate issues, " +
		"at least 1 search request, and --max-requests leaves 0"
	if !strings.Contains(got.stderr, want) {
		t.Errorf("want %q in:\n%s", want, got.stderr)
	}
}

// TestTheRefusalForOnePersonNamesTheirInvolvement is issue 302.
//
// With --user and no --jql the remedy used to say "--jql" and stop, and the
// clause it meant was in three documents and nowhere in the refusal. The
// reporter had to go and read one of them, and with the clause the same week
// was one page at exit 0. So the remedy now carries it, spelled for the person
// --user named: currentUser() where that is what was typed, so the query says
// "whoever runs this" as issue list does, and otherwise the id the deployment
// puts in JQL. It is shell-quoted, because a Data Center username can hold the
// one character that would end a single-quoted argument.
//
// The clause is not exact, which is why the remedy says what it misses. Both
// refusals carry it: the one without --max-requests, and the one where it
// leaves too little.
func TestTheRefusalForOnePersonNamesTheirInvolvement(t *testing.T) {
	const caveat = "which misses a comment, or a change to a field other than " +
		"status, on an issue they are not otherwise tied to"
	for _, tc := range []struct {
		name, deployment, user string
		budget                 []string
		clause                 string
	}{
		{
			name: "the reported run", deployment: "Server", user: "currentUser",
			clause: `'assignee = currentUser() OR reporter = currentUser() OR ` +
				`creator = currentUser() OR worklogAuthor = currentUser() OR ` +
				`status CHANGED BY currentUser()'`,
		},
		{
			name: "a name on Cloud", deployment: "Cloud", user: "Ada Lovelace",
			clause: `'assignee = "712020:8f3a" OR reporter = "712020:8f3a" OR ` +
				`creator = "712020:8f3a" OR worklogAuthor = "712020:8f3a" OR ` +
				`status CHANGED BY "712020:8f3a"'`,
		},
		{
			name: "a quote in a Data Center username", deployment: "Server",
			user: "Grace O'Brien",
			clause: `'assignee = "o'\''brien" OR reporter = "o'\''brien" OR ` +
				`creator = "o'\''brien" OR worklogAuthor = "o'\''brien" OR ` +
				`status CHANGED BY "o'\''brien"'`,
		},
		{
			name: "a budget too small", deployment: "Server", user: "currentUser",
			budget: []string{"--max-requests", "20"},
			clause: `'assignee = currentUser() OR reporter = currentUser() OR ` +
				`creator = currentUser() OR worklogAuthor = currentUser() OR ` +
				`status CHANGED BY currentUser()'`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jira := sweepJira(t, tc.deployment, 5873)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

			args := append(append([]string{}, tc.budget...), "issue", "activity",
				"--since", "-7d", "--user", tc.user, "--limit", "all", "--all-projects",
				"--format", "json")
			got := run(t, env, args...)
			if code := errorCodeOf(t, got.stderr); code != "SWEEP_TOO_LARGE" {
				t.Fatalf("code = %q, want SWEEP_TOO_LARGE\n%s", code, got.stderr)
			}

			remedy := remedyOf(t, got.stderr)
			if !strings.Contains(remedy, "--jql "+tc.clause) {
				t.Errorf("the remedy does not name the involvement clause\n"+
					"want: --jql %s\nin:   %s", tc.clause, remedy)
			}
			if !strings.Contains(remedy, caveat) {
				t.Errorf("the remedy names the clause without what it misses:\n%s", remedy)
			}
			// Narrowing is offered first and accepting the cost is still
			// there, so the number the remedy names is the one it always was.
			if !regexp.MustCompile(`--max-requests (to )?\d+ or more`).MatchString(remedy) {
				t.Errorf("the remedy no longer names a --max-requests value:\n%s", remedy)
			}
		})
	}
}

// TestTheRefusalLeavesAGivenQueryAlone is the other side of issue 302. A caller
// who passed --jql has already narrowed the candidates their own way, and
// handing them a second clause would be telling them their query was wrong.
// Without --user there is no person to spell a clause for.
func TestTheRefusalLeavesAGivenQueryAlone(t *testing.T) {
	const generic = "narrow the candidates with --project, --jql or a shorter --since, " +
		"or pass --max-requests"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"--jql given", []string{"--user", "currentUser", "--jql", "labels = backend"}},
		{"no --user", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jira := sweepJira(t, "Server", 5873)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

			args := append([]string{
				"issue", "activity", "--since", "-7d",
				"--all-projects", "--format", "json",
			}, tc.args...)
			got := run(t, env, args...)
			if code := errorCodeOf(t, got.stderr); code != "SWEEP_TOO_LARGE" {
				t.Fatalf("code = %q, want SWEEP_TOO_LARGE\n%s", code, got.stderr)
			}
			if remedy := remedyOf(t, got.stderr); !strings.HasPrefix(remedy, generic) {
				t.Errorf("remedy = %q, want it to begin %q", remedy, generic)
			}
		})
	}
}

// refusalOf reads the error document out of a JSON run's stderr. A warning
// may be written ahead of it, so it reads documents until one is an error.
func refusalOf(t *testing.T, stderr string) (code, remedy string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stderr))
	for {
		var doc struct {
			Error *struct {
				Code   string `json:"code"`
				Remedy string `json:"remedy"`
			} `json:"error"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			t.Fatalf("stderr holds no error document:\n%s", stderr)
		}
		if err != nil {
			t.Fatalf("stderr is not JSON: %v\n%s", err, stderr)
		}
		if doc.Error != nil {
			return doc.Error.Code, doc.Error.Remedy
		}
	}
}

func errorCodeOf(t *testing.T, stderr string) string {
	t.Helper()
	code, _ := refusalOf(t, stderr)
	return code
}

func remedyOf(t *testing.T, stderr string) string {
	t.Helper()
	_, remedy := refusalOf(t, stderr)
	return remedy
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
