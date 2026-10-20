//go:build write

package workflow_test

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
	"github.com/kmoneil/jr/internal/workflow"
)

// reopen is issue 265's sequence: the move first, then the changes that
// belong to the reopened ticket.
const reopen = `[
	["issue", "move", "ENG-1", "Reopen Issue"],
	["issue", "edit", "ENG-1", "--summary", "Reopened"],
	["issue", "comment", "add", "ENG-1", "Reopening: it came back"]
]`

const (
	reopenRoute   = "POST /rest/api/2/issue/ENG-1/transitions"
	editmetaRoute = "GET /rest/api/2/issue/ENG-1/editmeta"
	openScreen    = `{"fields":{"summary":{"name":"Summary"}}}`
)

// closedFake is an issue in Closed, where the classic workflow's default
// `jira.issue.editable=false` leaves the edit screen empty and the one
// transition on offer is Reopen Issue. Once the transition lands, the screen
// is the reopened issue's.
func closedFake() *fakeJira {
	f := newFakeJira()
	f.routes[editmetaRoute] = `{"fields":{}}`
	f.routes["GET /rest/api/2/issue/ENG-1/transitions"] = `{"transitions":[` +
		`{"id":"3","name":"Reopen Issue","to":{"id":"4","name":"Reopened",` +
		`"statusCategory":{"key":"new"}}}]}`
	f.then = map[string]map[string]string{reopenRoute: {editmetaRoute: openScreen}}
	return f
}

// checksOf reads every step's checks off a plan document, in order.
func checksOf(t *testing.T, doc *render.Doc) [][]string {
	t.Helper()
	steps, ok := doc.Record.ChildNamed("steps")
	if !ok {
		t.Fatal("the plan has no steps")
	}
	var out [][]string
	for _, s := range steps.Children {
		var checks []string
		if list, ok := s.ChildNamed("checks"); ok {
			for _, c := range list.Children {
				checks = append(checks, c.Text)
			}
		}
		out = append(out, checks)
	}
	return out
}

// TestAReopenIsPlannedWithTheLaterChecksWaiting is plan time. Checked against
// the closed issue, the edit would be refused for an empty edit screen that
// says nothing about the reopened one, so the checks that read the status wait
// for the move, and the plan says which.
func TestAReopenIsPlannedWithTheLaterChecksWaiting(t *testing.T) {
	doc, _, seen, err := planSequence(t, closedFake(), reopen, nil)
	if err != nil {
		t.Fatalf("the reopen was refused at plan time: %v", err)
	}
	checks := checksOf(t, doc)
	if !slices.Contains(checks[1], "after step 1: EDIT_ISSUES, the edit screen") {
		t.Errorf("the edit's checks are %q, want them named as waiting for the move", checks[1])
	}
	if !slices.Contains(checks[2], "after step 1: ADD_COMMENTS") {
		t.Errorf("the comment's checks are %q, want them named as waiting for the move", checks[2])
	}
	if !slices.Contains(checks[0], "TRANSITION_ISSUES") {
		t.Errorf("the move's checks are %q, want its own permission checked now", checks[0])
	}
	for _, r := range seen {
		if strings.Contains(r, "/editmeta") {
			t.Errorf("planning read the closed issue's edit screen: %s", r)
		}
		if strings.Contains(r, "/mypermissions") && strings.Contains(r, "EDIT_ISSUES") {
			t.Errorf("planning asked for a permission the move has not reached: %s", r)
		}
	}
}

// TestAReopenAppliesWithTheChecksAfterTheMove is the apply: the move first,
// then the waiting checks against the reopened issue, then the rest. The
// screen read after the transition is the evidence the checks ran where they
// claim to.
func TestAReopenAppliesWithTheChecksAfterTheMove(t *testing.T) {
	_, path, _, err := planSequence(t, closedFake(), reopen, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	fake := closedFake()
	doc, err := applySequence(t, fake, newLedger(t), path, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := strings.Join(outcomesOf(t, doc), " "); got != "applied applied applied" {
		t.Errorf("outcomes = %s", got)
	}
	want := []string{reopenRoute, "PUT /rest/api/2/issue/ENG-1", "POST /rest/api/2/issue/ENG-1/comment"}
	if got := writePaths(fake); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	requests := fake.requests()
	moved := slices.IndexFunc(requests, func(r string) bool { return strings.HasPrefix(r, reopenRoute) })
	read := slices.IndexFunc(requests, func(r string) bool { return strings.HasPrefix(r, editmetaRoute) })
	if read < 0 || read < moved {
		t.Errorf("the edit screen was read at %d and the move sent at %d, want it read "+
			"after the move: %v", read, moved, requests)
	}
}

// TestAReopenStopsWhenTheReopenedIssueRefusesALaterStep is the case the
// re-check exists for: the issue reopened into a status whose edit screen does
// not hold the field. The run stops with the issue moved and nothing else
// sent, the step reported with the check's own code, and the same file
// resumes once the screen is fixed.
func TestAReopenStopsWhenTheReopenedIssueRefusesALaterStep(t *testing.T) {
	_, path, _, err := planSequence(t, closedFake(), reopen, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	ledger := newLedger(t)

	fake := closedFake()
	fake.then[reopenRoute] = map[string]string{editmetaRoute: `{"fields":{"labels":{}}}`}
	// What Jira says when the edit is sent anyway, so a run without the
	// re-check reaches an answer rather than a missing route.
	fake.status["PUT /rest/api/2/issue/ENG-1"] = http.StatusBadRequest
	fake.routes["PUT /rest/api/2/issue/ENG-1"] = `{"errorMessages":[],"errors":{"summary":` +
		`"Field 'summary' cannot be set. It is not on the appropriate screen, or unknown."}}`

	_, err = applySequence(t, fake, ledger, path, false)
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("want a partial apply, got %v", err)
	}
	if got := strings.Join(outcomesOf(t, partial.Doc), " "); got != "applied failed:FIELD_NOT_ON_SCREEN not-attempted" {
		t.Errorf("outcomes = %s", got)
	}
	if e := errs.Coerce(partial.Cause); e.Code != "FIELD_NOT_ON_SCREEN" || !strings.HasPrefix(e.Message, "step 2: ") {
		t.Errorf("the cause is %s %q, want step 2's own refusal", e.Code, e.Message)
	}
	if got := writePaths(fake); strings.Join(got, " ") != reopenRoute {
		t.Errorf("sent %v, want the move and nothing after it", got)
	}

	// Not fixed yet. A resume runs in the status the move led to, so nothing
	// waits: the step is refused before anything is sent, and not sent to find
	// out.
	still := closedFake()
	still.routes[editmetaRoute] = `{"fields":{"labels":{}}}`
	still.status["PUT /rest/api/2/issue/ENG-1"] = fake.status["PUT /rest/api/2/issue/ENG-1"]
	still.routes["PUT /rest/api/2/issue/ENG-1"] = fake.routes["PUT /rest/api/2/issue/ENG-1"]
	_, err = applySequence(t, still, ledger, path, false)
	if code := codeOf(err); code != "FIELD_NOT_ON_SCREEN" {
		t.Errorf("a resume into the same screen = %q (%v), want FIELD_NOT_ON_SCREEN", code, err)
	}
	if writes := still.writes(); len(writes) > 0 {
		t.Errorf("a resume that could not run sent %v", writes)
	}

	// Fixed: the reopened issue's screen holds the field now. The move is
	// done and is not sent again; the rest are checked against the issue as
	// it is and sent.
	fixed := closedFake()
	fixed.routes[editmetaRoute] = openScreen
	doc, err := applySequence(t, fixed, ledger, path, false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := strings.Join(outcomesOf(t, doc), " "); got != "skipped applied applied" {
		t.Errorf("outcomes = %s", got)
	}
	if got := writePaths(fixed); slices.Contains(got, reopenRoute) || len(got) != 2 {
		t.Errorf("the resume sent %v, want the edit and the comment only", got)
	}
	if baseline, _ := doc.Record.AttrValue("baseline"); baseline != workflow.BaselineRechecked {
		t.Errorf("baseline = %s, want re-checked", baseline)
	}
}

// TestAStepAfterAMoveThatCouldNeverRunStopsTheMove holds the checks that do
// not read the status to plan time, so the issue is not reopened for a
// sequence whose later step could not have run in any status.
func TestAStepAfterAMoveThatCouldNeverRunStopsTheMove(t *testing.T) {
	fake := closedFake()
	fake.routes["GET /rest/agile/1.0/sprint/7"] = `{"id":7,"state":"closed","name":"Sprint 7"}`
	_, _, seen, err := planSequence(t, fake, `[
		["issue", "move", "ENG-1", "Reopen Issue"],
		["sprint", "add", "7", "ENG-1"]
	]`, nil)
	if code := codeOf(err); code != "SPRINT_CLOSED" {
		t.Errorf("refused as %q (%v), want SPRINT_CLOSED; requests: %v", code, err, seen)
	}
}

// TestAReopenReplaysTheRecordings plans and applies issue 265's reopen
// against what Cloud answered: the oneil-team sandbox on 2026-10-20, a probe
// issue made, moved to Done, reopened by the sequence and deleted, scrubbed
// to AGL-12.
//
// The plan's recording holds three reads and no edit screen. The apply's
// holds the transition, then the permissions and the edit screen of the
// reopened issue, then the edit and the comment: the order the server saw,
// which the fake can only assert of itself.
func TestAReopenReplaysTheRecordings(t *testing.T) {
	const steps = `[["issue","move","AGL-12","To Do"],` +
		`["issue","edit","AGL-12","--summary","Reopened by a sequence"],` +
		`["issue","comment","add","AGL-12","Reopening: recorded for issue 265."]]`
	cmd, ok := registry.Lookup("issue.sequence")
	if !ok {
		t.Fatal("issue sequence is not registered")
	}
	run := func(conn *transport.Client, args []string, flags registry.Flags, ledger *idem.Ledger) *render.Doc {
		t.Helper()
		inv := &registry.Invocation{
			Jira: &seqSession{conn: conn, kind: site.Cloud, ledger: ledger},
			Args: args, Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
		}
		if err := cmd.Validate(t.Context(), inv); err != nil {
			t.Fatalf("validate: %v", err)
		}
		doc, err := cmd.Run(t.Context(), inv)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return doc
	}

	planConn, planReplay := recordedConn(t, "sequence-reopen-plan.cloud.json")
	path := filepath.Join(t.TempDir(), "reopen.xml")
	flags := registry.NewFlags()
	flags.SetString("steps", steps)
	flags.SetString("plan-out", path)
	plan := run(planConn, []string{"AGL-12"}, flags, nil)
	if got := checksOf(t, plan)[1]; !slices.Contains(got, "after step 1: EDIT_ISSUES, the edit screen") {
		t.Errorf("the edit's checks are %q", got)
	}

	applyConn, applyReplay := recordedConn(t, "sequence-reopen-apply.cloud.json")
	flags = registry.NewFlags()
	flags.SetString("apply", path)
	doc := run(applyConn, nil, flags, newLedger(t))
	if got := strings.Join(outcomesOf(t, doc), " "); got != "applied applied applied" {
		t.Errorf("outcomes = %s", got)
	}
	for name, r := range map[string]*transport.Replayer{"plan": planReplay, "apply": applyReplay} {
		if unplayed := r.Unplayed(); len(unplayed) > 0 {
			t.Errorf("the %s never asked for: %v", name, unplayed)
		}
		if unmatched := r.Unmatched(); len(unmatched) > 0 {
			t.Errorf("the %s asked for what the recording does not hold: %v", name, unmatched)
		}
	}
}
