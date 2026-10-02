//go:build write

package workflow_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// fakeJira is a Data Center that answers the reads a sequence plan makes, its
// own and its steps', from a table, and records every request, so a test can
// assert that planning sent nothing but reads.
type fakeJira struct {
	mu     sync.Mutex
	routes map[string]string
	// status overrides the 200 a route answers with.
	status map[string]int
	seen   []string
}

func newFakeJira() *fakeJira {
	return &fakeJira{routes: map[string]string{
		"GET /rest/api/2/issue/ENG-1": `{"key":"ENG-1","fields":{"updated":"2026-10-02T12:58:41.490+0000"}}`,
		"GET /rest/api/2/issue/ENG-2": `{"key":"ENG-2","fields":{"status":{"name":"To Do"}}}`,
		"GET /rest/api/2/issue/ENG-1/editmeta": `{"fields":{"summary":{"name":"Summary"},` +
			`"labels":{"name":"Labels"}}}`,
		"GET /rest/api/2/issue/ENG-1/transitions": `{"transitions":[` +
			`{"id":"21","name":"In Progress","to":{"id":"3","name":"In Progress",` +
			`"statusCategory":{"key":"indeterminate"}}},` +
			`{"id":"31","name":"Done","to":{"id":"10002","name":"Done",` +
			`"statusCategory":{"key":"done"}}}]}`,
		"GET /rest/api/2/mypermissions": `{"permissions":{` +
			`"EDIT_ISSUES":{"havePermission":true},"ASSIGN_ISSUES":{"havePermission":true},` +
			`"ADD_COMMENTS":{"havePermission":true},"LINK_ISSUES":{"havePermission":true},` +
			`"SCHEDULE_ISSUES":{"havePermission":true},"TRANSITION_ISSUES":{"havePermission":true},` +
			`"RESOLVE_ISSUES":{"havePermission":true}}}`,
		"GET /rest/api/2/issueLinkType": `{"issueLinkTypes":[{"id":"10000","name":"Blocks",` +
			`"inward":"is blocked by","outward":"blocks"}]}`,
		"GET /rest/api/2/user/search": `[{"name":"ada","key":"ada","displayName":"Ada Lovelace",` +
			`"emailAddress":"ada@example.invalid","active":true}]`,
		"GET /rest/api/2/user": `{"name":"ada","key":"ada","displayName":"Ada Lovelace",` +
			`"emailAddress":"ada@example.invalid","active":true}`,
		"GET /rest/api/2/user/assignable/search": `[{"name":"ada","key":"ada","active":true}]`,
		"GET /rest/agile/1.0/sprint/7":           `{"id":7,"state":"active","name":"Sprint 7","originBoardId":1}`,
		// The writes every step type sends, so an apply has somewhere to land.
		"POST /rest/api/2/issue/ENG-1/comment":     `{"id":"10100","body":"x"}`,
		"PUT /rest/api/2/issue/ENG-1":              ``,
		"PUT /rest/api/2/issue/ENG-1/assignee":     ``,
		"POST /rest/api/2/issueLink":               ``,
		"POST /rest/agile/1.0/sprint/7/issue":      ``,
		"POST /rest/api/2/issue/ENG-1/transitions": ``,
	}, status: map[string]int{
		"POST /rest/api/2/issue/ENG-1/comment":     http.StatusCreated,
		"PUT /rest/api/2/issue/ENG-1":              http.StatusNoContent,
		"PUT /rest/api/2/issue/ENG-1/assignee":     http.StatusNoContent,
		"POST /rest/api/2/issueLink":               http.StatusCreated,
		"POST /rest/agile/1.0/sprint/7/issue":      http.StatusNoContent,
		"POST /rest/api/2/issue/ENG-1/transitions": http.StatusNoContent,
	}}
}

func (f *fakeJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.seen = append(f.seen, route+"?"+r.URL.RawQuery)
	body, ok := f.routes[route]
	status := f.status[route]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errorMessages":["no route in the fake for `+route+`"],"errors":{}}`)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	_, _ = io.WriteString(w, body)
}

// writes is every request that reached the fake and was not a read.
func (f *fakeJira) writes() []string {
	var out []string
	for _, r := range f.requests() {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeJira) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

// seqSession is a registry.Session over the fake, with metadata that reads
// through the same connection, as the steps' own Validate expects.
type seqSession struct {
	conn   *transport.Client
	ledger *idem.Ledger
	// kind is the deployment, Data Center unless a test says otherwise.
	kind site.Kind
}

func (s *seqSession) info() site.Info {
	if s.kind == "" {
		return site.Info{Kind: site.DataCenter}
	}
	return site.Info{Kind: s.kind}
}

func (s *seqSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, s.info(), nil
}

func (s *seqSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Client: s.conn, Info: s.info()}, nil
}

func (s *seqSession) Site() string                    { return "https://sequence.invalid" }
func (s *seqSession) Idempotency() *idem.Ledger       { return s.ledger }
func (s *seqSession) Project() string                 { return "ENG" }
func (s *seqSession) Board() string                   { return "" }
func (s *seqSession) CheckWritable(string) error      { return nil }
func (s *seqSession) RequireProject() (string, error) { return "ENG", nil }
func (s *seqSession) Fields() []string                { return nil }
func (s *seqSession) RequireBoard() (string, error)   { return "", nil }

// planSequence runs issue sequence --plan-out, Validate and body, against the
// fake, and returns the document, the plan's path and what reached the fake.
func planSequence(
	t *testing.T, fake *fakeJira, steps string, extra map[string]string,
) (*render.Doc, string, []string, error) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	conn, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: -1})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	cmd, ok := registry.Lookup("issue.sequence")
	if !ok {
		t.Fatal("issue sequence is not registered")
	}
	path := filepath.Join(t.TempDir(), "plan.xml")
	flags := registry.NewFlags()
	flags.SetString("steps", steps)
	flags.SetString("plan-out", path)
	for name, value := range extra {
		if name == "dry-run" {
			flags.SetBool(name, value == "true")
			continue
		}
		flags.SetString(name, value)
	}
	inv := &registry.Invocation{
		Jira: &seqSession{conn: conn}, Args: []string{"ENG-1"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		return nil, path, fake.requests(), err
	}
	doc, err := cmd.Run(t.Context(), inv)
	return doc, path, fake.requests(), err
}

const everyStep = `[
	["issue", "comment", "add", "ENG-1", "Shipped in 1.4."],
	["issue", "edit", "ENG-1", "--summary", "Shipped"],
	["issue", "assign", "ENG-1", "ada"],
	["issue", "link", "add", "ENG-1", "blocks", "ENG-2"],
	["sprint", "add", "7", "ENG-1"],
	["issue", "move", "ENG-1", "Done"]
]`

// TestAPlanSendsNothingAndHoldsWhatEachStepWouldSend is issue 157's plan time:
// every step dry-run as its own command and checked, one document holding the
// request each step resolved to, and nothing but reads reaching Jira.
func TestAPlanSendsNothingAndHoldsWhatEachStepWouldSend(t *testing.T) {
	doc, path, seen, err := planSequence(t, newFakeJira(), everyStep, nil)
	if err != nil {
		t.Fatalf("plan refused: %v\nrequests: %v", err, seen)
	}
	for _, r := range seen {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("planning sent %s, and it sends nothing but reads", r)
		}
	}
	if doc.Kind != workflow.KindSequencePlan {
		t.Fatalf("kind = %s", doc.Kind)
	}
	if got, _ := doc.Record.AttrValue("precondition"); got == "" {
		t.Error("the plan carries no baseline")
	}
	steps, _ := doc.Record.ChildNamed("steps")
	if len(steps.Children) != 6 {
		t.Fatalf("%d steps in the plan, want 6", len(steps.Children))
	}
	keys := map[string]bool{}
	for i, step := range steps.Children {
		k, _ := step.AttrValue("idempotency-key")
		if keys[k] {
			t.Errorf("step %d shares its idempotency key", i+1)
		}
		keys[k] = true
		requests, ok := step.ChildNamed("requests")
		if !ok || len(requests.Children) == 0 {
			t.Errorf("step %d holds no request", i+1)
		}
	}
	// What the move resolved to is in the plan: the transition's id, not its
	// name, so the apply sends what was checked.
	move := steps.Children[5]
	requests, _ := move.ChildNamed("requests")
	body, _ := requests.Children[0].ChildNamed("body")
	if !strings.Contains(body.Text, `"31"`) {
		t.Errorf("the move's request does not carry the resolved transition id: %s", body.Text)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the test's own temporary file.
	if err != nil {
		t.Fatalf("the plan file was not written: %v", err)
	}
	var probe struct {
		Kind string `xml:"kind,attr"`
	}
	if err := xml.Unmarshal(raw, &probe); err != nil || probe.Kind != workflow.KindSequencePlan {
		t.Errorf("the plan file is not a sequence plan: %v %q", err, probe.Kind)
	}
}

// TestTheStepsCanComeFromAFile is --steps-file, which a caller writing a
// sequence by hand reaches for before the inline form.
func TestTheStepsCanComeFromAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close.json")
	if err := os.WriteFile(path, []byte(everyStep), 0o600); err != nil {
		t.Fatalf("write steps: %v", err)
	}
	doc, _, _, err := planSequence(t, newFakeJira(), "", map[string]string{"steps-file": path})
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if steps, _ := doc.Record.ChildNamed("steps"); len(steps.Children) != 6 {
		t.Errorf("planned %d steps from the file, want 6", len(steps.Children))
	}
}

// TestABlockedStepRefusesThePlanAndNamesEveryOther is the refusal: one plan
// that cannot run is refused with nothing written, under the first blocked
// step's own code, and the detail names every blocked step so one fix cycle
// finds them all.
func TestABlockedStepRefusesThePlanAndNamesEveryOther(t *testing.T) {
	fake := newFakeJira()
	fake.routes["GET /rest/api/2/issue/ENG-1/editmeta"] = `{"fields":{"labels":{}}}`
	_, path, _, err := planSequence(t, fake, `[
		["issue", "edit", "ENG-1", "--summary", "Shipped"],
		["issue", "comment", "add", "ENG-1", "x"],
		["issue", "move", "ENG-1", "Closed"]
	]`, nil)
	if err == nil {
		t.Fatal("a plan with two blocked steps was written")
	}
	e := errs.Coerce(err)
	if e.Code != "FIELD_NOT_ON_SCREEN" || !strings.HasPrefix(e.Message, "step 1: ") {
		t.Errorf("refused as %s %q, want the first blocked step's own refusal", e.Code, e.Message)
	}
	if !strings.Contains(e.Detail, "step 1") || !strings.Contains(e.Detail, "step 3") ||
		strings.Contains(e.Detail, "step 2") {
		t.Errorf("detail %q does not name exactly the blocked steps", e.Detail)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a refused plan was written anyway")
	}
}

// TestEachCheckBeyondTheDryRunBlocksItsStep is one row per check the commands
// do not make, each the case a sequence would otherwise find half way through.
func TestEachCheckBeyondTheDryRunBlocksItsStep(t *testing.T) {
	for _, tc := range []struct {
		name, route, body, steps, code string
		exit                           exitcode.Code
	}{
		{
			"a permission the account lacks", "GET /rest/api/2/mypermissions",
			`{"permissions":{"ADD_COMMENTS":{"havePermission":false}}}`,
			`[["issue", "comment", "add", "ENG-1", "x"]]`, "PERMISSION_DENIED", exitcode.Permission,
		},
		{
			// The prefix trap: Data Center's username search answers "ada"
			// with "adam", and a non-empty answer is not a yes.
			"an assignee the issue does not take", "GET /rest/api/2/user/assignable/search",
			`[{"name":"adam","active":true}]`,
			`[["issue", "assign", "ENG-1", "ada"]]`, "USER_NOT_ASSIGNABLE", exitcode.Usage,
		},
		{
			"a sprint that is closed", "GET /rest/agile/1.0/sprint/7",
			`{"id":7,"state":"closed","name":"Sprint 7"}`,
			`[["sprint", "add", "7", "ENG-1"]]`, "SPRINT_CLOSED", exitcode.Conflict,
		},
		{
			"a link to an issue nobody can read", "GET /rest/api/2/issue/ENG-2", "",
			`[["issue", "link", "add", "ENG-1", "blocks", "ENG-2"]]`, "UNKNOWN_ISSUE", exitcode.NotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeJira()
			if tc.body == "" {
				delete(fake.routes, tc.route)
			} else {
				fake.routes[tc.route] = tc.body
			}
			_, _, seen, err := planSequence(t, fake, tc.steps, nil)
			if err == nil {
				t.Fatalf("planned anyway; requests: %v", seen)
			}
			if e := errs.Coerce(err); e.Code != tc.code || e.Exit != tc.exit {
				t.Errorf("refused as %s (exit %v), want %s (exit %v): %s",
					e.Code, e.Exit, tc.code, tc.exit, e.Message)
			}
		})
	}
}

// TestASequenceRefusesWhatItCannotPlanBeforeReadingAnything is the shape of
// the invocation: every refusal here costs no request.
func TestASequenceRefusesWhatItCannotPlanBeforeReadingAnything(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]string
		steps string
		code  string
	}{
		{"no plan", map[string]string{"plan-out": ""}, everyStep, "SEQUENCE_NEEDS_A_PLAN"},
		{
			"a dry run beside the plan",
			map[string]string{"dry-run": "true"},
			everyStep,
			"CONFLICTING_PLAN_FLAGS",
		},
		{"both kinds of steps", map[string]string{"steps-file": "close.json"}, everyStep, "STEPS_AND_STEPS_FILE"},
		{"no steps", nil, "", "NO_STEPS"},
		{
			"a description from stdin", nil,
			`[["issue", "edit", "ENG-1", "--description-file", "-"]]`, "STEP_READS_STDIN",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, seen, err := planSequence(t, newFakeJira(), tc.steps, tc.extra)
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("refused as %q (%v), want %s", code, err, tc.code)
			}
			if len(seen) > 0 {
				t.Errorf("a refusal the text decides made requests: %v", seen)
			}
		})
	}
}
