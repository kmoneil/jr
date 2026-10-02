package issue_test

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestActivityMergesFourSourcesInOneRequest is the whole argument for the verb.
//
// Comments, worklogs and the changelog are three projections on one search, so
// a page of issues yields every kind of event in the request that fetched the
// page. The recording holds exactly one interaction, so a second request would
// be a replay miss.
func TestActivityMergesFourSourcesInOneRequest(t *testing.T) {
	out, _, replayer := runActivity(t, nil)

	if got := replayer.Unplayed(); len(got) != 0 {
		t.Errorf("recorded requests nobody made: %v", got)
	}
	if got := replayer.Unmatched(); len(got) != 0 {
		t.Errorf("asked for something outside the recording: %v", got)
	}

	seen := map[string]bool{}
	for _, row := range strings.Split(strings.TrimRight(out, "\n"), "\n")[1:] {
		seen[strings.Split(row, "\t")[2]] = true
	}
	for _, kind := range []string{issue.EventComment, issue.EventField, issue.EventWorklog} {
		if !seen[kind] {
			t.Errorf("no %s event in a feed over an issue that has one:\n%s", kind, out)
		}
	}
}

// TestActivityIsNewestFirstWhateverTheServerSent is the finding that made this
// verb sort rather than concatenate.
//
// Measured 2026-08-12: Cloud returns one issue's changelog oldest-first from
// /issue/{key}/changelog and newest-first from the same data under
// expand=changelog, and Data Center returns it oldest-first from the
// projection. Three orderings for one feature, so inheriting any of them is a
// bug on at least one deployment.
func TestActivityIsNewestFirstWhateverTheServerSent(t *testing.T) {
	out, _, _ := runActivity(t, nil)

	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")[1:]
	if len(rows) < 3 {
		t.Fatalf("only %d rows; this asserts nothing about ordering", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		prev := strings.Split(rows[i-1], "\t")[0]
		this := strings.Split(rows[i], "\t")[0]
		if this > prev {
			t.Fatalf("row %d is newer than the one before it (%s after %s):\n%s",
				i, this, prev, out)
		}
	}
}

// TestActivityTruncatesAndSaysSo is the exit-3 proof this command declares.
//
// The clipped source is a Cloud comment thread: 20 of 25, which the projection
// caps and no second request here would fix — `issue comment list` is the
// command for that. So the run is incomplete, names the element, and carries no
// resume token, because the issues were all fetched and what is missing is
// inside one of them.
func TestActivityTruncatesAndSaysSo(t *testing.T) {
	_, result, _ := runActivity(t, nil)

	if result.Complete {
		t.Error("a feed missing five comments reported itself complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a resume token was invented for a clipped subresource: %q",
			result.NextPageToken)
	}
	if result.PartialElement != "event" {
		t.Errorf("PartialElement = %q, want event", result.PartialElement)
	}
}

// TestActivityKindFilterNarrowsTheFeed covers --kind, and covers it on the
// output rather than on the request: three of the four kinds come from the same
// response, so a filter that only changed the query would change nothing.
func TestActivityKindFilterNarrowsTheFeed(t *testing.T) {
	// Its own recording, because --kind narrows the *request* as well as the
	// feed: asking for comments alone drops `worklog` from the field list and
	// `expand=changelog` from the query, which is the point — a transition feed
	// should not pay for comment bodies. Replaying the all-kinds cassette would
	// have hidden that by matching a request this run does not make.
	out, _, _ := runActivityAgainst(t, "activity-comments-recorded.cloud.json",
		func(f registry.Flags) { f.SetString("kind", issue.EventComment) })

	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")[1:]
	if len(rows) == 0 {
		t.Fatal("--kind comment returned nothing at all")
	}
	for _, row := range rows {
		if got := strings.Split(row, "\t")[2]; got != issue.EventComment {
			t.Errorf("--kind comment returned a %s event", got)
		}
	}
}

// TestAnUnknownEventKindIsRefused keeps the closed set closed. A consumer
// branches on this attribute, so a kind nobody emits must not be accepted as a
// filter that silently matches nothing.
func TestAnUnknownEventKindIsRefused(t *testing.T) {
	cmd, ok := registry.Lookup("issue.activity")
	if !ok {
		t.Fatal("issue activity is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("since", "-1d")
	flags.SetString("kind", "transitions")
	err := cmd.Validate(t.Context(), &registry.Invocation{Flags: flags})
	if err == nil {
		t.Fatal("an unknown event kind was accepted")
	}
	if code := errs.Coerce(err).Code; code != "UNKNOWN_EVENT_KIND" {
		t.Errorf("code = %q, want UNKNOWN_EVENT_KIND", code)
	}
	if detail := errs.Coerce(err).Detail; !strings.Contains(detail, "worklog") {
		t.Errorf("the refusal does not list the kinds: %q", detail)
	}
}

// TestActivityIsInEveryBuild follows the rule reading always has.
func TestActivityIsInEveryBuild(t *testing.T) {
	cmd, ok := registry.Lookup("issue.activity")
	if !ok {
		t.Fatal("issue activity is not registered")
	}
	if len(cmd.RequiresTags) != 0 {
		t.Errorf("issue activity requires %v; reading needs no tag", cmd.RequiresTags)
	}
	if cmd.Mutating {
		t.Error("issue activity is marked mutating")
	}
}

// activitySinceKey and activityUntilKey mirror the unexported keys the command
// leaves its resolved window under, and farPast is an instant older than any
// recording in this tree. Together they are how a dated cassette is held still
// without switching off the filter that reads the date.
const (
	activitySinceKey = "issue.activity.since"
	activityUntilKey = "issue.activity.until"
	farPast          = "2000-01-01T00:00:00Z"
)

// runActivity drives the registered command against the recording.
func runActivity(
	t *testing.T, set func(registry.Flags),
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()
	return runActivityAgainst(t, "activity-recorded.cloud.json", set)
}

func runActivityAgainst(
	t *testing.T, fixture string, set func(registry.Flags),
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()
	return runActivityOn(t, site.Cloud, fixture, "key = AGL-3", farPast, set)
}

// runActivityOn takes the cutoff explicitly, because a test about the time
// bound has to be able to move it. Passing farPast means "every event in the
// fixture", which is what the callers that are about something else want, and
// passing "" means "keep whatever Validate resolved", which is what a test
// about --since itself wants. There is no way to ask for no bound at all,
// because that state is the bug.
func runActivityOn(
	t *testing.T, kind site.Kind, fixture, query, cutoff string,
	set ...func(registry.Flags),
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()
	return runActivityIn(t, kind, fixture, query, activityWindow{since: cutoff}, set...)
}

// activityWindow pins the feed's instants after Validate has resolved the
// flags against the clock. An empty end keeps what Validate resolved.
type activityWindow struct{ since, until string }

func runActivityIn(
	t *testing.T, kind site.Kind, fixture, query string, window activityWindow,
	set ...func(registry.Flags),
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.activity")
	if !ok {
		t.Fatal("issue activity is not registered")
	}

	flags := registry.NewFlags()
	// The window the recording was made in. The cutoff is also applied to each
	// event, so a window that excluded the fixture's own timestamps would empty
	// the feed without failing anything.
	flags.SetString("since", "-1d")
	flags.SetString("jql", query)
	flags.SetInt("page-size", 50)
	for _, apply := range set {
		if apply != nil {
			apply(flags)
		}
	}
	// --limit and --max-requests are global, and the CLI layer hands them to a
	// command as inv.Limit and as the transport's budget rather than as flags.
	// A test that sets either through the flag hook gets it carried across the
	// same way.
	limit := registry.Limit{All: true}
	if n := flags.Int("limit"); n > 0 {
		limit = registry.Limit{N: n}
	}
	conn, replayer := replayConnBudget(t, fixture, flags.Int("max-requests"))
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{
				body: catalogueJSON,
				// An absolute --since is read in the account's zone, which is
				// one GET to /myself. A relative one never asks.
				byPath: map[string]string{"myself": accountJSON},
			},
			conn: conn, kind: kind, unscoped: true,
		},
		Flags: flags, Limit: limit,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Validate resolves --since against the clock, and the recording is fixed
	// in time. Overriding it here keeps the fixture from ageing out of its own
	// window tomorrow, which is the way a dated cassette usually breaks.
	//
	// It used to override it with "", which is the value meaning *no bound at
	// all* — so every test in this file ran with the event filter switched off,
	// and the half of --since that this command exists for was covered by
	// nothing. An instant older than any recording keeps the fixture stable and
	// leaves the filter running.
	if window.since != "" {
		inv.SetValue(activitySinceKey, window.since)
	}
	if window.until != "" {
		inv.SetValue(activityUntilKey, window.until)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.String(), result, replayer
}

// TestActivityCutoffResolvesLocally covers the half of --since that the query
// does not carry.
//
// The bound is applied to each event as well as to the issue search, because an
// issue updated yesterday can hold a comment from last year, and reporting that
// comment because its issue matched would answer a question about issues while
// claiming to answer one about events.
//
// The zone is the account's, not this machine's and not UTC, because that is
// the clock Jira reads a literal in. America/Chicago is the recorded sandbox's,
// and it is UTC-6 in January and UTC-5 in August, so a table that spans both
// fails a resolver that hardcoded either.
func TestActivityCutoffResolvesLocally(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("this build cannot read the zone database: %v", err)
	}
	for input, want := range map[string]string{
		// An offset names an instant, which is the same in every zone.
		"-7d": "2026-08-05T12:00:00Z",
		"-1w": "2026-08-05T12:00:00Z",
		"-2h": "2026-08-12T10:00:00Z",
		// Case and compound resolve here too, or the feed would be bounded by
		// the query and not by the filter.
		"-7D":    "2026-08-05T12:00:00Z",
		"-1w 7d": "2026-07-29T12:00:00Z",
		// A literal is a wall clock, and midnight in Chicago is not midnight
		// in UTC. Both sides of the DST boundary, because the offset is not a
		// constant.
		"2026-01-01":       "2026-01-01T06:00:00Z",
		"2026-08-10":       "2026-08-10T05:00:00Z",
		"2026/08/10":       "2026-08-10T05:00:00Z",
		"2026-08-10 00:00": "2026-08-10T05:00:00Z",
		"2026/08/10 13:45": "2026-08-10T18:45:00Z",
		// An instant is not a wall clock, so Chicago does not move it.
		"2026-08-10T13:45:30Z":      "2026-08-10T13:45:30Z",
		"2026-08-10T13:45:30+02:00": "2026-08-10T11:45:30Z",
		// The server's to evaluate. This resolves to nothing, and the command
		// refuses rather than running with no bound.
		"startOfWeek()": "",
	} {
		if got := issue.ActivityCutoff(input, chicago, now); got != want {
			t.Errorf("ActivityCutoff(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestEveryAcceptedSinceIsBoundedOrRefused holds two sets to each other that
// have no other reason to agree.
//
// `jql.ParseDate` decides what --since may be, and the event filter decides
// what it can compare against. They were separate lists, and the four forms in
// the gap were accepted, sent to the server, and then applied to no event at
// all: exit 0, complete="true", and a feed of events from outside the window.
// Found by somebody using it, not by this file.
//
// So every form the first accepts must either resolve to an instant or be
// refused by name. There is no third outcome, and "bounds nothing" was it.
func TestEveryAcceptedSinceIsBoundedOrRefused(t *testing.T) {
	for _, input := range []string{
		"-7d", "+30m", "2w", "-1M", "-2h",
		// The spellings Jira accepts on a date field that this refused until
		// 2026-08-14: the uppercase units, and the compound form. A compound
		// is one duration, so it belongs on the same side of this test as any
		// other offset rather than being accepted by the query and applied to
		// no event.
		"-7D", "-1W", "-2H", "-4w 2d", "-1w 7d",
		"2026-08-10", "2026/08/10",
		"2026-08-10 00:00", "2026/08/10 13:45",
		// The instants this tool prints, which it refused until issue 213.
		"2026-08-10T13:45:00Z", "2026-08-10T13:45Z", "2026-08-10T13:45:00+02:00",
		"startOfWeek()", "endOfDay(-1)", "now()", "currentLogin()",
	} {
		if _, err := jql.ParseDate(input); err != nil {
			t.Fatalf("ParseDate refuses %q, so this table is stale: %v", input, err)
		}
		cutoff, err := validateActivitySince(t, input)

		// Which outcome is required comes from the classification rather than
		// from a second hand-written list, because a second list is the defect.
		// "Either resolved or refused" on its own would pass a build that
		// refused everything.
		switch jql.ClassifyDate(input) {
		case jql.DateRelative, jql.DateAbsolute, jql.DateInstant:
			if err != nil {
				t.Errorf("--since %q names an instant and was refused: %v", input, err)
			}
			if cutoff == "" {
				t.Errorf("--since %q was accepted and bounds no event", input)
			}
		case jql.DateFunction:
			if err == nil {
				t.Errorf("--since %q cannot be resolved here and was accepted "+
					"with cutoff %q", input, cutoff)
				continue
			}
			if code := errs.Coerce(err).Code; code != "UNBOUNDABLE_DATE" {
				t.Errorf("--since %q refused as %q, want UNBOUNDABLE_DATE",
					input, code)
			}
		case jql.DateInvalid:
			t.Errorf("%q classifies as invalid but ParseDate accepted it", input)
		}
	}
}

// TestTheCutoffBoundsTheFeedAndNotOnlyTheSearch is the half of the
// transcript's defect that a recording can show.
//
// The window the shipped code applied to events was empty, so this asserts the
// thing that empty value silently switched off: an instant inside the fixture's
// own span keeps the events at or after it and drops the rest.
//
// It moves the resolved cutoff rather than the flag, and that is a limit worth
// naming. The cassette is matched on the whole request, JQL included, and
// --since is in that JQL: any other value is a fixture miss, and the only value
// this recording carries is `-1d`, which against a 2026 recording resolves to a
// window that excludes all of it. So the flag-to-cutoff half is covered by
// TestEveryAcceptedSinceIsBoundedOrRefused against Validate, and the
// cutoff-to-feed half is covered here. Joining them costs a new recording.
func TestTheCutoffBoundsTheFeedAndNotOnlyTheSearch(t *testing.T) {
	all, _, _ := runActivity(t, nil)
	rows := strings.Split(strings.TrimRight(all, "\n"), "\n")[1:]
	if len(rows) < 3 {
		t.Fatalf("only %d rows; this asserts nothing about a bound", len(rows))
	}

	// Newest first, so the second row's instant drops at least the oldest.
	cutoff := strings.Split(rows[1], "\t")[0]
	bounded, _, _ := runActivityOn(t, site.Cloud,
		"activity-recorded.cloud.json", "key = AGL-3", cutoff)

	kept := strings.Split(strings.TrimRight(bounded, "\n"), "\n")[1:]
	if len(kept) == 0 {
		t.Fatal("the cutoff emptied a feed it was taken from")
	}
	if len(kept) >= len(rows) {
		t.Errorf("a cutoff at %s kept %d of %d rows and dropped nothing",
			cutoff, len(kept), len(rows))
	}
	for _, row := range kept {
		if at := strings.Split(row, "\t")[0]; at < cutoff {
			t.Errorf("event at %s is older than the cutoff %s", at, cutoff)
		}
	}
}

// TestADateFunctionIsRefusedRatherThanUnbounded pins the one form that cannot
// be resolved here at all.
//
// Computing startOfWeek() locally means picking a day the week starts on, and
// docs/output-contract.md records the decision not to: the function carries
// Jira's notion and a client that computed one would substitute its own. That
// leaves refusing it, because the alternative already shipped and it was a feed
// that reported itself complete while bounding nothing.
func TestADateFunctionIsRefusedRatherThanUnbounded(t *testing.T) {
	_, err := validateActivitySince(t, "startOfWeek()")
	if err == nil {
		t.Fatal("a date function was accepted on --since")
	}
	e := errs.Coerce(err)
	if e.Code != "UNBOUNDABLE_DATE" {
		t.Errorf("code = %q, want UNBOUNDABLE_DATE", e.Code)
	}
	if !strings.Contains(e.Remedy, "-7d") {
		t.Errorf("the refusal offers no form that works: %q", e.Remedy)
	}
}

// TestAnAbsoluteSinceCostsOneRequestAndAnOffsetCostsNone is the price of
// reading a literal in the right zone, asserted so it stays a price rather than
// becoming a habit.
//
// A wall clock needs the account's zone and that is a GET to /myself. An offset
// names an instant and needs nothing, which is most invocations and every
// example in the command's help.
func TestAnAbsoluteSinceCostsOneRequestAndAnOffsetCostsNone(t *testing.T) {
	for input, want := range map[string]int{"-7d": 0, "2026-08-10": 1} {
		doer := &stubDoer{
			body:   catalogueJSON,
			byPath: map[string]string{"myself": accountJSON},
		}
		if _, err := validateActivityWith(t, input, doer); err != nil {
			t.Fatalf("--since %q: %v", input, err)
		}
		if doer.calls != want {
			t.Errorf("--since %q made %d metadata requests, want %d",
				input, doer.calls, want)
		}
	}
}

// TestAnAccountWithNoZoneIsRefusedNotAssumedUTC keeps the fallback out.
//
// Both deployments have been recorded sending a zone, so this is a server
// breaking its own contract. Reading the literal as UTC anyway would be wrong
// by the offset with nothing in the output to say so, which is the defect this
// whole change is about, arriving through a different door.
func TestAnAccountWithNoZoneIsRefusedNotAssumedUTC(t *testing.T) {
	doer := &stubDoer{
		body:   catalogueJSON,
		byPath: map[string]string{"myself": `{"displayName":"Ada Lovelace"}`},
	}
	_, err := validateActivityWith(t, "2026-08-10", doer)
	if err == nil {
		t.Fatal("an account with no timezone resolved a literal anyway")
	}
	if code := errs.Coerce(err).Code; code != "NO_ACCOUNT_TIMEZONE" {
		t.Errorf("code = %q, want NO_ACCOUNT_TIMEZONE", code)
	}
}

// accountJSON is the shape /myself returns, with the zone the recorded Cloud
// sandbox actually carries.
const accountJSON = `{"accountId":"000000:00000000-0000-0000-0000-000000000000",
	"displayName":"Ada Lovelace","timeZone":"America/Chicago","active":true}`

// validateActivitySince runs the registered command's own Validate, which is
// where a streaming command has to refuse: its body writes the header before
// the first page arrives.
func validateActivitySince(t *testing.T, since string) (string, error) {
	t.Helper()
	return validateActivityWith(t, since, &stubDoer{
		body:   catalogueJSON,
		byPath: map[string]string{"myself": accountJSON},
	})
}

func validateActivityWith(
	t *testing.T, since string, doer *stubDoer,
) (string, error) {
	t.Helper()
	inv, err := validateActivityWindow(t, since, "", doer)
	cutoff, _ := inv.Value(activitySinceKey).(string)
	return cutoff, err
}

// validateActivityWindow runs Validate with both ends of a window and returns
// the invocation, which holds the instants each end resolved to.
func validateActivityWindow(
	t *testing.T, since, until string, doer *stubDoer,
) (*registry.Invocation, error) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.activity")
	if !ok {
		t.Fatal("issue activity is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("since", since)
	if until != "" {
		flags.SetString("until", until)
	}
	flags.SetInt("page-size", 50)
	inv := &registry.Invocation{
		Jira:  &stubSession{metaClient: doer, unscoped: true},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	}
	return inv, cmd.Validate(t.Context(), inv)
}

// accountDoer answers /myself with the recorded Cloud sandbox's zone.
func accountDoer() *stubDoer {
	return &stubDoer{body: catalogueJSON, byPath: map[string]string{"myself": accountJSON}}
}

// feedRows is a TSV feed's rows, without its header.
func feedRows(feed string) []string {
	return strings.Split(strings.TrimRight(feed, "\n"), "\n")[1:]
}

// TestUntilEndsTheWindowBeforeItsInstant is --until on a recorded feed. Two
// windows that meet at an instant inside the fixture's span split the feed
// between them: every event lands in exactly one, and the event at the
// instant lands in the later one, because --since is inclusive and --until is
// not. A window for "the half hour before 09:30" then holds nothing that
// happened at 09:30, and an hourly poll counts nothing twice.
//
// It moves the resolved instants rather than the flags, for the reason
// TestTheCutoffBoundsTheFeedAndNotOnlyTheSearch gives.
func TestUntilEndsTheWindowBeforeItsInstant(t *testing.T) {
	all, _, _ := runActivity(t, nil)
	rows := feedRows(all)
	if len(rows) < 3 {
		t.Fatalf("only %d rows; this asserts nothing about a bound", len(rows))
	}
	// Newest first, so the second row's instant leaves rows on both sides.
	edge := strings.Split(rows[1], "\t")[0]

	const fixture, query = "activity-recorded.cloud.json", "key = AGL-3"
	earlier, _, _ := runActivityIn(t, site.Cloud, fixture, query,
		activityWindow{since: farPast, until: edge})
	later, _, _ := runActivityIn(t, site.Cloud, fixture, query,
		activityWindow{since: edge})

	before, after := feedRows(earlier), feedRows(later)
	if len(before) == 0 || len(after) == 0 {
		t.Fatalf("windows meeting at %s split %d rows into %d and %d",
			edge, len(rows), len(before), len(after))
	}
	for _, row := range before {
		if at := strings.Split(row, "\t")[0]; at >= edge {
			t.Errorf("the window ending at %s kept an event at %s", edge, at)
		}
	}
	if split := slices.Concat(after, before); !slices.Equal(split, rows) {
		t.Errorf("the windows either side of %s are not the feed they split:\n"+
			"whole:\n%s\nsplit:\n%s", edge, strings.Join(rows, "\n"),
			strings.Join(split, "\n"))
	}
}

// TestEveryAcceptedUntilIsBoundedOrRefused is the --since table again, at the
// other end. An --until that ParseDate accepts and the event filter cannot
// resolve would be a window that only looked closed, so every form either
// becomes an instant or is refused by name.
func TestEveryAcceptedUntilIsBoundedOrRefused(t *testing.T) {
	for _, input := range []string{
		"-7d", "+30m", "2w", "-1M", "-2h", "-7D", "-4w 2d",
		"2026-08-10", "2026/08/10", "2026-08-10 00:00", "2026/08/10 13:45",
		"2026-08-10T13:45:00Z", "2026-08-10T13:45:00-05:00",
		"startOfWeek()", "endOfDay(-1)", "now()",
	} {
		inv, err := validateActivityWindow(t, "-3650d", input, accountDoer())
		until, _ := inv.Value(activityUntilKey).(string)
		switch jql.ClassifyDate(input) {
		case jql.DateRelative, jql.DateAbsolute, jql.DateInstant:
			if err != nil || until == "" {
				t.Errorf("--until %q names an instant and resolved to %q: %v",
					input, until, err)
			}
		case jql.DateFunction:
			if err == nil {
				t.Errorf("--until %q cannot be resolved here and was accepted as %q",
					input, until)
				continue
			}
			if e := errs.Coerce(err); e.Code != "UNBOUNDABLE_DATE" ||
				!strings.Contains(e.Message, "--until") {
				t.Errorf("--until %q refused as %s (%s), want UNBOUNDABLE_DATE "+
					"naming --until", input, e.Code, e.Message)
			}
		case jql.DateInvalid:
			t.Errorf("%q classifies as invalid; this table is stale", input)
		}
	}
}

// TestAWindowThatHoldsNoInstantIsRefused is --until at or before --since. Either
// would answer with an empty, complete feed, and an empty feed reads as
// "nothing happened" about a window nobody meant to ask about.
func TestAWindowThatHoldsNoInstantIsRefused(t *testing.T) {
	for _, tc := range []struct{ since, until string }{
		{"-1d", "-2d"},
		{"2026-08-10", "2026-08-10"},
		{"2026-08-10 09:30", "2026-08-10 09:00"},
	} {
		_, err := validateActivityWindow(t, tc.since, tc.until, accountDoer())
		if err == nil {
			t.Errorf("--since %s --until %s was accepted", tc.since, tc.until)
			continue
		}
		if e := errs.Coerce(err); e.Code != "EMPTY_WINDOW" || e.Exit != exitcode.Usage {
			t.Errorf("--since %s --until %s refused as %s at exit %v, want "+
				"EMPTY_WINDOW at exit 2", tc.since, tc.until, e.Code, e.Exit)
		}
	}
}

// TestAWindowOfTwoDatesAsksForTheZoneOnce is the price
// TestAnAbsoluteSinceCostsOneRequestAndAnOffsetCostsNone pins, for a window
// with a wall clock at each end. Both are read in the same account's zone, so
// the second costs nothing more.
func TestAWindowOfTwoDatesAsksForTheZoneOnce(t *testing.T) {
	doer := accountDoer()
	if _, err := validateActivityWindow(t, "2026-08-10", "2026-08-11", doer); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doer.calls != 1 {
		t.Errorf("a window of two dates made %d metadata requests, want 1", doer.calls)
	}
}

// TestAWrongEndIsRefusedBeforeTheZoneIsRead keeps a refusal free. A wall clock
// at one end costs a request for the account's zone, and a date function at
// the other is refused whatever that request says, so asking first would pay
// for an answer the caller cannot use.
func TestAWrongEndIsRefusedBeforeTheZoneIsRead(t *testing.T) {
	doer := accountDoer()
	_, err := validateActivityWindow(t, "2026-08-10", "startOfWeek()", doer)
	if err == nil || errs.Coerce(err).Code != "UNBOUNDABLE_DATE" {
		t.Fatalf("--until startOfWeek() was not refused as UNBOUNDABLE_DATE: %v", err)
	}
	if doer.calls != 0 {
		t.Errorf("a refused window made %d metadata requests first", doer.calls)
	}
}

// TestAnEmptyFeedNamesWhereItsWindowEnds is the EMPTY_RESULT frame. The
// instant --until became is on no envelope, and an empty answer is the one
// that cannot show it any other way.
func TestAnEmptyFeedNamesWhereItsWindowEnds(t *testing.T) {
	inv, err := validateActivityWindow(t, "-2h", "-1h", accountDoer())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	cmd, _ := registry.Lookup("issue.activity")
	until, _ := inv.Value(activityUntilKey).(string)
	if notes := cmd.EmptyFrame(inv); !slices.Contains(notes, "until="+until) {
		t.Errorf("the empty frame is %v, want until=%s among it", notes, until)
	}
}

// TestActivityOnDataCenterIsWholeAndStillSorted is the other deployment, and it
// is here for two reasons rather than symmetry.
//
// Data Center inlines every comment where Cloud caps at twenty, so this feed is
// complete where the Cloud one is not — the same command, the same flags, two
// honest and different answers. And its changelog arrives oldest-first where
// Cloud's arrives newest-first under the same expand, so a feed that inherited
// arrival order would be right on exactly one of them.
func TestActivityOnDataCenterIsWholeAndStillSorted(t *testing.T) {
	out, result, replayer := runActivityOn(t, site.DataCenter,
		"activity-recorded.datacenter.json", "key = ENG-1", farPast)

	if !result.Complete {
		t.Error("a feed with nothing clipped reported itself partial")
	}
	if got := replayer.Unplayed(); len(got) != 0 {
		t.Errorf("recorded requests nobody made: %v", got)
	}

	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")[1:]
	if len(rows) < 3 {
		t.Fatalf("only %d rows; this asserts nothing about ordering", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		prev := strings.Split(rows[i-1], "\t")[0]
		this := strings.Split(rows[i], "\t")[0]
		if this > prev {
			t.Fatalf("row %d is newer than the one before it, so the feed "+
				"inherited the server's order:\n%s", i, out)
		}
	}
}

// replayConnBudget is replayConn with a request budget, zero meaning none.
func replayConnBudget(
	t *testing.T, fixture string, budget int,
) (*transport.Client, *transport.Replayer) {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(),
		Retries: -1, MaxRequests: budget,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// runTwoPages is a feed over two candidates, one per page. Candidates arrive in
// issue-key order, which says nothing about when anything happened on them: the
// higher key, on page one, holds the older comment, and the lower key, on page
// two, holds the newer one. A budget of one request stops the walk after page
// one; zero means none.
func runTwoPages(
	t *testing.T, limit, budget int,
) (rows []string, out string, result registry.StreamResult) {
	t.Helper()
	out, result, replayer := runActivityOn(t, site.DataCenter,
		"activity-two-pages.datacenter.json", "key in (ENG-1, ENG-2)", farPast,
		func(f registry.Flags) {
			f.SetInt("page-size", 1)
			f.SetInt("limit", limit)
			f.SetInt("max-requests", budget)
		})
	if got := replayer.Unplayed(); budget == 0 && len(got) != 0 {
		t.Errorf("recorded requests nobody made: %v", got)
	}
	return strings.Split(strings.TrimRight(out, "\n"), "\n")[1:], out, result
}

// TestActivityIsNewestFirstAcrossPages is the ordering promise over more than
// one page. A feed sorted one page at a time is newest first inside each page
// and in key order between them, and every test above reads a single page.
func TestActivityIsNewestFirstAcrossPages(t *testing.T) {
	rows, out, result := runTwoPages(t, 0, 0)

	if !result.Complete {
		t.Error("a feed that read every candidate reported itself partial")
	}
	if result.Total != 0 {
		t.Errorf("a complete feed reported a total of %d; only a cut one has "+
			"anything to say it about", result.Total)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d:\n%s", len(rows), out)
	}
	if got := strings.Split(rows[0], "\t")[1]; got != "ENG-1" {
		t.Errorf("first row is %s's older event, not ENG-1's newer one, so "+
			"the feed is newest first only within a page:\n%s", got, out)
	}
}

// TestALimitedActivityFeedKeepsTheNewest is the same promise under --limit, and
// the one a caller cannot see is broken. The feed says it is incomplete either
// way, so exit 3 is right; what is wrong is which event it kept. Stopping once
// page one fills the limit keeps the older event and never shows the newer one.
func TestALimitedActivityFeedKeepsTheNewest(t *testing.T) {
	rows, out, result := runTwoPages(t, 1, 0)

	if result.Complete {
		t.Error("a feed cut at --limit reported itself complete")
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row under --limit 1, got %d:\n%s", len(rows), out)
	}
	if got := strings.Split(rows[0], "\t")[1]; got != "ENG-1" {
		t.Errorf("--limit 1 kept %s's older event over ENG-1's newer one:\n%s",
			got, out)
	}
	// Every candidate was read before the limit applied, so the feed knows
	// exactly what it cut.
	if result.Total != 2 {
		t.Errorf("Total = %d, want 2: the feed held two events and wrote one",
			result.Total)
	}
}

// TestABudgetCutFeedStatesNoTotal is the other side of that total. A walk the
// budget stopped left candidates unread, so however many events it holds, it
// cannot know how many the whole answer had: page two here holds one more.
func TestABudgetCutFeedStatesNoTotal(t *testing.T) {
	rows, out, result := runTwoPages(t, 0, 1)

	if result.Complete || result.StoppedBy != render.StopBudget {
		t.Fatalf("want a budget cut, got complete=%v stopped-by=%q:\n%s",
			result.Complete, result.StoppedBy, out)
	}
	if len(rows) != 1 {
		t.Fatalf("want page one's event, got %d rows:\n%s", len(rows), out)
	}
	if result.Total != 0 {
		t.Errorf("a budget cut stated a total of %d; page two was never read",
			result.Total)
	}
}

// TestTheCeilingIsTenPagesOfTheWalkTheSweepWillMake pins the boundary and the
// arithmetic under it.
//
// The ceiling is ten search pages, counted in the pages the sweep will really
// send. A cursor page and a keyset page each bring a full page of new rows, so a
// thousand candidates is ten. An offset page asks again for the row the last one
// ended on, and at the largest page size that leaves ninety-nine new rows, so
// the same thousand is eleven. That is the walk --all-projects makes on Data
// Center, which is the walk the report that raised this was on.
func TestTheCeilingIsTenPagesOfTheWalkTheSweepWillMake(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        site.Kind
		allProjects bool
		candidates  int
		refused     bool
	}{
		{"a keyset walk of ten pages", site.DataCenter, false, 1000, false},
		{"a keyset walk of eleven", site.DataCenter, false, 1001, true},
		{"an offset walk of the same thousand is eleven", site.DataCenter, true, 1000, true},
		{"an offset walk of ten", site.DataCenter, true, 991, false},
		{"a cursor walk of ten", site.Cloud, true, 1000, false},
		{"a cursor walk of eleven", site.Cloud, true, 1001, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.activity")
			if !ok {
				t.Fatal("issue activity is not registered")
			}
			flags := registry.NewFlags()
			flags.SetString("since", "-7d")
			flags.SetBool("all-projects", tc.allProjects)
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, kind: tc.kind,
					candidates: tc.candidates,
				},
				Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
			})
			switch {
			case tc.refused && errs.Coerce(err).Code != "SWEEP_TOO_LARGE":
				t.Errorf("%d candidates were not refused: %v", tc.candidates, err)
			case !tc.refused && err != nil:
				t.Errorf("%d candidates were refused: %v", tc.candidates, err)
			}
		})
	}
}
