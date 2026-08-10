//go:build write

package workflow_test

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
	"github.com/kmoneil/jira-cli/internal/workflow"
)

// TestTheThreeVerbsSendWhatTheySay pins the endpoint and the body of each move.
// The cassettes match on both, so a wrong path or a reordered body fails here
// rather than against a real instance.
//
// Every Cloud row here is a recording. That matters more than the coverage: the
// three Data Center rows are constructed, and a constructed cassette proves a
// request is unchanged and never that it was right — which is exactly how the
// Cloud `epic.add` row spent two releases asserting a request Jira refuses for
// the majority of Cloud projects. See TestTheAgileEpicEndpointIsCompanyManagedOnly.
func TestTheThreeVerbsSendWhatTheySay(t *testing.T) {
	for _, tc := range []struct {
		command, fixture string
		kind             site.Kind
		args             []string
		container        string
		id               string
		action           string
	}{
		{
			command: "sprint.add", fixture: "sprintadd", kind: site.DataCenter,
			args:      []string{"128", "ENG-1", "ENG-2"},
			container: "sprint", id: "128", action: "added",
		},
		{
			command: "sprint.add", fixture: "sprintadd", kind: site.Cloud,
			args:      []string{"1", "AGL-4", "AGL-5"},
			container: "sprint", id: "1", action: "added",
		},
		{
			command: "epic.add", fixture: "epicadd", kind: site.Cloud,
			args:      []string{"AGL-1", "AGL-2"},
			container: "epic", id: "AGL-1", action: "added",
		},
		{
			// Leaving an epic is spelled as joining the absence of one. There
			// is no DELETE, and the caller never names the epic they are in.
			command: "epic.remove", fixture: "epicremove", kind: site.DataCenter,
			args:      []string{"ENG-101", "ENG-102"},
			container: "epic", id: "none", action: "removed",
		},
		{
			command: "epic.remove", fixture: "epicremove", kind: site.Cloud,
			args:      []string{"AGL-2"},
			container: "epic", id: "none", action: "removed",
		},
	} {
		t.Run(tc.command+"/"+string(tc.kind), func(t *testing.T) {
			doc, replayer := runVerb(t, tc.command, tc.fixture, tc.kind, tc.args, false)

			if doc.Record.Name != tc.container {
				t.Errorf("element = %q, want %q", doc.Record.Name, tc.container)
			}
			if id, _ := doc.Record.AttrValue("id"); id != tc.id {
				t.Errorf("id = %q, want %q", id, tc.id)
			}
			if action, _ := doc.Record.AttrValue("action"); action != tc.action {
				t.Errorf("action = %q, want %q", action, tc.action)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the request was never sent: %v", unplayed)
			}
			if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
				t.Errorf("a request went somewhere the cassette does not answer: %v",
					unmatched)
			}
		})
	}
}

// TestTheAgileEpicEndpointIsCompanyManagedOnly pins the limit that no
// constructed cassette contained, because whoever wrote one did not know it was
// there.
//
// Cloud refuses `POST /rest/agile/1.0/epic/{ref}/issue` when the issue belongs
// to a team-managed (next-gen) project, which is the default for every project
// created on a Cloud site. This tool sends it anyway — nothing in
// internal/workflow branches on project style — so `epic add` and `epic remove`
// fail for most Cloud callers while epicadd.cloud.json reported the request
// well-formed.
//
// The refusal is a fixture rather than a sentence in a comment so that
// reinstating the endpoint for every project fails a test. Jira's own message
// names the route that does work, and this tool cannot take it: `issue edit`
// has no --parent.
func TestTheAgileEpicEndpointIsCompanyManagedOnly(t *testing.T) {
	cmd, ok := registry.Lookup("epic.add")
	if !ok {
		t.Fatal("epic add is not registered")
	}
	conn, replayer := replayConn(t, "epicadd-nextgen.cloud.json")

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud},
		Args: []string{"OPS-5", "OPS-10"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("a next-gen issue was reported as added to an epic")
	}
	if code := errs.Coerce(err).Code; code != "BAD_REQUEST" {
		t.Errorf("code = %q, want BAD_REQUEST", code)
	}
	// Jira names the way out in the body. Asserting it keeps the remedy in the
	// test rather than only in a backlog card.
	if detail := errs.Coerce(err).Detail; !strings.Contains(detail, "parent") {
		t.Errorf("detail = %q, want Jira's own remedy naming the parent property",
			detail)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the request was never sent: %v", unplayed)
	}
}

// TestTheAcknowledgementNamesEveryIssue covers what these verbs report. Jira
// answers all three with 204 and no body, so the result echoes what was asked
// for — re-reading the issues would be a second request whose answer could
// differ for reasons unrelated to this command.
func TestTheAcknowledgementNamesEveryIssue(t *testing.T) {
	doc, _ := runVerb(t, "sprint.add", "sprintadd", site.DataCenter,
		[]string{"128", "ENG-1", "ENG-2"}, false)

	issues, ok := doc.Record.ChildNamed("issues")
	if !ok {
		t.Fatal("the result named no issues")
	}
	var keys []string
	for _, child := range issues.Children {
		key, _ := child.AttrValue("key")
		keys = append(keys, key)
	}
	if strings.Join(keys, ",") != "ENG-1,ENG-2" {
		t.Errorf("issues = %v, want both keys in the order given", keys)
	}
}

// TestKeysAreCanonicalizedBeforeTheyAreSent covers the reason these verbs live
// in workflow at all: they use issue.ParseKey rather than a local copy, so a
// lowercase key reaches Jira in the form Jira uses.
func TestKeysAreCanonicalizedBeforeTheyAreSent(t *testing.T) {
	// The cassette records ENG-1 and ENG-2. Passing them in lowercase still
	// matches, which is only true if something upper-cased them on the way.
	_, replayer := runVerb(t, "sprint.add", "sprintadd", site.DataCenter,
		[]string{"128", "eng-1", "eng-2"}, false)
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a lowercase key was sent as written: %v", unmatched)
	}
}

// TestMoreThanTheCapIsRefusedRatherThanSplit is the "never approximate" rule at
// its most concrete. Two requests can half-succeed, and the result would be
// neither moved nor not moved — a state this tool has no honest way to report.
func TestMoreThanTheCapIsRefusedRatherThanSplit(t *testing.T) {
	args := []string{"128"}
	for i := 1; i <= workflow.MaxIssuesPerRequest+1; i++ {
		args = append(args, "ENG-"+strconv.Itoa(i))
	}

	cmd, _ := registry.Lookup("sprint.add")
	err := cmd.Validate(t.Context(), &registry.Invocation{
		Args: args, Flags: registry.NewFlags(), Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("more issues than the API accepts were allowed through")
	}
	e := errs.Coerce(err)
	if e.Code != "TOO_MANY_ISSUES" {
		t.Fatalf("code = %q, want TOO_MANY_ISSUES", e.Code)
	}
	if !strings.Contains(e.Remedy, "split") {
		t.Errorf("remedy = %q, want it to say the caller must split the run", e.Remedy)
	}

	// Exactly the cap is fine, so the boundary is not off by one.
	if err := cmd.Validate(t.Context(), &registry.Invocation{
		Args: args[:workflow.MaxIssuesPerRequest+1], Flags: registry.NewFlags(),
		Progress: registry.NoProgress,
	}); err != nil {
		t.Errorf("exactly %d issues were refused: %v", workflow.MaxIssuesPerRequest, err)
	}
}

// TestABadKeyIsRefusedBeforeAnythingIsSent covers the validation every one of
// these carries, so a typo costs no round trip.
func TestABadKeyIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		code    string
	}{
		{"sprint.add", []string{"128", "not-a-key"}, "INVALID_KEY"},
		{"sprint.add", []string{"ENG-1", "ENG-2"}, "INVALID_SPRINT_ID"},
		{"sprint.add", []string{"128"}, "NO_ISSUES"},
		{"epic.add", []string{"ENG-42", "nope"}, "INVALID_KEY"},
		{"epic.add", []string{"not an epic", "ENG-1"}, "INVALID_EPIC"},
		{"epic.add", []string{"ENG-42", "ENG-42"}, "SELF_EPIC"},
		{"epic.remove", []string{"ENG"}, "INVALID_KEY"},
		{"epic.remove", nil, "NO_ISSUES"},
	} {
		cmd, ok := registry.Lookup(tc.command)
		if !ok {
			t.Fatalf("%s is not registered", tc.command)
		}
		err := cmd.Validate(t.Context(), &registry.Invocation{
			Args: tc.args, Flags: registry.NewFlags(), Progress: registry.NoProgress,
		})
		if err == nil {
			t.Errorf("%s %v was accepted", tc.command, tc.args)
			continue
		}
		if code := errs.Coerce(err).Code; code != tc.code {
			t.Errorf("%s %v: code = %q, want %q", tc.command, tc.args, code, tc.code)
		}
	}
}

// TestDryRunPrintsTheRequestAndSendsNothing covers the flag every mutating verb
// carries.
func TestDryRunPrintsTheRequestAndSendsNothing(t *testing.T) {
	doc, replayer := runVerb(t, "epic.remove", "epicremove", site.DataCenter,
		[]string{"ENG-101", "ENG-102"}, true)

	if doc.Kind != registry.DryRunOutput().Kind {
		t.Errorf("kind = %q, want the dry-run kind", doc.Kind)
	}
	if path, _ := doc.Record.AttrValue("path"); path != "/rest/agile/1.0/epic/none/issue" {
		t.Errorf("path = %q, want the agile API", path)
	}
	body, ok := doc.Record.ChildNamed("body")
	if !ok {
		t.Fatal("the dry run printed no body")
	}
	// The bytes that would have gone out, not a description of them: a second
	// rendering of the request would drift from the first.
	if body.Text != `{"issues":["ENG-101","ENG-102"]}` {
		t.Errorf("body = %q", body.Text)
	}
	if len(replayer.Unplayed()) != 1 {
		t.Errorf("unplayed = %v, want the POST still untouched", replayer.Unplayed())
	}
}

// TestEveryVerbIsDeclaredMutating is what the CLI enforces read-only mode and
// the tag gate from. None of these is destructive: a move is reversible by
// moving back, and nothing is deleted.
func TestEveryVerbIsDeclaredMutating(t *testing.T) {
	for _, name := range []string{"sprint.add", "epic.add", "epic.remove"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !cmd.Mutating {
			t.Errorf("%s is not marked mutating, so read-only would not refuse it", name)
		}
		if cmd.Destructive {
			t.Errorf("%s is marked destructive; a move deletes nothing", name)
		}
		if len(cmd.RequiresTags) != 1 || cmd.RequiresTags[0] != "write" {
			t.Errorf("%s requires %v, want just write", name, cmd.RequiresTags)
		}
		found := false
		for _, f := range cmd.Flags {
			if f.Name == "dry-run" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not declare --dry-run", name)
		}
	}
}

// TestCommandsWithoutASessionFailLoudly covers the guard each one carries.
func TestCommandsWithoutASessionFailLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("epic.remove")
	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Args: []string{"ENG-1"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("epic remove ran without a session")
	}
	if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
}

func runVerb(
	t *testing.T, command, fixture string, kind site.Kind, args []string, dryRun bool,
) (*render.Doc, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup(command)
	if !ok {
		t.Fatalf("%s is not registered", command)
	}
	conn, replayer := replayConn(t, fixture+"."+string(kind)+".json")
	flags := registry.NewFlags()
	flags.SetBool("dry-run", dryRun)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: kind}, Args: args,
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return doc, replayer
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

func (s *stubSession) Idempotency() *idem.Ledger  { return nil }
func (s *stubSession) Project() string            { return "ENG" }
func (s *stubSession) Board() string              { return "" }
func (s *stubSession) CheckWritable(string) error { return nil }

func (s *stubSession) RequireProject() (string, error) { return "ENG", nil }

// Fields is the context default field set. Empty here: a stub that
// invented one would make every resource test assert a request nobody made.
func (s *stubSession) Fields() []string { return s.fields }

func (s *stubSession) RequireBoard() (string, error) {
	return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
}
