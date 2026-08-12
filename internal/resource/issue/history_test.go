package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestHistoryFlattensASaveIntoOneRowPerField is the shape decision, asserted
// rather than described. Jira records a save as one entry holding every field
// that moved together, and this reports one row per field carrying the entry's
// id, so a consumer can group them again.
//
// Driven against the Data Center recording, where entry 10123 is a real save
// that moved priority and labels at once.
func TestHistoryFlattensASaveIntoOneRowPerField(t *testing.T) {
	out, result, _ := runHistory(t, site.DataCenter, registry.Limit{All: true}, 0)

	if !result.Complete {
		t.Error("a whole history was reported incomplete")
	}
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")[1:]
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want the 6 items the recording holds:\n%s", len(rows), out)
	}

	var priority, labels bool
	for _, row := range rows {
		switch strings.Split(row, "\t")[2] {
		case "priority":
			priority = true
		case "labels":
			labels = true
		}
	}
	if !priority || !labels {
		t.Errorf("the two fields of one save did not both produce a row:\n%s", out)
	}
}

// TestAClearedFieldHasNoNewValueAtAll is the "absent, not defaulted" rule where
// it is easiest to get wrong. Unassigning an issue sends `to` and `toString`
// both null, which is not the same fact as assigning it to the empty string,
// and an element holding "" would publish the second.
func TestAClearedFieldHasNoNewValueAtAll(t *testing.T) {
	doc := historyDoc(t, site.DataCenter)

	last := doc.Collection.Items[len(doc.Collection.Items)-1]
	if _, ok := last.ChildNamed("from"); !ok {
		t.Error("the change that cleared the assignee lost its previous value")
	}
	if to, ok := last.ChildNamed("to"); ok {
		t.Errorf("a cleared field published a new value: %+v", to)
	}
}

// TestHistoryPagesBySaveAndNotByRow is the one thing the Cloud path does that
// the shared offset helper could not.
//
// The offset counts saves and a row is one flattened item, so a loop advancing
// by what it wrote would ask for startAt=3 after a first page of two saves and
// three items — skipping a save and reporting the result complete. The cassette
// answers startAt=2 and nothing else, so getting this wrong is a replay miss
// rather than a subtly short answer.
func TestHistoryPagesBySaveAndNotByRow(t *testing.T) {
	out, result, replayer := runHistory(t, site.Cloud, registry.Limit{All: true}, 2)

	if !result.Complete {
		t.Error("an exhausted history was reported incomplete")
	}
	if n := len(replayer.Unplayed()); n != 0 {
		t.Errorf("%d interactions went unplayed; the second page was not asked for",
			n)
	}
	if rows := strings.Count(strings.TrimRight(out, "\n"), "\n"); rows != 4 {
		t.Errorf("got %d rows, want the 4 items across 3 saves:\n%s", rows, out)
	}
}

// TestHistoryTruncatesAndSaysSo is the exit-3 proof this command declares.
//
// Run twice, because the two deployments reach the bound down different code
// paths: Cloud stops mid-page inside the paging loop, and Data Center holds the
// whole history already and is bounded while writing it.
func TestHistoryTruncatesAndSaysSo(t *testing.T) {
	for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
		t.Run(string(kind), func(t *testing.T) {
			out, result, _ := runHistory(t, kind, registry.Limit{N: 2}, 2)

			if result.Complete {
				t.Error("a truncated history was reported complete")
			}
			if result.NextPageToken != "" {
				t.Errorf("a page token was invented for an offset-paged "+
					"endpoint: %q", result.NextPageToken)
			}
			if rows := strings.Count(strings.TrimRight(out, "\n"), "\n"); rows != 2 {
				t.Errorf("got %d rows, want 2:\n%s", rows, out)
			}
		})
	}
}

// TestDataCenterAsksTheIssueAndCloudAsksTheChangelog pins the split that makes
// this command two implementations.
//
// Data Center answers 404 on /issue/{key}/changelog — measured against 9.12 —
// and serves the history under expand on the issue instead. Sending the Cloud
// request to Data Center would fail every invocation with a NOT_FOUND naming a
// route that is correct on the other deployment, which is the least debuggable
// error this tool could produce.
// The assertion is the replay itself. Each cassette holds one deployment's
// requests and nothing else, so asking for the other one's route produces an
// unmatched request rather than a wrong answer, and a route that was recorded
// and never asked for shows up as unplayed. Requiring both to be empty pins the
// split in the direction that matters: neither deployment may drift onto the
// other's endpoint without this failing.
func TestDataCenterAsksTheIssueAndCloudAsksTheChangelog(t *testing.T) {
	for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
		t.Run(string(kind), func(t *testing.T) {
			_, _, replayer := runHistory(t, kind, registry.Limit{All: true}, 2)
			if got := replayer.Unmatched(); len(got) != 0 {
				t.Errorf("asked for something this deployment does not serve: %v", got)
			}
			if got := replayer.Unplayed(); len(got) != 0 {
				t.Errorf("recorded requests nobody made: %v", got)
			}
		})
	}
}

// TestABadHistoryKeyIsRefusedLocally keeps a typo from costing a round trip,
// and keeps a caller's argument from reaching a URL path unchecked.
func TestABadHistoryKeyIsRefusedLocally(t *testing.T) {
	cmd, ok := registry.Lookup("issue.history")
	if !ok {
		t.Fatal("issue history is not registered")
	}
	for _, bad := range []string{"", "nonsense", "ENG", "../../admin"} {
		inv := &registry.Invocation{Args: []string{bad}, Flags: registry.NewFlags()}
		if bad == "" {
			inv.Args = nil
		}
		err := cmd.Validate(t.Context(), inv)
		if err == nil {
			t.Errorf("%q was accepted", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

// TestHistoryIsInEveryBuild follows the rule reading always has: no write tag,
// so a reader binary can answer why an issue is where it is.
func TestHistoryIsInEveryBuild(t *testing.T) {
	cmd, ok := registry.Lookup("issue.history")
	if !ok {
		t.Fatal("issue history is not registered")
	}
	if len(cmd.RequiresTags) != 0 {
		t.Errorf("issue history requires %v; reading needs no tag", cmd.RequiresTags)
	}
	if cmd.Mutating {
		t.Error("issue history is marked mutating")
	}
}

// runHistory drives the registered command against a recorded conversation.
//
// The two deployments use different fixtures and different issue keys because
// one of them is a real recording of a real issue and the other is not.
func runHistory(
	t *testing.T, kind site.Kind, limit registry.Limit, pageSize int,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.history")
	if !ok {
		t.Fatal("issue history is not registered")
	}

	fixture, key := "history.cloud.json", "ENG-101"
	if kind != site.Cloud {
		fixture, key = "history-recorded.datacenter.json", "ENG-2"
	}
	conn, replayer := replayConn(t, fixture)

	flags := registry.NewFlags()
	if pageSize > 0 {
		flags.SetInt("page-size", pageSize)
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
		},
		Args: []string{key}, Flags: flags, Limit: limit,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
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

// historyDoc reads one page directly, for the assertions about rendering rather
// than about paging.
func historyDoc(t *testing.T, kind site.Kind) *render.Doc {
	t.Helper()

	fixture, key := "history.cloud.json", "ENG-101"
	if kind != site.Cloud {
		fixture, key = "history-recorded.datacenter.json", "ENG-2"
	}
	conn, _ := replayConn(t, fixture)
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}
	page, err := client.ListHistory(t.Context(), key, 0, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	doc := issue.HistoryDoc(page.Changes, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc
}
