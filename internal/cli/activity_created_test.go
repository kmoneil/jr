package cli_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// createdSince bounds the feed by the fixture's own dates, for the reason
// fixtureSince gives.
const createdSince = "2026-11-04"

// createdJira is a Data Center site with three issues, each a different answer
// to "who created this":
//
//   - ENG-2 was created by ada inside the window and is reported by grace, the
//     case issue 278 reported: filed for somebody else, then given an epic.
//   - ENG-3 was created by grace inside the window and is reported by ada, so
//     a feed that matched the reporter would call it ada's.
//   - ENG-1 was created by ada before the window and commented on inside it,
//     so its creation is not an event in the window and its comment is.
//
// It keeps the fields parameter of every search, because whether `creator`
// is asked for is part of what is under test.
type createdJira struct {
	url    string
	mu     sync.Mutex
	fields []string
}

func (s *createdJira) searched() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.fields)
}

const (
	createdAda   = `{"name":"ada","key":"JIRAUSER10000","displayName":"Ada Lovelace"}`
	createdGrace = `{"name":"grace","key":"JIRAUSER10100","displayName":"Grace Hopper"}`
	// Every issue carries the three projections, whole, so nothing is topped
	// up and the feed is complete.
	createdNoWork = `"worklog":{"startAt":0,"maxResults":20,"total":0,"worklogs":[]}`
)

const createdIssues = `{"startAt":0,"maxResults":50,"total":3,"issues":[
{"id":"3","key":"ENG-3","fields":{
  "summary":"filed by grace, reported by ada",
  "status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},
  "creator":` + createdGrace + `,"reporter":` + createdAda + `,
  "created":"2026-11-04T19:05:00.000+0000","updated":"2026-11-04T19:05:00.000+0000",
  "comment":{"startAt":0,"maxResults":20,"total":0,"comments":[]},` + createdNoWork + `},
 "changelog":{"startAt":0,"maxResults":40,"total":0,"histories":[]}},
{"id":"2","key":"ENG-2","fields":{
  "summary":"filed by ada for grace",
  "status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},
  "creator":` + createdAda + `,"reporter":` + createdGrace + `,
  "created":"2026-11-04T19:12:22.000+0000","updated":"2026-11-04T19:18:51.000+0000",
  "comment":{"startAt":0,"maxResults":20,"total":0,"comments":[]},` + createdNoWork + `},
 "changelog":{"startAt":0,"maxResults":40,"total":1,"histories":[
   {"id":"7","created":"2026-11-04T19:18:51.000+0000","author":` + createdAda + `,
    "items":[{"field":"Epic Link","fromString":null,"toString":"ENG-9"}]}]}},
{"id":"1","key":"ENG-1","fields":{
  "summary":"filed by ada last month",
  "status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},
  "creator":` + createdAda + `,"reporter":` + createdAda + `,
  "created":"2026-10-01T09:00:00.000+0000","updated":"2026-11-04T20:00:00.000+0000",
  "comment":{"startAt":0,"maxResults":20,"total":1,"comments":[
    {"id":"9","author":` + createdAda + `,"created":"2026-11-04T20:00:00.000+0000",
     "updated":"2026-11-04T20:00:00.000+0000","body":"still happening"}]},` + createdNoWork + `},
 "changelog":{"startAt":0,"maxResults":40,"total":0,"histories":[]}}
]}`

func newCreatedJira(t *testing.T) *createdJira {
	t.Helper()
	s := &createdJira{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			_, _ = w.Write([]byte(`{"name":"ada","key":"JIRAUSER10000",` +
				`"displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server",` +
				`"serverTime":"2026-11-05T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/search"):
			s.mu.Lock()
			s.fields = append(s.fields, r.URL.Query().Get("fields"))
			s.mu.Unlock()
			_, _ = w.Write([]byte(createdIssues))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// activityRows runs the feed and returns each row as issue, kind and author,
// which is all these tests are about.
func activityRows(t *testing.T, s *createdJira, args ...string) []string {
	t.Helper()
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", s.url, "--project", "ENG")
	got := mustRun(t, env, append([]string{
		"issue", "activity", "--since", createdSince, "--format", "tsv",
	}, args...)...)

	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	header := strings.Split(lines[0], "\t")
	col := func(name string) int {
		i := slices.Index(header, name)
		if i < 0 {
			t.Fatalf("no %s column in %q", name, lines[0])
		}
		return i
	}
	issueAt, kindAt, authorAt := col("issue"), col("kind"), col("author")
	var rows []string
	for _, line := range lines[1:] {
		cells := strings.Split(line, "\t")
		rows = append(rows, cells[issueAt]+" "+cells[kindAt]+" "+cells[authorAt])
	}
	return rows
}

// TestCreatingAnIssueIsAnEventOfItsCreator is issue 278.
//
// `issue activity --user currentUser` merged comments, transitions, field
// changes and worklogs, and creating an issue was none of the four. An issue
// the caller filed for somebody else showed up as one unrelated field change,
// "set an epic link on a colleague's ticket", rather than "filed it". The
// changelog records no creation on either deployment, so the event is the
// issue's own created timestamp, attributed to its creator.
//
// Creator and not reporter, which is the half of this a fixture with one user
// cannot show: ENG-3 is reported by ada and was filed by grace, and it is not
// ada's creation.
func TestCreatingAnIssueIsAnEventOfItsCreator(t *testing.T) {
	s := newCreatedJira(t)

	got := activityRows(t, s, "--user", "currentUser")
	want := []string{
		"ENG-1 comment Ada Lovelace",
		"ENG-2 field Ada Lovelace",
		"ENG-2 created Ada Lovelace",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ada's feed is\n%s\nwant\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Unfiltered, every creation inside the window is an event, each its own
	// creator's, and ENG-1's, a month before the window, is not.
	all := activityRows(t, s)
	for _, row := range []string{"ENG-3 created Grace Hopper", "ENG-2 created Ada Lovelace"} {
		if !slices.Contains(all, row) {
			t.Errorf("no %q in the unfiltered feed:\n%s", row, strings.Join(all, "\n"))
		}
	}
	for _, row := range all {
		if strings.HasPrefix(row, "ENG-1 created") {
			t.Errorf("an issue created before the window was reported: %q", row)
		}
	}
}

// TestCreatorIsFetchedOnlyForACreatedEvent holds the cost. `creator` is one
// more field on every candidate, and a feed that did not ask for creations has
// no use for it, so --kind without `created` sends the field set it always did.
// --kind created alone asks for no projection at all, since a creation is read
// off the issue itself.
func TestCreatorIsFetchedOnlyForACreatedEvent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		creator bool
		rows    []string
	}{
		{"every kind, by default", nil, true, nil},
		{"created alone", []string{"--kind", "created"}, true, []string{
			"ENG-2 created Ada Lovelace", "ENG-3 created Grace Hopper",
		}},
		{"the four that are not created", []string{
			"--kind", "comment", "--kind", "transition", "--kind", "field", "--kind", "worklog",
		}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCreatedJira(t)
			got := activityRows(t, s, tc.args...)
			if tc.rows != nil && !slices.Equal(got, tc.rows) {
				t.Errorf("rows are\n%s\nwant\n%s",
					strings.Join(got, "\n"), strings.Join(tc.rows, "\n"))
			}
			// The candidate count is a search too, and fetches no fields at
			// all. The searches that fetch rows are the ones that name some.
			var pages []string
			for _, fields := range s.searched() {
				if fields != "" {
					pages = append(pages, fields)
				}
			}
			if len(pages) == 0 {
				t.Fatal("the feed fetched no rows, so this asserts nothing")
			}
			for _, fields := range pages {
				asked := slices.Contains(strings.Split(fields, ","), "creator")
				if asked != tc.creator {
					t.Errorf("fields=%s: creator asked for is %v, want %v",
						fields, asked, tc.creator)
				}
			}
		})
	}
}
