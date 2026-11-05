package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// instantFieldJira is a Data Center site whose catalogue types two fields
// `datetime`, one `date` and one `string`, and whose one issue carries a value
// for each, spelled the way Data Center spells them.
//
// The datetime values are issue 277's: `2026-11-04T13:50:16.000-0500` is how
// Jira wrote the reporter's resolutiondate, in the server's zone and with no
// colon in the offset. Actual start is chosen to cross a day, 22:15 on the 3rd
// at -0500 being 03:15 on the 4th in UTC. Team holds the same text as
// resolutiondate and is typed a string, so it is the control: text that looks
// like a timestamp is still text.
//
// resolved is the resolutiondate the issue carries, so a test can serve one
// that does not parse.
func instantFieldJira(t *testing.T, resolved string) string {
	t.Helper()
	issue := `{"id":"1","key":"ENG-1","fields":{` +
		`"summary":"an issue resolved in the server's zone",` +
		`"status":{"name":"Closed","statusCategory":{"key":"done","name":"Done"}},` +
		`"created":"2026-11-04T10:37:37.000-0500",` +
		`"updated":"2026-11-04T13:50:16.000-0500",` +
		`"resolutiondate":"` + resolved + `",` +
		`"duedate":"2026-11-04",` +
		`"customfield_10008":"2026-11-03T22:15:00.000-0500",` +
		`"customfield_10050":"2026-11-04T13:50:16.000-0500"}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"name":"ada","displayName":"Ada Lovelace",` +
				`"timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-11-05T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[
				{"id":"resolutiondate","name":"Resolved","custom":false,
				 "schema":{"type":"datetime","system":"resolutiondate"}},
				{"id":"duedate","name":"Due Date","custom":false,
				 "schema":{"type":"date","system":"duedate"}},
				{"id":"customfield_10008","name":"Actual start","custom":true,
				 "schema":{"type":"datetime",
				  "custom":"com.atlassian.jira.plugin.system.customfieldtypes:datetime"}},
				{"id":"customfield_10050","name":"Team","custom":true,
				 "schema":{"type":"string"}}
			]`))
		case strings.Contains(r.URL.Path, "/search"):
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":1,"issues":[` +
				issue + `]}`))
		default:
			_, _ = w.Write([]byte(issue))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

var instantFieldFlags = []string{
	"--field", "Resolved", "--field", "Due Date",
	"--field", "Actual start", "--field", "Team",
}

// TestARequestedDatetimeFieldIsWrittenAsATimestamp is issue 277.
//
// A field requested with --field was written exactly as Jira sent it, so a row
// carried `updated` as `2026-11-04T18:50:16Z` and `resolutiondate`, the same
// instant, as `2026-11-04T13:50:16.000-0500`: not RFC 3339, in the server's
// zone, and not something a date flag would take back. A field the catalogue
// types `datetime` is now written as `created` and `updated` are.
//
// A `date` is left alone, because a calendar date has no zone, and a string is
// left alone whatever it holds, because nothing said it was an instant.
func TestARequestedDatetimeFieldIsWrittenAsATimestamp(t *testing.T) {
	url := instantFieldJira(t, "2026-11-04T13:50:16.000-0500")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	list := mustRun(t, env, append([]string{"issue", "list", "--format", "json"},
		instantFieldFlags...)...)
	get := mustRun(t, env, append([]string{"issue", "get", "ENG-1", "--format", "json"},
		instantFieldFlags...)...)

	for name, got := range map[string]string{"list": list.stdout, "get": get.stdout} {
		for _, want := range []string{
			`"text": "2026-11-04T18:50:16Z"`, // resolutiondate, the instant updated is
			`"text": "2026-11-04T03:15:00Z"`, // Actual start, a day on in UTC
			`"text": "2026-11-04"`,           // duedate, a date and not an instant
			// Team is a string, and a string is written as it arrived.
			`"text": "2026-11-04T13:50:16.000-0500"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: no %s in the output:\n%s", name, want, got)
			}
		}
		// The one raw spelling left is Team's. Before the fix there were two.
		if n := strings.Count(got, "-0500"); n != 1 {
			t.Errorf("%s: %d values in Jira's spelling, want only the string field's:\n%s",
				name, n, got)
		}
	}
}

// TestARequestedDatetimeFieldFollowsTheZone is the second half of issue 277:
// `--tz` moved `updated` and left `resolutiondate` in the server's zone, so
// one row named one instant in two zones. Tokyo has no daylight saving, so
// the expected offset does not depend on the date.
func TestARequestedDatetimeFieldFollowsTheZone(t *testing.T) {
	url := instantFieldJira(t, "2026-11-04T13:50:16.000-0500")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	tsv := mustRun(t, env, append([]string{"issue", "list", "--tz", "Asia/Tokyo"},
		instantFieldFlags...)...)
	lines := strings.Split(strings.TrimSpace(tsv.stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and one row:\n%s", tsv.stdout)
	}
	header, row := strings.Split(lines[0], "\t"), strings.Split(lines[1], "\t")
	cell := func(column string) string {
		for i, h := range header {
			if h == column && i < len(row) {
				return row[i]
			}
		}
		t.Fatalf("no %s column in %q", column, lines[0])
		return ""
	}
	for column, want := range map[string]string{
		"updated":           "2026-11-05T03:50:16+09:00",
		"resolutiondate":    "2026-11-05T03:50:16+09:00",
		"customfield_10008": "2026-11-04T12:15:00+09:00",
		"duedate":           "2026-11-04",
		"customfield_10050": "2026-11-04T13:50:16.000-0500",
	} {
		if got := cell(column); got != want {
			t.Errorf("%s = %q, want %q", column, got, want)
		}
	}

	xml := mustRun(t, env, append([]string{"issue", "get", "ENG-1", "--tz", "Asia/Tokyo"},
		instantFieldFlags...)...)
	if want := `<resolutiondate name="Resolved">2026-11-05T03:50:16+09:00</resolutiondate>`; !strings.Contains(xml.stdout, want) {
		t.Errorf("issue get: no %s in:\n%s", want, xml.stdout)
	}
}

// TestARequestedDatetimeFieldThatDoesNotParseIsRefused holds a requested
// datetime to the rule `created` is held to. Passing an unreadable value
// through would put a value in a timestamp's place that is not one, which is
// the defect this replaces, arriving by another route.
func TestARequestedDatetimeFieldThatDoesNotParseIsRefused(t *testing.T) {
	url := instantFieldJira(t, "yesterday afternoon")
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	got := run(t, env, "issue", "get", "ENG-1", "--field", "Resolved")
	if got.exit != exitcode.Remote {
		t.Fatalf("exit = %v, want %v\nstdout: %s\nstderr: %s",
			got.exit, exitcode.Remote, got.stdout, got.stderr)
	}
	for _, want := range []string{"MALFORMED_TIMESTAMP", "resolutiondate", "yesterday afternoon"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr does not name %s:\n%s", want, got.stderr)
		}
	}
	if got.stdout != "" {
		t.Errorf("a refused read wrote a result:\n%s", got.stdout)
	}
}
