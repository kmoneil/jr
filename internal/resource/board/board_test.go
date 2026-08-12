package board_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/board"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

var deployments = []site.Kind{site.Cloud, site.DataCenter}

// TestListConvergesAcrossDeployments is why both fixtures exist. Cloud pages
// the listing and Data Center answers it whole — two different conversations a
// caller must not be able to tell apart.
//
// The project column is excluded from the comparison, and that exclusion is the
// finding rather than a concession. Data Center sends no location on any board,
// so it cannot name the project a board is on and Cloud can; the two outputs
// genuinely differ there, and it is the servers that differ. This test used to
// compare the whole row and pass, because the Data Center fixture had a
// location its author wrote and no Data Center has ever sent.
func TestListConvergesAcrossDeployments(t *testing.T) {
	got := map[site.Kind]string{}
	for _, kind := range deployments {
		out, result, replayer := runList(t, kind, "", registry.Limit{All: true})
		if !result.Complete {
			t.Errorf("%s: an exhausted list was reported incomplete", kind)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: a page was never fetched: %v", kind, unplayed)
		}
		got[kind] = out
	}

	cloud, dc := withoutProject(got[site.Cloud]), withoutProject(got[site.DataCenter])
	if cloud != dc {
		t.Errorf("the deployments disagree:\ncloud:\n%s\ndata center:\n%s", cloud, dc)
	}

	// The excluded column, asserted rather than waved past: filled on Cloud,
	// empty on every Data Center row. A Cloud regression that emptied it would
	// otherwise now be invisible here.
	if !strings.Contains(got[site.Cloud], "\tENG\n") {
		t.Errorf("cloud named no project:\n%s", got[site.Cloud])
	}
	for _, line := range strings.Split(strings.TrimRight(got[site.DataCenter], "\n"), "\n")[1:] {
		if cells := strings.Split(line, "\t"); cells[len(cells)-1] != "" {
			t.Errorf("data center named a project it was never sent: %q", line)
		}
	}
}

// withoutProject drops the project column, found by name in the header so that
// adding a column ahead of it does not silently start excluding a different one.
func withoutProject(tsv string) string {
	lines := strings.Split(strings.TrimRight(tsv, "\n"), "\n")
	drop := -1
	for i, header := range strings.Split(lines[0], "\t") {
		if header == "project" {
			drop = i
		}
	}
	if drop < 0 {
		return tsv
	}

	var out strings.Builder
	for _, line := range lines {
		cells := strings.Split(line, "\t")
		if drop < len(cells) {
			cells = append(cells[:drop:drop], cells[drop+1:]...)
		}
		out.WriteString(strings.Join(cells, "\t"))
		out.WriteByte('\n')
	}
	return out.String()
}

// TestBoardsSortNumerically is the issue-key lesson applied to board ids: 99
// sorts below 100 as a board and above it as a string. The fixture returns them
// out of order on purpose, so a sort that fell back to text fails here.
func TestBoardsSortNumerically(t *testing.T) {
	for _, kind := range deployments {
		out, _, _ := runList(t, kind, "", registry.Limit{All: true})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 4 {
			t.Fatalf("%s: got %d lines, want a header and three rows:\n%s",
				kind, len(lines), out)
		}
		if lines[0] != "id\tname\ttype\tproject" {
			t.Errorf("%s header = %q", kind, lines[0])
		}

		var ids []string
		for _, line := range lines[1:] {
			ids = append(ids, strings.SplitN(line, "\t", 2)[0])
		}
		if strings.Join(ids, ",") != "7,99,100" {
			t.Errorf("%s ids = %v, want 7,99,100 — a text sort puts 100 before 99",
				kind, ids)
		}
	}
}

// TestTheContextProjectScopesTheListing covers the default a configured caller
// gets. --project is global and it is a default, exactly as on issue list.
func TestTheContextProjectScopesTheListing(t *testing.T) {
	// The fixture only answers the scoped query, so a listing that ignored the
	// context would fail to match a recorded interaction rather than quietly
	// returning everything.
	out, result, replayer := runList(t, site.DataCenter, "ENG", registry.Limit{All: true})
	if !result.Complete {
		t.Error("a scoped listing was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the scoped request was never sent: %v", unplayed)
	}
	if n := strings.Count(out, "\n") - 1; n != 2 {
		t.Errorf("got %d rows, want the two ENG boards:\n%s", n, out)
	}
}

// TestAllProjectsLiftsTheScope covers the only way out of a context's project.
// An empty --project falls back to the context rather than clearing it, so
// without this a configured caller could not ask for every board on the site.
//
// It used to be spelled `--project all`, a magic value inside a normal flag —
// which meant a project genuinely keyed ALL could not be listed, and which
// `issue list` would have had to spell a second way.
func TestAllProjectsLiftsTheScope(t *testing.T) {
	// The unscoped fixture is the one that answers, which is the assertion:
	// the flag has to reach the client as no filter at all.
	_, _, replayer := runList(t, site.DataCenter, allProjects, registry.Limit{All: true})
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("--all-projects still scoped the query: %v", unplayed)
	}
}

// TestGetDefaultsToTheContextBoard is the context's --board finally being used.
func TestGetDefaultsToTheContextBoard(t *testing.T) {
	cmd, ok := registry.Lookup("board.get")
	if !ok {
		t.Fatal("board get is not registered")
	}

	for _, args := range [][]string{nil, {"99"}} {
		conn, replayer := replayConn(t, "board.datacenter.json")
		inv := &registry.Invocation{
			Jira: &stubSession{conn: conn, kind: site.DataCenter, board: "99"},
			Args: args, Flags: registry.NewFlags(),
			Stderr: io.Discard, Progress: registry.NoProgress,
		}
		doc, err := cmd.Run(t.Context(), inv)
		if err != nil {
			t.Fatalf("args=%v: %v", args, err)
		}
		if id, _ := doc.Record.AttrValue("id"); id != "99" {
			t.Errorf("args=%v: id = %q", args, id)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("args=%v: the board was never read: %v", args, unplayed)
		}
	}
}

// TestGetWithNoBoardAnywhereSaysSo covers the other half of that default: with
// no argument and no context board, the command names the flag rather than
// asking Jira for board "".
func TestGetWithNoBoardAnywhereSaysSo(t *testing.T) {
	cmd, _ := registry.Lookup("board.get")
	conn, _ := replayConn(t, "board.datacenter.json")
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter},
		Flags: registry.NewFlags(), Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if _, err := cmd.Run(t.Context(), inv); err == nil {
		t.Fatal("board get ran with no board anywhere")
	} else if code := errs.Coerce(err).Code; code != "NO_BOARD" {
		t.Errorf("code = %q, want NO_BOARD", code)
	}
}

// TestABoardOnAPersonHasNoProject covers a location that names a person rather
// than a project. An absent project key is reported as absent, not as an empty
// string that looks like an answer.
//
// It is a Cloud fixture because Cloud is the deployment that sends a location
// at all: the recorded Cloud boards carry one, and no Data Center probed sends
// one for any board, so the same file labelled datacenter was describing a
// response that deployment cannot produce. The comment it used to carry said
// this was what "Data Center allows and Cloud does not", which had the
// deployments the wrong way round.
func TestABoardOnAPersonHasNoProject(t *testing.T) {
	conn, _ := replayConn(t, "userboard.cloud.json")
	client := &board.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	got, err := client.Get(t.Context(), "5")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project != "" {
		t.Errorf("project = %q, want empty for a board located on a person", got.Project)
	}
	if _, ok := got.Node().ChildNamed("project"); ok {
		t.Error("an absent project was rendered as an element")
	}
}

// TestAnAgileRequestDoesNotGoToThePlatformAPI is the whole reason AgileBase
// exists. On Cloud the platform base is /rest/api/3, and a board path built
// from it is a 404 that reads like a board that is not there.
func TestAnAgileRequestDoesNotGoToThePlatformAPI(t *testing.T) {
	for _, kind := range deployments {
		conn, replayer := replayConn(t, "board."+string(kind)+".json")
		client := &board.Client{Transport: conn, Site: site.Info{Kind: kind}}
		if _, err := client.Get(t.Context(), "99"); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		// The cassette only answers /rest/agile/1.0/board/99, so a request
		// built from APIBase would never have matched.
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: the agile path was not used: %v", kind, unplayed)
		}
	}
}

// TestABoardIdIsDigits keeps a caller's argument out of a URL path as anything
// else, and makes a typo cost no round trip.
func TestABoardIdIsDigits(t *testing.T) {
	for _, id := range []string{"", "abc", "9 9", "42/../../admin", "-1"} {
		if err := board.ValidateID(id); err == nil {
			t.Errorf("%q was accepted as a board id", id)
		} else if code := errs.Coerce(err).Code; code != "INVALID_BOARD_ID" {
			t.Errorf("%q: code = %q, want INVALID_BOARD_ID", id, code)
		}
	}
	if err := board.ValidateID("42"); err != nil {
		t.Errorf("42 was refused: %v", err)
	}
}

// TestAnUnknownTypeIsRefusedBeforeTheRequest covers the flag the server would
// answer with an empty page and no complaint — indistinguishable from a site
// with no boards of that type.
func TestAnUnknownTypeIsRefusedBeforeTheRequest(t *testing.T) {
	cmd, _ := registry.Lookup("board.list")
	flags := registry.NewFlags()
	flags.SetString("type", "scrumban")
	inv := &registry.Invocation{Flags: flags, Progress: registry.NoProgress}

	err := cmd.Validate(t.Context(), inv)
	if err == nil {
		t.Fatal("an unknown board type was accepted")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_BOARD_TYPE" {
		t.Errorf("code = %q, want INVALID_BOARD_TYPE", code)
	}
	// Rejected in Validate rather than in the body, because a streaming command
	// has already written its header by the time the body runs.
	for _, ok := range []string{"scrum", "Kanban", "simple"} {
		f := registry.NewFlags()
		f.SetString("type", ok)
		if err := cmd.Validate(t.Context(), &registry.Invocation{
			Flags: f, Progress: registry.NoProgress,
		}); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// TestListTruncatesAndSaysSo covers the bound on a listing that arrives whole.
func TestListTruncatesAndSaysSo(t *testing.T) {
	_, result, _ := runList(t, site.DataCenter, "", registry.Limit{N: 1})
	if result.Complete {
		t.Error("a truncated board list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

// TestAMalformedResponseIsRefused covers the decode guard the reader carries. A
// login page is what a misconfigured site URL returns with status 200.
func TestAMalformedResponseIsRefused(t *testing.T) {
	conn, _ := replayConn(t, "html.datacenter.json")
	client := &board.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	if _, err := client.List(t.Context(), board.ListOptions{}); err == nil {
		t.Fatal("an HTML body was accepted as a board list")
	} else if code := errs.Coerce(err).Code; code != "MALFORMED_BOARDS" {
		t.Errorf("code = %q, want MALFORMED_BOARDS", code)
	}
}

// TestEveryBoardCommandIsAReadInEveryBuild keeps the resource out of the write
// tag: neither of these changes anything.
func TestEveryBoardCommandIsAReadInEveryBuild(t *testing.T) {
	for _, name := range []string{"board.list", "board.get"} {
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

// TestColumnsResolve keeps the default projection honest: a column whose path
// finds nothing renders as an empty cell, so nothing else would catch it.
func TestColumnsResolve(t *testing.T) {
	node := board.Board{ID: "42", Name: "ENG Scrum", Type: "scrum", Project: "ENG"}.Node()
	for _, col := range board.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}

	doc := board.ListDoc([]board.Board{{ID: "42", Name: "ENG Scrum"}}, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestCommandsWithoutASessionFailLoudly covers the guard each one carries.
func TestCommandsWithoutASessionFailLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("board.list")
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
		t.Error("board list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
}

// allProjects is the sentinel a test passes for "the caller lifted the scope",
// which the command spells as a flag rather than as a project.
const allProjects = "\x00all-projects"

// runList runs `board list` against a cassette chosen by the scope: no project
// and --all-projects both use the unscoped recording, and a project key uses
// the scoped one.
func runList(
	t *testing.T, kind site.Kind, project string, limit registry.Limit,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("board.list")
	if !ok {
		t.Fatal("board list is not registered")
	}

	flags := registry.NewFlags()
	if project == allProjects {
		flags.SetBool("all-projects", true)
		project = "ENG" // a context project the flag must override.
	}

	fixture := "boards." + string(kind) + ".json"
	if project != "" && !flags.Bool("all-projects") {
		fixture = "scoped." + string(kind) + ".json"
	}
	conn, replayer := replayConn(t, fixture)
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: kind, project: project},
		Flags: flags, Limit: limit,
		Stderr: io.Discard, Progress: registry.NoProgress,
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

// replayConn builds a transport backed by a recorded conversation.
func replayConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
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

// stubSession is a registry.Session over a recorded conversation, so a command
// runs with no auth, no config, and no network.
type stubSession struct {
	fields  []string
	conn    *transport.Client
	kind    site.Kind
	project string
	board   string
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, site.Info{Kind: s.kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Info: site.Info{Kind: s.kind}}, nil
}

func (s *stubSession) Idempotency() *idem.Ledger  { return nil }
func (s *stubSession) Project() string            { return s.project }
func (s *stubSession) Board() string              { return s.board }
func (s *stubSession) CheckWritable(string) error { return nil }

func (s *stubSession) RequireProject() (string, error) {
	if s.project == "" {
		return "", errs.Usage("NO_PROJECT", "this command needs a project and none is set")
	}
	return s.project, nil
}

// Fields is the context default field set. Empty here: a stub that
// invented one would make every resource test assert a request nobody made.
func (s *stubSession) Fields() []string { return s.fields }

func (s *stubSession) RequireBoard() (string, error) {
	if s.board == "" {
		return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
	}
	return s.board, nil
}
