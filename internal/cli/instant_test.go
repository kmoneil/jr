package cli_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// TestAnInstantIsSentAsTheAccountsMinute is issue 213 through the command line.
// Every timestamp jr prints is RFC 3339 in UTC, and pasting one back into a
// date flag was INVALID_DATE, so the caller converted it by hand into the
// account's zone, where a wrong conversion is a complete answer for the wrong
// window.
//
// The account here is on Europe/Berlin, UTC+2 in May. An instant with seconds
// is widened to the minute on the side that keeps the window whole, and the
// warning says so; one on the minute is sent exactly, and nothing is said.
func TestAnInstantIsSentAsTheAccountsMinute(t *testing.T) {
	var jql atomic.Value
	url := framedJiraIn(t, &jql, true, "Europe/Berlin")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "list",
		"--updated-after", "2026-05-12T09:00:30Z",
		"--updated-before", "2026-05-12T10:00:00Z")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	sent, _ := jql.Load().(string)
	for _, want := range []string{
		`updated >= "2026-05-12 11:00"`, `updated <= "2026-05-12 12:00"`,
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("sent %s\nwant it to carry %s", sent, want)
		}
	}
	for _, want := range []string{"DATE_ROUNDED", "--updated-after", "2026-05-12T09:00:00Z"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %s\nwant it to name %s", got.stderr, want)
		}
	}
	if strings.Contains(got.stderr, "--updated-before") {
		t.Errorf("stderr = %s\nwarned about an instant sent exactly", got.stderr)
	}
}

// TestAnInstantSinceIsComparedExactly is the same paste on issue activity. The
// search carries the minute below, because that is all JQL reads, and the
// window the events are held to is the instant itself, which is what an empty
// answer reports it was computed over. Nothing in the answer moved, so nothing
// is warned about.
func TestAnInstantSinceIsComparedExactly(t *testing.T) {
	var jql atomic.Value
	url := framedJiraIn(t, &jql, false, "Europe/Berlin")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "activity", "--since", "2026-05-12T09:00:30Z")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if sent, _ := jql.Load().(string); !strings.Contains(sent, `updated >= "2026-05-12 11:00"`) {
		t.Errorf("sent %s\nwant the search bounded at the minute below", sent)
	}
	if !strings.Contains(got.stderr, "since=2026-05-12T09:00:30Z") {
		t.Errorf("stderr = %s\nwant the empty answer framed by the instant typed", got.stderr)
	}
	if strings.Contains(got.stderr, "DATE_ROUNDED") {
		t.Errorf("stderr = %s\nwarned about a bound that moved nothing in the answer", got.stderr)
	}
}
