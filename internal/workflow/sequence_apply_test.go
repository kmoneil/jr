//go:build write

package workflow_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
	"github.com/kmoneil/jr/internal/workflow"
)

// writtenPlan plans everyStep against a fresh fake and returns the plan's
// path, so an apply test starts from a file --plan-out really wrote.
func writtenPlan(t *testing.T) string {
	t.Helper()
	_, path, _, err := planSequence(t, newFakeJira(), everyStep, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return path
}

// applySequence runs issue sequence --apply, Validate and body, against the
// fake, with the ledger given.
func applySequence(
	t *testing.T, fake *fakeJira, ledger *idem.Ledger, path string, dryRun bool,
) (*render.Doc, error) {
	t.Helper()
	return applySequenceRetrying(t, fake, ledger, path, dryRun, -1)
}

// applySequenceRetrying is applySequence at a chosen retry budget: -1 for
// none, 0 for the default a caller runs with.
func applySequenceRetrying(
	t *testing.T, fake *fakeJira, ledger *idem.Ledger, path string, dryRun bool, retries int,
) (*render.Doc, error) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	conn, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: retries})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	cmd, ok := registry.Lookup("issue.sequence")
	if !ok {
		t.Fatal("issue sequence is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("apply", path)
	flags.SetBool("dry-run", dryRun)
	inv := &registry.Invocation{
		Jira: &seqSession{conn: conn, ledger: ledger}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		return nil, err
	}
	return cmd.Run(t.Context(), inv)
}

// outcomesOf reads every step's outcome off an apply document, in order.
func outcomesOf(t *testing.T, doc *render.Doc) []string {
	t.Helper()
	steps, ok := doc.Record.ChildNamed("steps")
	if !ok {
		t.Fatal("the apply document has no steps")
	}
	var out []string
	for _, s := range steps.Children {
		o, _ := s.AttrValue("outcome")
		if code, ok := s.AttrValue("code"); ok {
			o += ":" + code
		}
		out = append(out, o)
	}
	return out
}

func newLedger(t *testing.T) *idem.Ledger {
	t.Helper()
	return &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")}
}

// writePaths is the method and path of each write, without the query.
func writePaths(fake *fakeJira) []string {
	var out []string
	for _, w := range fake.writes() {
		route, _, _ := strings.Cut(w, "?")
		out = append(out, route)
	}
	return out
}

var everyWrite = []string{
	"POST /rest/api/2/issue/ENG-1/comment",
	"PUT /rest/api/2/issue/ENG-1",
	"PUT /rest/api/2/issue/ENG-1/assignee",
	"POST /rest/api/2/issueLink",
	"POST /rest/agile/1.0/sprint/7/issue",
	"POST /rest/api/2/issue/ENG-1/transitions",
}

// TestAPlanAppliesItsStepsInOrder is issue 157's apply: one plan, one apply,
// every step sent once, in the order planned, and reported applied.
func TestAPlanAppliesItsStepsInOrder(t *testing.T) {
	path := writtenPlan(t)
	fake := newFakeJira()
	doc, err := applySequence(t, fake, newLedger(t), path, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := writePaths(fake); strings.Join(got, "\n") != strings.Join(everyWrite, "\n") {
		t.Errorf("sent\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(everyWrite, "\n"))
	}
	if doc.Kind != workflow.KindSequenceApply {
		t.Fatalf("kind = %s", doc.Kind)
	}
	if got := strings.Join(outcomesOf(t, doc), " "); got != strings.Repeat("applied ", 5)+"applied" {
		t.Errorf("outcomes = %s", got)
	}
	if baseline, _ := doc.Record.AttrValue("baseline"); baseline != workflow.BaselineCompared {
		t.Errorf("baseline = %s, want compared", baseline)
	}
}

// TestAnApplyStopsAtTheFirstFailure is the half-done issue the sequence exists
// to report exactly: the step before is applied, the failing one is failed
// with its own code, the rest are not attempted and nothing of theirs is sent,
// and the exit is the failing step's.
func TestAnApplyStopsAtTheFirstFailure(t *testing.T) {
	path := writtenPlan(t)
	fake := newFakeJira()
	fake.status["PUT /rest/api/2/issue/ENG-1"] = http.StatusBadRequest
	fake.routes["PUT /rest/api/2/issue/ENG-1"] = `{"errorMessages":[],"errors":{"summary":"refused"}}`

	_, err := applySequence(t, fake, newLedger(t), path, false)
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("want a partial apply, got %v", err)
	}
	got := outcomesOf(t, partial.Doc)
	if got[0] != "applied" || !strings.HasPrefix(got[1], "failed:") ||
		strings.Join(got[2:], " ") != strings.TrimSpace(strings.Repeat("not-attempted ", 4)) {
		t.Errorf("outcomes = %v", got)
	}
	if code := errs.Coerce(partial.Cause).Code; !strings.HasSuffix(got[1], ":"+code) {
		t.Errorf("the exit's cause is %s and step 2 reports %s", code, got[1])
	}
	if writes := writePaths(fake); len(writes) != 2 {
		t.Errorf("sent %v, want the comment and the refused edit and nothing after", writes)
	}
}

// TestReapplyingThePlanResumes is the resume: once the cause is fixed, the
// same file sends the steps not yet done and only those. The baseline the
// first run's own step moved is re-checked rather than compared, and says so.
func TestReapplyingThePlanResumes(t *testing.T) {
	path := writtenPlan(t)
	ledger := newLedger(t)
	broken := newFakeJira()
	broken.status["PUT /rest/api/2/issue/ENG-1"] = http.StatusBadRequest
	if _, err := applySequence(t, broken, ledger, path, false); err == nil {
		t.Fatal("the first apply did not fail")
	}

	// The first run's comment moved the issue, as a real one would.
	fixed := newFakeJira()
	fixed.routes["GET /rest/api/2/issue/ENG-1"] = `{"key":"ENG-1","fields":{"updated":"2026-10-02T13:05:00.000+0000"}}`
	doc, err := applySequence(t, fixed, ledger, path, false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := strings.Join(outcomesOf(t, doc), " "); got != "skipped"+strings.Repeat(" applied", 5) {
		t.Errorf("outcomes = %s", got)
	}
	if got := writePaths(fixed); strings.Join(got, "\n") != strings.Join(everyWrite[1:], "\n") {
		t.Errorf("the resume sent\n%s", strings.Join(got, "\n"))
	}
	if baseline, _ := doc.Record.AttrValue("baseline"); baseline != workflow.BaselineRechecked {
		t.Errorf("baseline = %s, want re-checked", baseline)
	}
}

// TestAnIssueChangedSincePlanningIsRefusedWithNothingSent is the baseline: the
// plan was read against an issue that has moved since, so none of it runs.
func TestAnIssueChangedSincePlanningIsRefusedWithNothingSent(t *testing.T) {
	path := writtenPlan(t)
	fake := newFakeJira()
	fake.routes["GET /rest/api/2/issue/ENG-1"] = `{"key":"ENG-1","fields":{"updated":"2026-10-02T13:05:00.000+0000"}}`
	_, err := applySequence(t, fake, newLedger(t), path, false)
	if e := errs.Coerce(err); e.Code != "STALE_WRITE" || e.Exit != exitcode.Conflict {
		t.Errorf("refused as %s (exit %v), want STALE_WRITE: %v", e.Code, e.Exit, err)
	}
	if writes := fake.writes(); len(writes) > 0 {
		t.Errorf("a stale plan sent %v", writes)
	}
}

// TestNothingIsSentFromThePlanFile is the property the plan's shape rests on.
// Its requests are evidence, not instructions: a plan whose recorded request
// was edited is refused before the first step, and the edited request never
// leaves this process. The same refusal catches a site that moved under the
// plan, here a transition whose id changed since planning.
func TestNothingIsSentFromThePlanFile(t *testing.T) {
	t.Run("an edited request", func(t *testing.T) {
		path := writtenPlan(t)
		raw, err := os.ReadFile(path) //nolint:gosec // the test's own temporary file.
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.Replace(string(raw), `/rest/api/2/issue/ENG-1/comment`,
			`/rest/api/2/project/ENG`, 1)
		if edited == string(raw) {
			t.Fatal("the plan carries no comment request to edit")
		}
		if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := newFakeJira()
		_, err = applySequence(t, fake, newLedger(t), path, false)
		if code := codeOf(err); code != "PLAN_DRIFTED" {
			t.Errorf("an edited plan was refused as %q (%v), want PLAN_DRIFTED", code, err)
		}
		if writes := fake.writes(); len(writes) > 0 {
			t.Errorf("an edited plan sent %v", writes)
		}
	})
	t.Run("a transition that changed", func(t *testing.T) {
		path := writtenPlan(t)
		fake := newFakeJira()
		fake.routes["GET /rest/api/2/issue/ENG-1/transitions"] = strings.Replace(
			fake.routes["GET /rest/api/2/issue/ENG-1/transitions"], `"id":"31"`, `"id":"41"`, 1)
		_, err := applySequence(t, fake, newLedger(t), path, false)
		e := coerced(err)
		if e.Code != "PLAN_DRIFTED" || !strings.Contains(e.Message, "step 6") {
			t.Errorf("refused as %s %q, want PLAN_DRIFTED at step 6", e.Code, e.Message)
		}
		if writes := fake.writes(); len(writes) > 0 {
			t.Errorf("a drifted plan sent %v", writes)
		}
	})
}

// TestADryRunOfAnApplySendsNothing is --dry-run beside --apply: the preview a
// caller passes through before the run, every request rebuilt and checked,
// none of them sent.
func TestADryRunOfAnApplySendsNothing(t *testing.T) {
	path := writtenPlan(t)
	fake := newFakeJira()
	doc, err := applySequence(t, fake, newLedger(t), path, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if doc.Kind != registry.KindDryRun || len(doc.Record.Children) != 6 {
		t.Errorf("preview is %s with %d requests, want dry-run with 6", doc.Kind,
			len(doc.Record.Children))
	}
	if writes := fake.writes(); len(writes) > 0 {
		t.Errorf("a dry run sent %v", writes)
	}
}

// TestAnApplyRefusesAPlanItDidNotWrite is the plan file as untrusted input:
// everything in it is held to what --plan-out would have produced, and every
// rule a typed sequence meets.
func TestAnApplyRefusesAPlanItDidNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, code string
	}{
		{"another kind", `kind="issue.sequence.plan"`, `kind="issue.plan"`, "INVALID_PLAN"},
		{"another version", `v="1"`, `v="9"`, "INVALID_PLAN"},
		{"a key from another plan", `idempotency-key="auto-`, `idempotency-key="auto-0`, "INVALID_PLAN"},
		{"a step turned destructive", `<arg>comment</arg>`, `<arg>delete</arg>`, "STEP_NOT_ALLOWED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writtenPlan(t)
			raw, err := os.ReadFile(path) //nolint:gosec // the test's own temporary file.
			if err != nil {
				t.Fatal(err)
			}
			edited := strings.Replace(string(raw), tc.from, tc.to, 1)
			if edited == string(raw) {
				t.Fatalf("the plan has no %q to edit", tc.from)
			}
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
			fake := newFakeJira()
			_, err = applySequence(t, fake, newLedger(t), path, false)
			if code := codeOf(err); code != tc.code {
				t.Errorf("refused as %q (%v), want %s", code, err, tc.code)
			}
			if seen := fake.requests(); len(seen) > 0 {
				t.Errorf("a plan refused on its text made requests: %v", seen)
			}
		})
	}
}

// TestAStepJiraMayHaveAppliedIsNotSentTwice is §6.3 for a sequence, at the
// retry budget a caller runs with. Jira did the comment and a proxy answered
// 503: the step is not sent again inside the run, it fails as an outcome
// nobody knows, and it keeps its claim, so an apply re-run at once refuses
// the step rather than posting the comment a second time.
func TestAStepJiraMayHaveAppliedIsNotSentTwice(t *testing.T) {
	path := writtenPlan(t)
	ledger := newLedger(t)
	fake := newFakeJira()
	fake.status["POST /rest/api/2/issue/ENG-1/comment"] = http.StatusServiceUnavailable

	_, err := applySequenceRetrying(t, fake, ledger, path, false, 0)
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("want a partial apply, got %v", err)
	}
	if got := outcomesOf(t, partial.Doc); !strings.HasPrefix(got[0], "failed:") {
		t.Errorf("outcomes = %v", got)
	}
	if posts := writePaths(fake); len(posts) != 1 {
		t.Errorf("a comment Jira may have made was sent %d times: %v", len(posts), posts)
	}

	again := newFakeJira()
	_, err = applySequenceRetrying(t, again, ledger, path, false, 0)
	if code := codeOf(err); code != "IDEMPOTENT_IN_FLIGHT" {
		t.Errorf("the re-run refused as %q (%v), want IDEMPOTENT_IN_FLIGHT", code, err)
	}
	if writes := again.writes(); len(writes) > 0 {
		t.Errorf("the re-run sent %v over a comment that may exist", writes)
	}
}

// codeOf is an error's code, and empty for no error, so a red that lets the
// apply succeed fails on the answer rather than on a nil.
func codeOf(err error) string {
	return coerced(err).Code
}

func coerced(err error) *errs.Error {
	if err == nil {
		return &errs.Error{}
	}
	return errs.Coerce(err)
}
