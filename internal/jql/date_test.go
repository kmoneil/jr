package jql_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/jql"
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

// TestClassifyDateNamesEveryFormParseDateAccepts is the enumeration the rest of
// the tool branches on.
//
// It exists because a second, narrower list of date forms lived in
// `internal/resource/issue` and the four forms in the gap were accepted by the
// query builder and applied by no filter. One enumeration, read by both.
func TestClassifyDateNamesEveryFormParseDateAccepts(t *testing.T) {
	for input, want := range map[string]jql.DateKind{
		"-7d":              jql.DateRelative,
		"+30m":             jql.DateRelative,
		"2w":               jql.DateRelative,
		"-1M":              jql.DateRelative,
		"2026-08-10":       jql.DateAbsolute,
		"2026/08/10":       jql.DateAbsolute,
		"2026-08-10 13:45": jql.DateAbsolute,
		"2026/08/10 13:45": jql.DateAbsolute,
		"  2026-08-10  ":   jql.DateAbsolute,
		"startOfWeek()":    jql.DateFunction,
		"endOfDay(-1)":     jql.DateFunction,
		// Shape, not validity: a bad function is still classified as one, so
		// that ParseDate can refuse it as a bad function rather than as a word.
		"nonsense()": jql.DateFunction,
		"":           jql.DateInvalid,
		"whenever":   jql.DateInvalid,
		"2020-13-45": jql.DateInvalid,
	} {
		got := jql.ClassifyDate(input)
		if got != want {
			t.Errorf("ClassifyDate(%q) = %v, want %v", input, got, want)
		}
		// Nothing ParseDate accepts may classify as invalid. A form one of them
		// knows and the other does not is the whole defect, one layer down.
		//
		// The implication is one-way on purpose: a function is classified by
		// its shape and validated by name, so `nonsense()` is a DateFunction
		// that ParseDate refuses. That is what lets it be refused as a bad
		// function rather than as a word.
		if _, err := jql.ParseDate(input); err == nil && got == jql.DateInvalid {
			t.Errorf("ParseDate accepts %q and ClassifyDate calls it invalid", input)
		}
	}
}

// TestResolveDateReadsALiteralInTheZoneItIsGiven pins the half of a date that
// only matters when a value is compared here rather than sent.
//
// A relative offset names an instant and is the same everywhere. A literal is a
// wall clock and means nothing without a zone — Jira reads it in the account's,
// so resolving it in UTC is wrong by the offset, in the direction that drops
// events for anybody east of it.
func TestResolveDateReadsALiteralInTheZoneItIsGiven(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("this build cannot read the zone database: %v", err)
	}

	for input, want := range map[string]string{
		"-7d":  "2026-08-05T12:00:00Z",
		"+30m": "2026-08-12T12:30:00Z",
		"-2h":  "2026-08-12T10:00:00Z",
		"2w":   "2026-08-26T12:00:00Z",
		"-1M":  "2026-07-13T12:00:00Z",
		// UTC+9, so midnight in Tokyo is 15:00Z the day before. A resolver
		// reading this in UTC would bound nine hours late and drop events the
		// caller asked for.
		"2026-08-10":       "2026-08-09T15:00:00Z",
		"2026/08/10":       "2026-08-09T15:00:00Z",
		"2026-08-10 13:45": "2026-08-10T04:45:00Z",
		"2026/08/10 13:45": "2026-08-10T04:45:00Z",
	} {
		got, ok := jql.ResolveDate(input, tokyo, now)
		if !ok {
			t.Errorf("ResolveDate(%q) resolved nothing", input)
			continue
		}
		if at := got.UTC().Format(time.RFC3339); at != want {
			t.Errorf("ResolveDate(%q) = %s, want %s", input, at, want)
		}
	}
}

// TestResolveDateReportsWhatItCannotName keeps the false honest.
//
// Every one of these used to resolve to the zero value in a caller that read it
// as "no bound", which is how a feed reported itself complete while filtering
// nothing. There is no input for which a false may be read as "everything".
func TestResolveDateReportsWhatItCannotName(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		input string
		loc   *time.Location
		why   string
	}{
		{"startOfWeek()", time.UTC, "a function is the server's to evaluate"},
		{"now()", time.UTC, "so is this one"},
		{"whenever", time.UTC, "not a date at all"},
		{"", time.UTC, "empty"},
		{"2026-08-10", nil, "a wall clock with no zone to read it in"},
		{"99999999999999999999d", time.UTC, "an offset too large to hold"},
	} {
		if _, ok := jql.ResolveDate(tc.input, tc.loc, now); ok {
			t.Errorf("ResolveDate(%q) claimed an instant: %s", tc.input, tc.why)
		}
	}
}

// TestEveryRelativeUnitResolves holds the unit switch to the pattern above it.
//
// The switch ends in a default arm that means months, which is correct only
// while the pattern's class is exactly these five. A unit added to one and not
// the other would silently become thirty days.
func TestEveryRelativeUnitResolves(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for unit, want := range map[string]time.Duration{
		"m": time.Minute,
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
		"M": 30 * 24 * time.Hour,
	} {
		got, ok := jql.ResolveDate("1"+unit, time.UTC, now)
		if !ok {
			t.Errorf("the unit %q does not resolve", unit)
			continue
		}
		if d := got.Sub(now); d != want {
			t.Errorf("1%s = %v, want %v", unit, d, want)
		}
	}
}
