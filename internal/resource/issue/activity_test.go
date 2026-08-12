package issue_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
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
	return runActivityOn(t, site.Cloud, fixture, "key = AGL-3", set)
}

func runActivityOn(
	t *testing.T, kind site.Kind, fixture, jql string, set ...func(registry.Flags),
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.activity")
	if !ok {
		t.Fatal("issue activity is not registered")
	}

	conn, replayer := replayConn(t, fixture)
	flags := registry.NewFlags()
	// The window the recording was made in. The cutoff is also applied to each
	// event, so a window that excluded the fixture's own timestamps would empty
	// the feed without failing anything.
	flags.SetString("since", "-1d")
	flags.SetString("jql", jql)
	flags.SetInt("page-size", 50)
	for _, apply := range set {
		if apply != nil {
			apply(flags)
		}
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: kind, unscoped: true,
		},
		Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Validate resolves --since against the clock, and the recording is fixed
	// in time. Overriding it here keeps the fixture from ageing out of its own
	// window tomorrow, which is the way a dated cassette usually breaks.
	inv.SetValue("issue.activity.since", "")

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
func TestActivityCutoffResolvesLocally(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for input, want := range map[string]string{
		"-7d":        "2026-08-05T12:00:00Z",
		"-1w":        "2026-08-05T12:00:00Z",
		"-2h":        "2026-08-12T10:00:00Z",
		"2026-01-01": "2026-01-01T00:00:00Z",
		// A date function is the server's to evaluate; the event filter then
		// bounds nothing, which is honest because the issues arrived narrowed.
		"startOfWeek()": "",
	} {
		if got := issue.ActivityCutoff(input, now); got != want {
			t.Errorf("ActivityCutoff(%q) = %q, want %q", input, got, want)
		}
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
		"activity-recorded.datacenter.json", "key = ENG-1")

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
