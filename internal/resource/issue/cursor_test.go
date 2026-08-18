package issue_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// wantCode fails unless err is this tool's structured error carrying code.
func wantCode(t *testing.T, err error, code string) *errs.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	structured, ok := errors.AsType[*errs.Error](err)
	if !ok {
		t.Fatalf("error is not structured: %v", err)
	}
	if structured.Code != code {
		t.Fatalf("code = %q, want %s (%v)", structured.Code, code, err)
	}
	return structured
}

func TestAChangeCursorSurvivesTheRoundTrip(t *testing.T) {
	through := time.Date(2026, 8, 17, 14, 30, 45, 0, time.UTC)
	encoded := issue.EncodeChangeCursor(issue.NewChangeCursor(site.Cloud, through))
	if encoded == "" {
		t.Fatal("a minted cursor encoded to nothing")
	}

	back, err := issue.DecodeChangeCursor(encoded, site.Cloud)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := back.Instant()
	if !ok {
		t.Fatalf("decoded cursor carries no instant: %+v", back)
	}
	if !got.Equal(through) {
		t.Errorf("instant = %s, want %s", got, through)
	}
}

// TestACursorIsTruncatedDownwardsToTheSecond holds the rounding direction, which
// decides whether a fraction of a second is claimed as reported or left for the
// next poll. Down is the safe direction: the changelog is published to the
// second, so rounding up would say a second had been reported when part of it
// had not been looked at.
func TestACursorIsTruncatedDownwardsToTheSecond(t *testing.T) {
	through := time.Date(2026, 8, 17, 14, 30, 45, 900_000_000, time.UTC)
	c := issue.NewChangeCursor(site.Cloud, through)

	got, ok := c.Instant()
	if !ok {
		t.Fatalf("cursor carries no instant: %+v", c)
	}
	want := time.Date(2026, 8, 17, 14, 30, 45, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("instant = %s, want %s", got, want)
	}
}

// TestAChangeCursorIsRefusedOnTheOtherDeployment is the same rule PageToken has,
// for a sharper reason: a cursor names a window of one site's changelog, and the
// other site's history in that window is somebody else's answer.
func TestAChangeCursorIsRefusedOnTheOtherDeployment(t *testing.T) {
	minted := issue.EncodeChangeCursor(
		issue.NewChangeCursor(site.Cloud, time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)))

	_, err := issue.DecodeChangeCursor(minted, site.DataCenter)
	e := wantCode(t, err, "INVALID_SINCE_TOKEN")
	if e.Remedy == "" {
		t.Error("refusal carries no remedy")
	}
}

func TestAGarbledSinceTokenIsAUsageError(t *testing.T) {
	cases := map[string]string{
		"not base64":         "not a token!!",
		"not JSON":           "bm90IEpTT04",                             // "not JSON".
		"names no site":      "eyJ0IjoiMjAyNi0wOC0xN1QxNDowMDowMFoifQ",  // {"t":"2026-08-17T14:00:00Z"}.
		"carries no time":    "eyJkIjoiY2xvdWQifQ",                      // {"d":"cloud"}.
		"time is not a time": "eyJkIjoiY2xvdWQiLCJ0IjoieWVzdGVyZGF5In0", // {"d":"cloud","t":"yesterday"}.
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := issue.DecodeChangeCursor(token, site.Cloud)
			e := wantCode(t, err, "INVALID_SINCE_TOKEN")
			if e.Exit == 0 {
				t.Error("refusal carries no exit code")
			}
		})
	}
}

// TestACursorIsToldApartFromADate keeps the two --since forms from being
// diagnosed as each other. A date that failed as a cursor would be reported as a
// garbled token, which sends somebody looking for the token they never had.
func TestACursorIsToldApartFromADate(t *testing.T) {
	cursor := issue.EncodeChangeCursor(
		issue.NewChangeCursor(site.DataCenter, time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)))
	if !issue.LooksLikeChangeCursor(cursor) {
		t.Errorf("a minted cursor %q does not look like one", cursor)
	}

	// Every date form the feed accepts, plus two that decode as base64 and are
	// still not cursors. "-7d" is not valid base64; "2026" is.
	for _, date := range []string{"-7d", "-1w 2d", "2026-08-10", "2026-08-10 14:30", "2026", "abcd"} {
		if issue.LooksLikeChangeCursor(date) {
			t.Errorf("%q was read as a cursor", date)
		}
	}
}

// TestTwoConsecutiveWindowsReportEveryChangeExactlyOnce is the property the
// whole design exists for, and it is asserted over the case a hand-written
// fixture would never contain: a bulk edit that stamps many changes with one
// timestamp, landing exactly on a window boundary.
//
// A pair cursor breaks here. It resumes from the last row it saw, so the rest of
// the tie is either re-reported (resume at the timestamp) or dropped (resume at
// the next one), and which of the two depends on the order the server returned
// them in. A window has no row in it and no tie to break.
func TestTwoConsecutiveWindowsReportEveryChangeExactlyOnce(t *testing.T) {
	at := func(s string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return parsed
	}

	// Four hundred changes on the boundary second, one before it, one after.
	var changes []time.Time
	changes = append(changes, at("2026-08-17T14:29:59Z"))
	for range 400 {
		changes = append(changes, at("2026-08-17T14:30:00Z"))
	}
	changes = append(changes, at("2026-08-17T14:30:01Z"))

	first, err := issue.NewChangeWindow(at("2026-08-17T14:00:00Z"), at("2026-08-17T14:30:00Z"))
	if err != nil {
		t.Fatalf("first window: %v", err)
	}
	// The second poll resumes from the first's cursor, the way a caller does.
	resumed, err := issue.DecodeChangeCursor(
		issue.EncodeChangeCursor(first.Cursor(site.Cloud)), site.Cloud)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	after, ok := resumed.Instant()
	if !ok {
		t.Fatal("resumed cursor carries no instant")
	}
	second, err := issue.NewChangeWindow(after, at("2026-08-17T15:00:00Z"))
	if err != nil {
		t.Fatalf("second window: %v", err)
	}

	for i, c := range changes {
		reported := 0
		for _, w := range []issue.ChangeWindow{first, second} {
			if w.Holds(c) {
				reported++
			}
		}
		if reported != 1 {
			t.Errorf("change %d at %s reported %d times, want exactly 1", i, c.Format(time.RFC3339), reported)
		}
	}

	// And the boundary is where it was claimed to be: the tie belongs to the
	// first window, not split between them.
	if !first.Holds(changes[1]) {
		t.Error("a change at the upper bound is not in the window that claimed it")
	}
	if second.Holds(changes[1]) {
		t.Error("a change at the previous bound is reported again by the next window")
	}
}

// TestAChangeOlderThanTheWindowIsNotReported covers the other half of the
// bound: the candidate query is widened to a whole minute, so the issues it
// returns carry changes from before the window that must not be emitted again.
func TestAChangeOlderThanTheWindowIsNotReported(t *testing.T) {
	after := time.Date(2026, 8, 17, 14, 30, 30, 0, time.UTC)
	through := time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC)
	w, err := issue.NewChangeWindow(after, through)
	if err != nil {
		t.Fatalf("window: %v", err)
	}

	// Inside the minute the floor widened to, and before the window opened.
	older := time.Date(2026, 8, 17, 14, 30, 5, 0, time.UTC)
	if w.Holds(older) {
		t.Errorf("a change at %s was reported by a window opening at %s", older, after)
	}
	// After the upper bound: it belongs to the next poll, not this one.
	newer := time.Date(2026, 8, 17, 15, 0, 1, 0, time.UTC)
	if w.Holds(newer) {
		t.Errorf("a change at %s was reported by a window closing at %s", newer, through)
	}
}

// TestAWindowFloorsItsQueryBoundToTheAccountsMinute holds the two things the
// bound has to get right at once, and both have already cost somebody a session
// in this repository: JQL cannot express a second, and it reads a literal in the
// account's timezone rather than in UTC.
func TestAWindowFloorsItsQueryBoundToTheAccountsMinute(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}

	w, err := issue.NewChangeWindow(
		time.Date(2026, 8, 17, 14, 30, 45, 0, time.UTC),
		time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("window: %v", err)
	}

	got, err := w.Floor(chicago)
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	// 14:30:45Z is 09:30:45 in Chicago, and the seconds are floored away rather
	// than rounded up: the window still needs the rest of that minute.
	if want := "2026-08-17 09:30"; got != want {
		t.Errorf("floor = %q, want %q", got, want)
	}

	utc, err := w.Floor(time.UTC)
	if err != nil {
		t.Fatalf("floor in UTC: %v", err)
	}
	if utc == got {
		t.Error("the bound did not move with the account's timezone, so it is not being read in one")
	}
}

func TestAWindowWithoutATimezoneIsRefused(t *testing.T) {
	w, err := issue.NewChangeWindow(
		time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	_, err = w.Floor(nil)
	_ = wantCode(t, err, "NO_TIMEZONE")
}

// TestAWindowThatRunsBackwardsIsRefused. A cursor ahead of the site's clock is
// not a rounding error to be clamped: it came from another site, or the clock
// moved, and either way this tool cannot say what happened in between.
func TestAWindowThatRunsBackwardsIsRefused(t *testing.T) {
	_, err := issue.NewChangeWindow(
		time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC))
	e := wantCode(t, err, "SINCE_AFTER_NOW")
	if e.Detail == "" {
		t.Error("refusal does not say which two times disagreed")
	}
}

// TestAnEmptyWindowIsAllowed. Two polls a second apart are ordinary, and a
// window whose ends are equal reports the changes stamped exactly there.
func TestAnEmptyWindowIsAllowed(t *testing.T) {
	at := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	w, err := issue.NewChangeWindow(at, at)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if w.Holds(at) {
		t.Error("a change at the previous bound was reported again")
	}
}
