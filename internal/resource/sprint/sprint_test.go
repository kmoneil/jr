package sprint_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/sprint"
	"github.com/kmoneil/jira-cli/internal/site"
)

// TestListConvergesAcrossDeployments is why both fixtures exist. Cloud pages the
// listing and Data Center answers it whole, and — the part that bites — they
// write their timestamps differently. A caller must not be able to tell.
func TestListConvergesAcrossDeployments(t *testing.T) {
	got := map[site.Kind]string{}
	for _, kind := range deployments {
		out, result, replayer := runList(t, kind, "sprints", nil, registry.Limit{All: true})
		if !result.Complete {
			t.Errorf("%s: an exhausted list was reported incomplete", kind)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: a page was never fetched: %v", kind, unplayed)
		}
		got[kind] = out
	}
	if got[site.Cloud] != got[site.DataCenter] {
		t.Errorf("the deployments disagree:\ncloud:\n%s\ndata center:\n%s",
			got[site.Cloud], got[site.DataCenter])
	}
}

// TestTheTwoDeploymentsWriteTimeDifferently is the reason there is a layout
// list rather than a call to time.Parse with time.RFC3339.
//
// Data Center writes the offset without a colon — 2026-07-01T09:00:00.000+0000
// — which RFC 3339 refuses outright. Cloud writes Z. Parsing only one of them
// would report every date on the other deployment as an error.
func TestTheTwoDeploymentsWriteTimeDifferently(t *testing.T) {
	for _, kind := range deployments {
		conn, _ := replayConn(t, "sprint."+string(kind)+".json")
		client := &sprint.Client{Transport: conn, Site: site.Info{Kind: kind}}

		got, err := client.Get(t.Context(), "100")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got.Start != "2026-07-01T09:00:00Z" {
			t.Errorf("%s start = %q, want it normalized to RFC 3339 UTC", kind, got.Start)
		}
		if got.End != "2026-07-15T09:00:00Z" {
			t.Errorf("%s end = %q", kind, got.End)
		}
		// An active sprint has not completed. Empty is the honest answer; a zero
		// time would compare as the year 1 rather than as absent.
		if got.Completed != "" {
			t.Errorf("%s completed = %q, want empty for a running sprint", kind, got.Completed)
		}
	}
}

// TestATimestampThatCannotBeParsedIsRefused covers the other half of that: a
// shape this tool does not know is an error, not a value passed through. Passing
// it through would make the output format depend on the server, and a consumer
// would break on a row rather than on a request.
func TestATimestampThatCannotBeParsedIsRefused(t *testing.T) {
	conn, _ := replayConn(t, "badtime.datacenter.json")
	client := &sprint.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	if _, err := client.List(t.Context(), "99", nil); err == nil {
		t.Fatal("an unparseable timestamp was accepted")
	} else if code := errs.Coerce(err).Code; code != "MALFORMED_TIMESTAMP" {
		t.Errorf("code = %q, want MALFORMED_TIMESTAMP", code)
	}
}

// TestSprintsSortNumerically is the issue-key lesson again: 99 is below 100 as
// a sprint and above it as a string. The fixtures return them out of order.
func TestSprintsSortNumerically(t *testing.T) {
	for _, kind := range deployments {
		out, _, _ := runList(t, kind, "sprints", nil, registry.Limit{All: true})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if lines[0] != "id\tname\tstate\tstart\tend" {
			t.Errorf("%s header = %q", kind, lines[0])
		}
		var ids []string
		for _, line := range lines[1:] {
			ids = append(ids, strings.SplitN(line, "\t", 2)[0])
		}
		if strings.Join(ids, ",") != "12,99,100" {
			t.Errorf("%s ids = %v, want 12,99,100 — a text sort puts 100 before 99",
				kind, ids)
		}
	}
}

// TestRepeatedStateFlagsBecomeOneParameter covers the shape mismatch between
// the flag rule and the API: flags repeat, and the agile API takes the set as
// one comma-separated value.
func TestRepeatedStateFlagsBecomeOneParameter(t *testing.T) {
	// The fixture only answers state=active,future, so a client that sent two
	// separate parameters — or dropped one — would miss it.
	_, _, replayer := runList(t, site.DataCenter, "states",
		[]string{"active", "future"}, registry.Limit{All: true})
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the states were not sent as one parameter: %v", unplayed)
	}
}

// TestAnUnknownStateIsRefusedBeforeTheRequest covers the flag the server would
// answer with an empty page and no complaint.
func TestAnUnknownStateIsRefusedBeforeTheRequest(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.list")
	if !ok {
		t.Fatal("sprint list is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("state", "started")

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{kind: site.DataCenter, board: "99"}, Flags: flags,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("an unknown sprint state was accepted")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_SPRINT_STATE" {
		t.Errorf("code = %q, want INVALID_SPRINT_STATE", code)
	}
}

// TestTheBoardIsRequiredBeforeTheHeaderGoesOut is the streaming rule in
// practice. A sprint listing is a question about one board, and a rejection
// from inside the body would arrive after the TSV header was on stdout.
func TestTheBoardIsRequiredBeforeTheHeaderGoesOut(t *testing.T) {
	cmd, _ := registry.Lookup("sprint.list")
	if cmd.Validate == nil {
		t.Fatal("sprint list does not validate, so the board is checked too late")
	}
	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{kind: site.DataCenter}, Flags: registry.NewFlags(),
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("sprint list validated with no board anywhere")
	}
	if code := errs.Coerce(err).Code; code != "NO_BOARD" {
		t.Errorf("code = %q, want NO_BOARD", code)
	}
}

// TestAKanbanBoardIsRefusedByName covers the one refusal this endpoint has that
// a caller can act on. The server's 400 says "check the request", which sends
// somebody looking at their flags instead of at the board.
func TestAKanbanBoardIsRefusedByName(t *testing.T) {
	conn, _ := replayConn(t, "kanban.datacenter.json")
	client := &sprint.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	_, err := client.List(t.Context(), "7", nil)
	if err == nil {
		t.Fatal("a board with no sprints returned a listing")
	}
	e := errs.Coerce(err)
	if e.Code != "SPRINTS_REFUSED" {
		t.Fatalf("code = %q, want SPRINTS_REFUSED", e.Code)
	}
	if !strings.Contains(e.Message, "7") {
		t.Errorf("message = %q, want it to name the board", e.Message)
	}
	if !strings.Contains(e.Remedy, "scrum") {
		t.Errorf("remedy = %q, want it to name the likely cause", e.Remedy)
	}
	// The server's own message survives as the detail, so this offers the cause
	// without asserting it.
	if !strings.Contains(e.Detail, "does not support sprints") {
		t.Errorf("detail = %q, want the server's message kept", e.Detail)
	}
}

// TestSprintAndBoardIdsAreDigits keeps a caller's argument out of a URL path as
// anything else, and makes a typo cost no round trip.
func TestSprintAndBoardIdsAreDigits(t *testing.T) {
	for _, id := range []string{"", "abc", "1 2", "100/../../admin", "-1"} {
		if err := sprint.ValidateID(id); err == nil {
			t.Errorf("%q was accepted as a sprint id", id)
		} else if code := errs.Coerce(err).Code; code != "INVALID_SPRINT_ID" {
			t.Errorf("%q: code = %q, want INVALID_SPRINT_ID", id, code)
		}
		if err := sprint.ValidateBoardID(id); err == nil {
			t.Errorf("%q was accepted as a board id", id)
		}
	}
	if err := sprint.ValidateID("100"); err != nil {
		t.Errorf("100 was refused: %v", err)
	}
}

// TestGetNeedsNoBoard covers the asymmetry with the listing: a sprint id
// addresses a sprint on its own, whichever board it came from.
func TestGetNeedsNoBoard(t *testing.T) {
	cmd, ok := registry.Lookup("sprint.get")
	if !ok {
		t.Fatal("sprint get is not registered")
	}
	conn, replayer := replayConn(t, "sprint.datacenter.json")
	inv := &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: []string{"100"},
		Flags: registry.NewFlags(), Progress: registry.NoProgress,
	}

	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "100" {
		t.Errorf("id = %q", id)
	}
	if board, _ := doc.Record.AttrValue("board"); board != "99" {
		t.Errorf("board = %q, want the board the sprint came from", board)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the sprint was never read: %v", unplayed)
	}
}

// TestTheReadVerbsNeedNoTag keeps listing and fetching in every build. Only
// closing a sprint is administration.
func TestTheReadVerbsNeedNoTag(t *testing.T) {
	for _, name := range []string{"sprint.list", "sprint.get"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if cmd.Mutating || cmd.Destructive {
			t.Errorf("%s is a read but declares otherwise", name)
		}
		if len(cmd.RequiresTags) != 0 {
			t.Errorf("%s requires %v; reading needs no tag", name, cmd.RequiresTags)
		}
	}
}

// TestListTruncatesAndSaysSo covers the bound on a listing that arrives whole.
func TestListTruncatesAndSaysSo(t *testing.T) {
	_, result, _ := runList(t, site.DataCenter, "sprints", nil, registry.Limit{N: 1})
	if result.Complete {
		t.Error("a truncated sprint list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

// TestColumnsResolve keeps the default projection honest: a column whose path
// finds nothing renders as an empty cell, so nothing else would catch it.
func TestColumnsResolve(t *testing.T) {
	node := sprint.Sprint{
		ID: "100", Name: "ENG Sprint 4", State: "active",
		Start: "2026-07-01T09:00:00Z", End: "2026-07-15T09:00:00Z",
	}.Node()
	for _, col := range sprint.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}

	doc := sprint.ListDoc([]sprint.Sprint{{ID: "100", Name: "ENG Sprint 4"}}, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestCommandsWithoutASessionFailLoudly covers the guard each one carries.
func TestCommandsWithoutASessionFailLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("sprint.list")
	inv := &registry.Invocation{
		Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
		Progress: registry.NoProgress,
	}
	stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := cmd.Stream(t.Context(), inv, stream); err == nil {
		t.Error("sprint list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
	// Validate refuses first, which is what matters for a streaming command:
	// by the time Stream runs, the header is already on stdout.
	if err := cmd.Validate(t.Context(), inv); err == nil {
		t.Error("sprint list validated without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("validate code = %q, want NO_SESSION", code)
	}
}
