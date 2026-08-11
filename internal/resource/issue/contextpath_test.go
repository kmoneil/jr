package issue_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// contextPathConn replays a cassette recorded against a Data Center served
// under /jira, with a base URL that carries the prefix.
//
// The base is the point of these tests rather than setup for them. Every path
// in the cassette carries /jira, so a client configured at the root asks for
// paths the replayer cannot answer, and a client that applied the prefix twice
// asks for /jira/jira/... — which against a real instance is the web server's
// 404 page rather than an API response.
func contextPathConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
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
	// http, not https, because the recording was made over http and the
	// content URL in it says so. Relative refuses a scheme that differs from
	// the configured one — a server talking a downgrade into a client is
	// exactly what that check is for — so a base of https here fails with
	// OFF_SITE_URL against a URL the server really sent.
	conn, err := transport.New(transport.Options{
		BaseURL:    "http://recorded.invalid/jira",
		HTTPClient: replayer.Client(),
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// TestTheRecordedContextPathDownloadIsAConversationAServerHad covers the one
// place a server-supplied URL becomes a request, against the deployment shape
// that makes it hard.
//
// Data Center has no content endpoint this tool can build. It reports an
// absolute URL on the attachment — here
// http://…/jira/secure/attachment/10000/rows.csv — and `Relative` has to
// reduce that to a path against a base that already ends in /jira, without
// repeating the prefix and without following anything off-site. Three defects
// have come out of that code and every one of them was argued from
// documentation and fixed without ever being run against an instance served
// under a context path.
//
// It is also the recording that found a shipped bug. The metadata check used
// to require an `id` in the response; a real Data Center does not send one, so
// every download against every Data Center failed with MALFORMED_ATTACHMENT —
// exit 9, marked retryable, against a response that would never change. The
// constructed fixture beside this one has an id in it because its author put
// one there.
func TestTheRecordedContextPathDownloadIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("issue.attachment.download")
	if !ok {
		t.Fatal("issue attachment download is not registered")
	}
	conn, replayer := contextPathConn(t, "download-contextpath.datacenter.json")

	// A real destination rather than the `--output -` the recording was made
	// with: writing to stdout is the one path that returns no document,
	// because there the bytes are the result.
	dest := filepath.Join(t.TempDir(), "rows.csv")
	flags := registry.NewFlags()
	flags.SetString("output", dest)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter},
		Args:  []string{"ENG-4", "10000"},
		Flags: flags, Stderr: io.Discard, Stdout: io.Discard,
		Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("a download a real Data Center served failed here: %v", err)
	}

	name, ok := doc.Record.ChildNamed("filename")
	if !ok {
		t.Fatal("the download named no file")
	}
	if name.Text != "rows.csv" {
		t.Errorf("filename = %q, want rows.csv", name.Text)
	}

	// The bytes the instance really stored, read back from where they landed.
	written, err := os.ReadFile(dest) //nolint:gosec // a path from t.TempDir.
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if want := "summary,status\nENG-1,To Do\n"; string(written) != want {
		t.Errorf("wrote %q, want %q", written, want)
	}

	// Both exchanges: the metadata read, and the content fetch at the URL the
	// server supplied. If Relative had refused or mangled the second, the
	// command would have failed and this cassette would report it unplayed.
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the content was never fetched: %v", unplayed)
	}
}

// TestTheRecordedContextPathListingIsAConversationAServerHad is the ordinary
// case, and the one that says the prefix is applied exactly once.
//
// Nothing in this tree carried a context path in a fixture before, so the
// whole of resolve's JoinPath against a base with a path was tested only by
// unit tests over strings this project wrote.
func TestTheRecordedContextPathListingIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	conn, replayer := contextPathConn(t, "list-contextpath.datacenter.json")

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	flags := registry.NewFlags()
	flags.SetString("project", "ENG")
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.DataCenter},
		Flags: flags, Limit: registry.Limit{N: 5},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if cmd.Validate != nil {
		if err := cmd.Validate(t.Context(), inv); err != nil {
			t.Fatalf("validate: %v", err)
		}
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(buf.String(), "ENG-") {
		t.Errorf("listing named no issue:\n%s", buf.String())
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded page was never requested: %v", unplayed)
	}
}
