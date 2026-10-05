package jql_test

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/jql"
)

// minute is the literal MinuteBound renders, spelled here so the test reads
// the literal back without asking the code under test how.
const minute = "2006-01-02 15:04"

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("this build cannot read zone %s: %v", name, err)
	}
	return loc
}

func instant(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("bad instant in the table: %v", err)
	}
	return at
}

// TestMinuteBoundMovesAnInstantOutwardToTheMinute is issue 213's rule. JQL
// bounds a date to a minute of the account's clock, and the instants this tool
// prints carry seconds, so the start of a window moves down and the end moves
// up: the window sent holds the one asked for, and less than a minute more on
// each side. An instant already on the minute does not move at all.
func TestMinuteBoundMovesAnInstantOutwardToTheMinute(t *testing.T) {
	berlin := mustZone(t, "Europe/Berlin")
	kolkata := mustZone(t, "Asia/Kolkata")
	chicago := mustZone(t, "America/Chicago")

	for _, tc := range []struct {
		name    string
		in      string
		loc     *time.Location
		dir     jql.Rounding
		literal string
		at      string
	}{
		{"seconds, down", "2026-05-12T09:00:30Z", berlin, jql.RoundDown, "2026-05-12 11:00", "2026-05-12T09:00:00Z"},
		{"seconds, up", "2026-05-12T09:00:30Z", berlin, jql.RoundUp, "2026-05-12 11:01", "2026-05-12T09:01:00Z"},
		{"on the minute, down", "2026-05-12T09:00:00Z", berlin, jql.RoundDown, "2026-05-12 11:00", "2026-05-12T09:00:00Z"},
		{"on the minute, up", "2026-05-12T09:00:00Z", berlin, jql.RoundUp, "2026-05-12 11:00", "2026-05-12T09:00:00Z"},
		{"a fraction, down", "2026-05-12T09:00:00.5Z", berlin, jql.RoundDown, "2026-05-12 11:00", "2026-05-12T09:00:00Z"},
		{"a fraction, up", "2026-05-12T09:00:00.5Z", berlin, jql.RoundUp, "2026-05-12 11:01", "2026-05-12T09:01:00Z"},
		// Winter in Berlin is UTC+1, so the offset comes from the date and
		// not from a constant.
		{"winter", "2026-01-12T09:00:30Z", berlin, jql.RoundDown, "2026-01-12 10:00", "2026-01-12T09:00:00Z"},
		// A half-hour zone.
		{"half hour", "2026-05-12T09:00:30Z", kolkata, jql.RoundDown, "2026-05-12 14:30", "2026-05-12T09:00:00Z"},
		// The minute up is the next day in the account's zone.
		{"across midnight", "2026-05-12T04:59:59Z", chicago, jql.RoundUp, "2026-05-12 00:00", "2026-05-12T05:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			literal, at, ok := jql.MinuteBound(instant(t, tc.in), tc.loc, tc.dir)
			if !ok {
				t.Fatal("no bound")
			}
			if literal != tc.literal || !at.Equal(instant(t, tc.at)) {
				t.Errorf("got %q (%s), want %q (%s)", literal,
					at.UTC().Format(time.RFC3339), tc.literal, tc.at)
			}
		})
	}
}

// TestMinuteBoundNeverSendsAMinuteThatOccursTwice is the night the clocks go
// back. In New York on 2026-11-01 every minute from 01:00 to 01:59 happens
// twice, an hour apart, and a JQL literal cannot say which. Truncating to the
// minute and sending it leaves the choice to the server, and the wrong choice
// on an upper bound is an hour of rows dropped. So the bound moves on, outward,
// to a minute that names one instant.
func TestMinuteBoundNeverSendsAMinuteThatOccursTwice(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	for _, tc := range []struct {
		name    string
		in      string
		dir     jql.Rounding
		literal string
		at      string
	}{
		// 01:30 EDT, the first time round.
		{"first, down", "2026-11-01T05:30:00Z", jql.RoundDown, "2026-11-01 00:59", "2026-11-01T04:59:00Z"},
		{"first, up", "2026-11-01T05:30:00Z", jql.RoundUp, "2026-11-01 02:00", "2026-11-01T07:00:00Z"},
		// 01:30 EST, the second.
		{"second, down", "2026-11-01T06:30:00Z", jql.RoundDown, "2026-11-01 00:59", "2026-11-01T04:59:00Z"},
		{"second, up", "2026-11-01T06:30:00Z", jql.RoundUp, "2026-11-01 02:00", "2026-11-01T07:00:00Z"},
		// 00:59:30 EDT, just short of the hour. Up is 01:00, which happens
		// twice, so it crosses both copies of the hour: the widest this gets.
		{"just short, up", "2026-11-01T04:59:30Z", jql.RoundUp, "2026-11-01 02:00", "2026-11-01T07:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			literal, at, ok := jql.MinuteBound(instant(t, tc.in), ny, tc.dir)
			if !ok {
				t.Fatal("no bound")
			}
			if literal != tc.literal || !at.Equal(instant(t, tc.at)) {
				t.Errorf("got %q (%s), want %q (%s)", literal,
					at.UTC().Format(time.RFC3339), tc.literal, tc.at)
			}
		})
	}
}

// TestMinuteBoundStepsOverAMinuteThatDoesNotExist is the night the clocks go
// forward. In New York on 2026-03-08 the clock reads 01:59:59 and then 03:00,
// so the minute up from 01:59:30 is 02:00, which names no instant. The bound is
// 03:00, thirty seconds later.
func TestMinuteBoundStepsOverAMinuteThatDoesNotExist(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	literal, at, ok := jql.MinuteBound(instant(t, "2026-03-08T06:59:30Z"), ny, jql.RoundUp)
	if !ok {
		t.Fatal("no bound")
	}
	if want := "2026-03-08 03:00"; literal != want {
		t.Errorf("literal = %q, want %q", literal, want)
	}
	if want := instant(t, "2026-03-08T07:00:00Z"); !at.Equal(want) {
		t.Errorf("at = %s, want %s", at.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestEveryMinuteBoundHoldsTheInstantAndNamesOneReading sweeps a year of
// instants through zones with every shape of offset, densely around each
// transition, and checks the bound against a reading of the literal that does
// not use the code under test.
//
// Each bound has to: be a literal this package accepts; name the instant it
// reports; name no other instant in the zone, which is checked by stepping a
// quarter of an hour either way, since every offset in use differs from its
// neighbours by a multiple of that; hold the instant on the side it rounds
// from; and move it less than a minute, except within three hours of a
// transition, where it may cross a repeated hour.
func TestEveryMinuteBoundHoldsTheInstantAndNamesOneReading(t *testing.T) {
	zones := []string{
		"UTC", "America/New_York", "Europe/Berlin", "Asia/Kolkata",
		"America/St_Johns", "Australia/Lord_Howe", "Pacific/Chatham",
		"Asia/Kathmandu",
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	for _, name := range zones {
		loc := mustZone(t, name)
		var instants []time.Time
		for at := start; at.Before(end); at = at.Add(97*time.Minute + 13*time.Second) {
			instants = append(instants, at)
		}
		for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
			_, before := hour.In(loc).Zone()
			_, after := hour.Add(time.Hour).In(loc).Zone()
			if before == after {
				continue
			}
			for at := hour.Add(-3 * time.Hour); at.Before(hour.Add(4 * time.Hour)); at = at.Add(61 * time.Second) {
				instants = append(instants, at)
			}
		}

		for _, at := range instants {
			for _, dir := range []jql.Rounding{jql.RoundDown, jql.RoundUp} {
				checkMinuteBound(t, name, loc, at, dir)
			}
		}
	}
}

func checkMinuteBound(t *testing.T, name string, loc *time.Location, in time.Time, dir jql.Rounding) {
	t.Helper()
	literal, at, ok := jql.MinuteBound(in, loc, dir)
	if !ok {
		t.Fatalf("%s %s: no bound", name, in.Format(time.RFC3339))
	}
	if jql.ClassifyDate(literal) != jql.DateAbsolute {
		t.Fatalf("%s %s: %q is not a literal this package accepts", name, in.Format(time.RFC3339), literal)
	}
	if got := at.In(loc).Format(minute); got != literal || at.In(loc).Second() != 0 {
		t.Fatalf("%s %s: the bound %q reports %s, which reads %s", name,
			in.Format(time.RFC3339), literal, at.Format(time.RFC3339), got)
	}
	for k := -16; k <= 16; k++ {
		other := at.Add(time.Duration(k) * 15 * time.Minute)
		if k != 0 && other.In(loc).Format(minute) == literal {
			t.Fatalf("%s %s: %q names both %s and %s", name, in.Format(time.RFC3339),
				literal, at.UTC().Format(time.RFC3339), other.UTC().Format(time.RFC3339))
		}
	}
	if dir == jql.RoundDown && at.After(in) || dir == jql.RoundUp && at.Before(in) {
		t.Fatalf("%s %s: the bound %s is inside the window it bounds", name,
			in.Format(time.RFC3339), at.UTC().Format(time.RFC3339))
	}
	moved := at.Sub(in).Abs()
	_, early := in.Add(-3 * time.Hour).In(loc).Zone()
	_, late := in.Add(3 * time.Hour).In(loc).Zone()
	if early == late && moved >= time.Minute || moved > 2*time.Hour+time.Minute {
		t.Fatalf("%s %s: moved %s to %s", name, in.Format(time.RFC3339), moved,
			at.UTC().Format(time.RFC3339))
	}
}

// TestAnInstantNeedsNoZoneToResolve is the half of an instant that the event
// filters on issue activity and issue changes use. It names itself, so it
// costs no request for the account's zone to compare against.
func TestAnInstantNeedsNoZoneToResolve(t *testing.T) {
	got, ok := jql.ResolveDate("2026-05-12T11:00:30+02:00", nil, time.Time{})
	if !ok {
		t.Fatal("an instant did not resolve without a zone")
	}
	if want := instant(t, "2026-05-12T09:00:30Z"); !got.Equal(want) {
		t.Errorf("resolved to %s, want %s", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestMinuteBoundRefusesAMinuteNoLiteralCanSpell is the edge of the calendar.
// A literal spells its year in four digits, and the last minute of 9999 in UTC
// is already 10000 on Kiritimati, so there is no literal to send.
func TestMinuteBoundRefusesAMinuteNoLiteralCanSpell(t *testing.T) {
	kiritimati := mustZone(t, "Pacific/Kiritimati")
	newYork := mustZone(t, "America/New_York")
	for _, tc := range []struct {
		in  time.Time
		loc *time.Location
	}{
		{time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC), kiritimati},
		{time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), newYork},
	} {
		if literal, _, ok := jql.MinuteBound(tc.in, tc.loc, jql.RoundDown); ok {
			t.Errorf("%s in %s was sent as %q", tc.in.Format(time.RFC3339), tc.loc, literal)
		}
	}
}

// TestMinuteBoundSpellsTheFirstAndLastYearALiteralCan is the other side of
// that edge. Years 1 and 9999 are four digits, so their minutes are sent; a
// refusal one year inside the range is a bound the caller loses for nothing.
func TestMinuteBoundSpellsTheFirstAndLastYearALiteralCan(t *testing.T) {
	for _, tc := range []struct {
		in   time.Time
		dir  jql.Rounding
		want string
	}{
		{time.Date(1, 1, 1, 0, 0, 30, 0, time.UTC), jql.RoundDown, "0001-01-01 00:00"},
		{time.Date(9999, 12, 31, 23, 58, 30, 0, time.UTC), jql.RoundUp, "9999-12-31 23:59"},
	} {
		literal, _, ok := jql.MinuteBound(tc.in, time.UTC, tc.dir)
		if !ok || literal != tc.want {
			t.Errorf("%s gave %q, %v; want %q", tc.in.Format(time.RFC3339), literal, ok, tc.want)
		}
	}
}

// TestMinuteBoundGivesNoBoundRatherThanAGuess is a zone in which no minute
// names exactly one instant: its offset swings between UTC and UTC+3 every
// hour, so each even hour of its clock happens twice and each odd hour never.
// No real zone is like it. The point is what MinuteBound does when the walk
// finds nothing: it says so, rather than sending the minute it started from.
func TestMinuteBoundGivesNoBoundRatherThanAGuess(t *testing.T) {
	loc := swingingZone(t)
	in := time.Date(2026, 5, 13, 0, 0, 30, 0, time.UTC)
	for _, dir := range []jql.Rounding{jql.RoundDown, jql.RoundUp} {
		if literal, _, ok := jql.MinuteBound(in, loc, dir); ok {
			t.Errorf("a zone with no unambiguous minute gave %q", literal)
		}
	}
}

// swingingZone builds the zone TestMinuteBoundGivesNoBoundRatherThanAGuess
// needs, as the TZif bytes the zone database is made of: six days of hourly
// transitions from 2026-05-10, alternating between two offsets.
func swingingZone(t *testing.T) *time.Location {
	t.Helper()
	const (
		base        int32 = 1778371200 // 2026-05-10T00:00:00Z.
		transitions int32 = 6 * 24
	)
	var b bytes.Buffer
	b.WriteString("TZif")
	b.WriteByte(0)            // Version 1.
	b.Write(make([]byte, 15)) // Reserved.
	// The counts: UT/local indicators, standard/wall indicators, leap
	// seconds, transitions, local time types, abbreviation bytes.
	for _, count := range []int32{0, 0, 0, transitions, 2, 4} {
		_ = binary.Write(&b, binary.BigEndian, count)
	}
	for i := range transitions {
		_ = binary.Write(&b, binary.BigEndian, base+i*3600)
	}
	for i := range transitions {
		b.WriteByte(byte(i % 2))
	}
	for _, offset := range []int32{0, 3 * 3600} {
		_ = binary.Write(&b, binary.BigEndian, offset)
		b.WriteByte(0) // Not daylight saving.
		b.WriteByte(0) // The one abbreviation.
	}
	b.WriteString("SWG\x00")
	loc, err := time.LoadLocationFromTZData("Test/Swinging", b.Bytes())
	if err != nil {
		t.Fatalf("building the zone: %v", err)
	}
	return loc
}
