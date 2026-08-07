package epic_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/epic"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

var deployments = []site.Kind{site.Cloud, site.DataCenter}

// TestListConvergesAcrossDeployments is why both fixtures exist. Cloud pages the
// listing and Data Center answers it whole — a caller must not be able to tell.
func TestListConvergesAcrossDeployments(t *testing.T) {
	got := map[site.Kind]string{}
	for _, kind := range deployments {
		out, result, replayer := runList(t, kind, "epics", "", registry.Limit{All: true})
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

// TestNameAndSummaryAreBothReported is the field pair that would otherwise make
// two views of one epic disagree. The board shows the name; a JQL search shows
// the summary. They are different fields and they differ here on purpose.
func TestNameAndSummaryAreBothReported(t *testing.T) {
	for _, kind := range deployments {
		conn, _ := replayConn(t, "epic."+string(kind)+".json")
		client := &epic.Client{Transport: conn, Site: site.Info{Kind: kind}}

		got, err := client.Get(t.Context(), "ENG-42")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got.Name != "Retry path" {
			t.Errorf("%s name = %q", kind, got.Name)
		}
		if got.Summary != "Make the transport retry safely" {
			t.Errorf("%s summary = %q", kind, got.Summary)
		}
		if got.Name == got.Summary {
			t.Errorf("%s: name and summary collapsed into one value", kind)
		}
		if got.Color != "color_4" {
			t.Errorf("%s color = %q, want the swatch the board draws", kind, got.Color)
		}
	}
}

// TestAnEpicIsAddressableByKeyOrID covers what the agile API accepts. A key is
// what a person has; an id is what a listing reports.
func TestAnEpicIsAddressableByKeyOrID(t *testing.T) {
	cmd, ok := registry.Lookup("epic.get")
	if !ok {
		t.Fatal("epic get is not registered")
	}
	for _, ref := range []string{"ENG-42", "10101"} {
		conn, _ := replayConn(t, "epic.datacenter.json")
		doc, err := cmd.Run(t.Context(), &registry.Invocation{
			Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: []string{ref},
			Flags: registry.NewFlags(), Progress: registry.NoProgress,
		})
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if key, _ := doc.Record.AttrValue("key"); key != "ENG-42" {
			t.Errorf("%s: key = %q", ref, key)
		}
	}
}

// TestEpicsSortByIDNotByKey pins the ordering decision and the reason for it.
//
// ENG-1000 is above ENG-7 as an issue and below it as a string, and ordering by
// key would need the parsing that lives in the issue resource — which this one
// may not import. Ordering by id sidesteps that entirely and is creation order.
func TestEpicsSortByIDNotByKey(t *testing.T) {
	for _, kind := range deployments {
		out, _, _ := runList(t, kind, "epics", "", registry.Limit{All: true})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if lines[0] != "key\tname\tsummary\tdone" {
			t.Errorf("%s header = %q", kind, lines[0])
		}
		var keys []string
		for _, line := range lines[1:] {
			keys = append(keys, strings.SplitN(line, "\t", 2)[0])
		}
		// Id order: 10099, 10101, 10200 — which is ENG-7, ENG-42, ENG-1000.
		if strings.Join(keys, ",") != "ENG-7,ENG-42,ENG-1000" {
			t.Errorf("%s keys = %v, want them in id order", kind, keys)
		}
	}
}

// TestDoneIsAnEnumNotABareFlag covers the filter's three states. A bare --done
// would default to false and silently hide every finished epic from a caller
// who never passed it.
func TestDoneIsAnEnumNotABareFlag(t *testing.T) {
	cmd, ok := registry.Lookup("epic.list")
	if !ok {
		t.Fatal("epic list is not registered")
	}
	for _, f := range cmd.Flags {
		if f.Name != "done" {
			continue
		}
		if f.Type != registry.TypeEnum {
			t.Errorf("--done is %q; a bool cannot express \"no filter\"", f.Type)
		}
		if f.Default != "" {
			t.Errorf("--done defaults to %q, which hides half the epics", f.Default)
		}
	}

	// Omitting it sends no parameter at all, which the unfiltered fixture is
	// the proof of: it records a query with no done= in it.
	_, _, replayer := runList(t, site.DataCenter, "epics", "", registry.Limit{All: true})
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("an omitted --done still filtered: %v", unplayed)
	}

	// And passing it does narrow, which is the other half of "a flag either
	// affects the output or does not exist".
	out, _, replayer := runList(t, site.DataCenter, "undone", "false", registry.Limit{All: true})
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("--done false did not reach the query: %v", unplayed)
	}
	if n := strings.Count(out, "\n") - 1; n != 2 {
		t.Errorf("got %d rows, want the two unfinished epics:\n%s", n, out)
	}
}

// TestABadDoneValueIsRefusedBeforeTheHeader keeps the rejection ahead of the
// stream, which a streaming command opens before its body runs.
func TestABadDoneValueIsRefusedBeforeTheHeader(t *testing.T) {
	cmd, _ := registry.Lookup("epic.list")
	flags := registry.NewFlags()
	flags.SetString("done", "yes")

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{kind: site.DataCenter, board: "99"}, Flags: flags,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("--done yes was accepted")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_DONE" {
		t.Errorf("code = %q, want INVALID_DONE", code)
	}
}

// TestTheBoardIsRequiredBeforeTheHeaderGoesOut is the streaming rule again: the
// epics on a board are the epics that board shows, and there is no default.
func TestTheBoardIsRequiredBeforeTheHeaderGoesOut(t *testing.T) {
	cmd, _ := registry.Lookup("epic.list")
	if cmd.Validate == nil {
		t.Fatal("epic list does not validate, so the board is checked too late")
	}
	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{kind: site.DataCenter}, Flags: registry.NewFlags(),
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("epic list validated with no board anywhere")
	}
	if code := errs.Coerce(err).Code; code != "NO_BOARD" {
		t.Errorf("code = %q, want NO_BOARD", code)
	}
}

// TestAnEpicReferenceIsAKeyOrDigits keeps a caller's argument out of a URL path
// as anything else, and makes a typo cost no round trip.
func TestAnEpicReferenceIsAKeyOrDigits(t *testing.T) {
	for _, ref := range []string{"", "ENG", "ENG-", "-42", "ENG-4x", "../../admin", "ENG 42"} {
		if err := epic.ValidateRef(ref); err == nil {
			t.Errorf("%q was accepted as an epic reference", ref)
		} else if code := errs.Coerce(err).Code; code != "INVALID_EPIC" {
			t.Errorf("%q: code = %q, want INVALID_EPIC", ref, code)
		}
	}
	for _, ref := range []string{"ENG-42", "10101", "ENG-1000"} {
		if err := epic.ValidateRef(ref); err != nil {
			t.Errorf("%q was refused: %v", ref, err)
		}
	}
	if err := epic.ValidateBoardID("ENG"); err == nil {
		t.Error("a board id of ENG was accepted")
	}
}

// TestAMalformedResponseIsRefused covers the decode guard the reader carries.
func TestAMalformedResponseIsRefused(t *testing.T) {
	conn, _ := replayConn(t, "html.datacenter.json")
	client := &epic.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	if _, err := client.List(t.Context(), "99", ""); err == nil {
		t.Fatal("an HTML body was accepted as an epic list")
	} else if code := errs.Coerce(err).Code; code != "MALFORMED_EPICS" {
		t.Errorf("code = %q, want MALFORMED_EPICS", code)
	}
}

// TestEveryEpicCommandIsAReadInEveryBuild keeps the resource out of the write
// tag: neither of these changes anything. Moving issues into an epic does, and
// it lives in internal/workflow.
func TestEveryEpicCommandIsAReadInEveryBuild(t *testing.T) {
	for _, name := range []string{"epic.list", "epic.get"} {
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
	_, result, _ := runList(t, site.DataCenter, "epics", "", registry.Limit{N: 1})
	if result.Complete {
		t.Error("a truncated epic list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

// TestColumnsResolve keeps the default projection honest: a column whose path
// finds nothing renders as an empty cell, so nothing else would catch it.
func TestColumnsResolve(t *testing.T) {
	node := epic.Epic{
		ID: "10101", Key: "ENG-42", Name: "Retry path", Summary: "Retry safely",
	}.Node()
	for _, col := range epic.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}

	doc := epic.ListDoc([]epic.Epic{{ID: "10101", Key: "ENG-42", Name: "Retry path"}}, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestCommandsWithoutASessionFailLoudly covers the guard each one carries.
func TestCommandsWithoutASessionFailLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("epic.list")
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
		t.Error("epic list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
	if err := cmd.Validate(t.Context(), inv); err == nil {
		t.Error("epic list validated without a session")
	}
}

// runList runs `epic list` against the named cassette for one deployment.
func runList(
	t *testing.T, kind site.Kind, fixture, done string, limit registry.Limit,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("epic.list")
	if !ok {
		t.Fatal("epic list is not registered")
	}

	conn, replayer := replayConn(t, fixture+"."+string(kind)+".json")
	flags := registry.NewFlags()
	if done != "" {
		flags.SetString("done", done)
	}
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: kind, board: "99"},
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
	fields []string
	conn   *transport.Client
	kind   site.Kind
	board  string
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, site.Info{Kind: s.kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Info: site.Info{Kind: s.kind}}, nil
}

func (s *stubSession) Idempotency() *idem.Ledger  { return nil }
func (s *stubSession) Project() string            { return "" }
func (s *stubSession) Board() string              { return s.board }
func (s *stubSession) CheckWritable(string) error { return nil }

func (s *stubSession) RequireProject() (string, error) {
	return "", errs.Usage("NO_PROJECT", "this command needs a project and none is set")
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
