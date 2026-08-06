package meta_test

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/meta"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

const transitionsJSON = `{"transitions":[
	{"id":"11","name":"Start Progress","hasScreen":false,
	 "to":{"id":"3","name":"In Progress",
	       "statusCategory":{"key":"indeterminate","name":"In Progress"}}},
	{"id":"2","name":"Close Issue","hasScreen":true,
	 "to":{"id":"6","name":"Closed","statusCategory":{"key":"done","name":"Done"}},
	 "fields":{"resolution":{"required":true,"name":"Resolution",
	                         "schema":{"type":"resolution"},
	                         "allowedValues":[{"id":"1","name":"Fixed"}]}}}
]}`

const dcCreateMetaJSON = `{"projects":[{"key":"ENG","issuetypes":[{"id":"10001","name":"Bug",
	"fields":{
		"summary":{"required":true,"name":"Summary","schema":{"type":"string"}},
		"priority":{"required":false,"name":"Priority","schema":{"type":"priority"},
		            "hasDefaultValue":true,
		            "allowedValues":[{"id":"1","name":"High"}]}}}]}]}`

func TestTransitionsEmitTheWorkflow(t *testing.T) {
	out, result := runStream(t, "meta.transitions",
		map[string]string{}, []string{"ENG-101"},
		registry.Limit{All: true}, transitionsJSON)

	if !result.Complete {
		t.Error("a whole transition list was reported incomplete")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), out)
	}
	if lines[0] != "id\tname\tto\tcategory" {
		t.Errorf("header = %q", lines[0])
	}
	// Ordered numerically, so 2 comes before 11.
	if !strings.HasPrefix(lines[1], "2\tClose Issue\tClosed\tdone") {
		t.Errorf("row = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "11\tStart Progress\tIn Progress\tin-progress") {
		t.Errorf("row = %q", lines[2])
	}
}

// TestTransitionColumnsResolve keeps the default TSV projection honest: a
// column whose path finds nothing renders as an empty cell rather than
// failing, so nothing else would catch it.
func TestTransitionColumnsResolve(t *testing.T) {
	node := meta.TransitionNode(site.Transition{
		ID: "11", Name: "Start Progress",
		To: site.Status{ID: "3", Name: "In Progress", Category: site.CategoryInProgress},
	})
	for _, col := range meta.TransitionColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

func TestCreateMetaColumnsResolve(t *testing.T) {
	node := meta.MetaFieldNode(site.MetaField{
		ID: "summary", Name: "Summary", Required: true, Type: "string",
	})
	for _, col := range meta.CreateMetaColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

func TestCreateMetaEmitsRequiredFieldsFirst(t *testing.T) {
	out, result := runStream(t, "meta.createmeta",
		map[string]string{"project": "ENG", "type": "Bug"}, nil,
		registry.Limit{All: true}, dcCreateMetaJSON)

	if !result.Complete {
		t.Error("a whole field list was reported incomplete")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), out)
	}
	if lines[0] != "id\tname\trequired\ttype" {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "summary\tSummary\ttrue\tstring") {
		t.Errorf("the required field is not first: %q", lines[1])
	}
}

// TestLimitTruncatesAndSaysSo covers the one thing that would be a silent lie:
// both endpoints answer whole, so a caller's --limit is applied here, and a cut
// result is never reported as complete.
func TestLimitTruncatesAndSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name, command, body string
		flags               map[string]string
		args                []string
	}{
		{
			"transitions", "meta.transitions", transitionsJSON,
			map[string]string{},
			[]string{"ENG-101"},
		},
		{
			"createmeta", "meta.createmeta", dcCreateMetaJSON,
			map[string]string{"project": "ENG", "type": "Bug"},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, result := runStream(t, tc.command, tc.flags, tc.args,
				registry.Limit{N: 1}, tc.body)

			if result.Complete {
				t.Error("a truncated result was reported complete")
			}
			if rows := strings.Count(strings.TrimRight(out, "\n"), "\n"); rows != 1 {
				t.Errorf("got %d rows, want 1:\n%s", rows, out)
			}
			// Neither endpoint has a cursor, so there is nothing to resume
			// from. A token that meant nothing would be worse than none.
			if result.NextPageToken != "" {
				t.Errorf("a page token was invented: %q", result.NextPageToken)
			}
		})
	}
}

func TestTransitionsDocIsWellFormed(t *testing.T) {
	transitions, err := site.FetchTransitions(t.Context(),
		&stubDoer{body: transitionsJSON}, site.Info{Kind: site.Cloud}, "ENG-101")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	doc := meta.TransitionsDoc(transitions, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var xml strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`<transition id="2"`,
		`<name>Close Issue</name>`,
		`<to id="6" category="done">Closed</to>`,
		`<field id="resolution" required="true"`,
		`<allowed-value>Fixed</allowed-value>`,
	} {
		if !strings.Contains(xml.String(), want) {
			t.Errorf("the output does not contain %s:\n%s", want, xml.String())
		}
	}
}

// TestBothCommandsDeclarePartial keeps the truncation contract declared as well
// as implemented.
func TestBothCommandsDeclarePartial(t *testing.T) {
	for _, name := range []string{"meta.transitions", "meta.createmeta"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !cmd.Paginated {
			t.Errorf("%s bounds its output but is not paginated", name)
		}
		found := false
		for _, code := range cmd.AllExitCodes() {
			if code == exitcode.Partial {
				found = true
			}
		}
		if !found {
			t.Errorf("%s can truncate but does not declare exit 3", name)
		}
		if cmd.Mutating || cmd.Destructive {
			t.Errorf("%s is read-only but declares otherwise", name)
		}
	}
}

// runStream executes a registered command against a stubbed transport. It goes
// through the registry rather than calling the stream function directly,
// because the command as registered is what a user actually invokes.
func runStream(
	t *testing.T, name string, flags map[string]string, args []string,
	limit registry.Limit, body string,
) (string, registry.StreamResult) {
	t.Helper()

	cmd, ok := registry.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}

	parsed := registry.NewFlags()
	for k, v := range flags {
		parsed.SetString(k, v)
	}

	inv := &registry.Invocation{
		Jira:     &stubSession{doer: &stubDoer{body: body}},
		Args:     args,
		Flags:    parsed,
		Limit:    limit,
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
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

// stubSession is a registry.Session backed by a stubbed transport, so a command
// is exercised with no auth, no config, and no network.
type stubSession struct {
	doer   *stubDoer
	record *recordingDoer
	meta   *site.Metadata
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return nil, site.Info{Kind: site.DataCenter}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	if s.meta == nil {
		var client site.Doer = s.doer
		if s.record != nil {
			client = s.record
		}
		s.meta = &site.Metadata{Client: client, Info: site.Info{Kind: site.DataCenter}}
	}
	return s.meta, nil
}

func (s *stubSession) Project() string                 { return "ENG" }
func (s *stubSession) RequireProject() (string, error) { return "ENG", nil }
func (s *stubSession) Board() string                   { return "" }

// RequireBoard is what an agile command calls. None of these fixtures set a
// board, so it fails the way the real session does rather than returning one.
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

// TestBadIssueKeyIsRefusedBeforeAnyRequest covers the local check. A 404 for a
// malformed key reads like a missing issue rather than a typo, and the value
// reaches a URL path — so it is refused here, before the stream opens and its
// header is written.
func TestBadIssueKeyIsRefusedBeforeAnyRequest(t *testing.T) {
	cmd, ok := registry.Lookup("meta.transitions")
	if !ok {
		t.Fatal("meta transitions is not registered")
	}

	for _, bad := range []string{
		"", "foo", "ENG", "ENG-", "-101", "ENG_101", "../../admin",
		"ENG-101/../../admin", "eng 101", "ENG-1.5",
	} {
		doer := &stubDoer{body: transitionsJSON}
		inv := &registry.Invocation{
			Jira: &stubSession{doer: doer}, Args: []string{bad},
			Flags: registry.NewFlags(),
		}
		err := cmd.Validate(t.Context(), inv)
		if err == nil {
			t.Errorf("Validate(%q) accepted a malformed key", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
		if code := errs.Coerce(err).Code; code != "INVALID_KEY" {
			t.Errorf("%q code = %q, want INVALID_KEY", bad, code)
		}
		// Nothing was sent, so a typo costs no round trip.
		if doer.calls != 0 {
			t.Errorf("%q reached the network %d times", bad, doer.calls)
		}
	}

	// A key with no arguments at all is refused rather than panicking on
	// Args[0], because Validate runs before cobra's arity check in some paths.
	inv := &registry.Invocation{Flags: registry.NewFlags()}
	if err := cmd.Validate(t.Context(), inv); err == nil {
		t.Error("a missing key was accepted")
	}
}

func TestGoodIssueKeysAreAccepted(t *testing.T) {
	cmd, _ := registry.Lookup("meta.transitions")
	for _, ok := range []string{"ENG-1", "ENG-101", "A-1", "PROJ_X-42", "eng-1"} {
		inv := &registry.Invocation{Args: []string{ok}, Flags: registry.NewFlags()}
		if err := cmd.Validate(t.Context(), inv); err != nil {
			t.Errorf("Validate(%q) = %v", ok, err)
		}
	}
}

// TestCreateMetaDefaultsToTheContextProject covers --project being a default
// rather than a requirement, and the refusal when there is no default either.
func TestCreateMetaDefaultsToTheContextProject(t *testing.T) {
	cmd, ok := registry.Lookup("meta.createmeta")
	if !ok {
		t.Fatal("meta createmeta is not registered")
	}

	// No --project: the context's project is used.
	doer := &recordingDoer{body: dcCreateMetaJSON}
	flags := registry.NewFlags()
	flags.SetString("type", "Bug")
	inv := &registry.Invocation{
		Jira:  &stubSession{doer: &stubDoer{body: dcCreateMetaJSON}, record: doer},
		Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := cmd.Stream(t.Context(), inv, stream); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := doer.query.Get("projectKeys"); got != "ENG" {
		t.Errorf("asked for project %q, want the context's ENG", got)
	}
}

// TestCommandsWithoutASessionFailLoudly covers the guard every command carries.
// A resource that dereferenced a nil session would panic in production rather
// than fail in its own tests.
func TestCommandsWithoutASessionFailLoudly(t *testing.T) {
	for _, name := range []string{"meta.transitions", "meta.createmeta"} {
		cmd, _ := registry.Lookup(name)
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
			t.Errorf("%s ran without a session", name)
		} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
			t.Errorf("%s code = %q, want NO_SESSION", name, code)
		}
	}
}

func TestCreateMetaDocIsWellFormed(t *testing.T) {
	created, err := site.FetchCreateMeta(t.Context(),
		&stubDoer{body: dcCreateMetaJSON}, site.Info{Kind: site.DataCenter}, "ENG", "Bug")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	doc := meta.CreateMetaDoc(created, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var xml strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`<field id="summary" required="true"`,
		`<name>Summary</name>`,
		`<type>string</type>`,
		`has-default="true"`,
		`<allowed-value>High</allowed-value>`,
	} {
		if !strings.Contains(xml.String(), want) {
			t.Errorf("the output does not contain %s:\n%s", want, xml.String())
		}
	}
}

// recordingDoer remembers the last query it was asked for, so a test can assert
// what reached the wire rather than only what came back.
type recordingDoer struct {
	body  string
	query url.Values
	calls int
}

func (r *recordingDoer) Do(
	_ context.Context, req transport.Request,
) (*transport.Response, error) {
	r.calls++
	r.query = req.Query
	return &transport.Response{
		Status: 200,
		Body:   []byte(r.body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}
