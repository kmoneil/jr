package jql

import (
	"math"
	"regexp"
	"slices"
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
//
// **A minute is not accepted on every field.** Data Center refuses one on
// `worklogDate` while taking it on `updated`, and Cloud takes it on both, so
// which of these four a given clause may use is a property of the field and the
// deployment together. That belongs where the deployment is known, not here;
// `hasTime` is what lets a caller ask which layouts carry a clock without
// keeping a second copy of this list, and a second copy of this list is exactly
// the defect that produced the card this note was written under.
var dateLayouts = []struct {
	layout string
	// hasTime marks a layout that names a time of day as well as a date.
	hasTime bool
}{
	{"2006-01-02 15:04", true},
	{"2006/01/02 15:04", true},
	{"2006-01-02", false},
	{"2006/01/02", false},
}

// dateValueUnits are the units Jira accepts in a period on a date field, and
// the duration each one names.
//
// Keyed on the lowercase spelling, because Jira reads these case-insensitively.
// That is not a convenience here, it is the reason `M` means minutes: `M` and
// `m` are one unit, and this code once read `M` as thirty days, so
// `--updated-after -1M` asked the server for the last minute and told the local
// filter it meant the last month. Measured 2026-08-14 against Cloud and Data
// Center 10.4 in both directions: `-60M`, `-60m` and `-1h` return the same rows,
// `-43200M` returns the same rows as `-30d`, and `-7D`, `-2H` and `-1W` are
// accepted and answer identically to their lowercase spellings.
//
// `y` and `s` are not units here. `updated >= "-1y"` is refused by the server,
// which matters because a *function argument* does take `y` and is a different
// grammar, see functionArgUnits.
//
// **This table is the only place a date-value unit exists.** relativePattern's
// character class is generated from it and relativeOffset resolves through it,
// so the pattern cannot accept a unit the resolver has never heard of. What
// stood here before was a hand-written class beside a switch ending in a default
// arm that meant minutes, and adding `D` to that class would have made `-7D`
// seven minutes without a compiler or a test saying anything.
//
// The keys have to stay lowercase, because the lookup folds and the map does
// not: an uppercase key would generate a class character that matches and a key
// the fold cannot find, which is the same silent zero in a new costume.
// TestEveryRelativeUnitResolves is what holds them there. It sweeps every
// letter and requires the pattern's answer and this table's to agree, so a unit
// added to one and not the other fails rather than resolving to nothing.
var dateValueUnits = map[byte]time.Duration{
	'm': time.Minute,
	'h': time.Hour,
	'd': 24 * time.Hour,
	'w': 7 * 24 * time.Hour,
}

// functionArgUnits are the units Jira accepts in a duration argument to a date
// function: `endOfDay(-1d)`, `startOfMonth(-1M)`.
//
// **A different grammar from the one above**, which is why it is a different
// table. Measured 2026-08-14 through the server's own parser, and it disagrees
// with a date value about three things:
//
//   - Case. These are case-sensitive. `endOfDay(-1D)`, `-1W`, `-1H` and `-1Y`
//     are all refused, quoted or unquoted, while `updated >= "-1D"` is accepted.
//   - `M`. It is **months** here and minutes there. Jira's own message says so:
//     "should have the format (+/-)n(yMwdm), e.g -1M for 1 month earlier".
//     Measured rather than believed, because that message is also incomplete:
//     `startOfDay(-1M)` returns what `-30d` returns where a minute returns none,
//     and `h` is accepted despite not appearing in the list.
//   - Compounds. `endOfDay("-1w 2d")` is refused; `updated >= "-1w 2d"` is not.
//
// One pattern served both grammars until this table existed, which left `jr`
// refusing `endOfDay(-1y)` that Jira accepts, and would have made any widening
// of the date-value class start accepting `endOfDay(-1D)` that Jira refuses.
//
// **No duration, deliberately.** A year and a month are not a fixed count of
// nanoseconds, and a function is the server's to evaluate: ResolveDate refuses
// a DateFunction rather than computing one, so nothing here needs to name an
// instant. A table carrying a duration would be an invitation to compute one.
var functionArgUnits = map[byte]bool{
	'y': true, // Years.
	'M': true, // Months, where the other grammar reads the same letter as minutes.
	'w': true, // Weeks.
	'd': true, // Days.
	'h': true, // Hours, accepted although Jira's error text omits it.
	'm': true, // Minutes.
}

// unitClass renders the character class a pattern matches, from the table that
// gives those characters their meaning.
//
// Generated rather than written, because a class and a lookup table are two
// lists with no reason to agree, and this file has already shipped the
// consequence once. Sorted so the compiled pattern does not depend on map
// iteration order.
func unitClass[V any](units map[byte]V) string {
	class := make([]byte, 0, len(units))
	for u := range units {
		class = append(class, u)
	}
	slices.Sort(class)
	return string(class)
}

// relativeComponent is one `<number><unit>` of a period on a date field.
var relativeComponent = `\d+[` + unitClass(dateValueUnits) + `]`

// relativePattern matches Jira's relative date syntax on a date field: an
// optional sign, then one or more components separated by spaces. `-7d`, `2w`,
// `+30m`, `-4w 2d`.
//
// Case-insensitive because Jira is, and the flag is the claim: the alternative
// is spelling both cases into the class, which reads as a list rather than as
// the property it encodes.
//
// The sign is written once, on the front of the whole period. `-4w -2d` is
// refused by Jira and by this, and `4w2d` with no space is refused by both.
// Components sum and the sign applies to the total: `-1w 7d` returns exactly
// what `-14d` returns, measured.
var relativePattern = regexp.MustCompile(
	`(?i)^[-+]?` + relativeComponent + `(?: +` + relativeComponent + `)*$`)

// functionArgPattern matches a duration argument to a date function. Case
// matters, and there is no compound form.
var functionArgPattern = regexp.MustCompile(
	`^[-+]?\d+[` + unitClass(functionArgUnits) + `]$`)

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
	for _, l := range dateLayouts {
		if _, err := time.Parse(l.layout, s); err == nil {
			return DateAbsolute
		}
	}
	return DateInvalid
}

// DateHasTimeOfDay reports whether a date names a time as well as a day.
//
// It exists because a minute is not accepted on every field: Data Center
// refuses one on `worklogDate` and takes one on `updated`, and Cloud takes both.
// Which fields those are is a property of the deployment, so the rule lives with
// the code that knows the deployment; what that code needs from here is only
// whether the caller wrote a clock, which is this package's business because
// this package owns the layouts.
//
// False for a relative offset and for a function. Neither carries a time of day
// in a sense a field can refuse: an offset is arithmetic on the server's clock,
// and Data Center accepts `-5d` on `worklogDate` in the same breath as it
// refuses `2026-08-10 00:00`.
// The layouts answer the question on their own: nothing else this package
// accepts parses as one, so an offset and a function fall through to the same
// false as a word does.
func DateHasTimeOfDay(input string) bool {
	s := strings.TrimSpace(input)
	for _, l := range dateLayouts {
		if _, err := time.Parse(l.layout, s); err == nil {
			return l.hasTime
		}
	}
	return false
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
		for _, l := range dateLayouts {
			if t, err := time.ParseInLocation(l.layout, s, loc); err == nil {
				return t, true
			}
		}
	case DateInvalid, DateFunction:
		return time.Time{}, false
	}
	return time.Time{}, false
}

// relativeOffset reads Jira's relative date syntax as a duration.
//
// **`M` is minutes, not months**, because the units are Jira's and Jira's are
// case-insensitive. This code believed otherwise and resolved `M` as thirty
// days, so `--updated-after -1M` asked the server for the last minute and told
// any local filter it meant the last month, a factor of 43,200 apart and silent
// on both sides. Measured 2026-08-14 against Cloud and Data Center 10.4:
//
//	              Data Center   Cloud
//	-1h                     5       0
//	-60M                    5       0     equal to -1h on both
//	-60m                    5       0
//	-1M                     0       0     one minute, not one month
//	-30d                    5       6
//	-43200M                 -        6     43200 minutes, equal to -30d
//	-1440M                  -        0     equal to -1d
//
// Cloud's sandbox had nothing touched within the hour, so the minute-scale rows
// there are zero for both spellings and the 43200/1440 pair is what carries the
// argument on that deployment.
//
// A compound period sums its components and takes its sign from the front:
// `-1w 7d` is fourteen days back, and returned exactly what `-14d` returned on
// the sandbox. That is what makes a compound one duration like any other, and
// therefore resolvable here rather than a form the query accepts and the event
// filter cannot apply.
//
// Call it only for a string relativePattern has matched. That is what makes the
// unit lookup safe with no fallback: the class the pattern matches is generated
// from dateValueUnits, so a unit it accepts is a key that exists. There is no
// default arm to be wrong, which is the whole reason the class is generated.
func relativeOffset(s string) (time.Duration, bool) {
	// One character, because the pattern allows one. Trimming the set would
	// quietly accept `--1d` if the pattern ever did.
	var negative bool
	switch s[0] {
	case '-':
		negative, s = true, s[1:]
	case '+':
		s = s[1:]
	}

	var total time.Duration
	for part := range strings.FieldsSeq(s) {
		n, err := strconv.Atoi(part[:len(part)-1])
		if err != nil {
			// A number too large to hold. It matched the pattern, so it is a
			// date the caller wrote and not a typo, and the honest answer is
			// that this process cannot name the instant.
			return 0, false
		}
		unit := dateValueUnits[toLowerASCII(part[len(part)-1])]

		// A duration is nanoseconds in an int64, which runs out at 292 years,
		// and a period Jira answers happily can pass that: `-1000000d` is a
		// legal query and 2739 years. Multiplying it here would wrap to a
		// negative and name an instant in the future, which is the same class
		// of defect as reading `M` as months, so it is refused for the same
		// reason a twenty-digit number is.
		if int64(n) > int64(maxDuration/unit) {
			return 0, false
		}
		component := time.Duration(n) * unit
		if total > maxDuration-component {
			return 0, false
		}
		total += component
	}

	if negative {
		return -total, true
	}
	return total, true
}

// maxDuration is the largest instant offset a time.Duration can hold, about 292
// years.
const maxDuration = time.Duration(math.MaxInt64)

// toLowerASCII folds one unit letter, which is all the case-insensitivity
// relativePattern's `(?i)` lets through.
func toLowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
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
			WithRemedy("use YYYY-MM-DD, a relative offset like -7d or -4w 2d, " +
				"or a function like startOfWeek()")
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
		WithRemedy("use YYYY-MM-DD, YYYY-MM-DD HH:MM, a relative offset like " +
			"-7d or -4w 2d, or a function like startOfWeek()")
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
		case functionArgPattern.MatchString(arg), isNumeric(arg):
			f.Args = append(f.Args, Text(arg))
		default:
			// functionArgPattern and not relativePattern: a duration argument
			// is a narrower grammar than a date value, with its own units and
			// its own answer for `M`. The remedy names the units because the
			// difference is invisible from here, `-1D` being accepted on a
			// field and refused in a function.
			return nil, errs.Usage("INVALID_DATE",
				"%q is not a valid argument to %s", arg, name).
				WithDetail("input: %s", s).
				WithRemedy("an offset argument looks like -1, -1d, or -2M; " +
					"the units are y M w d h m and are case-sensitive here, " +
					"where M is months")
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
