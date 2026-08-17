package issue_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// TestTheCloudChangelogProjectionSaysItIsClipped reads the bound off the
// recording rather than off anybody's belief about Cloud.
//
// `expand=changelog` on the Cloud search is a paged bean, and the recorded
// conversation in activity-recorded.cloud.json has carried
// `startAt=0 maxResults=40 total=58 histories=40` since it was made on
// 2026-08-12. Eighteen of that issue's saves are not in the response.
//
// Nothing read either number until 2026-08-17. `attachChanges` decoded the
// entries and dropped the count, `Issue.HasChanges` was assigned and never read,
// and the comment on `Issue.Changes` said the projection was whole on both
// deployments over a recording that already said otherwise. So `issue activity`
// omitted the oldest saves of any Cloud issue with more than forty and reported
// `complete="true"` at exit 0, which is the one answer this tool is not allowed
// to give.
//
// The clip lands on the oldest saves, because Cloud returns this projection
// newest-first, which is why nobody met it: a feed about the last week is
// usually inside the newest forty. Usually is not an answer.
func TestTheCloudChangelogProjectionSaysItIsClipped(t *testing.T) {
	conn, replayer := replayConn(t, "activity-recorded.cloud.json")
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	result, err := client.List(t.Context(), issue.ListOptions{
		// The same request the recording holds, which is what `issue activity`
		// sends: three projections on one search.
		Query: issue.QueryOptions{
			JQL: "key = AGL-3", UpdatedAfter: "-1d",
		},
		Limit:         registry.Limit{All: true},
		PageSize:      50,
		Fields:        issue.DefaultFields(),
		WithComments:  true,
		WithWorklogs:  true,
		WithChangelog: true,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := replayer.Unmatched(); len(got) != 0 {
		t.Fatalf("asked for something outside the recording: %v", got)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("got %d issues, want the one the recording holds", len(result.Issues))
	}

	i := result.Issues[0]
	if !i.HasChanges {
		t.Fatal("the changelog projection was not read at all")
	}
	if i.ChangeSaves != 40 {
		t.Errorf("ChangeSaves = %d, want the 40 the recording holds", i.ChangeSaves)
	}
	if i.ChangeTotal == nil {
		t.Fatal("the projection's own total was dropped, so nothing can tell it is clipped")
	}
	if *i.ChangeTotal != 58 {
		t.Errorf("ChangeTotal = %d, want the 58 the recording reports", *i.ChangeTotal)
	}
	if i.ChangesComplete() {
		t.Error("40 saves of 58 reported themselves complete")
	}
}

// TestChangesCompleteCountsSavesAndNotRows is the trap the total invites, and
// it is asserted on the type rather than on the recording because no recording
// can show it: the count Jira sends is of *saves*, and one save that moved three
// fields flattens into three rows, so an issue whose forty entries carried sixty
// field moves would compare sixty against fifty-eight and call a clipped
// changelog whole. The recorded issue has 42 rows over 40 saves against a total
// of 58, where both counts answer "clipped" and neither proves which was read.
//
// It is the same distinction HistoryPage.Total carries a comment about, and the
// same one that made `streamPagedHistory` advance by saves rather than by rows.
func TestChangesCompleteCountsSavesAndNotRows(t *testing.T) {
	total := func(n int) *int { return &n }

	// Forty saves that flattened into sixty rows, out of fifty-eight saves. Rows
	// against the total says complete; saves against it says clipped.
	clipped := issue.Issue{
		Changes:     make([]issue.Change, 60),
		HasChanges:  true,
		ChangeSaves: 40,
		ChangeTotal: total(58),
	}
	if clipped.ChangesComplete() {
		t.Error("40 saves of 58, flattened to 60 rows, reported itself complete: " +
			"the total is being compared against rows")
	}

	// The same shape, whole: every save arrived, and it still flattens to more
	// rows than there are saves.
	whole := issue.Issue{
		Changes:     make([]issue.Change, 60),
		HasChanges:  true,
		ChangeSaves: 40,
		ChangeTotal: total(40),
	}
	if !whole.ChangesComplete() {
		t.Error("40 saves of 40 reported itself clipped")
	}

	// A projection that carried no count has not said the changelog is whole,
	// on the same terms as WorkComplete and for the same reason: reading an
	// absent total as zero is what published clipped threads as complete.
	unknown := issue.Issue{
		Changes:     make([]issue.Change, 3),
		HasChanges:  true,
		ChangeSaves: 3,
	}
	if unknown.ChangesComplete() {
		t.Error("a changelog whose length the server never stated reported itself complete")
	}
}
