package jql

import (
	"slices"
	"time"
)

// Rounding is the way MinuteBound moves an instant JQL cannot carry exactly.
type Rounding int

const (
	// RoundDown is for the start of a window: --updated-after, --since.
	RoundDown Rounding = iota
	// RoundUp is for the end of one: --updated-before.
	RoundUp
)

// MinuteBound is the JQL literal for an instant, read in the zone Jira will
// read it in, and the instant that literal names.
//
// JQL bounds a date to a minute of the account's clock, and almost every
// instant this tool prints carries seconds. So the instant moves outward, down
// for the start of a window and up for the end, and the window sent holds
// every instant the one asked for does, plus less than a minute either side.
// Moving it inward would drop part of the minute the caller asked for, with
// nothing in the answer to say so. A caller compares the returned instant with
// its own to say how far it moved.
//
// **A minute can occur twice.** When the clocks go back, every wall clock in
// the repeated hour names two instants, and a JQL literal has no way to say
// which one it means. So a minute in that hour is never sent: the bound keeps
// moving outward to the nearest minute that names one instant. On the one
// night a year it applies, that costs up to two hours more, when a bound just
// short of the repeated hour has to cross both copies of it, and the window is
// still a superset. Sending the ambiguous minute would leave the choice to the
// server, and the wrong choice on an upper bound is an hour of rows dropped.
// A minute the clocks skip when they go forward names no instant and is
// stepped over the same way.
//
// A false means no such minute exists within two days of the instant, or the
// one found is outside the years a literal can spell. Neither is a real zone on
// a real date, and neither is a reason to send something approximate.
func MinuteBound(t time.Time, loc *time.Location, dir Rounding) (string, time.Time, bool) {
	local := t.In(loc)
	wall := clockOf(local).Truncate(time.Minute)
	step := -time.Minute
	if dir == RoundUp {
		step = time.Minute
		if !clockOf(local).Equal(wall) {
			wall = wall.Add(time.Minute)
		}
	}
	for range maxBoundSteps {
		at, ok := onlyInstantAt(wall, loc)
		if ok && (dir == RoundDown && !at.After(t) || dir == RoundUp && !at.Before(t)) {
			if wall.Year() < 1 || wall.Year() > 9999 {
				return "", time.Time{}, false
			}
			return wall.Format(minuteLayout), at, true
		}
		wall = wall.Add(step)
	}
	return "", time.Time{}, false
}

// maxBoundSteps is how many minutes MinuteBound walks before giving up: two
// days, which is longer than any hour a zone has repeated or skipped. Samoa
// skipped a whole day in 2011, and that is the longest on record.
const maxBoundSteps = 2 * 24 * 60

// clockOf is the wall clock t shows, carried in UTC's fields so that it can be
// compared and stepped without a zone moving it. It is a reading, not an
// instant.
func clockOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// onlyInstantAt is the one instant a wall clock names in loc, and false when it
// names two, in the hour the clocks go back, or none, in the hour they go
// forward.
//
// An instant reads as the wall clock exactly when it is the wall clock less the
// offset in force at that instant. So each offset loc uses near the wall clock
// is tried, sampled every three hours for two days either side, which no pair
// of real transitions has fallen inside.
func onlyInstantAt(wall time.Time, loc *time.Location) (time.Time, bool) {
	var found []time.Time
	for near := -48 * time.Hour; near <= 48*time.Hour; near += 3 * time.Hour {
		_, offset := wall.Add(near).In(loc).Zone()
		u := wall.Add(-time.Duration(offset) * time.Second)
		if clockOf(u.In(loc)).Equal(wall) && !slices.ContainsFunc(found, u.Equal) {
			found = append(found, u)
		}
	}
	if len(found) != 1 {
		return time.Time{}, false
	}
	return found[0], true
}
