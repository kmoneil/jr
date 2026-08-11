package project_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// recordedConn is replayConn plus the check that makes a recording worth
// keeping separately: a cassette that has quietly become hand-written replays
// exactly like a recorded one and would assert nothing about the API.
func recordedConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()

	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	if !cassette.Evidence() {
		t.Fatalf("%s is not a recording, so replaying it establishes nothing "+
			"about the API", fixture)
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

// streamRecorded runs one streaming command against a Data Center recording and
// returns what it wrote.
//
// It fails on an interaction the run never asked for, because that is the whole
// assertion here: the replayer matches on method, path, and query, so an
// unplayed interaction means this code built a request the recorded server was
// not asked, and a played one means it built the request that server answered.
func streamRecorded(
	t *testing.T, command, fixture string, args []string, format render.Format,
) string {
	t.Helper()

	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	conn, replayer := recordedConn(t, fixture)

	var buf strings.Builder
	stream, err := render.NewStream(&buf, format, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter},
		Args:  args,
		Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}, stream)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !result.Complete {
		t.Error("an exhausted listing was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("recorded but never requested: %v", unplayed)
	}
	return buf.String()
}

// TestTheRecordedDataCenterListingIsAConversationAServerHad is what
// projects.datacenter.json beside it cannot be.
//
// That one is worth keeping — it holds two projects so the ordering has
// something to order — but its author decided both halves of the exchange, so
// it establishes that a response shape is handled and never that the request
// was accepted. `project list` on Data Center hits `/rest/api/2/project` with
// `expand=lead` rather than Cloud's paged `/project/search`, and the replayer
// matches on path and query: a row here means a real Jira Data Center answered
// exactly that.
func TestTheRecordedDataCenterListingIsAConversationAServerHad(t *testing.T) {
	got := streamRecorded(t, "project.list",
		"projects-recorded.datacenter.json", nil, render.TSV)

	// One project, because that is what the instance recorded from holds. The
	// paging and ordering cases stay with the constructed fixtures, which can
	// produce a second page and this cannot.
	want := "key\tname\ttype\tlead\n" +
		"ENG\tEngineering\tsoftware\tAda Lovelace\n"
	if got != want {
		t.Errorf("listing =\n%s\nwant\n%s", got, want)
	}
}

// TestTheRecordedDataCenterProjectIsAConversationAServerHad covers the read by
// key. What the recording adds over project.datacenter.json is that the absent
// fields are absent because Data Center does not send them, not because the
// fixture's author left them out: a real 10.4.0 project payload carries no
// `style`, and reporting one would be an invention.
func TestTheRecordedDataCenterProjectIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("project.get")
	if !ok {
		t.Fatal("project get is not registered")
	}
	conn, replayer := recordedConn(t, "project-recorded.datacenter.json")

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter},
		Args: []string{"ENG"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if key, _ := doc.Record.AttrValue("key"); key != "ENG" {
		t.Errorf("key = %q, want ENG", key)
	}
	if id, _ := doc.Record.AttrValue("id"); id != "10000" {
		t.Errorf("id = %q, want 10000", id)
	}
	if _, ok := doc.Record.AttrValue("style"); ok {
		t.Error("a style was reported for a Data Center project, which has none")
	}
	for _, want := range []struct{ child, text string }{
		{"name", "Engineering"},
		{"type", "software"},
		{"lead", "Ada Lovelace"},
	} {
		child, ok := doc.Record.ChildNamed(want.child)
		if !ok {
			t.Errorf("the project named no %s", want.child)
			continue
		}
		if child.Text != want.text {
			t.Errorf("%s = %q, want %q", want.child, child.Text, want.text)
		}
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the project was never read: %v", unplayed)
	}
}

// TestTheRecordedDataCenterComponentsAreAConversationAServerHad covers
// `project components`. components.datacenter.json gives every component a
// lead, which is the tidy case; a real one has an assignee and no lead at all,
// so the lead cell here is empty and asserted empty rather than filled in with
// the assignee, which is a different person for a different reason.
func TestTheRecordedDataCenterComponentsAreAConversationAServerHad(t *testing.T) {
	got := streamRecorded(t, "project.components",
		"components-recorded.datacenter.json", []string{"ENG"}, render.TSV)

	want := "id\tname\tlead\tassignee-type\n" +
		"10000\tapi\t\tPROJECT_DEFAULT\n"
	if got != want {
		t.Errorf("components =\n%s\nwant\n%s", got, want)
	}
}

// TestTheRecordedDataCenterVersionsAreAConversationAServerHad covers
// `project versions`. versions.datacenter.json exists to order three versions,
// which no recording available here can do; what this establishes instead is
// that the unreleased, undated version a server actually sends decodes to
// released=false and archived=false separately, and to an empty date rather
// than a midnight nobody set.
func TestTheRecordedDataCenterVersionsAreAConversationAServerHad(t *testing.T) {
	got := streamRecorded(t, "project.versions",
		"versions-recorded.datacenter.json", []string{"ENG"}, render.TSV)

	want := "id\tname\treleased\tarchived\trelease-date\n" +
		"10000\t1.0\tfalse\tfalse\t\n"
	if got != want {
		t.Errorf("versions =\n%s\nwant\n%s", got, want)
	}
}

// TestTheRecordedDataCenterStatusesAreAConversationAServerHad covers
// `project statuses`. statuses.datacenter.json holds two issue types with names
// its author picked; the recording holds the five a Jira Software 10.4.0
// project is created with, and their categories arrive as Data Center's own
// keys — new, indeterminate, done — which is the mapping nothing else here
// exercises against real input.
func TestTheRecordedDataCenterStatusesAreAConversationAServerHad(t *testing.T) {
	got := streamRecorded(t, "project.statuses",
		"statuses-recorded.datacenter.json", []string{"ENG"}, render.TSV)

	// Ordered by type, so two runs agree. Every type shares one workflow on a
	// freshly created project, which is why every row reads the same.
	want := "type\tstatuses\n" +
		"Bug\tTo Do,In Progress,Done\n" +
		"Epic\tTo Do,In Progress,Done\n" +
		"Story\tTo Do,In Progress,Done\n" +
		"Sub-task\tTo Do,In Progress,Done\n" +
		"Task\tTo Do,In Progress,Done\n"
	if got != want {
		t.Errorf("statuses =\n%s\nwant\n%s", got, want)
	}

	// The names are what TSV can carry; the category is what anything automated
	// branches on, and it only exists in a format with room for it.
	asXML := streamRecorded(t, "project.statuses",
		"statuses-recorded.datacenter.json", []string{"ENG"}, render.XML)
	for _, want := range []string{
		`<status id="10000" category="to-do">To Do</status>`,
		`<status id="3" category="in-progress">In Progress</status>`,
		`<status id="10001" category="done">Done</status>`,
	} {
		if !strings.Contains(asXML, want) {
			t.Errorf("statuses XML has no %s:\n%s", want, asXML)
		}
	}
}
