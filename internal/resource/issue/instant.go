package issue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
)

// DateRoundedCode is the warning a query carries when an instant on a date flag
// could not be sent exactly, and names the bound that was sent instead.
const DateRoundedCode = "DATE_ROUNDED"

// dateBound is a date flag whose value reaches a query, and the way an instant
// on it moves to reach a minute JQL can carry: down for the start of a window,
// up for the end, so the window sent always holds the one asked for.
type dateBound struct {
	flag string
	dir  jql.Rounding
}

// listDateBounds are the date flags on issue list. Each one is a clause the
// server evaluates, so an instant on any of them is converted before sending.
var listDateBounds = []dateBound{
	{"created-after", jql.RoundDown},
	{"created-before", jql.RoundUp},
	{"updated-after", jql.RoundDown},
	{"updated-before", jql.RoundUp},
	{"changed-after", jql.RoundDown},
	{"changed-before", jql.RoundUp},
	{"worklog-after", jql.RoundDown},
	{"worklog-before", jql.RoundUp},
}

// activityDateBounds is the end of an activity window its query carries.
// --until is not in the query: it bounds only the events, which are compared in
// this process to the instant itself.
var activityDateBounds = []dateBound{{sinceFlag, jql.RoundDown}}

// resolvedDatesKey is where Validate leaves the minute each instant became.
const resolvedDatesKey = "issue.dates"

// movedBound is an instant the query carries as a different one, because JQL
// could not carry it exactly.
type movedBound struct {
	flag, typed, literal string
	zone                 *time.Location
	from, to             time.Time
}

// zoneOnce reads the account's zone the first time a date needs it, and hands
// every later date the same answer: they are all read in the one account's
// zone, so a second request would be paying twice for one fact.
func zoneOnce(
	ctx context.Context, inv *registry.Invocation,
) func(flag string) (*time.Location, error) {
	var loc *time.Location
	return func(flag string) (*time.Location, error) {
		if loc != nil {
			return loc, nil
		}
		resolved, err := accountLocation(ctx, inv, flag)
		if err != nil {
			return nil, err
		}
		loc = resolved
		return loc, nil
	}
}

// resolveInstants turns each RFC 3339 instant among these flags into the minute
// of the account's clock that bounds it, and leaves the literal for the query.
// It reports the bounds that moved, for a caller whose answer they widen.
//
// JQL refuses an instant on both deployments and reads a literal in the Jira
// account's timezone, so an instant costs the one request that learns the zone,
// and nothing is spent when no flag carries one. It happens in Validate for the
// reason everything on a streaming command does: a refusal from the body would
// arrive after the header.
func resolveInstants(
	inv *registry.Invocation, bounds []dateBound,
	zone func(flag string) (*time.Location, error),
) ([]movedBound, error) {
	resolved := map[string]string{}
	var moved []movedBound
	for _, b := range bounds {
		typed := strings.TrimSpace(inv.Flags.String(b.flag))
		if jql.ClassifyDate(typed) != jql.DateInstant {
			continue
		}
		loc, err := zone(b.flag)
		if err != nil {
			return nil, err
		}
		from, _ := jql.ResolveDate(typed, nil, time.Time{})
		literal, to, ok := jql.MinuteBound(from, loc, b.dir)
		if !ok {
			return nil, errs.Usage("UNBOUNDABLE_DATE",
				"--%s cannot be sent as a minute of the account's clock", b.flag).
				WithDetail("input: %s, read in %s", typed, loc).
				WithRemedy("use an absolute date like 2026-08-10, or a relative " +
					"offset like -7d")
		}
		resolved[b.flag] = literal
		if !to.Equal(from) {
			moved = append(moved, movedBound{
				flag: b.flag, typed: typed, literal: literal,
				zone: loc, from: from, to: to,
			})
		}
	}
	inv.SetValue(resolvedDatesKey, resolved)
	return moved, nil
}

// resolvedDate returns the literal Validate converted an instant to, or what
// the caller typed where there was nothing to convert.
func resolvedDate(inv *registry.Invocation, flag string) string {
	if literals, ok := inv.Value(resolvedDatesKey).(map[string]string); ok {
		if literal := literals[flag]; literal != "" {
			return literal
		}
	}
	return inv.Flags.String(flag)
}

// unresolvedInstants names the instants an explanation carries as typed. The
// minute each becomes depends on the account's zone, and an explanation makes
// no request to learn it.
func unresolvedInstants(inv *registry.Invocation, bounds []dateBound) []jql.UnresolvedFilter {
	var out []jql.UnresolvedFilter
	for _, b := range bounds {
		value := strings.TrimSpace(inv.Flags.String(b.flag))
		if jql.ClassifyDate(value) == jql.DateInstant {
			out = append(out, jql.UnresolvedFilter{Flag: b.flag, Value: value})
		}
	}
	return out
}

// warnMovedDates reports each bound the query carries in place of the instant
// typed, because the window sent is wider than the one asked for and nothing in
// the rows says by how much. Rounding inward instead would have kept the window
// and silently dropped part of it, and refusing would have refused most of the
// instants this tool prints, which carry seconds.
func warnMovedDates(inv *registry.Invocation, moved []movedBound) {
	for _, m := range moved {
		warn(inv, DateRoundedCode, m.message())
	}
}

// message names the instant typed, the literal sent and the zone it is read
// in, the instant that literal is, and how far and which way it moved.
func (m movedBound) message() string {
	way := "earlier"
	if m.to.After(m.from) {
		way = "later"
	}
	distance := m.to.Sub(m.from).Abs()
	why := "JQL bounds a date to a minute of the account's clock"
	if distance >= time.Minute {
		// Plain rounding moves an instant less than a minute. Anything further
		// is a minute the clocks repeated, stepped over.
		why = "the nearest minute happens twice in that zone, the night the " +
			"clocks go back, and JQL cannot say which"
	}
	return fmt.Sprintf("--%s %s was sent as %q in %s, which is %s, %s %s: %s",
		m.flag, m.typed, m.literal, m.zone, m.to.UTC().Format(time.RFC3339),
		distance, way, why)
}
