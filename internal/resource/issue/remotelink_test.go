package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// remoteLinkFixtures names the recorded conversations, one per deployment: the
// sandbox for Cloud and the rig for Data Center. Each server holds one link a
// person added and one an application wrote, so each recording shows both
// shapes the endpoint serves; the shapes agree and only the invented content
// differs.
var remoteLinkFixtures = map[site.Kind]struct {
	list, get, key, app string
}{
	site.Cloud: {
		list: "remotelinks-recorded.cloud.json",
		get:  "get-remotelinks-recorded.cloud.json",
		key:  "AGL-2", app: "Example CI",
	},
	site.DataCenter: {
		list: "remotelinks-recorded.datacenter.json",
		get:  "get-remotelinks-recorded.datacenter.json",
		key:  "ENG-2", app: "Recorded CI",
	},
}

// TestRemoteLinksAreReadOnBothDeployments covers the decoding, and the one
// normalization in it: Jira reports a hand-added link as an empty application
// object, and empty-object and absent are the same fact, reported by absence.
func TestRemoteLinksAreReadOnBothDeployments(t *testing.T) {
	for kind, tc := range remoteLinkFixtures {
		t.Run(string(kind), func(t *testing.T) {
			conn, replayer := replayConn(t, tc.list)
			client := &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}

			links, err := client.ListRemoteLinks(t.Context(), tc.key)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(links) != 2 {
				t.Fatalf("got %d links, want 2", len(links))
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the links were never read: %v", unplayed)
			}

			// Ordered by id, which is creation order; the hand-added link was
			// made first on both servers.
			plain, app := links[0], links[1]
			if plain.ID >= app.ID {
				t.Errorf("ids out of order: %d before %d", plain.ID, app.ID)
			}

			if plain.Application != "" {
				t.Errorf("a hand-added link names an application: %q", plain.Application)
			}
			if plain.GlobalID != "" {
				t.Errorf("a hand-added link carries a globalId: %q", plain.GlobalID)
			}
			if plain.Resolved != nil {
				t.Error("a hand-added link reports a resolved state")
			}
			if plain.Title == "" || plain.URL == "" {
				t.Errorf("the required pair is missing: title %q url %q",
					plain.Title, plain.URL)
			}

			if app.Application != tc.app {
				t.Errorf("application = %q, want %q", app.Application, tc.app)
			}
			if app.GlobalID == "" {
				t.Error("the application link lost its globalId")
			}
			if app.Relationship == "" {
				t.Error("the application link lost its relationship")
			}
			if app.Resolved == nil || !*app.Resolved {
				t.Error("the remote side's resolved state was dropped")
			}
		})
	}
}

// TestRemoteLinkListRunsAsARegisteredCommand exercises the command wrapper and
// the rendered shape, which the client-level test above does not reach.
func TestRemoteLinkListRunsAsARegisteredCommand(t *testing.T) {
	cmd, ok := registry.Lookup("issue.remotelink.list")
	if !ok {
		t.Fatal("issue remotelink list is not registered")
	}

	conn, replayer := replayConn(t, remoteLinkFixtures[site.DataCenter].list)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-2"}, Flags: registry.NewFlags(),
		Limit:  registry.Limit{All: true},
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
	if !result.Complete {
		t.Error("a whole remote link list was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the links were never read: %v", unplayed)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if lines[0] != "id\tapplication\trelationship\ttitle\turl" {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), buf.String())
	}
	// Creation order: the hand-added link first, with its application and
	// relationship cells empty rather than invented.
	if !strings.Contains(lines[1], "Deploy runbook") ||
		!strings.Contains(lines[1], "\t\t") {
		t.Errorf("first row = %q", lines[1])
	}
	if !strings.Contains(lines[2], "Recorded CI") ||
		!strings.Contains(lines[2], "mentioned in") {
		t.Errorf("second row = %q", lines[2])
	}
}

// TestRemoteLinkColumnsResolve keeps the default projection honest: a column
// whose path finds nothing renders as an empty cell, so nothing else would
// catch it.
func TestRemoteLinkColumnsResolve(t *testing.T) {
	resolved := true
	node := issue.RemoteLink{
		ID: 1, GlobalID: "system=x&id=1", Application: "CI",
		Relationship: "mentioned in", Title: "a title",
		URL: "https://example.invalid/1", Summary: "a summary", Resolved: &resolved,
	}.Node()
	for _, col := range issue.RemoteLinkColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

// TestRemoteLinkDocsAreWellFormed covers the document renderer, which the
// streaming path does not use.
func TestRemoteLinkDocsAreWellFormed(t *testing.T) {
	resolved := true
	doc := issue.RemoteLinkListDoc([]issue.RemoteLink{{
		ID: 1, GlobalID: "system=x&id=1", Application: "CI",
		Relationship: "mentioned in", Title: "a title",
		URL: "https://example.invalid/1", Resolved: &resolved,
	}, {
		ID: 2, Title: "a web link", URL: "https://example.invalid/2",
	}}, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("remotelinks: %v", err)
	}

	var xml strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`<remotelink id="1" global-id="system=x&amp;id=1">`,
		`<resolved>true</resolved>`,
	} {
		if !strings.Contains(xml.String(), want) {
			t.Errorf("the output does not contain %s:\n%s", want, xml.String())
		}
	}
	// The hand-added link's absences stay absent.
	if strings.Contains(xml.String(), `<remotelink id="2"><application>`) {
		t.Errorf("an empty application was rendered:\n%s", xml.String())
	}
}

// TestRemoteLinkListTruncatesAndSaysSo covers the bound. The set arrives
// whole, so the cut happens here and has to be admitted.
func TestRemoteLinkListTruncatesAndSaysSo(t *testing.T) {
	cmd, _ := registry.Lookup("issue.remotelink.list")
	conn, _ := replayConn(t, remoteLinkFixtures[site.DataCenter].list)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-2"}, Flags: registry.NewFlags(),
		Limit:  registry.Limit{N: 1},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
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
	if result.Complete {
		t.Error("a truncated remote link list was reported complete")
	}
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

// TestRemoteLinkReadsFailLoudlyWithoutASession covers the shared guard.
func TestRemoteLinkReadsFailLoudlyWithoutASession(t *testing.T) {
	cmd, _ := registry.Lookup("issue.remotelink.list")
	inv := &registry.Invocation{
		Args: []string{"ENG-1"}, Flags: registry.NewFlags(),
		Limit: registry.Limit{All: true}, Progress: registry.NoProgress,
	}
	stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := cmd.Stream(t.Context(), inv, stream); err == nil {
		t.Error("issue remotelink list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
}

func runGetWithRemoteLinks(
	t *testing.T, fixture, key string, kind site.Kind, with bool,
) (*render.Doc, *transport.Replayer) {
	t.Helper()
	cmd, ok := registry.Lookup("issue.get")
	if !ok {
		t.Fatal("issue get is not registered")
	}
	conn, replayer := replayConn(t, fixture)

	flags := registry.NewFlags()
	if with {
		flags.SetBool("with-remote-links", true)
	}
	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: kind}, Args: []string{key},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return doc, replayer
}

// TestWithRemoteLinksFoldsThemIntoTheRecord covers the fold on both
// deployments. Unlike the comment thread the container is never partial: the
// endpoint hands the whole set over in one response.
func TestWithRemoteLinksFoldsThemIntoTheRecord(t *testing.T) {
	for kind, tc := range remoteLinkFixtures {
		t.Run(string(kind), func(t *testing.T) {
			doc, replayer := runGetWithRemoteLinks(t, tc.get, tc.key, kind, true)
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the links were never fetched: %v", unplayed)
			}

			links, ok := doc.Record.ChildNamed("remotelinks")
			if !ok {
				t.Fatal("the record carries no remotelinks")
			}
			if count, _ := links.AttrValue("count"); count != "2" {
				t.Errorf("count = %q, want 2", count)
			}
			if !doc.IsComplete() {
				t.Error("a record holding every remote link reports itself partial")
			}
		})
	}
}

// TestRemoteLinksCostNothingWithoutTheFlag is the other half. A second request
// nobody asked for is a second request against --max-requests.
func TestRemoteLinksCostNothingWithoutTheFlag(t *testing.T) {
	tc := remoteLinkFixtures[site.DataCenter]
	doc, replayer := runGetWithRemoteLinks(t, tc.get, tc.key, site.DataCenter, false)
	if _, has := doc.Record.ChildNamed("remotelinks"); has {
		t.Error("remote links arrived without --with-remote-links")
	}
	// The remotelink interaction is deliberately left unplayed here, which is
	// the assertion: the cassette holds it and the command must not reach for
	// it.
	unplayed := replayer.Unplayed()
	if len(unplayed) != 1 || !strings.Contains(unplayed[0], "/remotelink") {
		t.Errorf("unplayed = %v, want exactly the remotelink request", unplayed)
	}
}
