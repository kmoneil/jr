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
		// The two grammars a period has, and the boundaries between them.
		"-7D", "-4w 2d", "4w2d", "-4w -2d", "-1w  2d", "-1w\t2d",
		"endOfDay(-1y)", "endOfDay(-1D)", `endOfDay("-1w 2d")`,
		// Periods that match the pattern and cannot be held in a Duration.
		"1000000d", "100000d 100000d",
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
		"-7D":              jql.DateRelative,
		"-1W":              jql.DateRelative,
		"-4w 2d":           jql.DateRelative,
		"  -1w 7d  ":       jql.DateRelative,
		"4w2d":             jql.DateInvalid,
		"-1y":              jql.DateInvalid,
		"endOfDay(-1y)":    jql.DateFunction,
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
		// Minutes. Jira's units are case-insensitive, and this used to read
		// `M` as thirty days.
		"-1M":  "2026-08-12T11:59:00Z",
		"-60M": "2026-08-12T11:00:00Z",
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
		// A Duration is nanoseconds in an int64, so it runs out at 292 years
		// while Jira answers a query for 2739 of them. Both of these used to
		// wrap: the first to a negative offset naming an instant in the
		// future, which is a bound pointing the wrong way rather than a
		// missing one.
		{"1000000d", time.UTC, "a single component larger than a Duration"},
		{"100000d 100000d", time.UTC, "two components that fit and do not sum"},
	} {
		if _, ok := jql.ResolveDate(tc.input, tc.loc, now); ok {
			t.Errorf("ResolveDate(%q) claimed an instant: %s", tc.input, tc.why)
		}
	}
}

// TestEveryRelativeUnitResolves sweeps every letter rather than iterating a
// list, because a list is what it was and a list cannot see what was added.
//
// `M` is minutes. Jira's period units are case-insensitive, and this resolved
// `M` as thirty days, so `-1M` asked the server for one minute and told the
// local filter it meant one month. Measured 2026-08-14 against both
// deployments: `-60M`, `-60m` and `-1h` return the same rows, and `-43200M`
// returns the same rows as `-30d`.
//
// The old switch ended in a default arm meaning minutes, correct only while the
// pattern's class was exactly five characters, and the old test iterated those
// five. Adding `D` to the class would have made `-7D` seven minutes with both
// of them green. So the assertion runs the other way now: every letter the
// pattern accepts must appear here with a duration somebody measured, and every
// letter it does not accept must be refused. A unit cannot enter the pattern
// without entering this table, whatever it is added to.
func TestEveryRelativeUnitResolves(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	// Both cases of each unit, because Jira reads them case-insensitively:
	// `-7D` and `-1W` are accepted by both deployments and answer identically
	// to their lowercase spellings. `y` and `s` are absent because Jira refuses
	// them on a date field, which is what makes this a measurement rather than
	// a preference.
	want := map[string]time.Duration{
		"m": time.Minute, "M": time.Minute,
		"h": time.Hour, "H": time.Hour,
		"d": 24 * time.Hour, "D": 24 * time.Hour,
		"w": 7 * 24 * time.Hour, "W": 7 * 24 * time.Hour,
	}

	for _, unit := range letters() {
		input := "1" + unit
		expected, named := want[unit]

		if kind := jql.ClassifyDate(input); (kind == jql.DateRelative) != named {
			if named {
				t.Errorf("the unit %q is measured and the pattern refuses it", unit)
			} else {
				t.Errorf("the pattern accepts the unit %q and nothing here "+
					"says what it means", unit)
			}
			continue
		}
		if !named {
			continue
		}

		got, ok := jql.ResolveDate(input, time.UTC, now)
		if !ok {
			t.Errorf("the unit %q is accepted and does not resolve", unit)
			continue
		}
		if d := got.Sub(now); d != expected {
			t.Errorf("1%s = %v, want %v", unit, d, expected)
		}
	}
}

// letters is every ASCII letter, which is the space a unit can live in.
func letters() []string {
	all := make([]string, 0, 52)
	for c := byte('A'); c <= 'Z'; c++ {
		all = append(all, string(c), string(c+('a'-'A')))
	}
	return all
}

// TestACompoundPeriodSumsItsComponents pins the form the card that raised this
// thought had no local representation.
//
// It has one: the components sum and the sign is written once, on the front of
// the whole period. Measured on Cloud, counting rows, because the arithmetic is
// the part a server can disagree about: `-1w 7d` returned exactly what `-14d`
// returned, `-1w 1d` returned nine rows where `-7d` returned seven and `-14d`
// returned eighteen, and `-1d 1d` returned two days' worth.
//
// That is what makes a compound one duration like any other, and therefore
// something the query and the local event filter can both apply. Accepting it
// in the query and refusing to resolve it here would recreate the split that
// `--since` spent a day closing.
func TestACompoundPeriodSumsItsComponents(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for input, want := range map[string]time.Duration{
		"-1w 7d":    -14 * 24 * time.Hour,
		"-1w 1d":    -8 * 24 * time.Hour,
		"-1d 1d":    -2 * 24 * time.Hour,
		"1d 2h 3m":  26*time.Hour + 3*time.Minute,
		"+1d 2h":    26 * time.Hour,
		"-1d 2h 3m": -(26*time.Hour + 3*time.Minute),
		// Case is insensitive in a compound too, and two spaces are accepted:
		// `updated >= "-1w  2d"` validates.
		"-1D 2H":  -26 * time.Hour,
		"-1w  2d": -9 * 24 * time.Hour,
	} {
		got, ok := jql.ResolveDate(input, time.UTC, now)
		if !ok {
			t.Errorf("ResolveDate(%q) resolved nothing", input)
			continue
		}
		if d := got.Sub(now); d != want {
			t.Errorf("ResolveDate(%q) = %v from now, want %v", input, d, want)
		}
	}
}

// TestTheCompoundFormsJiraRefusesAreRefusedHere is the other half, and the
// reason it is a separate test is that a period this accepts and Jira does not
// is a round trip spent on a BAD_REQUEST.
//
// Every one of these was put to the server: it refuses a sign on any component
// but the first, refuses components run together with no space, and refuses a
// unit it does not have. A tab is not measured either way, so it is refused,
// which is the direction a caller can see and rewrite.
func TestTheCompoundFormsJiraRefusesAreRefusedHere(t *testing.T) {
	for _, input := range []string{
		"4w2d",    // no space: "Date value '4w2d' ... is invalid"
		"-4w -2d", // a sign on the second component
		"-1w 2y",  // `y` is not a unit on a date field
		"-1w\t2d", // a tab, which nothing has measured
		"-1w 2",   // a component with no unit
		"- 1w",    // a detached sign
	} {
		if kind := jql.ClassifyDate(input); kind == jql.DateRelative {
			t.Errorf("ClassifyDate(%q) = DateRelative, and Jira refuses it", input)
		}
		if _, err := jql.ParseDate(input); err == nil {
			t.Errorf("ParseDate accepted %q, which Jira refuses", input)
		}
	}
}

// TestADateFunctionArgumentIsItsOwnGrammar holds the second table to the
// server, and it exists because there was only one table.
//
// A duration argument to a date function is not spelled like a period on a date
// field. Measured 2026-08-14 against Cloud and Data Center 10.4, both
// identical: the units are `y M w d h m`, they are case-sensitive, there is no
// compound form, and **`M` is months here and minutes there**. On Data Center,
// `updated >= endOfDay(-1M)` returns the seeded issues and `endOfDay(-1m)`
// returns none, which is months against end-of-day-a-minute-ago.
//
// Sharing one pattern with the date-value grammar had `jr` refusing
// `endOfDay(-1y)` that Jira accepts, and would have made widening that pattern
// accept `endOfDay(-1D)` that Jira refuses, quoted or not.
func TestADateFunctionArgumentIsItsOwnGrammar(t *testing.T) {
	// Case-sensitive, so this is the exact spelling and not a fold.
	accepted := map[string]bool{
		"y": true, // Years, which a date value has no unit for.
		"M": true, // Months, where a date value reads the same letter as minutes.
		"w": true, "d": true, "m": true,
		"h": true, // Accepted, although Jira's own error text omits it.
	}

	for _, unit := range letters() {
		input := "endOfDay(-1" + unit + ")"
		_, err := jql.ParseDate(input)
		if accepted[unit] && err != nil {
			t.Errorf("ParseDate(%q) refused a unit Jira accepts: %v", input, err)
		}
		if !accepted[unit] && err == nil {
			t.Errorf("ParseDate(%q) accepted a unit Jira refuses", input)
		}
	}

	// The forms either side of the unit table.
	for input, valid := range map[string]bool{
		"endOfDay(-1)":       true,  // A bare number is days, and is accepted.
		"endOfDay(0)":        true,  //
		`endOfDay("-1w 2d")`: false, // No compound form here.
		"endOfDay(-1.5d)":    false, // No fractions either.
	} {
		if _, err := jql.ParseDate(input); (err == nil) != valid {
			t.Errorf("ParseDate(%q) valid = %v, want %v", input, err == nil, valid)
		}
	}
}

// TestDateHasTimeOfDay is what lets a caller apply a rule this package cannot
// know: which fields take a clock is a property of the field and the deployment
// together, and both live elsewhere.
func TestDateHasTimeOfDay(t *testing.T) {
	for input, want := range map[string]bool{
		"2026-08-10 13:45":     true,
		"2026/08/10 13:45":     true,
		"  2026-08-10 13:45  ": true,
		"2026-08-10":           false,
		"2026/08/10":           false,
		// An offset is arithmetic on the server's clock, and Data Center takes
		// `-5d` on the same field it refuses `2026-08-10 00:00` on.
		"-7d":  false,
		"+30m": false,
		// A function is the server's, and refusing it for carrying a clock
		// would be this package guessing at what it computes.
		"startOfDay()": false,
		"whenever":     false,
		"":             false,
	} {
		if got := jql.DateHasTimeOfDay(input); got != want {
			t.Errorf("DateHasTimeOfDay(%q) = %v, want %v", input, got, want)
		}
	}
}
