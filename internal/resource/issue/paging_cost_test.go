package issue_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// What a run of N issues costs in round trips is the number this tool is
// tuned on, and until now nothing pinned it. `DefaultPageSize` sat at 50
// behind a comment describing a cost the arithmetic already avoided —
// `wantFor` asks for min(pageSize, remaining) — and the comment could be
// wrong for as long as it liked, because a change to the constant broke no
// assertion. Every cassette in this package is three issues long, which is
// below any page size worth arguing about.
//
// So this constructs its input rather than recording one. The sandbox has
// nowhere near enough issues to page at 100, and a request count is exactly
// the kind of claim a fixture recorded from a small project cannot make.

// pagingServer answers searches out of a synthetic project of `total` issues,
// keyed ENG-1 through ENG-<total> and served newest-first, and records what
// each request asked for.
//
// It speaks both deployments, because they end a run by different evidence:
// Cloud is authoritative through nextPageToken and isLast, Data Center runs
// out when a keyset page comes back shorter than it asked for. A request-count
// assertion that only covered one would be pinning half the loop.
func pagingServer(t *testing.T, kind site.Kind, total int) (*httptest.Server, *pagingLog) {
	t.Helper()
	log := &pagingLog{}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want, err := strconv.Atoi(q.Get("maxResults"))
		if err != nil || want < 1 {
			http.Error(w, "maxResults: "+q.Get("maxResults"), http.StatusBadRequest)
			return
		}
		log.record(want)

		// Where this page starts, counted from the newest issue.
		from := 0
		switch {
		case kind == site.Cloud && q.Get("nextPageToken") != "":
			from, _ = strconv.Atoi(q.Get("nextPageToken"))
		case kind != site.Cloud:
			// The keyset bound rides in the JQL, which is the whole point of
			// keyset paging: `issuekey < "ENG-151"` means the next page starts
			// at ENG-150, which is the 100th issue counting down from 250.
			if m := keysetBound.FindStringSubmatch(q.Get("jql")); m != nil {
				bound, _ := strconv.Atoi(m[1])
				from = total - bound + 1
			}
		}

		n := min(want, max(0, total-from))
		keys := make([]string, 0, n)
		for i := range n {
			keys = append(keys, fmt.Sprintf("ENG-%d", total-from-i))
		}

		_, _ = w.Write([]byte(searchBody(kind, keys, from+n, total)))
	})), log
}

// keysetBound reads the cursor back out of the JQL the client built.
var keysetBound = regexp.MustCompile(`issuekey < "ENG-(\d+)"`)

// searchBody renders a page in the shape each deployment sends. Only the
// fields the client reads are present; a search returns far more.
func searchBody(kind site.Kind, keys []string, next, total int) string {
	issues := make([]string, 0, len(keys))
	for _, k := range keys {
		issues = append(issues, fmt.Sprintf(
			`{"id":"1","key":%q,"fields":{"summary":"s","status":{"name":"To Do",`+
				`"statusCategory":{"key":"new"}},"issuetype":{"name":"Task"}}}`, k,
		))
	}
	body := "{\"issues\":[" + strings.Join(issues, ",") + "]"

	if kind == site.Cloud {
		if next >= total {
			return body + `,"isLast":true}`
		}
		return body + fmt.Sprintf(`,"isLast":false,"nextPageToken":"%d"}`, next)
	}
	return body + fmt.Sprintf(`,"startAt":%d,"total":%d}`, next-len(keys), total)
}

// pagingLog records the maxResults of every request, in order. The count is
// len(asked) rather than a second field, so the two cannot disagree.
type pagingLog struct {
	mu    sync.Mutex
	asked []int
}

func (l *pagingLog) record(want int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked = append(l.asked, want)
}

func (l *pagingLog) values() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.asked...)
}

// TestAListCostsTheRoundTripsItShould pins the request count of a run, and the
// page size each request actually asked the server for.
//
// The two are asserted together on purpose. A count alone would pass if the
// client asked for the right number of pages at the wrong size, which is the
// half a caller pays for on a slow link.
func TestAListCostsTheRoundTripsItShould(t *testing.T) {
	const total = 250

	for _, tc := range []struct {
		name     string
		limit    registry.Limit
		requests int
		asked    []int
	}{
		// The default invocation, and the reason raising the page size cost
		// nothing: min(100, 50) is 50, so this is one request for fifty rows
		// exactly as it was when the default page was fifty.
		{"the default limit", registry.Limit{N: registry.DefaultLimit}, 1, []int{50}},

		// Below the default, the page size is invisible: the client never asks
		// for a row it was not told to fetch.
		{"a small limit", registry.Limit{N: 10}, 1, []int{10}},

		// Above it, the page size is the whole cost. Five hundred rows is five
		// requests at a hundred and was ten at fifty.
		{"a limit spanning pages", registry.Limit{N: 250}, 3, []int{100, 100, 50}},

		// The last request asks for a full page and gets a short one, which is
		// how each deployment learns the set ran out.
		{"everything", registry.Limit{All: true}, 3, []int{100, 100, 100}},
	} {
		for _, kind := range deployments {
			t.Run(tc.name+"/"+string(kind), func(t *testing.T) {
				srv, log := pagingServer(t, kind, total)
				defer srv.Close()

				conn, err := transport.New(transport.Options{BaseURL: srv.URL})
				if err != nil {
					t.Fatalf("new transport: %v", err)
				}
				client := &issue.Client{
					Transport: conn,
					Site:      site.Info{Kind: kind, Version: "test"},
				}

				result, err := client.List(t.Context(), issue.ListOptions{
					Query:  issue.QueryOptions{Project: "ENG"},
					Limit:  tc.limit,
					Fields: issue.DefaultFields(),
				})
				if err != nil {
					t.Fatalf("list: %v", err)
				}

				if result.Requests != tc.requests {
					t.Errorf("cost %d requests, want %d. A change here is a change "+
						"in what a run costs against a real instance, which is the "+
						"number DefaultPageSize exists to set",
						result.Requests, tc.requests)
				}
				if got := log.values(); !equalInts(got, tc.asked) {
					t.Errorf("asked the server for %v rows per request, want %v", got, tc.asked)
				}

				wantRows := total
				if !tc.limit.All {
					wantRows = min(tc.limit.N, total)
				}
				if len(result.Issues) != wantRows {
					t.Errorf("returned %d issues, want %d — the request count is only "+
						"worth pinning if the rows arrived", len(result.Issues), wantRows)
				}
			})
		}
	}
}

// TestKeysetPaysOneExtraRequestWhenTheSetDividesEvenly measures a deployment
// difference in cost that nothing had written down.
//
// Cloud is told the run is over: the server sets isLast. Data Center is not —
// with a keyset bound there is no offset arithmetic to trust, so the only
// evidence the set ran out is a page shorter than the one asked for. When the
// result count is an exact multiple of the page size that page never comes,
// and the run costs one more request to be told nothing.
//
// This is the price of the stability keyset buys and not a defect: the
// alternative is believing a `total` that moves when somebody files an issue
// mid-run. It is worth one request per exhaustive run in the worst case, and
// worth an assertion so that number is known rather than assumed.
func TestKeysetPaysOneExtraRequestWhenTheSetDividesEvenly(t *testing.T) {
	const total = 200 // exactly two pages of DefaultPageSize.

	for kind, want := range map[site.Kind]int{site.Cloud: 2, site.DataCenter: 3} {
		t.Run(string(kind), func(t *testing.T) {
			srv, _ := pagingServer(t, kind, total)
			defer srv.Close()

			conn, err := transport.New(transport.Options{BaseURL: srv.URL})
			if err != nil {
				t.Fatalf("new transport: %v", err)
			}
			client := &issue.Client{Transport: conn, Site: site.Info{Kind: kind, Version: "test"}}

			result, err := client.List(t.Context(), issue.ListOptions{
				Query:  issue.QueryOptions{Project: "ENG"},
				Limit:  registry.Limit{All: true},
				Fields: issue.DefaultFields(),
			})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if result.Requests != want {
				t.Errorf("cost %d requests, want %d", result.Requests, want)
			}
			if len(result.Issues) != total || !result.Complete {
				t.Errorf("got %d issues, complete %v — want all %d and complete, "+
					"whatever the extra request cost",
					len(result.Issues), result.Complete, total)
			}
		})
	}
}

// TestTheDefaultPageIsTheLargestTheSearchAccepts is the constant itself, held
// to the bound beside it.
//
// DefaultPageSize is written as MaxPageSize rather than as 100 so the two
// cannot disagree, and this fails if somebody unpicks that by writing a
// literal back in — the state it was in when the default was half the cap and
// the comment explaining why described something else entirely.
func TestTheDefaultPageIsTheLargestTheSearchAccepts(t *testing.T) {
	if issue.DefaultPageSize != issue.MaxPageSize {
		t.Errorf("DefaultPageSize is %d against a cap of %d. Asking for less than "+
			"the server honours doubles the round trips of every run above --limit "+
			"%d and buys nothing below it, because the client already asks for "+
			"min(pageSize, remaining). If this is deliberate, the reason belongs "+
			"in the comment on the constant, where the last wrong one was",
			issue.DefaultPageSize, issue.MaxPageSize, registry.DefaultLimit)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
