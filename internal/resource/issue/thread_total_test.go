package issue_test

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestARecordThreadWithNoTotalSaysItDoesNotKnow is `issue get --with-comments`
// against a server that returns comments and no count.
//
// It used to write total="0" beside two real comments and complete="true" at
// exit 0, because the count was an int and an absent one decoded to zero, so
// `count >= total` held for any thread. Two comments may be the whole
// discussion or the first two of four hundred, and this command deliberately
// fetches one bounded page: there is no second request that would settle it.
// So it says nothing about the length and reports itself partial.
func TestARecordThreadWithNoTotalSaysItDoesNotKnow(t *testing.T) {
	conn, replayer := threadWithoutTotal(t)
	doc := runGetWithCommentsAgainst(t, conn)
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the comments were never fetched: %v", unplayed)
	}

	comments, ok := doc.Record.ChildNamed("comments")
	if !ok {
		t.Fatal("the record carries no comments")
	}
	if total, has := comments.AttrValue("total"); has {
		t.Errorf("total = %q on a thread whose length nobody reported; the "+
			"attribute is optional so that it can be left off", total)
	}
	if count, _ := comments.AttrValue("count"); count != "2" {
		t.Errorf("count = %q, want 2: the comments themselves are not in doubt", count)
	}
	if complete, _ := comments.AttrValue(render.CompleteAttr); complete != "false" {
		t.Errorf("complete = %q, want false: an unknown length is not a known end",
			complete)
	}
	if doc.IsComplete() {
		t.Fatal("a record holding a thread of unknown length reports itself complete")
	}

	// doc.IsComplete drives the exit, and the warning beside it has to name the
	// part that is in doubt: "this issue is partial" does not say which.
	var warning strings.Builder
	if err := render.WriteTruncationWarning(&warning, doc, render.XML); err != nil {
		t.Fatalf("warning: %v", err)
	}
	for _, want := range []string{"RESULT_TRUNCATED", "comments", "issue.get"} {
		if !strings.Contains(warning.String(), want) {
			t.Errorf("the warning does not mention %q:\n%s", want, warning.String())
		}
	}
}

// TestARowThreadWithNoTotalSaysItDoesNotKnow is the same defect on the other
// path, which reaches it by a different route and cannot ask again either.
//
// A row's thread arrives as a projection inside the search response, so there
// is no comment request to page. The whole listing is therefore incomplete,
// with the warning naming `comments` rather than offering --limit, because
// raising a bound that was never reached would fetch no further comment.
func TestARowThreadWithNoTotalSaysItDoesNotKnow(t *testing.T) {
	conn, replayer := rowThreadWithoutTotal(t)
	out, result := runListWithCommentsAgainst(t, conn)
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Fatalf("asked for something outside the cassette: %v", unmatched)
	}

	if result.Complete {
		t.Error("a row holding a thread of unknown length was reported complete")
	}
	if result.PartialElement != "comments" {
		t.Errorf("PartialElement = %q, want comments", result.PartialElement)
	}
	if result.NextPageToken != "" {
		t.Errorf("a resume token was invented for a thread nothing can page: %q",
			result.NextPageToken)
	}
	if strings.Contains(out, `total=`) {
		t.Errorf("the container wrote a total nobody reported:\n%s", out)
	}
	if !strings.Contains(out, `<comments count="2" complete="false">`) {
		t.Errorf("the container does not say it holds part of something:\n%s", out)
	}
}

// TestAKnownThreadStillCarriesItsTotal is the control, and it is the assertion
// that keeps the fix from being "never write the attribute".
//
// Every Jira observed sends a count, so the recorded cassettes exercise this
// branch and the two tests above exercise the one nothing can record. Both have
// to hold, or the optional attribute is optional in the wrong direction.
func TestAKnownThreadStillCarriesItsTotal(t *testing.T) {
	doc, _ := runGetWithComments(t, "get-comments.datacenter.json", true)

	comments, ok := doc.Record.ChildNamed("comments")
	if !ok {
		t.Fatal("the record carries no comments")
	}
	if total, has := comments.AttrValue("total"); !has || total != "2" {
		t.Errorf("total = %q (present %v), want 2: a count the server gave is "+
			"still published", total, has)
	}
	if !doc.IsComplete() {
		t.Error("a thread known to be whole reports itself partial")
	}
}

// threadWithoutTotal is the recorded `issue get --with-comments` conversation
// with one field removed from the comment page: the count.
//
// The requests come from the recording so they match what the command actually
// sends, and only the response is written here. No Jira omits `total`, so there
// is nothing to record and a file in testdata would read as a claim about an
// API rather than about this client's arithmetic.
func threadWithoutTotal(t *testing.T) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette := recordedShape(t, "get-comments.datacenter.json")
	cassette.Interactions[1].Response.Body = fmt.Sprintf(
		`{"startAt": 0, "maxResults": 50, "comments": [%s, %s]}`,
		comment("10000", "Reproduced on 9.4."), comment("10001", "And again on 10.4."))
	return replayFrom(t, cassette)
}

// rowThreadWithoutTotal is the same subtraction one level in: the projection a
// search returns, with its count taken out.
func rowThreadWithoutTotal(t *testing.T) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette := recordedShape(t, "with-comments-recorded.datacenter.json")
	cassette.Interactions[0].Response.Body = fmt.Sprintf(`{
		"startAt": 0, "maxResults": 50, "total": 1,
		"issues": [{"id": "10101", "key": "ENG-101", "fields": {
			"summary": "Search returns 500 for a key with a hyphen",
			"status": {"name": "Open", "statusCategory": {"key": "new"}},
			"issuetype": {"name": "Bug"},
			"project": {"key": "ENG"},
			"created": "2026-08-01T09:00:00.000+0000",
			"updated": "2026-08-01T09:15:00.000+0000",
			"labels": [],
			"comment": {"startAt": 0, "maxResults": 50, "comments": [%s, %s]}
		}}]
	}`, comment("10000", "Reproduced on 9.4."), comment("10001", "And again on 10.4."))
	return replayFrom(t, cassette)
}

// comment is one comment in the shape both deployments send.
func comment(id, body string) string {
	return fmt.Sprintf(
		`{"id": %q, "author": {"name": "ada", "displayName": "Ada Lovelace"}, `+
			`"body": %q, "created": "2026-08-01T09:15:00.000+0000", `+
			`"updated": "2026-08-01T09:15:00.000+0000"}`, id, body)
}

// recordedShape loads a cassette and marks it constructed, because what comes
// back from here is about to have a response rewritten and is no longer
// evidence of what a server sent.
func recordedShape(t *testing.T, fixture string) *transport.Cassette {
	t.Helper()

	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	cassette.Source = transport.Constructed
	return cassette
}

func replayFrom(
	t *testing.T, cassette *transport.Cassette,
) (*transport.Client, *transport.Replayer) {
	t.Helper()

	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// runGetWithCommentsAgainst is runGetWithComments over a caller-supplied
// conversation rather than a fixture name, the way runCommentListAgainst is.
func runGetWithCommentsAgainst(t *testing.T, conn *transport.Client) *render.Doc {
	t.Helper()

	cmd, ok := registry.Lookup("issue.get")
	if !ok {
		t.Fatal("issue get is not registered")
	}

	flags := registry.NewFlags()
	flags.SetBool("with-comments", true)
	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: []string{"ENG-101"},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return doc
}

// runListWithCommentsAgainst is runListIn over a caller-supplied conversation,
// in XML because the assertion is about the container's attributes.
func runListWithCommentsAgainst(
	t *testing.T, conn *transport.Client,
) (string, registry.StreamResult) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	flags := registry.NewFlags()
	flags.SetBool("with-comments", true)
	flags.SetInt("page-size", 50)
	flags.SetString("project", "ENG")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.XML, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
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

// TestAWorklogProjectionWithNoTotalIsToppedUp is the same defect on the third
// path, where it is not an attribute at all: it is a request that does not
// happen.
//
// `issue activity` gets each issue's worklog as a projection, and both
// deployments inline the *oldest* twenty, which for a feed about recent work is
// the wrong twenty. WorkComplete decides whether to refetch, and with an int
// total an absent one read as zero and every projection looked whole. So a
// server that sends no count made the top-up look unnecessary and the feed
// reported itself complete while missing entries. The assertion is the request:
// the cassette holds it, and the run has to reach for it.
func TestAWorklogProjectionWithNoTotalIsToppedUp(t *testing.T) {
	conn, replayer := worklogProjectionWithoutTotal(t)
	_, result := runActivityAgainstConn(t, conn)

	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the worklog was never refetched, so the feed was built from "+
			"a projection nobody said was whole: %v", unplayed)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Fatalf("asked for something outside the cassette: %v", unmatched)
	}
	// The top-up says no more than the projection did, so what is missing is
	// still missing and the feed says so rather than counting the refetch as an
	// answer.
	if result.Complete {
		t.Error("a feed whose worklog length nobody reported was called complete")
	}
	if result.PartialElement != "event" {
		t.Errorf("PartialElement = %q, want event: the rows that could not be "+
			"read were never rendered, so no container in the document is short",
			result.PartialElement)
	}
}

// worklogProjectionWithoutTotal is one issue whose worklog arrives with no
// count, and a top-up that does not supply one either.
func worklogProjectionWithoutTotal(t *testing.T) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette := recordedShape(t, "activity-recorded.cloud.json")
	cassette.Interactions[0].Response.Body = fmt.Sprintf(`{
		"isLast": true,
		"issues": [{"id": "10020", "key": "AGL-3", "fields": {
			"summary": "Board filter drops subtasks",
			"status": {"name": "In Progress", "statusCategory": {"key": "indeterminate"}},
			"issuetype": {"name": "Task"},
			"project": {"key": "AGL"},
			"created": "2026-08-12T09:00:00.000-0500",
			"updated": "2026-08-12T12:29:57.025-0500",
			"labels": [],
			"worklog": {"startAt": 0, "maxResults": 20, "worklogs": [%s]}
		}}]
	}`, worklogEntry("10024", "2h"))
	cassette.Interactions = append(cassette.Interactions, transport.Interaction{
		Request: transport.RecordedRequest{
			Method: "GET",
			Path:   "/rest/api/3/issue/AGL-3/worklog",
			Query:  fmt.Sprintf("maxResults=%d&startAt=0", issue.MaxPageSize),
		},
		Response: transport.RecordedResponse{
			Status: 200,
			Header: map[string][]string{"Content-Type": {"application/json"}},
			// Still no total. A refetch is not an answer by itself.
			Body: fmt.Sprintf(`{"startAt": 0, "maxResults": %d, "worklogs": [%s]}`,
				issue.MaxPageSize, worklogEntry("10024", "2h")),
		},
	})
	return replayFrom(t, cassette)
}

// worklogEntry is one worklog in the shape Cloud sends, trimmed to the fields
// this package reads.
func worklogEntry(id, spent string) string {
	return fmt.Sprintf(
		`{"id": %q, "author": {"accountId": "000000:00000000-0000-0000-0000-000000000000", `+
			`"displayName": "Ada Lovelace"}, "timeSpent": %q, "timeSpentSeconds": 7200, `+
			`"started": "2026-08-12T12:29:56.775-0500", `+
			`"created": "2026-08-12T12:29:57.025-0500", `+
			`"updated": "2026-08-12T12:29:57.025-0500"}`, id, spent)
}

// runActivityAgainstConn is runActivityOn over a caller-supplied conversation.
func runActivityAgainstConn(
	t *testing.T, conn *transport.Client,
) (string, registry.StreamResult) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.activity")
	if !ok {
		t.Fatal("issue activity is not registered")
	}

	flags := registry.NewFlags()
	flags.SetString("since", "-1d")
	flags.SetString("jql", "key = AGL-3")
	flags.SetInt("page-size", 50)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: site.Cloud, unscoped: true,
		},
		Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// As in runActivityOn: the conversation is fixed in time and the window
	// would otherwise age out of itself tomorrow.
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
	return buf.String(), result
}
