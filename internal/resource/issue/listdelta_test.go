package issue_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// Issue 120: a labels or Sprint change used to be two whole lists, the one
// before and the one after, so a row grew with the issue's age and said what
// moved only to a reader who diffed them. These are recorded from the sandbox
// on 2026-09-30, where AGL-6 took its components, fix versions and labels
// through add one, add a second, remove the first, and AGL-2 (already in closed
// sprint 1) was added to sprint 2 and then moved to sprint 3.

// recordedChanges reads one recorded Cloud changelog page.
func recordedChanges(t *testing.T, fixture, key string) []issue.Change {
	t.Helper()
	return recordedChangesOn(t, site.Cloud, fixture, key)
}

func recordedChangesOn(t *testing.T, kind site.Kind, fixture, key string) []issue.Change {
	t.Helper()
	conn, replayer := replayConn(t, fixture)
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}
	page, err := client.ListHistory(t.Context(), key, 0, 100)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Fatalf("recorded exchanges were never asked for: %v", unplayed)
	}
	return page.Changes
}

// sides renders one change as the pair a reader sees, with "-" for a side the
// server did not send, so an absent side and an empty one cannot be confused.
func sides(c issue.Change) [2]string {
	out := [2]string{"-", "-"}
	if c.HasFrom {
		out[0] = c.From + "#" + c.FromID
	}
	if c.HasTo {
		out[1] = c.To + "#" + c.ToID
	}
	return out
}

func changesOf(changes []issue.Change, field string) [][2]string {
	var out [][2]string
	for _, c := range changes {
		if c.Field == field {
			out = append(out, sides(c))
		}
	}
	return out
}

func sameSides(t *testing.T, what string, got, want [][2]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d rows %v, want %d %v", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s row %d = %v, want %v", what, i, got[i], want[i])
		}
	}
}

// TestALabelsChangeIsTheLabelsThatMoved is the field the issue is right about.
// Jira sends labels as both whole lists joined by a space, with no ids, and a
// label cannot hold a space, so the split is exact.
func TestALabelsChangeIsTheLabelsThatMoved(t *testing.T) {
	changes := recordedChanges(t, "history-lists-recorded.cloud.json", "AGL-6")

	sameSides(t, "labels", changesOf(changes, "labels"), [][2]string{
		{"-", "probe-a#"}, // "" to "probe-a"
		{"-", "probe-b#"}, // "probe-a" to "probe-a probe-b"
		{"probe-a#", "-"}, // "probe-a probe-b" to "probe-b"
	})
}

// TestASprintChangeIsTheSprintsThatMoved is the other one. Sprint sends ids
// joined by ", " beside names joined the same way; the delta is taken on the
// ids, and the sprint the issue stayed in, closed sprint 1, is on no row.
func TestASprintChangeIsTheSprintsThatMoved(t *testing.T) {
	changes := recordedChanges(t, "history-sprints-recorded.cloud.json", "AGL-2")

	sameSides(t, "Sprint", changesOf(changes, "Sprint"), [][2]string{
		{"-", "AGL Sprint 1#1"},
		{"-", "AGL Sprint 2#2"}, // "1" to "1, 2"
		{"AGL Sprint 2#2", "-"}, // "1, 2" to "1, 3": the removal first,
		{"-", "AGL Sprint 3#3"}, // then the addition, from one save
	})
}

// TestAComponentWasAlreadyTheDelta pins the shape the two fields above were
// made to match, and that nothing here splits it. Jira records a component one
// element per item with its own id, so a comma in its name is part of the name.
func TestAComponentWasAlreadyTheDelta(t *testing.T) {
	changes := recordedChanges(t, "history-lists-recorded.cloud.json", "AGL-6")

	sameSides(t, "Component", changesOf(changes, "Component"), [][2]string{
		{"-", "probe, comma#10000"},
		{"-", "probe plain#10001"},
		{"probe, comma#10000", "-"},
	})
	sameSides(t, "Fix Version", changesOf(changes, "Fix Version"), [][2]string{
		{"-", "probe 1.0, beta#10000"},
		{"-", "probe 2.0#10001"},
		{"probe 1.0, beta#10000", "-"},
	})
}

// TestADataCenterLabelsChangeSplitsTheSameWay is the Data Center recording from
// the rig, whose save 10123 moved a priority and labels together, "pagination"
// to "keyset pagination". Measured on 10.4.0 on 2026-09-30 to join and null the
// same way Cloud does.
func TestADataCenterLabelsChangeSplitsTheSameWay(t *testing.T) {
	changes := recordedChangesOn(t, site.DataCenter,
		"history-recorded.datacenter.json", "ENG-2")

	sameSides(t, "labels", changesOf(changes, "labels"), [][2]string{
		{"-", "keyset#"},
	})
}
