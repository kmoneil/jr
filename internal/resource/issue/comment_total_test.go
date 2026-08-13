package issue_test

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestACommentListWithNoTotalKeepsAsking is the regression for a result that
// reported itself complete after one page of three.
//
// `total` decoded into a plain int, so a response without one decoded to zero,
// and the loop's `startAt >= total` was satisfied by the first non-empty page.
// One request, two of three comments on stdout, complete="true", exit 0: a
// truncation reported as completeness, which is the one thing this tool's
// output is not allowed to be.
//
// The cassette is built here rather than kept in testdata, and deliberately so.
// It encodes a server that does not send `total`, which no Jira does, so there
// is nothing to record and a file in the tree would read as a claim about an
// API. What it asserts is this client's arithmetic, and that is the whole of
// what it should assert.
func TestACommentListWithNoTotalKeepsAsking(t *testing.T) {
	conn, replayer := commentsWithoutTotal(t)

	out, result := runCommentListAgainst(t, conn, registry.Limit{All: true}, 2)

	if !result.Complete {
		t.Error("a list that reached the end of the thread was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the loop stopped early; these were never fetched: %v", unplayed)
	}

	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")[1:]
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want all three comments:\n%s", len(rows), out)
	}
	var ids []string
	for _, row := range rows {
		ids = append(ids, strings.SplitN(row, "\t", 2)[0])
	}
	if got := strings.Join(ids, ","); got != "10001,10002,10003" {
		t.Errorf("ids = %s, want 10001,10002,10003", got)
	}
}

// TestACommentListWithNoTotalIsNotCompleteWhenBounded is the other half. The
// caller's limit stops it, and a limit is not the server running out however
// little the server said.
func TestACommentListWithNoTotalIsNotCompleteWhenBounded(t *testing.T) {
	conn, _ := commentsWithoutTotal(t)

	out, result := runCommentListAgainst(t, conn, registry.Limit{N: 2}, 2)

	if result.Complete {
		t.Error("a list the caller's limit cut short was reported complete")
	}
	if rows := strings.Count(strings.TrimRight(out, "\n"), "\n"); rows != 2 {
		t.Errorf("got %d rows, want 2:\n%s", rows, out)
	}
}

// commentsWithoutTotal is three comments over two pages and then an empty one,
// from a server that never says how many there are.
//
// The empty third page is the point. With no total, the only thing that can end
// the loop is the server running out, so this costs one request more than a
// deployment that counts. That is the price of an honest answer and it is paid
// by nobody today.
func commentsWithoutTotal(t *testing.T) (*transport.Client, *transport.Replayer) {
	t.Helper()

	page := func(startAt int, ids ...string) transport.Interaction {
		comments := make([]string, 0, len(ids))
		for _, id := range ids {
			comments = append(comments, fmt.Sprintf(
				`{"id": %q, "author": {"name": "ada", "displayName": "Ada Lovelace"}, `+
					`"body": "a comment", "created": "2026-08-01T09:15:00.000+0000", `+
					`"updated": "2026-08-01T09:15:00.000+0000"}`, id))
		}
		return transport.Interaction{
			Request: transport.RecordedRequest{
				Method: "GET",
				Path:   "/rest/api/2/issue/ENG-101/comment",
				Query:  fmt.Sprintf("maxResults=2&orderBy=created&startAt=%d", startAt),
			},
			Response: transport.RecordedResponse{
				Status: 200,
				Header: map[string][]string{"Content-Type": {"application/json"}},
				// No "total", which is the whole fixture.
				Body: fmt.Sprintf(`{"startAt": %d, "maxResults": 2, "comments": [%s]}`,
					startAt, strings.Join(comments, ", ")),
			},
		}
	}

	cassette := &transport.Cassette{
		Deployment: transport.DataCenter,
		Source:     transport.Constructed,
		Interactions: []transport.Interaction{
			page(0, "10001", "10002"),
			page(2, "10003"),
			// startAt 3, not 4: the loop advances by what it received, and the
			// second page was short.
			page(3),
		},
	}

	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// runCommentListAgainst drives the registered command against a caller-supplied
// conversation. runCommentList in comment_test.go names a fixture file; this
// one takes the connection, because the conversation here is built in memory.
func runCommentListAgainst(
	t *testing.T, conn *transport.Client, limit registry.Limit, pageSize int,
) (string, registry.StreamResult) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.comment.list")
	if !ok {
		t.Fatal("issue comment list is not registered")
	}

	flags := registry.NewFlags()
	flags.SetInt("page-size", pageSize)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-101"}, Flags: flags, Limit: limit,
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
	return buf.String(), result
}
