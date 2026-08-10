package issue

import (
	"strconv"
	"time"

	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// ageFlagName adds a human-readable age to the output.
//
// It **adds** a column rather than changing one, and that is the whole design.
// Every timestamp this tool emits is RFC 3339 in UTC and is part of the output
// contract, so rendering `updated` as "3 hours" would break every consumer that
// parses it — silently, and for the caller who put the flag in a shell alias
// months earlier. Appending a second column instead leaves `updated` exactly
// where it was and costs a consumer nothing, which is the same bargain --url
// makes.
//
// Off by default, for the reason --url is: §2.4 says no field appears unless it
// was requested or is in the documented default set.
const ageFlagName = "age"

// ageFlag is declared once because `issue list` and `issue get` need the same
// question answered the same way.
func ageFlag() registry.Flag {
	return registry.Flag{
		Name: ageFlagName, Type: registry.TypeBool,
		Usage: "include an age column: how long since the issue was last " +
			"updated, coarsely, e.g. 3 hours or 14 days",
	}
}

// ageColumn is the TSV column --age appends.
//
// Appended rather than inserted, so adding it cannot shift the position of a
// column somebody already parses.
func ageColumn() render.Column {
	return render.Column{Header: ageFlagName, Path: ageFlagName}
}

// stampAges fills in the age of each issue on a page, measured from one
// instant.
//
// The instant is passed in rather than read per row, so every row on a page is
// measured against the same clock. Reading time.Now per issue would let a slow
// page report the first row as older than the last for no reason but the
// rendering — the same defect as Cache.Put stamping with time.Now while Get
// used an injected clock, which is where "one clock per component" comes from.
func stampAges(issues []Issue, now time.Time) {
	for i := range issues {
		issues[i].Age = Age(issues[i].Updated, now)
	}
}

// Age renders how long ago an RFC 3339 instant was, coarsely and in one unit.
//
// It is a pure function of two instants so that the interesting part — where
// the units change, and what happens either side of a boundary — is testable
// without a clock anywhere near a command. The command supplies time.Now once
// per invocation and nothing else here knows what time it is.
//
// **It stops at days.** A month has no fixed length and a year has two, so
// "3 months" would mean whichever divisor this file happened to pick, and the
// caller could not tell which. 412 days is longer to read and is the number
// this tool actually knows. The exact instant is in the column beside it.
//
// An empty timestamp gives an empty age: absent stays absent rather than
// becoming "0 seconds", which would claim the issue was updated just now.
func Age(timestamp string, now time.Time) string {
	at, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		// Two cases, one answer. An empty stamp is an issue Jira reported no
		// `updated` for, and absent has to stay absent — "0 seconds" would
		// claim it had just been touched. An unparseable one cannot happen
		// through normalizeTime, which refuses what it cannot read, and there
		// is nothing useful to say about it in a table cell either way.
		//
		// There was a guard for the empty case above this. It was removed
		// because time.Parse("") fails to exactly the same result, so nothing
		// could tell the two paths apart — including the test written to.
		return ""
	}

	d := now.Sub(at)
	if d < 0 {
		// The server's clock is ahead of this machine's. Reporting a negative
		// age would be reporting the skew, which is not what the column is
		// about, and inventing a positive one would be worse.
		d = 0
	}

	switch {
	case d < time.Minute:
		return plural(int(d/time.Second), "second")
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/(24*time.Hour)), "day")
	}
}

// plural renders a count and its unit, with no "ago" — the column is headed
// `age`, so the suffix would say a second time what the header already says.
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
