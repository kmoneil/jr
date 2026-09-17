package issue_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// allProjectsWalk is the invocation every test here drives: no project, so the
// walk pages by offset, which is the only mode a shift can reach.
func allProjectsWalk(pageSize int) issue.ListOptions {
	return issue.ListOptions{
		Query:    issue.QueryOptions{},
		Limit:    registry.Limit{All: true},
		PageSize: pageSize,
		Fields:   issue.DefaultFields(),
	}
}

// TestAPageThatDoesNotStartWhereTheLastOneEndedIsRefused is the regression for
// the defect measured on the rig at Jira 10.4.0 on 2026-09-17.
//
// Six rows at two a page. Page one takes ENG-7 and ENG-6. Between the two
// requests ENG-7 leaves the result set and ENG-1 joins it, so every row below
// ENG-7 moves up one and the row after ENG-6 is now at the position the walk
// was about to skip. The old walk asked for `startAt=2`, was handed ENG-4 and
// ENG-3, and reported six rows and `complete="true"` at exit 0 over a set it
// had never read ENG-5 from. Both counts agreed, because one row left and one
// joined, which is why `PAGINATION_SHORT` saw nothing.
//
// The cassette answers only the overlap request, so a client that goes back to
// asking `startAt=2` fails here with an unmatched request rather than with a
// wrong answer: the defect was not that page two was wrong, it was that nothing
// checked page two against page one.
func TestAPageThatDoesNotStartWhereTheLastOneEndedIsRefused(t *testing.T) {
	client := dcClient(t, "shifted-walk.datacenter.json")

	_, err := client.List(t.Context(), allProjectsWalk(2))
	if err == nil {
		t.Fatal("a walk over a set that moved under it reported no error")
	}
	structured, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("err = %v, want a structured error", err)
	}
	if structured.Code != "PAGINATION_SHIFTED" {
		t.Fatalf("code = %q, want PAGINATION_SHIFTED", structured.Code)
	}
	// Both keys, because "paging is unstable" is what the caller already knows
	// by the time they are reading this. Which row was expected and which one
	// turned up is the part they cannot get anywhere else.
	for _, want := range []string{"ENG-6", "ENG-5"} {
		if !strings.Contains(structured.Detail, want) {
			t.Errorf("detail %q does not name %q", structured.Detail, want)
		}
	}
}

// TestAnOffsetWalkEmitsEveryRowExactlyOnce holds the other half: the row a page
// re-reads is a check, not a result.
//
// Getting this wrong is not subtle at the server and is invisible in the
// answer: the anchor row would simply appear twice, which for `issue activity`
// is one issue's whole changelog written into the feed a second time.
func TestAnOffsetWalkEmitsEveryRowExactlyOnce(t *testing.T) {
	client := dcClient(t, "stable-walk.datacenter.json")

	result, err := client.List(t.Context(), allProjectsWalk(2))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Keyset {
		t.Fatal("a query spanning projects paged by key")
	}
	want := []string{"ENG-6", "ENG-5", "ENG-4", "ENG-3", "ENG-2", "ENG-1"}
	var keys []string
	for _, i := range result.Issues {
		keys = append(keys, i.Key)
	}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if !result.Complete {
		t.Error("an exhausted result set was reported incomplete")
	}
	// Three requests for six rows at two a page: the overlap buys the check
	// with a row, not with a round trip.
	if result.Requests != 3 {
		t.Errorf("requests = %d, want 3", result.Requests)
	}
}

// TestAWalkDoesNotRefuseAChangeBelowItsCursor is the false-positive guard, and
// it is the reason the check compares a key rather than a count.
//
// A row joining underneath the walk moves nothing it has already read, so the
// anchor still arrives where it was left and the walk must collect the new row
// rather than refusing. A check written against the server's `total` would have
// fired here, on a set that lost nothing.
func TestAWalkDoesNotRefuseAChangeBelowItsCursor(t *testing.T) {
	client := dcClient(t, "grown-below-walk.datacenter.json")

	result, err := client.List(t.Context(), allProjectsWalk(2))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var keys []string
	for _, i := range result.Issues {
		keys = append(keys, i.Key)
	}
	want := "ENG-6 ENG-5 ENG-4 ENG-3 ENG-2 ENG-1 ABC-9"
	if strings.Join(keys, " ") != want {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if !result.Complete {
		t.Error("an exhausted result set was reported incomplete")
	}
}

// TestAResumedWalkChecksItsFirstPage covers the case the anchor travels in the
// token for.
//
// A resume is where a shift is most likely, because the gap between the page
// that minted the token and the request that spends it is however long the
// caller took. The token names the row the next page has to begin with, so the
// first request after a resume is checked like any other.
func TestAResumedWalkChecksItsFirstPage(t *testing.T) {
	client := dcClient(t, "resume-shifted.datacenter.json")

	opt := allProjectsWalk(2)
	opt.PageToken = issue.EncodePageToken(issue.PageToken{
		Deployment: site.DataCenter, Offset: 2, Anchor: "ENG-6",
	})

	_, err := client.List(t.Context(), opt)
	if err == nil {
		t.Fatal("a resumed walk over a moved set reported no error")
	}
	structured, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("err = %v, want a structured error", err)
	}
	if structured.Code != "PAGINATION_SHIFTED" {
		t.Fatalf("code = %q, want PAGINATION_SHIFTED", structured.Code)
	}
}

// TestAnAnchorWithoutAnOffsetIsRefused keeps a hand-assembled token from
// reaching the arithmetic that subtracts the overlap row.
func TestAnAnchorWithoutAnOffsetIsRefused(t *testing.T) {
	encoded := issue.EncodePageToken(issue.PageToken{
		Deployment: site.DataCenter, Offset: 3, Anchor: "ENG-6",
	})
	if _, err := issue.ParsePageToken(encoded); err != nil {
		t.Fatalf("a token this tool mints was refused: %v", err)
	}

	// Offset zero is the first row of the set, so there is no row above it to
	// have ended a previous page.
	encoded = issue.EncodePageToken(issue.PageToken{
		Deployment: site.DataCenter, Offset: 0, Anchor: "ENG-6", Cursor: "x",
	})
	_, err := issue.ParsePageToken(encoded)
	if err == nil {
		t.Fatal("a token carrying an anchor and no offset was accepted")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_PAGE_TOKEN" {
		t.Errorf("code = %q, want INVALID_PAGE_TOKEN", code)
	}
}
