package jql_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/jql"
)

func TestParseDateAccepts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-08-04", `"2026-08-04"`},
		{"2026/08/04", `"2026/08/04"`},
		{"2026-08-04 11:32", `"2026-08-04 11:32"`},
		{"2024-02-29", `"2024-02-29"`}, // A real leap day.
		{"-7d", `"-7d"`},
		{"+2w", `"+2w"`},
		{"30m", `"30m"`},
		{"-1M", `"-1M"`},
		{"4h", `"4h"`},
		{"now()", `now()`},
		{"startOfDay()", `startOfDay()`},
		{"startOfWeek(-1)", `startOfWeek("-1")`},
		{"endOfMonth(-1d)", `endOfMonth("-1d")`},
		{" 2026-08-04 ", `"2026-08-04"`},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			v, err := jql.ParseDate(tc.in)
			if err != nil {
				t.Fatalf("ParseDate(%q): %v", tc.in, err)
			}
			got, err := jql.Render(&jql.Query{
				Where: &jql.Clause{Field: "created", Op: jql.OpGte, Value: v},
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			want := "created >= " + tc.want
			if got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
		})
	}
}

// TestParseDateRejects is the spec's first "nothing is guessed" row. The
// incumbent passes 2020-13-45 through as a literal and returns an empty result
// set, which reads exactly like "no matching issues".
func TestParseDateRejects(t *testing.T) {
	cases := []struct {
		in     string
		detail string
	}{
		{"2020-13-45", "month 13 is out of range"},
		{"2020-01-45", "day 45 is out of range"},
		{"2026-02-30", "that day does not exist in that month"},
		{"2025-02-29", "that day does not exist in that month"},
		{"", ""},
		{"yesterday", ""},
		{"7", ""},
		{"-7", ""},
		{"7days", ""},
		{"-7y", ""},
		{"04-08-2026", ""},
		{"2026-8", ""},
		{"tomorrow()", ""},
		{"startOfWeek(monday)", ""},
		{"startOfWeek(", ""},
		{"drop table()", ""},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			v, err := jql.ParseDate(tc.in)
			if err == nil {
				t.Fatalf("ParseDate(%q) was accepted as %v", tc.in, v)
			}
			assertUsageError(t, err, "INVALID_DATE")
			if tc.detail != "" && !strings.Contains(err.Error(), tc.detail) {
				t.Errorf("error %q does not explain the near miss (%q)", err.Error(), tc.detail)
			}
		})
	}
}

func TestSinceAndUntil(t *testing.T) {
	since, err := jql.Since("created", "-7d")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	until, err := jql.Until("created", "2026-08-04")
	if err != nil {
		t.Fatalf("Until: %v", err)
	}

	got, err := jql.New().Project("ENG").Where(since).Where(until).Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `project = "ENG" AND created >= "-7d" AND created <= "2026-08-04"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSinceRejectsBadDates(t *testing.T) {
	if _, err := jql.Since("created", "2020-13-45"); err == nil {
		t.Fatal("Since accepted an impossible date")
	}
	if _, err := jql.Until("created", "whenever"); err == nil {
		t.Fatal("Until accepted a word")
	}
}

// FuzzParseDateDoesNotPanic asserts that any input is either a valid date value
// or a structured usage error — never a panic, and never silently accepted.
func FuzzParseDateDoesNotPanic(f *testing.F) {
	for _, seed := range []string{
		"", "2026-08-04", "-7d", "now()", "2020-13-45", "(", ")", "()",
		"a()", "\x00", "0000-00-00", "9999-99-99",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		v, err := jql.ParseDate(in)
		if err != nil {
			assertUsageError(t, err, "INVALID_DATE")
			return
		}
		// Anything accepted must render without error.
		if _, err := jql.Render(&jql.Query{
			Where: &jql.Clause{Field: "created", Op: jql.OpGte, Value: v},
		}); err != nil {
			t.Fatalf("ParseDate(%q) accepted a value that cannot render: %v", in, err)
		}
	})
}
