package nearest_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/nearest"
)

// commands stands in for a closed candidate set the caller could have meant.
var commands = []string{
	"issue list", "issue get", "issue create", "issue comment list",
	"project list", "sprint list", "board list",
}

func TestStringsRanksTheClosestFirst(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  []string
	}{
		// `issue get` is two edits from `issue lst` and inside the same
		// threshold, so it is offered second rather than not at all. The
		// ordering is the answer: the closest candidate leads.
		{"one edit leads", "issue lst", []string{"issue list", "issue get"}},
		{"exact", "board list", []string{"board list"}},
		{"case is not a mistake", "Board List", []string{"board list"}},
		{"nothing close", "worklog", nil},
		{"empty input", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nearest.Strings(tc.input, commands, 5)
			if len(got) != len(tc.want) {
				t.Fatalf("Strings(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Strings(%q) = %v, want %v", tc.input, got, tc.want)
				}
			}
		})
	}
}

// TestStringsSaysNothingRatherThanGuessing is the half that matters more than
// the ranking.
//
// The rule this replaced matched on substring, so `list` offered every command
// with the word in it and `pro` offered `project list`. A caller who typed a
// word this tool does not have is better served by silence than by three
// candidates they have to rule out one at a time.
func TestStringsSaysNothingRatherThanGuessing(t *testing.T) {
	for _, input := range []string{"list", "pro", "xyzzy", "the quick brown fox"} {
		if got := nearest.Strings(input, commands, 5); len(got) > 0 {
			t.Errorf("Strings(%q) guessed %v", input, got)
		}
	}
}

// TestStringsIsStableAcrossRuns pins the tie-break. Two candidates at the same
// distance must not swap places between invocations, because a refusal that
// reads differently each time is one nobody can paste into a bug report.
func TestStringsIsStableAcrossRuns(t *testing.T) {
	candidates := []string{"reporter", "requester", "resolution"}
	first := strings.Join(nearest.Strings("reporte", candidates, 5), ",")
	for range 20 {
		if got := strings.Join(nearest.Strings("reporte", candidates, 5), ","); got != first {
			t.Fatalf("ranking moved between runs: %q then %q", first, got)
		}
	}
}

// TestStringsHonoursItsCap keeps a refusal readable. Forty candidates is a
// catalogue, not a suggestion.
func TestStringsHonoursItsCap(t *testing.T) {
	candidates := []string{"statas", "statbs", "statcs", "statds", "states", "statfs"}
	got := nearest.Strings("status", candidates, 3)
	if len(got) != 3 {
		t.Errorf("Strings returned %d candidates with a cap of 3: %v", len(got), got)
	}
	if len(nearest.Strings("status", candidates, 0)) != 0 {
		t.Error("a cap of zero returned candidates")
	}
}

// TestThresholdScalesWithTheInput pins the rule that makes a long word strict
// and a short one generous.
func TestThresholdScalesWithTheInput(t *testing.T) {
	for input, want := range map[string]int{
		"":                      2,
		"id":                    2,
		"status":                2,
		"resolutiondate":        3,
		"customfield_10042_xyz": 5,
	} {
		if got := nearest.Threshold(input); got != want {
			t.Errorf("Threshold(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestDistanceCountsEdits(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"status", "status", 0},
		{"status", "statuss", 1},
		{"summry", "summary", 1},
		{"kitten", "sitting", 3},
		// Runes, not bytes: one accented character is one edit, not two.
		{"café", "cafe", 1},
	} {
		if got := nearest.Distance(tc.a, tc.b); got != tc.want {
			t.Errorf("Distance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
