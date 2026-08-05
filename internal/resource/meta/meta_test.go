package meta_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/exitcode"
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
	doer *stubDoer
	meta *site.Metadata
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return nil, site.Info{Kind: site.DataCenter}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	if s.meta == nil {
		s.meta = &site.Metadata{Client: s.doer, Info: site.Info{Kind: site.DataCenter}}
	}
	return s.meta, nil
}

func (s *stubSession) Project() string                 { return "ENG" }
func (s *stubSession) RequireProject() (string, error) { return "ENG", nil }
func (s *stubSession) Board() string                   { return "" }
func (s *stubSession) CheckWritable(string) error      { return nil }

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
