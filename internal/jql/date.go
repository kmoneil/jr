package jql

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/errs"
)

// dateLayouts are the absolute forms Jira accepts, most specific first.
//
// Each is parsed with time.Parse, which validates ranges: 2020-13-45 is
// rejected here rather than being passed through as a literal that quietly
// matches nothing.
//
// **A minute is the finest bound JQL can express, and that is a property of
// Jira rather than of this list.** Measured 2026-08-12 against Cloud and
// against Data Center 9.12: `updated >= "2026-08-12 18:13"` parses and
// `updated >= "2026-08-12 18:13:30"` is refused as invalid on both, as is
// RFC 3339. Neither operator can bisect a minute either — with three issues
// updated at :19, :23 and :27, both `>= "…18:13"` and `> "…18:13"` returned
// all three, and `>= "…18:14"` returned none.
//
// Anything building a cursor on a date field has to know that: a poller that
// resumes at the minute it last saw re-reads part of it, and one that resumes
// at the next minute skips whatever landed after its last read. Ties make the
// same point from the other side — two issues edited concurrently were stamped
// with the identical `updated` — so a timestamp is not a cursor at any
// precision, and the resume point has to be a (timestamp, key) pair.
var dateLayouts = []string{
	"2006-01-02 15:04",
	"2006/01/02 15:04",
	"2006-01-02",
	"2006/01/02",
}

// relativePattern matches Jira's relative date syntax: an optional sign, a
// number, and a unit. `-7d`, `2w`, `+30m`.
var relativePattern = regexp.MustCompile(`^[-+]?\d+[mhdwM]$`)

// datePattern and dateSeparator tell an out-of-range date from a word, so
// dateHint can say which part is wrong. Compiled once, beside relativePattern
// rather than inside the function that uses them — every date on a query goes
// through here, and one file compiling the same expression two ways is a
// question a reader has to answer before they can trust either.
var (
	datePattern   = regexp.MustCompile(`^\d{4}[-/]\d{1,2}[-/]\d{1,2}$`)
	dateSeparator = regexp.MustCompile(`[-/]`)
)

// dateFunctions are the JQL functions valid in a date comparison.
var dateFunctions = map[string]bool{
	"now":                  true,
	"currentlogin":         true,
	"lastlogin":            true,
	"startofday":           true,
	"endofday":             true,
	"startofweek":          true,
	"endofweek":            true,
	"startofmonth":         true,
	"endofmonth":           true,
	"startofyear":          true,
	"endofyear":            true,
	"currentuser":          true,
	"membersof":            true,
	"componentsleadbyuser": true,
	"projectsleadbyuser":   true,
}

// DateKind classifies a date this package accepts, so a caller that has to
// compare values locally can tell what it is being handed before it tries.
//
// The distinction is not cosmetic. A relative offset names an instant, and an
// instant is the same in every timezone. An absolute literal names a wall clock
// and means nothing until a zone is supplied. A function is arithmetic only the
// server can do.
type DateKind int

const (
	// DateInvalid is anything ParseDate refuses.
	DateInvalid DateKind = iota
	// DateRelative is Jira's offset syntax: -7d, +30m, 2w.
	DateRelative
	// DateAbsolute is one of dateLayouts, with or without a time of day.
	DateAbsolute
	// DateFunction is shaped like a call: startOfWeek(), endOfDay(-1). The
	// shape is what is classified, not the validity — parseDateFunction
	// settles whether the name and the arguments are real, so that a bad
	// function is still refused as a bad function rather than as a bad date.
	DateFunction
)

// ClassifyDate reports which of the three forms an input takes.
//
// This is the single enumeration of what a date may be. ParseDate branches on
// it and so does ResolveDate, which is what stops the two from drifting: a
// layout added here is accepted and resolvable in the same commit, and
// `TestEveryAcceptedDateIsBoundedOrRefused` fails if a command can be handed a
// form it cannot apply.
func ClassifyDate(input string) DateKind {
	s := strings.TrimSpace(input)
	switch {
	case s == "":
		return DateInvalid
	case strings.HasSuffix(s, ")"):
		return DateFunction
	case relativePattern.MatchString(s):
		return DateRelative
	}
	for _, layout := range dateLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return DateAbsolute
		}
	}
	return DateInvalid
}

// ResolveDate resolves a date into the instant it names, for a caller that must
// compare against it in this process rather than send it to Jira.
//
// Only `issue activity` needs this, and it needs it because three of its four
// event kinds are matched here rather than by the server. Everything else on a
// query passes the value through, which is deliberate and documented in
// `docs/output-contract.md`: computing a date locally substitutes this client's
// notion of a boundary for Jira's.
//
// loc is the timezone on the **Jira account's profile**, because that is the
// clock Jira evaluates a literal in. Passing the local machine's zone, or UTC
// on an account that is not on UTC, produces a bound that is wrong by the
// offset — silently, and in the direction that drops events on any account east
// of UTC.
//
// Reports false for a function, for an unparseable input, and for an absolute
// literal with no zone to read it in. A false is never a reason to fall back to
// an unbounded filter; the caller refuses.
func ResolveDate(input string, loc *time.Location, now time.Time) (time.Time, bool) {
	s := strings.TrimSpace(input)
	switch ClassifyDate(s) {
	case DateRelative:
		if d, ok := relativeOffset(s); ok {
			return now.Add(d), true
		}
	case DateAbsolute:
		if loc == nil {
			return time.Time{}, false
		}
		for _, layout := range dateLayouts {
			if t, err := time.ParseInLocation(layout, s, loc); err == nil {
				return t, true
			}
		}
	case DateInvalid, DateFunction:
		return time.Time{}, false
	}
	return time.Time{}, false
}

// relativeOffset reads Jira's relative date syntax as a duration. The units are
// Jira's, where `m` is minutes and `M` is months.
//
// A month is thirty days here and not a calendar month. That is an
// approximation, and where this feeds a client-side event filter it is an
// approximation in the direction that **drops** events: the server's `-1M` from
// the 31st reaches back further than thirty days, so an event in the gap
// satisfies the query and is then filtered out here. Whether Jira accepts `M`
// at all is unmeasured against either deployment, which is why this still says
// thirty rather than having been corrected to something equally unmeasured: a
// calendar month computed here would be a second guess, not a fix.
// Call it only for a string relativePattern has matched. That is what makes the
// indexing safe and the default arm correct: the pattern's unit class is
// exactly `mhdwM`, so anything that is not the first four is the fifth. A unit
// added to the pattern without a case here would silently become months, which
// is why the two are written one above the other.
func relativeOffset(s string) (time.Duration, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(s[:len(s)-1], "+"))
	if err != nil {
		// A number too large to hold. It matched the pattern, so it is a date
		// the caller wrote and not a typo, and the honest answer is that this
		// process cannot name the instant.
		return 0, false
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	default:
		return time.Duration(n) * 30 * 24 * time.Hour, true
	}
}

// ParseDate resolves a user-supplied date into a JQL value.
//
// It accepts an absolute date, a relative offset, or a date function. Anything
// else is a usage error naming what was wrong — never a literal passed through
// to Jira, which is how the incumbent turns a typo into an empty result set.
func ParseDate(input string) (Value, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, errs.Usage("INVALID_DATE", "date is empty").
			WithRemedy("use YYYY-MM-DD, a relative offset like -7d, or a function like startOfWeek()")
	}

	switch ClassifyDate(s) {
	case DateFunction:
		// A function call: startOfWeek(), endOfDay(-1).
		return parseDateFunction(s)
	case DateRelative, DateAbsolute:
		return Text(s), nil
	case DateInvalid:
	}

	e := errs.Usage("INVALID_DATE", "%q is not a date", input).
		WithRemedy("use YYYY-MM-DD, YYYY-MM-DD HH:MM, a relative offset like -7d, " +
			"or a function like startOfWeek()")
	if hint := dateHint(s); hint != "" {
		return nil, e.WithDetail("%s", hint)
	}
	return nil, e
}

// dateHint explains a near-miss, so an off-by-one month is not reported the
// same way as a word.
func dateHint(s string) string {
	if !datePattern.MatchString(s) {
		return ""
	}
	parts := dateSeparator.Split(s, 3)
	month, _ := strconv.Atoi(parts[1])
	day, _ := strconv.Atoi(parts[2])
	switch {
	case month < 1 || month > 12:
		return "month " + parts[1] + " is out of range"
	case day < 1 || day > 31:
		return "day " + parts[2] + " is out of range"
	default:
		return "that day does not exist in that month"
	}
}

// parseDateFunction validates a function call and rebuilds it as a Func node,
// so the rendered form comes from the renderer rather than from user text.
func parseDateFunction(s string) (Value, error) {
	open := strings.Index(s, "(")
	if open < 0 {
		return nil, errs.Usage("INVALID_DATE", "%q is not a date function", s).
			WithRemedy("a function call looks like startOfWeek() or endOfDay(-1)")
	}

	name := strings.TrimSpace(s[:open])
	if !isSimpleIdent(name) || !dateFunctions[strings.ToLower(name)] {
		return nil, errs.Usage("INVALID_DATE", "%q is not a known date function", name).
			WithDetail("input: %s", s).
			WithRemedy("valid functions include now(), startOfDay(), startOfWeek(), " +
				"endOfMonth(), and currentLogin()")
	}

	inner := strings.TrimSpace(s[open+1 : len(s)-1])
	f := &Func{Name: name}
	if inner == "" {
		return f, nil
	}

	for raw := range strings.SplitSeq(inner, ",") {
		arg := strings.TrimSpace(raw)
		arg = strings.Trim(arg, `"'`)
		switch {
		case arg == "":
			return nil, errs.Usage("INVALID_DATE", "%s has an empty argument", name).
				WithDetail("input: %s", s)
		case relativePattern.MatchString(arg), isNumeric(arg):
			f.Args = append(f.Args, Text(arg))
		default:
			return nil, errs.Usage("INVALID_DATE",
				"%q is not a valid argument to %s", arg, name).
				WithDetail("input: %s", s).
				WithRemedy("an offset argument looks like -1, -1d, or 2w")
		}
	}
	return f, nil
}

// Since builds `field >= value` from a user-supplied date.
func Since(field, date string) (Expr, error) {
	v, err := ParseDate(date)
	if err != nil {
		return nil, err
	}
	return &Clause{Field: field, Op: OpGte, Value: v}, nil
}

// Until builds `field <= value` from a user-supplied date.
func Until(field, date string) (Expr, error) {
	v, err := ParseDate(date)
	if err != nil {
		return nil, err
	}
	return &Clause{Field: field, Op: OpLte, Value: v}, nil
}
