package jql_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
)

// TestEveryQueryCarriesAnOrderBy is the invariant this file exists for. Without
// an ORDER BY the order is whatever the server happens to do, which is
// undocumented and not guaranteed to be the same between two requests — so a
// paged result can interleave two orderings and nobody sees it happen.
func TestEveryQueryCarriesAnOrderBy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sort, order string
		want        string
	}{
		{
			name: "no sort at all",
			want: "ORDER BY issuekey DESC",
		},
		{
			// --order with no --sort names the direction of the ordering that
			// is there anyway. Dropping it instead answered an explicit `asc`
			// with a descending result set.
			name: "order without sort", order: "asc",
			want: "ORDER BY issuekey ASC",
		},
		{
			name:  "order without sort, the default direction said out loud",
			order: "desc",
			want:  "ORDER BY issuekey DESC",
		},
		{
			// A sort with no direction is ascending, and the key follows as a
			// tiebreaker: a caller's field is rarely unique, so ties would
			// break arbitrarily and differently each run.
			name: "sort without order", sort: "updated",
			want: "ORDER BY updated ASC, issuekey DESC",
		},
		{
			name: "sort with order", sort: "updated", order: "desc",
			want: "ORDER BY updated DESC, issuekey DESC",
		},
		{
			// Sorting by the key itself needs no tiebreaker; adding one would
			// name the same field twice.
			name: "sort by the key", sort: "issuekey", order: "asc",
			want: "ORDER BY issuekey ASC",
		},
		{
			name: "sort by the key, cased differently", sort: "IssueKey", order: "desc",
			want: "ORDER BY IssueKey DESC",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := jql.New()
			if err := jql.AppendOrder(b, tc.sort, tc.order); err != nil {
				t.Fatalf("AppendOrder: %v", err)
			}
			got, err := b.Render()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAnUnknownOrderIsRefused covers the value that must not be defaulted.
// "desc" misspelled and silently read as ascending reverses a result set
// without saying so.
func TestAnUnknownOrderIsRefused(t *testing.T) {
	for _, order := range []string{"descending!", "up", "1", "as c"} {
		for _, sort := range []string{"updated", ""} {
			// With no --sort as well: the direction still applies, so a
			// misspelling still has to be refused rather than fall through to
			// the default it happens to resemble.
			err := jql.AppendOrder(jql.New(), sort, order)
			if err == nil {
				t.Errorf("AppendOrder accepted --order %q with --sort %q", order, sort)
				continue
			}
			if code := errs.Coerce(err).Code; code != "INVALID_ORDER" {
				t.Errorf("%q: code = %q, want INVALID_ORDER", order, code)
			}
		}
	}
	// The spellings that are accepted, so the refusal above is not just strict.
	// Case and surrounding space are normalized; anything else is not.
	for _, order := range []string{"asc", "ASC", "descending", "ASCENDING ", " desc "} {
		if err := jql.AppendOrder(jql.New(), "updated", order); err != nil {
			t.Errorf("AppendOrder refused --order %q: %v", order, err)
		}
	}
}

// TestSortsByKeyIsTheKeysetPrecondition covers the predicate keyset pagination
// branches on. Getting it wrong the permissive way resumes a query with
// `issuekey < last` against a result set that is not ordered by key, which
// silently drops rows.
func TestSortsByKeyIsTheKeysetPrecondition(t *testing.T) {
	for _, tc := range []struct {
		sort, order string
		want        bool
	}{
		{"", "", true},
		{"", "desc", true},
		{"issuekey", "desc", true},
		{"ISSUEKEY", "desc", true},
		// Ascending by key is still the wrong direction to resume with a
		// less-than bound, however the caller spelled it. `--order asc` with no
		// --sort answered true while AppendOrder was dropping the order, and
		// naming the key with no direction ascends like any other named field.
		{"issuekey", "asc", false},
		{"", "asc", false},
		{"issuekey", "", false},
		{"updated", "", false},
		{"updated", "desc", false},
		// An order that will be refused is not an ordering to resume from.
		{"", "sideways", false},
	} {
		if got := jql.SortsByKey(tc.sort, tc.order); got != tc.want {
			t.Errorf("SortsByKey(%q, %q) = %v, want %v",
				tc.sort, tc.order, got, tc.want)
		}
	}
}
