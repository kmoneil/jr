package issue_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/render"
)

// historySaves is a changelog of n saves, oldest first, one a second from
// 14:00Z, each setting labels to its own index so a row says which save it
// came from. All of it is inside the feed's window, which opens at 13:30Z.
func historySaves(n int) []string {
	first := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	saves := make([]string, n)
	for k := range saves {
		at := first.Add(time.Duration(k) * time.Second).Format("2006-01-02T15:04:05.000-0700")
		saves[k] = feedSave(fmt.Sprint(1000+k), at, "labels", "", fmt.Sprint(k))
	}
	return saves
}

// toValues is the `to` column of every row of a TSV feed, which historySaves
// sets to the index of the save the row came from.
func toValues(doc string) []string {
	var out []string
	for _, row := range strings.Split(strings.TrimRight(doc, "\n"), "\n")[1:] {
		cells := strings.Split(row, "\t")
		out = append(out, cells[len(cells)-1])
	}
	return out
}

// TestAClippedChangelogIsToppedUpAndSaysSoWhenItCannotBe is the second request
// a clipped changelog costs, and the request it must not cost.
//
// Cloud inlines the newest forty saves of an issue's changelog in a search and
// says how many it holds. Until this, a feed over an issue with more reported
// the issue clipped and left the rest to `issue history`; a poll over it
// issued no cursor at all, so one busy issue stalled `issue changes` until it
// left the window. Cloud's paged /issue/{key}/changelog reads a hundred saves
// a request, so one further request makes most such issues whole.
//
// Three cases, and the request log is asserted in each, because the cost is
// half the design: an issue the projection answered for costs nothing, a
// clipped one costs exactly one, and one past a hundred costs one and asks for
// the newest page. The paged route is oldest first, so asking from zero there
// would trade the forty newest saves the projection held for the hundred
// oldest.
func TestAClippedChangelogIsToppedUpAndSaysSoWhenItCannotBe(t *testing.T) {
	for _, tc := range []struct {
		name string
		// held is the changelog the issue has; sent is how many of its newest
		// saves the search projection carries.
		held, sent int
		asks       []string
		complete   bool
		rows       int
		// newest and oldest are the first and last `to` values the feed
		// writes, oldest first.
		oldest, newest string
	}{
		{
			name: "under the bound costs nothing",
			held: 3, sent: 3,
			asks: nil, complete: true,
			rows: 3, oldest: "0", newest: "2",
		},
		{
			name: "clipped is read whole in one request",
			held: 58, sent: 40,
			asks: []string{"ENG-1 startAt=0"}, complete: true,
			rows: 58, oldest: "0", newest: "57",
		},
		{
			name: "past one page reads the newest page and stays short",
			held: 150, sent: 40,
			asks: []string{"ENG-1 startAt=50"}, complete: false,
			rows: 100, oldest: "50", newest: "149",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := historySaves(tc.held)
			projection := slices.Clone(history[tc.held-tc.sent:])
			slices.Reverse(projection) // Cloud's projection is newest first.
			server := &feedServer{
				issues:  []string{feedIssue("ENG-1", tc.held, projection...)},
				history: map[string][]string{"ENG-1": history},
			}

			doc, result := runFeed(t, server, render.TSV, everyRow)

			if !slices.Equal(server.historyAsks, tc.asks) {
				t.Errorf("changelog requests = %q, want %q", server.historyAsks, tc.asks)
			}
			if result.Complete != tc.complete {
				t.Errorf("complete = %t, want %t", result.Complete, tc.complete)
			}
			got := toValues(doc)
			if len(got) != tc.rows {
				t.Fatalf("%d rows, want %d:\n%s", len(got), tc.rows, doc)
			}
			if got[0] != tc.oldest || got[len(got)-1] != tc.newest {
				t.Errorf("rows run from save %s to save %s, want %s to %s",
					got[0], got[len(got)-1], tc.oldest, tc.newest)
			}
		})
	}
}
