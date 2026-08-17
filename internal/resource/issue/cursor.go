package issue

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/site"
)

// A change feed resumes from a window, not from a row.
//
// The obvious cursor is the pair a keyset listing uses: the last row's
// timestamp and its key, resumed with "everything after that pair". It cannot
// be exact here, and the reason is measured rather than argued.
//
// JQL's finest bound is a minute, and neither comparison operator bisects one:
// with three issues updated at :19, :23 and :27, both `updated >= "…18:13"` and
// `updated > "…18:13"` return all three, and `>= "…18:14"` returns none. Both
// deployments, measured 2026-08-12, and written down beside dateLayouts in
// internal/jql. So a pair cursor cannot be sent to the server. It has to be
// applied here, by asking for the whole minute and dropping every row at or
// below the pair, and that drop compares timestamps as this tool publishes them
// (normalized to the second) against the order the server walked (whatever
// precision it stores). Two rows inside one second sort one way here and
// possibly the other way there, so the row on a page boundary is skipped or
// repeated. That is the same silent skip a feed exists to avoid, moved from the
// timestamp to the tie-break, and no client can measure which way a given server
// broke the tie.
//
// A window has no tie to break. One poll reports the changes created after the
// previous bound and at or before this poll's own start:
//
//	(previous bound, this walk's start]
//
// Both ends are instants this tool chose rather than rows it saw, the next poll
// compares against the same instant with `>` where this one used `<=`, and a
// bulk edit that stamps four hundred changes with one timestamp falls entirely
// inside one window or entirely inside the next. Never half of each.
//
// The walk inside a window is then ordinary key-ordered pagination, which this
// package already has: `ORDER BY issuekey DESC` with the window's lower bound as
// an `updated >=` filter. The key is immutable, so an issue edited underneath
// the walk cannot move between pages and cannot be skipped, which is a property
// ordering by `updated` does not have on either deployment. An issue edited
// during the walk writes a change created after the upper bound, so it belongs
// to the next window, and its `updated` only moves forward, so it is still in
// the next window's candidate set. Nothing is lost by not seeing it now.

// ChangeCursor is the opaque resume point of a change feed.
//
// It carries the deployment for the same reason PageToken does: the two are
// different servers with different clocks and different changelogs, and a token
// minted against one names nothing on the other. It is refused there rather than
// read as an instant that happens to parse.
type ChangeCursor struct {
	// Deployment is the kind of site this cursor was minted against.
	Deployment site.Kind `json:"d"`
	// Through is the upper bound already reported, RFC 3339 in UTC and truncated
	// to the second. Everything created at or before it has been emitted; the
	// next poll reports what was created after it.
	Through string `json:"t"`
}

// NewChangeCursor mints the cursor a poll ends at.
//
// through is truncated to the second, downwards, because the changelog
// timestamps it will be compared against are published to the second. Rounding
// the other way would claim a fraction of a second nothing has looked at yet.
func NewChangeCursor(kind site.Kind, through time.Time) ChangeCursor {
	return ChangeCursor{
		Deployment: kind,
		Through:    through.UTC().Truncate(time.Second).Format(time.RFC3339),
	}
}

// EncodeChangeCursor renders a cursor for the caller.
//
// Unlike EncodePageToken, an empty cursor is not a meaningful value and never
// reaches here: a poll that reported nothing still has a bound, because the
// bound is the clock rather than the last row.
func EncodeChangeCursor(c ChangeCursor) string {
	if c.Through == "" {
		return ""
	}
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// ParseChangeCursor reads a --since value's own shape, and says nothing about
// which site it belongs to.
//
// Separate from DecodeChangeCursor for the reason ParsePageToken is separate
// from DecodePageToken: everything here is arithmetic on a string the caller
// typed, so a garbled cursor is refused before a session is built and the
// deployment probe's round trip is never spent on a typo.
func ParseChangeCursor(encoded string) (ChangeCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ChangeCursor{}, invalidCursor().Wrap(err)
	}

	var c ChangeCursor
	if err := json.Unmarshal(data, &c); err != nil {
		return ChangeCursor{}, invalidCursor().Wrap(err)
	}
	if c.Deployment == "" {
		return ChangeCursor{}, invalidCursor().
			WithDetail("it names no deployment, and every cursor this tool mints does")
	}
	if _, ok := c.Instant(); !ok {
		return ChangeCursor{}, invalidCursor().
			WithDetail("it carries %q, which is not an instant", c.Through)
	}
	return c, nil
}

// DecodeChangeCursor parses a --since value and checks it against the site it is
// about to be used on.
func DecodeChangeCursor(encoded string, want site.Kind) (ChangeCursor, error) {
	c, err := ParseChangeCursor(encoded)
	if err != nil {
		return ChangeCursor{}, err
	}
	if c.Deployment != want {
		// Two sites, two changelogs, two clocks. Resuming one's cursor against
		// the other would report a window of somebody else's history as this
		// site's, which is worse than refusing because it looks like an answer.
		return ChangeCursor{}, errs.Usage("INVALID_SINCE_TOKEN",
			"--since carries a cursor issued for a %s site and this is %s",
			deploymentName(c.Deployment), deploymentName(want)).
			WithRemedy("re-run with a date or an offset to start a new feed " +
				"against this site")
	}
	return c, nil
}

// LooksLikeChangeCursor reports whether a --since value is a cursor this tool
// minted rather than a date somebody typed.
//
// The two forms are told apart by shape and not by trying each in turn: a
// mistyped date that happened to be valid base64 holding valid JSON would
// otherwise be reported as a bad cursor, and a mistyped cursor as a bad date.
// Every cursor this tool mints is base64 of an object naming a deployment, and
// no date jql.ParseDate accepts is.
func LooksLikeChangeCursor(value string) bool {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	var probe struct {
		Deployment string `json:"d"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Deployment != ""
}

// Instant is the bound this cursor carries, and whether it carries one at all.
func (c ChangeCursor) Instant() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, c.Through)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// String renders the cursor for debugging. It is the encoded form, because the
// decoded one invites a caller to construct it by hand.
func (c ChangeCursor) String() string { return EncodeChangeCursor(c) }

func invalidCursor() *errs.Error {
	return errs.Usage("INVALID_SINCE_TOKEN",
		"--since is not a cursor this tool issued").
		WithRemedy("pass a next-since-token from a previous result, or a date " +
			"like 2026-08-10 or an offset like -7d to start a new feed")
}

// ChangeWindow is the half-open interval of changes one poll reports:
// everything created after After, up to and including Through.
type ChangeWindow struct {
	// After is the previous poll's bound, exclusive.
	After time.Time
	// Through is this poll's bound, inclusive. It is the server's clock at the
	// moment the walk began, and never this process's clock: the timestamps it
	// is compared against were written by the server, and a client running two
	// minutes fast would otherwise report a window it had not looked at.
	Through time.Time
}

// NewChangeWindow bounds one poll, refusing a window that runs backwards.
//
// A cursor ahead of the server's clock is not arithmetic to be clamped. It means
// the cursor came from somewhere else, or the site's clock moved, and either way
// the honest answer is that this tool cannot say what happened in between.
func NewChangeWindow(after, through time.Time) (ChangeWindow, error) {
	a := after.UTC().Truncate(time.Second)
	t := through.UTC().Truncate(time.Second)
	if t.Before(a) {
		return ChangeWindow{}, errs.Usage("SINCE_AFTER_NOW",
			"--since names a time the site's clock has not reached").
			WithDetail("cursor %s, site clock %s",
				a.Format(time.RFC3339), t.Format(time.RFC3339)).
			WithRemedy("check the cursor came from this site, or pass a date to " +
				"start a new feed")
	}
	return ChangeWindow{After: a, Through: t}, nil
}

// Holds reports whether a change belongs to this window.
//
// Exclusive below and inclusive above, which is what makes two consecutive
// windows cover every instant exactly once. Both bounds are truncated to the
// second by NewChangeWindow, and the changelog timestamps this compares against
// are published to the second, so the comparison is between two values of the
// same precision rather than between a published one and a stored one.
func (w ChangeWindow) Holds(created time.Time) bool {
	c := created.UTC().Truncate(time.Second)
	return c.After(w.After) && !c.After(w.Through)
}

// Floor is the JQL bound that selects every issue this window could hold a
// change for.
//
// It is the window's lower bound rounded **down** to the minute, because a
// minute is the finest bound JQL can express. That widens the candidate set by
// up to fifty-nine seconds of issues, whose older changes Holds then drops. The
// widening is the cost of the measurement in the file comment above, and it is
// the safe direction: rounding up would ask the server to skip the part of the
// minute the window still needs.
//
// loc is the timezone on the Jira account's profile, because JQL evaluates a
// literal in that zone rather than in UTC. Passing UTC for an account on
// America/Chicago produces a bound five hours wrong, silently, and in the
// direction that drops changes.
func (w ChangeWindow) Floor(loc *time.Location) (string, error) {
	if loc == nil {
		return "", errs.Runtime("NO_TIMEZONE",
			"a change feed cannot mint a query bound without the account's timezone")
	}
	return w.After.In(loc).Truncate(time.Minute).Format(jqlMinuteLayout), nil
}

// jqlMinuteLayout is the absolute form JQL accepts with a time of day. It is one
// of the layouts in internal/jql, spelled here because this renders a bound
// rather than reading one.
const jqlMinuteLayout = "2006-01-02 15:04"

// Cursor is the resume point a poll over this window ends at.
func (w ChangeWindow) Cursor(kind site.Kind) ChangeCursor {
	return NewChangeCursor(kind, w.Through)
}
