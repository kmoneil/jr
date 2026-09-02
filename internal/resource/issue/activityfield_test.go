package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
)

// activityRows splits a feed into rows, dropping the header.
func activityRows(t *testing.T, out string) [][]string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("the feed has no rows at all:\n%s", out)
	}
	rows := make([][]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

// The column positions this file reads, from ActivityColumns.
const (
	colKind  = 2
	colField = 4
)

// TestTheFieldFilterCutsEventsAndNotKinds is issue 119.
//
// One day of one project came to 110 events and roughly 15 carried signal; the
// rest was Rank grooming, which --kind cannot cut because Rank, Sprint and
// assignee are all kind=field.
//
// The recorded conversation makes the distinction sharper than a hand-written
// fixture would. Its changelog carries a field literally named `Comment`, and
// the same issue holds twenty real comment events. A filter that confused the
// changelog's field name with the event's kind would pass a test built on
// tidier data and fail here.
func TestTheFieldFilterCutsEventsAndNotKinds(t *testing.T) {
	t.Run("--not-changed-field drops that field and nothing else", func(t *testing.T) {
		before, _, _ := runActivity(t, nil)
		after, _, _ := runActivity(t, func(f registry.Flags) {
			f.SetString("not-changed-field", "Comment")
		})

		was, now := activityRows(t, before), activityRows(t, after)
		if len(now) >= len(was) {
			t.Fatalf("the filter removed nothing: %d rows before, %d after",
				len(was), len(now))
		}

		for _, row := range now {
			if strings.EqualFold(row[colField], "Comment") {
				t.Errorf("a Comment field change survived --not-changed-field: %v", row)
			}
		}

		// The whole point of the negative twin. An event that moves no field
		// is not the field you excluded, so the conversation stays.
		var comments int
		for _, row := range now {
			if row[colKind] == issue.EventComment {
				comments++
			}
		}
		if comments == 0 {
			t.Error("--not-changed-field Comment dropped the comment events too, " +
				"which confuses a changelog field name with an event kind")
		}
	})

	t.Run("--changed-field keeps only events about that field", func(t *testing.T) {
		out, _, _ := runActivity(t, func(f registry.Flags) {
			f.SetString("changed-field", "timespent")
		})

		rows := activityRows(t, out)
		for _, row := range rows {
			if !strings.EqualFold(row[colField], "timespent") {
				t.Errorf("a row survived that is not about timespent: %v", row)
			}
		}
		// A comment and a worklog move no field, so a positive filter cannot
		// match them. That is the same rule as above read the other way, and
		// it is why --kind is the flag for a question about kinds.
		for _, row := range rows {
			if row[colKind] == issue.EventComment || row[colKind] == issue.EventWorklog {
				t.Errorf("a fieldless event matched a positive field filter: %v", row)
			}
		}
	})

	t.Run("the negative wins where both name one field", func(t *testing.T) {
		out, _, _ := runActivity(t, func(f registry.Flags) {
			f.SetString("changed-field", "timespent")
			f.SetString("not-changed-field", "timespent")
		})
		if rows := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(rows) > 1 {
			t.Errorf("a field named in both flags survived:\n%s", out)
		}
	})

	t.Run("neither flag leaves the feed alone", func(t *testing.T) {
		before, _, _ := runActivity(t, nil)
		after, _, _ := runActivity(t, func(f registry.Flags) {
			f.SetString("changed-field", "")
		})
		if before != after {
			t.Error("an empty --changed-field changed the feed; the zero value " +
				"of this filter has to keep everything")
		}
	})
}

// TestAFieldlessEventSurvivesBecauseTheFilterCannotHoldAnEmptyName pins the
// invariant that the filter's comparisons rest on.
//
// A comment and a worklog move no field, so they reach keepField with an empty
// name. They survive a --not-changed-field because the filter's list cannot
// contain the empty string, not because anything compares against it. That is a
// deliberate choice: the guard this replaced, `name != "" && ...`, read as the
// thing keeping the conversation in the feed and could never fire, because
// lowerFields had already dropped the only value that would have reached it.
//
// Staging a red is what found it. Removing that guard changed no behaviour and
// every assertion stayed green, which is the signature of a check that cannot
// fail. So the property now hangs on lowerFields, and this is the test that
// notices if lowerFields stops holding it up.
func TestAFieldlessEventSurvivesBecauseTheFilterCannotHoldAnEmptyName(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		out, _, _ := runActivity(t, func(f registry.Flags) {
			f.SetString("not-changed-field", blank)
		})

		rows := activityRows(t, out)
		var comments int
		for _, row := range rows {
			if row[colKind] == issue.EventComment {
				comments++
			}
		}
		if comments == 0 {
			t.Errorf("--not-changed-field %q emptied the conversation out of "+
				"the feed; a blank value has to filter nothing at all",
				blank)
		}
	}
}
