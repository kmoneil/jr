package field_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/field"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// catalogueJSON is what /field returns. It carries the shapes the renderer has
// to cope with: a native field, a custom one with clause names, and an array
// field whose element type matters.
const catalogueJSON = `[
	{"id":"summary","name":"Summary","custom":false,
	 "searchable":true,"orderable":true,"navigable":true,
	 "clauseNames":["summary"],"schema":{"type":"string"}},
	{"id":"customfield_10042","name":"Story Points","custom":true,
	 "searchable":true,"orderable":true,"navigable":true,
	 "clauseNames":["cf[10042]","Story Points"],"schema":{"type":"number"}},
	{"id":"customfield_10099","name":"Sprint","custom":true,
	 "searchable":true,"orderable":false,"navigable":true,
	 "clauseNames":["cf[10099]","Sprint"],
	 "schema":{"type":"array","items":"json"}}
]`

func TestListEmitsTheCatalogue(t *testing.T) {
	out, result := run(t, registry.Limit{All: true}, render.TSV)

	if !result.Complete {
		t.Error("a whole catalogue was reported incomplete")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want a header and three rows:\n%s", len(lines), out)
	}
	if lines[0] != "id\tname\ttype\tcustom" {
		t.Errorf("header = %q", lines[0])
	}
	// Sorted by id, so the rows are the same on every run.
	if !strings.HasPrefix(lines[1], "customfield_10042\tStory Points\tnumber\ttrue") {
		t.Errorf("row = %q", lines[1])
	}
}

// TestListDeclaresColumnsThatResolve keeps the default TSV projection honest: a
// column whose path finds nothing renders as an empty cell rather than failing,
// so nothing else would catch it.
func TestListDeclaresColumnsThatResolve(t *testing.T) {
	node := field.Node(site.Field{
		ID: "customfield_10042", Name: "Story Points", Custom: true, Type: "number",
	})
	for _, col := range field.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

// TestLimitTruncatesAndSaysSo covers the one thing that would be a silent lie:
// the endpoint returns everything at once, so a caller's --limit is applied
// here, and a cut result must never be reported as complete.
func TestLimitTruncatesAndSaysSo(t *testing.T) {
	out, result := run(t, registry.Limit{N: 2}, render.TSV)

	if result.Complete {
		t.Error("a truncated catalogue was reported complete")
	}
	if lines := strings.Count(strings.TrimRight(out, "\n"), "\n"); lines != 2 {
		t.Errorf("got %d rows, want 2:\n%s", lines, out)
	}
	// There is no cursor on this endpoint, so there is no token to hand back.
	// One that meant nothing would be worse than none.
	if result.NextPageToken != "" {
		t.Errorf("a page token was invented: %q", result.NextPageToken)
	}
}

func TestListDocIsWellFormed(t *testing.T) {
	catalogue, err := site.FetchFields(t.Context(),
		&stubDoer{body: catalogueJSON}, site.Info{Kind: site.Cloud})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	doc := field.ListDoc(catalogue.Fields, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var xml strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`<field id="customfield_10042"`,
		`<name>Story Points</name>`,
		`<type>number</type>`,
		`<clause-name>cf[10042]</clause-name>`,
		`<items>json</items>`,
	} {
		if !strings.Contains(xml.String(), want) {
			t.Errorf("the output does not contain %s:\n%s", want, xml.String())
		}
	}
}

func TestListDeclaresPartial(t *testing.T) {
	cmd, ok := registry.Lookup("field.list")
	if !ok {
		t.Fatal("field list is not registered")
	}
	if !cmd.Paginated {
		t.Error("field list bounds its output but is not paginated")
	}
	found := false
	for _, code := range cmd.AllExitCodes() {
		if code == exitcode.Partial {
			found = true
		}
	}
	if !found {
		t.Error("field list can truncate but does not declare exit 3")
	}
}

// run executes the registered command against a stubbed catalogue and returns
// what reached the stream. It goes through the registry rather than calling the
// stream function directly, because the command as registered is what a user
// actually invokes.
func run(t *testing.T, limit registry.Limit, format render.Format) (string, registry.StreamResult) {
	t.Helper()

	cmd, ok := registry.Lookup("field.list")
	if !ok {
		t.Fatal("field list is not registered")
	}

	inv := &registry.Invocation{
		Jira:     &stubSession{doer: &stubDoer{body: catalogueJSON}},
		Flags:    registry.Flags{},
		Limit:    limit,
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, format, render.StreamSpec{
		Kind:    cmd.Kind(),
		Version: cmd.KindVersion(),
		Name:    cmd.CollectionName,
		Columns: cmd.Columns,
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

// stubSession is a registry.Session backed by a stubbed transport, so the
// command is exercised with no auth, no config, and no network.
type stubSession struct {
	fields []string
	doer   *stubDoer
	meta   *site.Metadata
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return nil, site.Info{Kind: site.Cloud}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	if s.meta == nil {
		s.meta = &site.Metadata{Client: s.doer, Info: site.Info{Kind: site.Cloud}}
	}
	return s.meta, nil
}

func (s *stubSession) Project() string                 { return "" }
func (s *stubSession) RequireProject() (string, error) { return "", nil }
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

// Idempotency implements registry.Session. A nil ledger means no protection,
// which is what a command that does not mutate should never notice.
func (s *stubSession) Idempotency() *idem.Ledger { return nil }

// stubDoer answers with a fixed body and counts how often it was asked.
type stubDoer struct {
	body  string
	calls int
}

func (s *stubDoer) Do(context.Context, transport.Request) (*transport.Response, error) {
	s.calls++
	return &transport.Response{
		Status: 200,
		Body:   []byte(s.body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestListWithoutASessionFailsLoudly covers the guard. A resource that
// dereferenced a nil session would panic in production rather than fail here.
func TestListWithoutASessionFailsLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("field.list")
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
		t.Error("field list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
}

// TestListSurfacesAFetchFailure covers the path where the catalogue cannot be
// read. A command that swallowed it would print an empty catalogue and exit 0,
// which reads as "this site has no fields".
func TestListSurfacesAFetchFailure(t *testing.T) {
	cmd, _ := registry.Lookup("field.list")
	inv := &registry.Invocation{
		Jira:  &stubSession{doer: &stubDoer{body: `<html>gateway timeout</html>`}},
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
		t.Error("an unreadable catalogue was reported as an empty one")
	} else if code := errs.Coerce(err).Code; code != "MALFORMED_FIELD_LIST" {
		t.Errorf("code = %q, want MALFORMED_FIELD_LIST", code)
	}
}
