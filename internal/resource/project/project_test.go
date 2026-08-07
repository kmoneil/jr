package project_test

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
	"github.com/kmoneil/jira-cli/internal/resource/project"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

var deployments = []site.Kind{site.Cloud, site.DataCenter}

// TestListConvergesAcrossDeployments is why both fixtures exist. Cloud pages a
// search endpoint and Data Center returns everything from one request — two
// genuinely different conversations that a caller must not be able to tell
// apart.
func TestListConvergesAcrossDeployments(t *testing.T) {
	got := map[site.Kind]string{}
	for _, kind := range deployments {
		out, result, replayer := runList(t, kind, registry.Limit{All: true})
		if !result.Complete {
			t.Errorf("%s: an exhausted list was reported incomplete", kind)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: a page was never fetched: %v", kind, unplayed)
		}
		got[kind] = out
	}

	// Ordered by key on both, so the rows and their order agree.
	for kind, out := range got {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("%s: got %d lines, want a header and two rows:\n%s",
				kind, len(lines), out)
		}
		if lines[0] != "key\tname\ttype\tlead" {
			t.Errorf("%s header = %q", kind, lines[0])
		}
		var keys []string
		for _, line := range lines[1:] {
			keys = append(keys, strings.SplitN(line, "\t", 2)[0])
		}
		if strings.Join(keys, ",") != "ENG,OPS" {
			t.Errorf("%s keys = %v, want them ordered by key", kind, keys)
		}
	}
}

// TestGetDefaultsToTheContextProject covers the argument being optional, which
// is what makes a configured caller able to ask without repeating themselves.
func TestGetDefaultsToTheContextProject(t *testing.T) {
	cmd, ok := registry.Lookup("project.get")
	if !ok {
		t.Fatal("project get is not registered")
	}

	for _, args := range [][]string{nil, {"ENG"}} {
		conn, replayer := replayConn(t, "project.datacenter.json")
		inv := &registry.Invocation{
			Jira: &stubSession{conn: conn, kind: site.DataCenter},
			Args: args, Flags: registry.NewFlags(),
			Stderr: io.Discard, Progress: registry.NoProgress,
		}
		doc, err := cmd.Run(t.Context(), inv)
		if err != nil {
			t.Fatalf("args=%v: %v", args, err)
		}
		if key, _ := doc.Record.AttrValue("key"); key != "ENG" {
			t.Errorf("args=%v: key = %q", args, key)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("args=%v: the project was never read: %v", args, unplayed)
		}
	}
}

// TestVersionsAreNewestFirstAndUndatedLast covers the ordering decision. An
// absent release date is not the beginning of time, so an undated version sorts
// after the dated ones rather than before them.
func TestVersionsAreNewestFirstAndUndatedLast(t *testing.T) {
	for _, kind := range deployments {
		conn, _ := replayConn(t, "versions."+string(kind)+".json")
		client := &project.Client{Transport: conn, Site: site.Info{Kind: kind}}

		versions, err := client.Versions(t.Context(), "ENG")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		var names []string
		for _, v := range versions {
			names = append(names, v.Name)
		}
		if strings.Join(names, ",") != "2.0,1.0,next" {
			t.Errorf("%s versions = %v, want newest first with the undated last",
				kind, names)
		}

		// Released and archived are separate, because they are: a version can
		// be either, both, or neither.
		if !versions[1].Released || !versions[1].Archived {
			t.Errorf("%s: 1.0 = %+v, want released and archived", kind, versions[1])
		}
		if versions[0].Released == versions[0].Archived {
			t.Errorf("%s: 2.0 = %+v, want released and not archived", kind, versions[0])
		}
	}
}

// TestStatusesCarryTheirCategory is what makes the output usable by anything
// automated: a project can rename a status, but the category stays one of three
// values.
func TestStatusesCarryTheirCategory(t *testing.T) {
	for _, kind := range deployments {
		conn, _ := replayConn(t, "statuses."+string(kind)+".json")
		client := &project.Client{Transport: conn, Site: site.Info{Kind: kind}}

		types, err := client.Statuses(t.Context(), "ENG")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if len(types) == 0 {
			t.Fatalf("%s: no issue types", kind)
		}

		// Asserted by name where the name is one whose category is not a
		// matter of opinion. The two fixtures hold different workflows — the
		// Cloud one is recorded, so it has whatever that instance defines —
		// and pinning either one's exact list would make this a test of the
		// sandbox rather than of the mapping.
		want := map[string]string{
			"Open": site.CategoryToDo, "To Do": site.CategoryToDo,
			"Closed": site.CategoryDone, "Done": site.CategoryDone,
			"In Progress": site.CategoryInProgress,
			"In Review":   site.CategoryInProgress,
		}
		checked := 0
		for _, ty := range types {
			if len(ty.Statuses) == 0 {
				t.Errorf("%s: issue type %q has no statuses", kind, ty.Type)
			}
			for _, st := range ty.Statuses {
				if st.Category == "" {
					t.Errorf("%s: %s has no category", kind, st.Name)
				}
				if w, known := want[st.Name]; known {
					checked++
					if st.Category != w {
						t.Errorf("%s: %s category = %q, want %q",
							kind, st.Name, st.Category, w)
					}
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s: no status had a name this test knows, so it asserted "+
				"nothing about the mapping", kind)
		}
	}
}

// TestComponentsReportHowTheyAssign covers the field worth knowing before
// filing: a component can hand an issue to somebody nobody chose.
func TestComponentsReportHowTheyAssign(t *testing.T) {
	conn, _ := replayConn(t, "components.datacenter.json")
	client := &project.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	components, err := client.Components(t.Context(), "ENG")
	if err != nil {
		t.Fatalf("components: %v", err)
	}
	// Ordered by name, so two runs agree.
	if len(components) != 2 || components[0].Name != "render" {
		t.Fatalf("components = %+v, want them ordered by name", components)
	}
	if components[0].AssigneeType != "PROJECT_LEAD" {
		t.Errorf("assignee type = %q", components[0].AssigneeType)
	}
	if components[1].Lead != "Ada Lovelace" {
		t.Errorf("lead = %q", components[1].Lead)
	}
}

// TestEveryProjectCommandIsAReadInEveryBuild keeps the resource out of the
// write tag: none of these change anything.
func TestEveryProjectCommandIsAReadInEveryBuild(t *testing.T) {
	for _, name := range []string{
		"project.list", "project.get", "project.components",
		"project.versions", "project.statuses",
	} {
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

// TestColumnsResolve keeps every default projection honest: a column whose path
// finds nothing renders as an empty cell, so nothing else would catch it.
func TestColumnsResolve(t *testing.T) {
	node := project.Project{
		ID: "1", Key: "ENG", Name: "Engineering", Type: "software", Lead: "Ada",
	}.Node()
	for _, col := range project.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}

	doc := project.ListDoc([]project.Project{{Key: "ENG", Name: "Engineering"}}, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestCommandsWithoutASessionFailLoudly covers the guard each one carries.
func TestCommandsWithoutASessionFailLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("project.list")
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
		t.Error("project list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
}

func runList(
	t *testing.T, kind site.Kind, limit registry.Limit,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()
	return runListFixture(t, kind, limit, "projects."+string(kind)+".json")
}

func runListFixture(
	t *testing.T, kind site.Kind, limit registry.Limit, fixture string,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("project.list")
	if !ok {
		t.Fatal("project list is not registered")
	}

	conn, replayer := replayConn(t, fixture)
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: kind},
		Flags: registry.NewFlags(), Limit: limit,
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
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, site.Info{Kind: s.kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Info: site.Info{Kind: s.kind}}, nil
}

func (s *stubSession) Idempotency() *idem.Ledger       { return nil }
func (s *stubSession) Project() string                 { return "ENG" }
func (s *stubSession) RequireProject() (string, error) { return "ENG", nil }
func (s *stubSession) Board() string                   { return "" }

// RequireBoard is what an agile command calls. None of these fixtures set a
// board, so it fails the way the real session does rather than returning one.
// Fields is the context default field set. Empty here: a stub that
// invented one would make every resource test assert a request nobody made.
func (s *stubSession) Fields() []string { return s.fields }

func (s *stubSession) RequireBoard() (string, error) {
	return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
}
func (s *stubSession) CheckWritable(string) error { return nil }

// TestTheThreePerProjectListingsRunAsCommands exercises the wrappers, which the
// client-level tests above do not reach.
func TestTheThreePerProjectListingsRunAsCommands(t *testing.T) {
	for _, tc := range []struct {
		command, fixture, header string
		rows                     map[site.Kind]int
	}{
		{
			command: "project.components", fixture: "components",
			header: "id\tname\tlead\tassignee-type",
			rows:   map[site.Kind]int{site.Cloud: 2, site.DataCenter: 2},
		},
		{
			command: "project.versions", fixture: "versions",
			header: "id\tname\treleased\tarchived\trelease-date",
			rows:   map[site.Kind]int{site.Cloud: 3, site.DataCenter: 3},
		},
		{
			command: "project.statuses", fixture: "statuses",
			header: "type\tstatuses",
			// Seven on Cloud because that is what the recorded instance
			// defines; two on Data Center because that fixture is written.
			rows: map[site.Kind]int{site.Cloud: 7, site.DataCenter: 2},
		},
	} {
		for _, kind := range deployments {
			t.Run(tc.fixture+"/"+string(kind), func(t *testing.T) {
				cmd, ok := registry.Lookup(tc.command)
				if !ok {
					t.Fatalf("%s is not registered", tc.command)
				}

				conn, replayer := replayConn(t, tc.fixture+"."+string(kind)+".json")
				inv := &registry.Invocation{
					Jira:  &stubSession{conn: conn, kind: kind},
					Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
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
				if !result.Complete {
					t.Error("a whole listing was reported incomplete")
				}
				if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
					t.Errorf("the request was never sent: %v", unplayed)
				}

				lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
				if lines[0] != tc.header {
					t.Errorf("header = %q, want %q", lines[0], tc.header)
				}
				if len(lines) != tc.rows[kind]+1 {
					t.Errorf("got %d rows, want %d:\n%s",
						len(lines)-1, tc.rows[kind], buf.String())
				}

				// Every column has to show something somewhere. `project
				// statuses` declared a column over a list element and emitted a
				// blank cell on every row for both deployments, and this test
				// passed the whole time because it checked the header and the
				// row count and never looked at a cell.
				for i, header := range strings.Split(tc.header, "\t") {
					filled := false
					for _, line := range lines[1:] {
						cells := strings.Split(line, "\t")
						if i < len(cells) && cells[i] != "" {
							filled = true
							break
						}
					}
					if !filled {
						t.Errorf("column %q is empty on every row:\n%s",
							header, buf.String())
					}
				}
			})
		}
	}
}

// TestListTruncatesAndSaysSo covers the bound on a listing that arrives whole.
func TestListTruncatesAndSaysSo(t *testing.T) {
	_, result, _ := runList(t, site.DataCenter, registry.Limit{N: 1})
	if result.Complete {
		t.Error("a truncated project list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

// TestAMalformedResponseIsRefused covers the decode guard each reader carries.
func TestAMalformedResponseIsRefused(t *testing.T) {
	conn, _ := replayConn(t, "html.datacenter.json")
	client := &project.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	_, err := client.List(t.Context())
	if err == nil {
		t.Fatal("an HTML body was accepted as a project list")
	}
	if code := errs.Coerce(err).Code; code != "MALFORMED_PROJECTS" {
		t.Errorf("code = %q, want MALFORMED_PROJECTS", code)
	}
}

// TestListPagesUntilExhausted keeps the paging path covered now that the
// recorded listing no longer exercises it.
//
// The sandbox has two projects and `project list` has no page size, so a real
// conversation there is one request and proves nothing about the second page.
// This fixture is constructed and says so: three projects over two pages, which
// is a thing no recording available here can produce.
func TestListPagesUntilExhausted(t *testing.T) {
	out, result, replayer := runListFixture(t, site.Cloud,
		registry.Limit{All: true}, "projects-paged.cloud.json")

	if !result.Complete {
		t.Error("an exhausted list was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the second page was never fetched: %v", unplayed)
	}

	var keys []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n")[1:] {
		keys = append(keys, strings.SplitN(line, "\t", 2)[0])
	}
	// Ordered across the page boundary, not merely within a page.
	if strings.Join(keys, ",") != "ENG,OPS,ZZZ" {
		t.Errorf("keys = %v, want them ordered by key across both pages", keys)
	}
}

// TestRecordedConversationsReplay is the test a hand-written fixture cannot be.
//
// The replayer matches a request by method, path, and query, and fails when
// nothing matches. So replaying a recording asserts that the request this code
// builds is the request a real Jira answered — which is exactly what every
// fixture here implied and none of them established. Three of them encoded a
// call the API rejects, and all three passed their tests.
//
// It stays honest by refusing to run on a fixture that only claims to be a
// recording: a file marked constructed proves nothing, and quietly passing over
// it would let a recording be replaced by a hand-written stand-in unnoticed.
func TestRecordedConversationsReplay(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		call    func(*project.Client) error
	}{
		{"project.cloud.json", func(c *project.Client) error {
			_, err := c.Get(t.Context(), "ENG")
			return err
		}},
		{"statuses.cloud.json", func(c *project.Client) error {
			_, err := c.Statuses(t.Context(), "ENG")
			return err
		}},
		{"versions-empty.cloud.json", func(c *project.Client) error {
			_, err := c.Versions(t.Context(), "ENG")
			return err
		}},
		{"components-empty.cloud.json", func(c *project.Client) error {
			_, err := c.Components(t.Context(), "ENG")
			return err
		}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			cassette, err := transport.LoadCassette(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if !cassette.Evidence() {
				t.Fatalf("%s is not a recording, so replaying it asserts nothing "+
					"about the API", tc.fixture)
			}

			conn, replayer := replayConn(t, tc.fixture)
			client := &project.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}
			if err := tc.call(client); err != nil {
				t.Fatalf("the request this code builds is not the one the server "+
					"answered: %v", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("recorded but never requested: %v", unplayed)
			}
		})
	}
}

// TestTheRecordedListingIsEvidence guards the listing fixture the same way. It
// is separate because the listing runs as a command rather than a client call.
func TestTheRecordedListingIsEvidence(t *testing.T) {
	cassette, err := transport.LoadCassette(filepath.Join("testdata", "projects.cloud.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cassette.Evidence() {
		t.Error("projects.cloud.json is no longer a recording")
	}
}
