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

// pagedJira is a site holding total issues, ENG-total down to ENG-1, that
// pages them the way the deployment it claims to be does: Cloud by an opaque
// cursor with isLast, Data Center by startAt with a total, and by a key bound
// in the JQL when the walk narrows the query instead. It counts every request
// it is sent, which is the number an estimate has to match.
type pagedJira struct {
	url      string
	requests atomic.Int32
}

// pagedKeyBound reads the keyset bound a narrowed query carries.
var pagedKeyBound = regexp.MustCompile(`key < "?ENG-(\d+)"?`)

func newPagedJira(t *testing.T, deployment string, total int) *pagedJira {
	t.Helper()
	j := &pagedJira{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/myself"):
			_, _ = w.Write([]byte(`{"accountId":"acc-1","name":"ada",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(path, "/serverInfo"):
			_, _ = fmt.Fprintf(w, `{"version":"10.4.0","deploymentType":%q,`+
				`"serverTime":"2026-11-06T12:00:00.000+0000"}`, deployment)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/jql/parse"):
			_, _ = w.Write([]byte(`{"queries":[{"query":"q","errors":[],"warnings":[]}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/search/approximate-count"):
			_, _ = fmt.Fprintf(w, `{"count":%d}`, total)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/search"):
			_, _ = fmt.Fprintf(w, `{"startAt":0,"maxResults":0,"total":%d,"issues":[]}`, total)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/search/jql"):
			from, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("nextPageToken"), "o"))
			want, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
			rows := pagedRows(total, from, want)
			last := from+want >= total
			next := ""
			if !last {
				next = fmt.Sprintf(`"nextPageToken":"o%d",`, from+want)
			}
			_, _ = fmt.Fprintf(w, `{"issues":[%s],%s"isLast":%t}`,
				strings.Join(rows, ","), next, last)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/search"):
			// Below a key bound the set is the issues under it; without one it
			// is all of them. An offset walk never sends a bound, and a keyset
			// walk never sends an offset, so one rule serves both.
			below := total
			if m := pagedKeyBound.FindStringSubmatch(r.URL.Query().Get("jql")); m != nil {
				bound, _ := strconv.Atoi(m[1])
				below = bound - 1
			}
			startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
			want, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
			rows := pagedRows(below, startAt, want)
			_, _ = fmt.Fprintf(w, `{"startAt":%d,"maxResults":%d,"total":%d,"issues":[%s]}`,
				startAt, want, below, strings.Join(rows, ","))
		default:
			t.Errorf("the cost test was sent a request it did not expect: %s %s",
				r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	j.url = srv.URL
	return j
}

// pagedRows is want rows of a set of n issues, numbered down from ENG-n,
// starting at position from.
func pagedRows(n, from, want int) []string {
	var rows []string
	for i := from; i < min(from+want, n); i++ {
		k := n - i
		rows = append(rows, fmt.Sprintf(`{"id":"%d","key":"ENG-%d","fields":{`+
			`"summary":"row %d","status":{"name":"Open",`+
			`"statusCategory":{"key":"new","name":"To Do"}},`+
			`"created":"2026-11-01T00:00:00.000+0000",`+
			`"updated":"2026-11-05T00:00:00.000+0000"}}`, k, k, k))
	}
	return rows
}

// statedCost is the part of a request.cost document a test reads.
type statedCost struct {
	requests, retries, spent int
	approximate              bool
	paging                   string
}

var costElement = regexp.MustCompile(
	`<cost requests="(\d+)" approximate="(true|false)" retries="(\d+)" spent="(\d+)">`)

var searchPaging = regexp.MustCompile(`<step name="search"[^>]* paging="(\w+)"`)

func readCost(t *testing.T, stdout string) statedCost {
	t.Helper()
	m := costElement.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no cost element in:\n%s", stdout)
	}
	var c statedCost
	c.requests, _ = strconv.Atoi(m[1])
	c.approximate = m[2] == "true"
	c.retries, _ = strconv.Atoi(m[3])
	c.spent, _ = strconv.Atoi(m[4])
	if p := searchPaging.FindStringSubmatch(stdout); p != nil {
		c.paging = p[1]
	}
	return c
}

// TestTheStatedCostIsWhatTheRunSpends is the card's done-when, t34: the number
// --explain-cost states is the number of requests the same invocation makes,
// and as a --max-requests it is enough and one fewer is not.
//
// It is asserted against a walk rather than against arithmetic, on all three
// ways a walk pages, because the estimate is only worth anything while the
// two agree, and a change to pagination that moved the real cost has to fail
// here rather than quietly make the estimate a lie. Seven issues at two a page
// is four pages by cursor or keyset and four by offset too, since each offset
// page after the first asks again for the row the last one ended on and
// brings two new ones at this size; at the largest page size it brings one
// fewer, which TestAnOffsetPageAtTheLargestSizeBringsOneFewer holds.
//
// Each case runs the command once first, so the deployment probe is cached
// and the preparation the estimate measures is the preparation every later
// run makes.
func TestTheStatedCostIsWhatTheRunSpends(t *testing.T) {
	for _, tc := range []struct {
		name, deployment string
		total            int
		args             []string
		paging           string
		approximate      bool
	}{
		{
			name: "cloud cursor", deployment: "Cloud", total: 7,
			args: []string{"--limit", "all"}, paging: "cursor", approximate: true,
		},
		{
			name: "data center keyset", deployment: "Server", total: 7,
			args: []string{"--limit", "all"}, paging: "keyset",
		},
		{
			name: "data center offset", deployment: "Server", total: 7,
			args: []string{"--limit", "all", "--all-projects"}, paging: "offset",
		},
		{
			// The raw query is checked before it runs, which is preparation
			// the estimate has to have measured.
			name: "data center offset with a raw query", deployment: "Server", total: 7,
			args:   []string{"--limit", "all", "--all-projects", "--jql", "priority = High"},
			paging: "offset",
		},
		{
			name: "a limit below the count", deployment: "Server", total: 7,
			args: []string{"--limit", "5", "--all-projects"}, paging: "offset",
		},
		{
			name: "a limit above the count", deployment: "Cloud", total: 3,
			args: []string{"--limit", "10"}, paging: "cursor", approximate: true,
		},
		{
			// No rows on Cloud over one project costs a question about the
			// project after the walk.
			name: "nothing on cloud", deployment: "Cloud", total: 0,
			args: []string{"--limit", "all"}, paging: "cursor", approximate: true,
		},
		{
			name: "nothing on data center", deployment: "Server", total: 0,
			args: []string{"--limit", "all"}, paging: "keyset",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jira := newPagedJira(t, tc.deployment, tc.total)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")
			list := append([]string{"issue", "list", "--page-size", "2"}, tc.args...)

			_ = run(t, env, list...)

			priced := run(t, env, append([]string{"--explain-cost"}, list...)...)
			if priced.exit != exitcode.OK {
				t.Fatalf("--explain-cost exit = %v\nstdout: %s\nstderr: %s",
					priced.exit, priced.stdout, priced.stderr)
			}
			cost := readCost(t, priced.stdout)
			if cost.paging != tc.paging {
				t.Errorf("paging = %q, want %q", cost.paging, tc.paging)
			}
			if cost.approximate != tc.approximate {
				t.Errorf("approximate = %t, want %t", cost.approximate, tc.approximate)
			}

			before := jira.requests.Load()
			enough := run(t, env, append([]string{
				"--max-requests", strconv.Itoa(cost.requests),
			}, list...)...)
			made := int(jira.requests.Load() - before)
			if made != cost.requests {
				t.Errorf("--explain-cost said %d requests and the run made %d\n%s",
					cost.requests, made, priced.stdout)
			}
			if strings.Contains(enough.stderr, "raise --max-requests") {
				t.Errorf("--max-requests %d, the stated cost, did not buy the run:\n%s",
					cost.requests, enough.stderr)
			}
			spent := fmt.Sprintf("%d of %d requests spent", cost.requests, cost.requests)
			if !strings.Contains(enough.stderr, spent) {
				t.Errorf("the run under its stated cost did not report %q:\n%s",
					spent, enough.stderr)
			}

			// One request has no fewer that is a budget: --max-requests 0
			// means no cap at all.
			if cost.requests < 2 {
				return
			}
			short := run(t, env, append([]string{
				"--max-requests", strconv.Itoa(cost.requests - 1),
			}, list...)...)
			if !strings.Contains(short.stderr, "raise --max-requests") {
				t.Errorf("--max-requests %d, one under the stated cost, was not "+
					"cut short: exit = %v\n%s", cost.requests-1, short.exit, short.stderr)
			}
		})
	}
}

// TestAnOffsetPageAtTheLargestSizeBringsOneFewer is where dividing by the page
// size goes wrong. Each offset page after the first asks again for the row the
// last one ended on, and never for more than a hundred, so at the largest page
// size it brings ninety-nine new rows: 250 issues are three pages, where
// 250/100 rounds up to the same three by accident and 200 would be two by
// division and is three by the walk.
func TestAnOffsetPageAtTheLargestSizeBringsOneFewer(t *testing.T) {
	jira := newPagedJira(t, "Server", 200)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")
	list := []string{"issue", "list", "--limit", "all", "--all-projects"}

	_ = run(t, env, list...)
	cost := readCost(t, run(t, env, append([]string{"--explain-cost"}, list...)...).stdout)

	before := jira.requests.Load()
	_ = run(t, env, list...)
	if made := int(jira.requests.Load() - before); made != cost.requests {
		t.Errorf("--explain-cost said %d requests and the walk made %d",
			cost.requests, made)
	}
	if !strings.Contains(run(t, env, append([]string{"--explain-cost"}, list...)...).stdout,
		`<step name="search" requests="3" rows="200" page-size="100" paging="offset"`) {
		t.Errorf("200 issues by offset at a hundred a page is three pages")
	}
}

// TestExplainCostSpendsOneCountAndNoPage holds the flag to what it says: it
// sizes the search with one count and reads no page of it.
func TestExplainCostSpendsOneCountAndNoPage(t *testing.T) {
	jira := newPagedJira(t, "Server", 7)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")
	list := []string{"issue", "list", "--limit", "all"}

	_ = run(t, env, list...)
	before := jira.requests.Load()
	got := run(t, env, append([]string{"--explain-cost"}, list...)...)
	cost := readCost(t, got.stdout)

	made := int(jira.requests.Load() - before)
	if made != cost.spent {
		t.Errorf("the answer says it spent %d and the site saw %d", cost.spent, made)
	}
	// Preparation, which is nothing on a warm cache with no raw query, and
	// the count.
	if cost.spent != 1 {
		t.Errorf("spent = %d, want 1: the count and nothing else\n%s", cost.spent, got.stdout)
	}
	if cost.retries != 3 {
		t.Errorf("retries = %d, want the default 3", cost.retries)
	}
}

// TestExplainCostIsRefusedWhereItCannotAnswer covers the refusals. Each one
// would otherwise run the command, spending what the caller asked to be told.
func TestExplainCostIsRefusedWhereItCannotAnswer(t *testing.T) {
	jira := newPagedJira(t, "Server", 7)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{
			name: "a command that cannot price itself",
			args: []string{"--explain-cost", "project", "list"},
			says: "cannot price itself",
		},
		{
			name: "beside --explain",
			args: []string{"--explain-cost", "--explain", "issue", "list"},
			says: "--explain and --explain-cost",
		},
		{
			name: "beside --describe",
			args: []string{"--explain-cost", "--describe", "issue", "list"},
			says: "--describe and --explain-cost",
		},
		{
			name: "a resumed walk",
			args: []string{"--explain-cost", "issue", "list", "--page-token", "anything"},
			says: "--page-token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := jira.requests.Load()
			got := run(t, env, tc.args...)
			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want 2\nstdout: %s\nstderr: %s",
					got.exit, got.stdout, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a refusal wrote to stdout:\n%s", got.stdout)
			}
			if !strings.Contains(got.stderr, tc.says) {
				t.Errorf("the refusal does not say %q:\n%s", tc.says, got.stderr)
			}
			if tc.name != "a resumed walk" {
				if n := jira.requests.Load() - before; n != 0 {
					t.Errorf("a refusal the declaration decides sent %d requests", n)
				}
			}
		})
	}
}

// TestARunWithoutABudgetSaysNothingAboutSpend is the other half of
// REQUESTS_SPENT: a run nobody budgeted writes nothing extra to stderr.
func TestARunWithoutABudgetSaysNothingAboutSpend(t *testing.T) {
	jira := newPagedJira(t, "Server", 3)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", jira.url, "--project", "ENG")

	got := run(t, env, "issue", "list", "--limit", "all")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\n%s", got.exit, got.stderr)
	}
	if strings.Contains(got.stderr, "REQUESTS_SPENT") {
		t.Errorf("a run with no --max-requests reported its spend:\n%s", got.stderr)
	}
}
