package issue_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The measurement these two tests exist for, run 2026-09-04 against Jira
// 10.4.0 Data Center and against Cloud, over two projects ABC and ENG:
//
//	ORDER BY issuekey DESC      ENG-5 ENG-4 ENG-3 ENG-2 ENG-1 ABC-6 … ABC-1
//	issuekey < "ENG-1"          no rows
//	issuekey > "ENG-1"          ENG-5 ENG-4 ENG-3 ENG-2, and no ABC row
//
// `ORDER BY issuekey` orders across projects and `issuekey <` does not compare
// across them at all. So a keyset walk over a result set spanning projects took
// a bound at the end of its first page that excluded every project it had not
// reached, saw a short page, and read that as the set running out:
// `issue list --all-projects --limit all --page-size 5` returned five of the
// eleven rows on that instance at complete="true" and exit 0, and it takes a
// page size smaller than the set to show at all. On the reporter's instance,
// where the default page of a hundred is nowhere near it, 111 of 697.
//
// Both cassettes are constructed, and the halves that could be measured were.
// The rows and their order come from that instance; the page boundary is chosen,
// because the point is which rows fall on the far side of one.

// dcClient replays one constructed Data Center conversation.
func dcClient(t *testing.T, cassette string) *issue.Client {
	t.Helper()
	tape, err := transport.LoadCassette(filepath.Join("testdata", cassette))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	conn, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(tape).Client(),
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &issue.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}
}

// TestAWalkAcrossProjectsPagesToExhaustion is the regression.
//
// The cassette answers `startAt=2` and nothing else, so a client that still
// resumed with `issuekey < "ENG-1"` fails here with an unmatched request rather
// than by returning half a result set. That is deliberate: the defect was not
// that the second page was wrong, it was that the second page was never asked
// for, and a fixture that answered both bounds could not tell the two apart.
func TestAWalkAcrossProjectsPagesToExhaustion(t *testing.T) {
	client := dcClient(t, "allprojects.datacenter.json")

	result, err := client.List(t.Context(), issue.ListOptions{
		// No project: this is `--all-projects`, or a context that sets none.
		Query:    issue.QueryOptions{},
		Limit:    registry.Limit{All: true},
		PageSize: 2,
		Fields:   issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Keyset {
		t.Error("a query spanning projects paged by key, which drops every " +
			"project after the first")
	}
	var keys []string
	for _, i := range result.Issues {
		keys = append(keys, i.Key)
	}
	want := []string{"ENG-2", "ENG-1", "ABC-6", "ABC-5"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
	if !result.Complete {
		t.Error("an exhausted result set was reported incomplete")
	}
}

// TestAWalkShortOfTheServersCountIsRefused holds the invariant that would have
// made the defect above loud on the day it shipped.
//
// Every stop condition a walk has is read off the page it just fetched, and
// each of them was true when it returned a tenth of the set: the page was
// short, so the rows had run out; the next page was empty, so there were none.
// Both statements are about the bounded query the server was sent, and a bound
// that excludes rows the caller asked for makes them true without making them
// an answer.
//
// The count on the first response is the one number in the exchange that is
// about the whole query, so a walk is made to reconcile against it. Here the
// server counts four rows, sends two, and then sends an empty page whose own
// count has dropped to two. Trusting that second count would be the same
// mistake in miniature, so the check is against the first.
func TestAWalkShortOfTheServersCountIsRefused(t *testing.T) {
	client := dcClient(t, "short-walk.datacenter.json")

	_, err := client.List(t.Context(), issue.ListOptions{
		Query:    issue.QueryOptions{},
		Limit:    registry.Limit{All: true},
		PageSize: 2,
		Fields:   issue.DefaultFields(),
	})
	if err == nil {
		t.Fatal("a walk that saw two of four rows reported no error")
	}
	structured, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("err = %v, want a structured error", err)
	}
	if structured.Code != "PAGINATION_SHORT" {
		t.Errorf("code = %q, want PAGINATION_SHORT", structured.Code)
	}
	// The two numbers are the whole diagnosis. Without them the message says
	// only that something is wrong with paging, which is what the caller
	// already knows by the time they are reading it.
	for _, want := range []string{"4 rows", "returned 2"} {
		if !strings.Contains(structured.Detail, want) {
			t.Errorf("detail %q does not name %q", structured.Detail, want)
		}
	}
}
