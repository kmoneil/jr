//go:build write

package workflow_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
	"github.com/kmoneil/jr/internal/workflow"
)

// TestTheThreeVerbsSendWhatTheySay pins the endpoint and the body of each move.
// The cassettes match on both, so a wrong path or a reordered body fails here
// rather than against a real instance.
//
// Every Cloud row here is a recording. That matters more than the coverage: the
// three Data Center rows are constructed, and a constructed cassette proves a
// request is unchanged and never that it was right — which is exactly how the
// Cloud `epic.add` row spent two releases asserting a request Jira refuses for
// the majority of Cloud projects. See
// TestTheCloudEpicPathNeverCallsTheAgileEndpoint.
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
			// Two issues and two requests: on Cloud an epic move is the parent
			// field, which has no batched spelling.
			command: "epic.add", fixture: "epicadd", kind: site.Cloud,
			args:      []string{"AGL-1", "AGL-2", "AGL-3"},
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
			args:      []string{"AGL-2", "AGL-3"},
			container: "epic", id: "none", action: "removed",
		},
		{
			// The project style the old endpoint refused. Same verb, same
			// arguments shape, no branch in the code — which is the claim.
			command: "epic.add", fixture: "epicadd-nextgen-parent", kind: site.Cloud,
			args:      []string{"OPS-5", "OPS-10"},
			container: "epic", id: "OPS-5", action: "added",
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

// TestTheCloudEpicPathNeverCallsTheAgileEndpoint is what stops this being
// reinstated, and it asserts the request rather than the outcome.
//
// Cloud refuses `POST /rest/agile/1.0/epic/{ref}/issue` when the issue belongs
// to a team-managed (next-gen) project — the default for every project created
// on a Cloud site. Both epic verbs called it anyway for two releases, behind a
// constructed cassette that reported the request well-formed.
//
// The replacement has no branch on project style, so the way it regresses is
// somebody reaching for the batched endpoint again because one request is
// cheaper than three. This fails the moment they do.
func TestTheCloudEpicPathNeverCallsTheAgileEndpoint(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"epic.add", []string{"OPS-5", "OPS-10", "OPS-11"}},
		{"epic.remove", []string{"OPS-10", "OPS-11"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd, ok := registry.Lookup(tc.command)
			if !ok {
				t.Fatalf("%s is not registered", tc.command)
			}
			flags := registry.NewFlags()
			flags.SetBool("dry-run", true)
			doc, err := cmd.Run(t.Context(), &registry.Invocation{
				Jira: &stubSession{kind: site.Cloud}, Args: tc.args, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			})
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}

			// One request per issue, because the parent field has no batched
			// spelling. A count of one here would mean the batch came back.
			requests := doc.Record.Children
			want := len(tc.args)
			if tc.command == "epic.add" {
				want-- // the first argument is the epic, not an issue.
			}
			if len(requests) != want {
				t.Fatalf("%d requests, want one per issue (%d)", len(requests), want)
			}
			for _, r := range requests {
				path, _ := r.AttrValue("path")
				if strings.Contains(path, "/rest/agile/") {
					t.Errorf("path = %q, and the agile epic endpoint is refused "+
						"for team-managed projects; see epicadd-nextgen.cloud.json",
						path)
				}
				if method, _ := r.AttrValue("method"); method != transport.MethodPut {
					t.Errorf("method = %q, want PUT of the parent field", method)
				}
			}
		})
	}
}

// TestTheRefusalThatCausedTheChangeIsStillOnRecord keeps the evidence
// load-bearing.
//
// epicadd-nextgen.cloud.json is a request this tool no longer makes, so nothing
// drives it through a command any more, and an unread cassette is a file
// somebody deletes during a tidy-up. Reading it here means the reason the
// endpoint was replaced cannot quietly leave the tree — and it is the reason,
// in Jira's own words, rather than in a comment restating them.
func TestTheRefusalThatCausedTheChangeIsStillOnRecord(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "epicadd-nextgen.cloud.json"),
	)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cassette.Evidence() {
		t.Fatal("the refusal is not a recording, so it is somebody's account " +
			"of a refusal rather than one")
	}
	if len(cassette.Interactions) != 1 {
		t.Fatalf("got %d interactions, want the one refused request",
			len(cassette.Interactions))
	}

	it := cassette.Interactions[0]
	if !strings.Contains(it.Request.Path, "/rest/agile/") {
		t.Errorf("path = %q, want the agile endpoint this replaced", it.Request.Path)
	}
	if it.Response.Status != 400 {
		t.Errorf("status = %d, want the 400 that made the endpoint unusable",
			it.Response.Status)
	}
	// Jira names the way out in the body, and that route is what the fix took.
	if !strings.Contains(it.Response.Body, "parent") {
		t.Errorf("the recorded refusal no longer names the parent property: %s",
			it.Response.Body)
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
	// v2 wraps every preview in a list, including a preview of one request. A
	// shape that varied with the count would be a shape every consumer has to
	// branch on.
	if len(doc.Record.Children) != 1 {
		t.Fatalf("got %d requests, want the one this sends", len(doc.Record.Children))
	}
	request := doc.Record.Children[0]
	if path, _ := request.AttrValue("path"); path != "/rest/agile/1.0/epic/none/issue" {
		t.Errorf("path = %q, want the agile API", path)
	}
	body, ok := request.ChildNamed("body")
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

// TestAHalfAppliedMoveSaysExactlyWhereItGotTo is the case the Cloud path
// created and the batched endpoint could not have.
//
// One request per issue means a run can stop in the middle. What the caller
// must never be given is a bare PERMISSION, because the safe reading of that —
// nothing happened — is the false one, and the issue that did move stays moved.
//
// So all three facts are asserted: which issue moved, which was refused, and
// which was never sent at all. The last is a separate status from "failed" on
// purpose: "Jira refused this" and "nobody asked Jira" want different retries.
func TestAHalfAppliedMoveSaysExactlyWhereItGotTo(t *testing.T) {
	cmd, ok := registry.Lookup("epic.add")
	if !ok {
		t.Fatal("epic add is not registered")
	}
	conn, replayer := replayConn(t, "epicadd-partial.cloud.json")

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.Cloud},
		Args:  []string{"AGL-1", "AGL-2", "AGL-3", "AGL-4"},
		Flags: registry.NewFlags(), Stderr: io.Discard,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("a run that was refused halfway reported success")
	}

	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("err = %T, want a PartiallyApplied carrying the document; "+
			"without it the CLI has nothing to write and the caller cannot "+
			"learn that AGL-2 moved", err)
	}
	// The exit is the cause's, which is what a caller branches on. Coerce
	// traverses Unwrap, so wrapping must not hide it.
	if code := errs.Coerce(err).Code; code != "FORBIDDEN" && code != "PERMISSION_DENIED" {
		t.Logf("code = %q", code)
	}
	if exit := errs.Coerce(err).Exit; exit != exitcode.Permission {
		t.Errorf("exit = %v, want %v — the failing request's own",
			exit, exitcode.Permission)
	}

	record := partial.Doc.Record
	if got, _ := record.AttrValue("requested"); got != "3" {
		t.Errorf("requested = %q, want 3", got)
	}
	if got, _ := record.AttrValue("applied"); got != "1" {
		t.Errorf("applied = %q, want 1", got)
	}

	issues, ok := record.ChildNamed("issues")
	if !ok {
		t.Fatal("the document named no issues")
	}
	want := []struct{ key, status string }{
		{"AGL-2", workflow.StatusMoved},
		{"AGL-3", workflow.StatusFailed},
		{"AGL-4", workflow.StatusNotAttempted},
	}
	if len(issues.Children) != len(want) {
		t.Fatalf("got %d issues, want %d", len(issues.Children), len(want))
	}
	for i, w := range want {
		key, _ := issues.Children[i].AttrValue("key")
		status, _ := issues.Children[i].AttrValue("status")
		if key != w.key || status != w.status {
			t.Errorf("issue %d = %s/%s, want %s/%s", i, key, status, w.key, w.status)
		}
	}

	// The third PUT was never built into a sent request. The cassette has only
	// two interactions, so this is the replayer confirming nothing else went
	// out rather than the code reporting on itself.
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request went out after the failure: %v", unmatched)
	}
}

// TestTheDocumentAHalfAppliedMoveCarriesIsDeclared covers the part that would
// otherwise be found by running it: a document the command does not declare is
// refused at the CLI layer, and this one takes a different route to stdout than
// every other result.
func TestTheDocumentAHalfAppliedMoveCarriesIsDeclared(t *testing.T) {
	cmd, _ := registry.Lookup("epic.add")
	conn, _ := replayConn(t, "epicadd-partial.cloud.json")

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: site.Cloud},
		Args:  []string{"AGL-1", "AGL-2", "AGL-3"},
		Flags: registry.NewFlags(), Stderr: io.Discard,
		Progress: registry.NoProgress,
	})
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("err = %T, want PartiallyApplied", err)
	}
	if !cmd.Emits(partial.Doc.Kind, partial.Doc.Version) {
		t.Errorf("epic add does not declare kind %q v%d, so the CLI would "+
			"refuse to write it and the caller would learn nothing",
			partial.Doc.Kind, partial.Doc.Version)
	}
}
