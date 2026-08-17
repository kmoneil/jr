package issue_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// everyRow is the unbounded limit, which is what a poll normally runs with.
var everyRow = registry.Limit{All: true}

// The site's clock, and the window bound derived from it. Fixed, because a feed
// whose test moved with the wall clock would assert a different thing every run.
const (
	feedServerTime = "2026-08-17T09:30:00.000-0500" // 14:30:00Z.
	feedNow        = "2026-08-17T14:30:00Z"
)

// feedSave is one changelog entry as Jira sends it.
func feedSave(id, created, field, from, to string) string {
	return fmt.Sprintf(`{"id":%q,"created":%q,
		"author":{"accountId":"000000:aaa","displayName":"Ada Lovelace"},
		"items":[{"field":%q,"fieldtype":"jira","fromString":%q,"toString":%q}]}`,
		id, created, field, from, to)
}

// feedIssue is one search row carrying a changelog projection.
//
// total is what the server says the changelog holds, which is the number that
// decides whether the projection was clipped. Passing more than len(saves) is
// how a test says "Cloud sent forty of fifty-eight".
func feedIssue(key string, total int, saves ...string) string {
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"a row"},
		"changelog":{"startAt":0,"maxResults":%d,"total":%d,"histories":[%s]}}`,
		key, len(saves), total, strings.Join(saves, ","))
}

// feedServer answers the three requests one poll makes: the site's clock, the
// account's timezone, and the search.
//
// It is a stub rather than a cassette because what is under test is the window
// arithmetic and the cursor, not the shape of a Jira response — that shape is
// already evidenced by activity-recorded.cloud.json, whose changelog projection
// this mirrors, down to the bound it reports.
type feedServer struct {
	issues   []string
	searches int
	queries  []string
}

func (f *feedServer) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{}`
	switch {
	case strings.HasSuffix(r.URL.Path, "/serverInfo"):
		body = fmt.Sprintf(`{"deploymentType":"Cloud","version":"1001.0.0",
			"serverTime":%q}`, feedServerTime)
	case strings.Contains(r.URL.Path, "/search"):
		f.searches++
		f.queries = append(f.queries, r.URL.Query().Get("jql"))
		body = fmt.Sprintf(`{"issues":[%s],"isLast":true}`,
			strings.Join(f.issues, ","))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// runFeed drives `issue changes` and hands back the rendered document, so a test
// can read the envelope the cursor lives in.
func runFeed(
	t *testing.T, server *feedServer, format render.Format, limit registry.Limit,
	set ...func(registry.Flags),
) (string, registry.StreamResult) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.changes")
	if !ok {
		t.Fatal("issue changes is not registered")
	}
	conn, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: &http.Client{Transport: server},
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}

	flags := registry.NewFlags()
	flags.SetString("since", "-1h")
	for _, apply := range set {
		if apply != nil {
			apply(flags)
		}
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{
				body:   catalogueJSON,
				byPath: map[string]string{"myself": accountJSON},
			},
			conn: conn, kind: site.Cloud, unscoped: true,
		},
		Flags: flags, Limit: limit,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, format, render.StreamSpec{
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
	return buf.String(), result
}

// sinceTokenOf reads the cursor out of a JSON answer, which is where a poller
// reads it from.
func sinceTokenOf(t *testing.T, doc string) string {
	t.Helper()
	var envelope struct {
		Complete bool   `json:"complete"`
		Token    string `json:"next-since-token"`
	}
	if err := json.Unmarshal([]byte(doc), &envelope); err != nil {
		t.Fatalf("unmarshal %s: %v", doc, err)
	}
	return envelope.Token
}

// TestTheFeedBoundsItsWindowWithTheSiteClock is the whole argument for spending
// a request on /serverInfo. The changelog timestamps a window is compared
// against were written by the server, so a client running behind the site would
// claim to have reported through an instant the site had not reached, and
// anything written in between would never be reported and never asked for again.
func TestTheFeedBoundsItsWindowWithTheSiteClock(t *testing.T) {
	server := &feedServer{issues: []string{
		feedIssue("ENG-1", 2,
			feedSave("101", "2026-08-17T14:00:00.000+0000", "status", "To Do", "In Progress"),
			feedSave("102", "2026-08-17T14:29:59.000+0000", "summary", "old", "new")),
	}}

	doc, result := runFeed(t, server, render.JSON, everyRow)
	if !result.Complete {
		t.Fatalf("a whole window reported itself incomplete: %s", doc)
	}

	token := sinceTokenOf(t, doc)
	if token == "" {
		t.Fatal("a complete poll issued no cursor, so the next one cannot resume")
	}
	cursor, err := issue.DecodeChangeCursor(token, site.Cloud)
	if err != nil {
		t.Fatalf("the cursor it issued cannot be read back: %v", err)
	}
	got, ok := cursor.Instant()
	if !ok {
		t.Fatal("the cursor carries no instant")
	}
	want, err := time.Parse(time.RFC3339, feedNow)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("cursor = %s, want the site's clock %s (not this machine's)", got, want)
	}
}

// TestTheFeedReportsEachChangeExactlyOnceAcrossPolls drives the property the
// design exists for through the command rather than through the window: two
// polls, the second resuming from the first's cursor, over a changelog holding a
// bulk edit stamped with one timestamp.
func TestTheFeedReportsEachChangeExactlyOnceAcrossPolls(t *testing.T) {
	// Four saves at the same instant, which is what a bulk edit writes, plus one
	// either side of it.
	saves := []string{
		feedSave("100", "2026-08-17T14:00:00.000+0000", "status", "To Do", "In Progress"),
		feedSave("101", "2026-08-17T14:29:59.000+0000", "labels", "", "a"),
		feedSave("102", "2026-08-17T14:29:59.000+0000", "labels", "", "b"),
		feedSave("103", "2026-08-17T14:29:59.000+0000", "labels", "", "c"),
		feedSave("104", "2026-08-17T14:29:59.000+0000", "labels", "", "d"),
	}
	server := &feedServer{issues: []string{feedIssue("ENG-1", len(saves), saves...)}}

	first, result := runFeed(t, server, render.JSON, everyRow)
	if !result.Complete {
		t.Fatalf("first poll incomplete: %s", first)
	}
	token := sinceTokenOf(t, first)

	// The second poll sees the same server state: nothing new happened.
	second, result := runFeed(t, server, render.JSON, everyRow,
		func(f registry.Flags) { f.SetString("since", token) })
	if !result.Complete {
		t.Fatalf("second poll incomplete: %s", second)
	}

	counted := map[string]int{}
	for _, doc := range []string{first, second} {
		for _, id := range changeIDs(t, doc) {
			counted[id]++
		}
	}
	for _, save := range []string{"100", "101", "102", "103", "104"} {
		if counted[save] != 1 {
			t.Errorf("save %s was reported %d times across two polls, want exactly 1\n"+
				"first:  %s\nsecond: %s", save, counted[save], first, second)
		}
	}
}

// changeIDs reads the save ids out of a JSON answer.
func changeIDs(t *testing.T, doc string) []string {
	t.Helper()
	var envelope struct {
		Changes []struct {
			ID string `json:"id"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(doc), &envelope); err != nil {
		t.Fatalf("unmarshal %s: %v", doc, err)
	}
	out := make([]string, 0, len(envelope.Changes))
	for _, c := range envelope.Changes {
		out = append(out, c.ID)
	}
	return out
}

// TestTheFeedLeavesAChangeNewerThanItsWindowForTheNextPoll. The upper bound is
// the walk's start, so anything written while the walk was running belongs to
// the next answer. Without that, a change written after the walk passed its
// issue would be inside the reported window and outside the answer.
func TestTheFeedLeavesAChangeNewerThanItsWindowForTheNextPoll(t *testing.T) {
	server := &feedServer{issues: []string{
		feedIssue("ENG-1", 2,
			feedSave("200", "2026-08-17T14:29:00.000+0000", "status", "To Do", "Done"),
			// One second after the site clock this poll read.
			feedSave("201", "2026-08-17T14:30:01.000+0000", "summary", "a", "b")),
	}}

	doc, result := runFeed(t, server, render.JSON, everyRow)
	if !result.Complete {
		t.Fatalf("incomplete: %s", doc)
	}
	got := changeIDs(t, doc)
	if len(got) != 1 || got[0] != "200" {
		t.Errorf("reported %v, want only the save inside the window", got)
	}
}

// TestTheFeedIssuesNoCursorWhenItWasCutShort is the rule that keeps a truncated
// poll from losing what it did not report. Advancing the cursor past a window
// that was only partly reported is exactly how a feed drops a change and says
// nothing.
func TestTheFeedIssuesNoCursorWhenItWasCutShort(t *testing.T) {
	server := &feedServer{issues: []string{
		feedIssue("ENG-1", 3,
			feedSave("300", "2026-08-17T14:00:00.000+0000", "status", "To Do", "In Progress"),
			feedSave("301", "2026-08-17T14:10:00.000+0000", "labels", "", "a"),
			feedSave("302", "2026-08-17T14:20:00.000+0000", "summary", "a", "b")),
	}}

	// The limit is on the invocation and not on the flags: --limit is bound
	// before a command runs, and writeRows reads what it was bound to.
	doc, result := runFeed(t, server, render.JSON, registry.Limit{N: 1})

	if result.Complete {
		t.Error("a feed cut short by --limit reported itself complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a resume token was invented for merged rows: %q", result.NextPageToken)
	}
	if token := sinceTokenOf(t, doc); token != "" {
		t.Errorf("a truncated poll issued a cursor %q, so the next poll would "+
			"start after changes this one never reported", token)
	}
}

// TestTheFeedIssuesNoCursorWhenTheChangelogWasClipped is the same rule for the
// other way a poll can be short, and it is why this command could not be built
// before the clip was detectable: Cloud bounds the changelog projection at
// forty saves, and a feed that emitted what it got and advanced anyway would
// skip the rest for good.
func TestTheFeedIssuesNoCursorWhenTheChangelogWasClipped(t *testing.T) {
	server := &feedServer{issues: []string{
		// Two saves sent, fifty-eight held: the shape the recorded Cloud
		// cassette carries.
		feedIssue("ENG-1", 58,
			feedSave("400", "2026-08-17T14:00:00.000+0000", "status", "To Do", "Done"),
			feedSave("401", "2026-08-17T14:10:00.000+0000", "labels", "", "a")),
	}}

	doc, result := runFeed(t, server, render.JSON, everyRow)

	if result.Complete {
		t.Error("a poll over a clipped changelog reported itself complete")
	}
	if result.PartialElement != "change" {
		t.Errorf("PartialElement = %q, want change", result.PartialElement)
	}
	if token := sinceTokenOf(t, doc); token != "" {
		t.Errorf("a clipped poll issued a cursor %q, so the saves it could not "+
			"read would never be asked for again", token)
	}
}

// TestTheFeedSendsOneAbsoluteBoundForEveryPage. A relative offset would be
// re-evaluated against a later clock on each page of the walk, moving the bound
// forward mid-run and dropping whatever sat in the band it moved past. The bound
// is minted once, and it is a minute because that is the finest JQL expresses.
func TestTheFeedSendsOneAbsoluteBoundForEveryPage(t *testing.T) {
	server := &feedServer{issues: []string{
		feedIssue("ENG-1", 1,
			feedSave("500", "2026-08-17T14:00:00.000+0000", "status", "To Do", "Done")),
	}}

	if _, _ = runFeed(t, server, render.TSV, everyRow); server.searches == 0 {
		t.Fatal("no search was made")
	}
	query := server.queries[0]
	// 13:30Z is 08:30 in America/Chicago, which is the account's zone in the
	// stub, and the window opened an hour before the site's 14:30Z clock.
	if !strings.Contains(query, `updated >= "2026-08-17 08:30"`) {
		t.Errorf("query = %q, want an absolute minute bound in the account's zone", query)
	}
	if strings.Contains(query, "-1h") {
		t.Errorf("query = %q, carries the caller's offset rather than a bound "+
			"this poll minted; a relative bound moves between pages", query)
	}
}

// TestTheFeedRefusesACursorFromTheOtherDeployment. Two sites are two changelogs
// and two clocks, so replaying one's cursor against the other would report a
// window of somebody else's history as this site's.
func TestTheFeedRefusesACursorFromTheOtherDeployment(t *testing.T) {
	minted := issue.EncodeChangeCursor(
		issue.NewChangeCursor(site.DataCenter, time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)))
	server := &feedServer{issues: []string{feedIssue("ENG-1", 0)}}

	cmd, ok := registry.Lookup("issue.changes")
	if !ok {
		t.Fatal("issue changes is not registered")
	}
	conn, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: &http.Client{Transport: server},
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	flags := registry.NewFlags()
	flags.SetString("since", minted)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{
				body:   catalogueJSON,
				byPath: map[string]string{"myself": accountJSON},
			},
			conn: conn, kind: site.Cloud, unscoped: true,
		},
		Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	// It passes the shape check, because the shape is fine. What it cannot pass
	// is the site it is pointed at, and that needs the session.
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate refused a well-formed cursor: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.JSON, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	_, err = cmd.Stream(t.Context(), inv, stream)
	_ = wantCode(t, err, "INVALID_SINCE_TOKEN")
	if buf.String() != "" {
		t.Errorf("a refused poll wrote %q to stdout", buf.String())
	}
}
