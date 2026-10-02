package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// resolvedDatesKey mirrors where Validate leaves the minute each instant
// became, as activitySinceKey does for the window.
const resolvedDatesKey = "issue.dates"

// validateList runs issue list's own Validate with these flags, against an
// account on America/Chicago, and returns the invocation and what it said on
// stderr.
func validateList(
	t *testing.T, kind site.Kind, doer *stubDoer, flags map[string]string,
) (*registry.Invocation, string, error) {
	t.Helper()
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	f := registry.NewFlags()
	for name, value := range flags {
		f.SetString(name, value)
	}
	var stderr strings.Builder
	inv := &registry.Invocation{
		Jira:  &stubSession{project: "ENG", kind: kind, metaClient: doer},
		Flags: f, Limit: registry.Limit{N: 50}, Format: render.TSV,
		Stderr: &stderr, Progress: registry.NoProgress,
	}
	err := cmd.Validate(t.Context(), inv)
	return inv, stderr.String(), err
}

// TestAnInstantOnADateFlagIsSentAsTheAccountsMinute is issue 213. Every
// timestamp this tool prints is RFC 3339, and every date flag refused it, so
// "find the moment, then ask what happened around it" needed a conversion by
// hand into the account's zone, where a wrong one is a complete, exit-0 answer
// for the wrong window.
//
// 09:00:30Z is 04:00:30 in Chicago in May. The start of a window moves down to
// 04:00 and the end up to 04:01, on every date flag, so the window sent holds
// the one asked for.
func TestAnInstantOnADateFlagIsSentAsTheAccountsMinute(t *testing.T) {
	for _, tc := range []struct{ flag, clause string }{
		{"created-after", `created >= "2026-05-12 04:00"`},
		{"created-before", `created <= "2026-05-12 04:01"`},
		{"updated-after", `updated >= "2026-05-12 04:00"`},
		{"updated-before", `updated <= "2026-05-12 04:01"`},
		{"changed-after", `AFTER "2026-05-12 04:00"`},
		{"changed-before", `BEFORE "2026-05-12 04:01"`},
		{"worklog-after", `worklogDate >= "2026-05-12 04:00"`},
		{"worklog-before", `worklogDate <= "2026-05-12 04:01"`},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			inv, stderr, err := validateList(t, site.Cloud, accountDoer(),
				map[string]string{tc.flag: "2026-05-12T09:00:30Z"})
			if err != nil {
				t.Fatalf("an instant was refused: %v", err)
			}
			query, err := issue.BuildQuery(issue.ListQueryFor(inv))
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !strings.Contains(query, tc.clause) {
				t.Errorf("query = %s\nwant it to carry %s", query, tc.clause)
			}
			if strings.Contains(query, "T09:00") {
				t.Errorf("query = %s\ncarries the instant, which JQL refuses", query)
			}
			// The window sent is wider than the one asked for, by thirty
			// seconds, and nothing in the rows says so.
			for _, want := range []string{issue.DateRoundedCode, "--" + tc.flag, "30s"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q\nwant it to name %s", stderr, want)
				}
			}
		})
	}
}

// TestAnInstantOnTheMinuteIsSentExactlyAndNotWarnedAbout is the instant that
// needs no rounding. It becomes the account's minute, which names the same
// instant, so there is nothing to report.
func TestAnInstantOnTheMinuteIsSentExactlyAndNotWarnedAbout(t *testing.T) {
	inv, stderr, err := validateList(t, site.Cloud, accountDoer(), map[string]string{
		"updated-after":  "2026-05-12T09:00:00Z",
		"updated-before": "2026-05-12T12:00:00+02:00",
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	query, err := issue.BuildQuery(issue.ListQueryFor(inv))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{
		`updated >= "2026-05-12 04:00"`, `updated <= "2026-05-12 05:00"`,
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query = %s\nwant %s", query, want)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q\nwant nothing said about an instant sent exactly", stderr)
	}
}

// TestInstantsCostOneRequestForTheZoneAndOtherDatesNone is the price of reading
// a minute in the right zone. Two instants are read in the same account's zone,
// so the second costs nothing more, and a command with no instant pays nothing.
func TestInstantsCostOneRequestForTheZoneAndOtherDatesNone(t *testing.T) {
	for _, tc := range []struct {
		flags map[string]string
		want  int
	}{
		{map[string]string{"updated-after": "-7d", "created-before": "2026-05-12"}, 0},
		{map[string]string{
			"updated-after": "2026-05-12T09:00:30Z", "created-before": "2026-05-13T00:00:00Z",
		}, 1},
	} {
		doer := accountDoer()
		if _, _, err := validateList(t, site.Cloud, doer, tc.flags); err != nil {
			t.Fatalf("%v: %v", tc.flags, err)
		}
		if doer.calls != tc.want {
			t.Errorf("%v made %d requests, want %d", tc.flags, doer.calls, tc.want)
		}
	}
}

// TestAnInstantWorklogBoundIsRefusedOnDataCenter keeps an instant inside the
// rule an absolute date already follows. Data Center reads worklogDate to the
// day and refuses a time of day on it, and an instant always reaches Jira as a
// minute. It is refused before the zone is read, because the refusal does not
// depend on it.
func TestAnInstantWorklogBoundIsRefusedOnDataCenter(t *testing.T) {
	doer := accountDoer()
	_, _, err := validateList(t, site.DataCenter, doer,
		map[string]string{"worklog-after": "2026-05-12T09:00:00Z"})
	if code := errs.Coerce(err).Code; code != "INVALID_DATE" {
		t.Fatalf("refused as %q (%v), want INVALID_DATE", code, err)
	}
	if doer.calls != 0 {
		t.Errorf("made %d requests before refusing", doer.calls)
	}
}

// TestAnInstantSinceBoundsTheEventsExactlyAndTheSearchByTheMinute is the same
// instant on issue activity, where it does two jobs. The search carries it as
// the account's minute, rounded down, because that is all JQL can read; each
// event is compared to the instant itself, so the rounding widens the search
// and moves nothing in the answer, and nothing is warned about.
func TestAnInstantSinceBoundsTheEventsExactlyAndTheSearchByTheMinute(t *testing.T) {
	inv, err := validateActivityWindow(t, "2026-05-12T09:00:30Z", "2026-05-12T10:15:45+01:00",
		accountDoer())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	since, _ := inv.Value(activitySinceKey).(string)
	until, _ := inv.Value(activityUntilKey).(string)
	if since != "2026-05-12T09:00:30Z" || until != "2026-05-12T09:15:45Z" {
		t.Errorf("window = [%s, %s), want the instants typed, to the second", since, until)
	}
	literals, _ := inv.Value(resolvedDatesKey).(map[string]string)
	if got, want := literals["since"], "2026-05-12 04:00"; got != want {
		t.Errorf("the search carries --since as %q, want %q", got, want)
	}
	if _, ok := literals["until"]; ok {
		t.Error("--until reached the search, which it does not bound")
	}
}

// TestAnInstantSinceAndADateUntilAskForTheZoneOnce is the price
// TestAWindowOfTwoDatesAsksForTheZoneOnce pins, for a window whose start is an
// instant: the search needs the zone for one and the filter for the other, and
// it is one account's zone.
func TestAnInstantSinceAndADateUntilAskForTheZoneOnce(t *testing.T) {
	doer := accountDoer()
	if _, err := validateActivityWindow(t, "2026-05-12T09:00:30Z", "2026-05-13", doer); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doer.calls != 1 {
		t.Errorf("made %d metadata requests, want 1", doer.calls)
	}
}

// TestEveryDateFormIsSentAsSomethingJQLReads holds the list of forms a date flag
// takes to what reaches the query. An instant is the first form whose text the
// query cannot carry as typed, so this asserts that no accepted form arrives in
// a sent query in a shape JQL refuses, by classifying what was sent.
func TestEveryDateFormIsSentAsSomethingJQLReads(t *testing.T) {
	for _, input := range []string{
		"-7d", "2026-05-12", "2026-05-12 09:00", "startOfDay()",
		"2026-05-12T09:00:00Z", "2026-05-12T09:00Z", "2026-05-12T11:00:00.250+02:00",
	} {
		inv, _, err := validateList(t, site.Cloud, accountDoer(),
			map[string]string{"updated-after": input})
		if err != nil {
			t.Fatalf("--updated-after %q: %v", input, err)
		}
		sent := issue.ListQueryFor(inv).UpdatedAfter
		if kind := jql.ClassifyDate(sent); kind == jql.DateInstant || kind == jql.DateInvalid {
			t.Errorf("--updated-after %q reaches the query as %q", input, sent)
		}
	}
}
