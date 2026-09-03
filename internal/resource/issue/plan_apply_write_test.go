//go:build write

package issue_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// jiraStub is a Jira with three endpoints and a memory, which is what a plan
// and an apply between them touch.
//
// It counts requests by kind because most of what is worth asserting here is
// arithmetic: a plan costs one search however many rows it has, and an apply
// that skips a row costs nothing for that row.
type jiraStub struct {
	mu sync.Mutex
	// updated is each issue's version, and moving one between the plan and the
	// apply is how a stale row is staged.
	updated map[string]string
	// putStatus overrides the response to a write, by key.
	putStatus map[string]int

	searches int
	gets     int
	puts     []string
}

func newJiraStub(updated map[string]string) *jiraStub {
	return &jiraStub{updated: updated, putStatus: map[string]int{}}
}

func (s *jiraStub) counts() (searches, gets int, puts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.searches, s.gets, append([]string(nil), s.puts...)
}

func (s *jiraStub) moveOn(key, updated string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated[key] = updated
}

func (s *jiraStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.Contains(r.URL.Path, "/search"):
		s.searches++
		issues := make([]map[string]any, 0, len(s.updated))
		for key, updated := range s.updated {
			issues = append(issues, map[string]any{
				"id": "1", "key": key,
				"fields": map[string]any{"updated": updated},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issues": issues, "isLast": true, "total": len(issues),
		})

	case r.Method == http.MethodGet:
		s.gets++
		key := lastSegment(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "1", "key": key,
			"fields": map[string]any{
				"summary": "s", "labels": []string{},
				"status":  map[string]any{"name": "X", "statusCategory": map[string]any{"key": "new"}},
				"updated": s.updated[key],
			},
		})

	case r.Method == http.MethodPut:
		key := lastSegment(r.URL.Path)
		if status, ok := s.putStatus[key]; ok {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"errorMessages":["refused"]}`)
			return
		}
		s.puts = append(s.puts, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func lastSegment(path string) string {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	return parts[len(parts)-1]
}

// runEditCmd drives the registered command, which is the only way the flags,
// the validation and the run are exercised together.
func runEditCmd(
	t *testing.T, stub *jiraStub, ledgerPath string, args []string, flags map[string]string,
) (*render.Doc, error) {
	t.Helper()

	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	conn, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: -1})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}

	set := registry.NewFlags()
	for name, value := range flags {
		set.SetString(name, value)
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			conn: conn, kind: site.Cloud, project: "ENG",
			ledger: &idem.Ledger{Path: ledgerPath},
		},
		Args: args, Flags: set,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		return nil, err
	}
	return cmd.Run(t.Context(), inv)
}

func ledgerIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "idem.toml")
}

// planFile writes a plan for the given keys and returns its path.
func planFile(t *testing.T, stub *jiraStub, keys ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "plan.xml")
	_, err := runEditCmd(t, stub, ledgerIn(t), keys, map[string]string{
		"add-label": "", "plan-out": path,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return path
}

// TestAPlanCostsOneRequestHoweverManyRows is the arithmetic the whole surface
// was built for, asserted rather than assumed.
//
// A baseline per row is what makes apply able to refuse a stale write, and the
// obvious way to collect forty of them is forty `issue get` calls. `key IN (...)`
// returns `updated` for every row in one page, which is the same saving
// `issue list --precondition` exists for pointed at a named set. If this ever
// regressed to a get per row it would still work, and a forty-row plan would
// quietly cost forty times what it should.
func TestAPlanCostsOneRequestHoweverManyRows(t *testing.T) {
	stub := newJiraStub(map[string]string{
		"ENG-1": listedAt, "ENG-2": listedAt, "ENG-3": listedAt,
	})
	path := filepath.Join(t.TempDir(), "plan.xml")

	doc, err := runEditCmd(t, stub, ledgerIn(t),
		[]string{"ENG-1", "ENG-2", "ENG-3"},
		map[string]string{"add-label": "triaged", "plan-out": path})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	searches, gets, puts := stub.counts()
	if searches != 1 {
		t.Errorf("a three-row plan made %d searches, want 1", searches)
	}
	if gets != 0 {
		t.Errorf("a plan made %d gets, want none: the search already has the versions", gets)
	}
	if len(puts) != 0 {
		t.Fatalf("a plan wrote to Jira: %v", puts)
	}
	if doc.Kind != issue.KindPlan {
		t.Errorf("kind = %q, want %q", doc.Kind, issue.KindPlan)
	}

	// The file is what apply reads, so its existence is half the feature.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the plan: %v", err)
	}
	for _, key := range []string{"ENG-1", "ENG-2", "ENG-3"} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("the plan file does not mention %s:\n%s", key, raw)
		}
	}
}

// TestApplyWritesEveryRowAndSaysSo is the happy path, and the counts in the
// envelope are the part a script reads.
func TestApplyWritesEveryRowAndSaysSo(t *testing.T) {
	stub := newJiraStub(map[string]string{"ENG-1": listedAt, "ENG-2": listedAt})
	path := planFile(t, stub, "ENG-1", "ENG-2")

	doc, err := runEditCmd(t, stub, ledgerIn(t), nil, map[string]string{"apply": path})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	_, _, puts := stub.counts()
	if len(puts) != 2 {
		t.Errorf("puts = %v, want both rows written", puts)
	}
	assertApplyCounts(t, doc, 2, 0, 0)
}

// TestAStaleRowFailsAndTheRunGoesOn is the decision this surface was designed
// around, and the assertion that separates it from the alternative.
//
// The row that moved is refused with nothing sent. Every other row still
// applies, which is what makes a forty-row sweep survive one person touching
// one ticket. Asserting the outcome counts alone would pass on a run that
// stopped, so the write set is checked too.
func TestAStaleRowFailsAndTheRunGoesOn(t *testing.T) {
	stub := newJiraStub(map[string]string{
		"ENG-1": listedAt, "ENG-2": listedAt, "ENG-3": listedAt,
	})
	path := planFile(t, stub, "ENG-1", "ENG-2", "ENG-3")

	// Somebody edits the middle issue between the plan and the apply.
	stub.moveOn("ENG-2", "2026-08-04T11:41:55.008+0000")

	doc, err := runEditCmd(t, stub, ledgerIn(t), nil, map[string]string{"apply": path})
	if err == nil {
		t.Fatal("a run with a failed row exited 0")
	}

	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("err = %T, want PartiallyApplied carrying the document", err)
	}
	if e := errs.Coerce(partial.Cause); e.Code != "STALE_WRITE" {
		t.Errorf("cause = %q, want STALE_WRITE", e.Code)
	}
	assertApplyCounts(t, partial.Doc, 2, 0, 1)

	_, _, puts := stub.counts()
	if len(puts) != 2 {
		t.Errorf("puts = %v, want ENG-1 and ENG-3 written and ENG-2 not", puts)
	}
	for _, key := range puts {
		if key == "ENG-2" {
			t.Error("the stale row was written anyway, which loses the other edit")
		}
	}
	if doc != nil {
		t.Error("a partial apply must return its document through the error, not beside it")
	}
}

// TestReapplyingAPlanSkipsWhatAlreadyLanded is the resume, and it needs no flag
// to ask for it.
//
// An idempotency key means "do this at most once". Honouring that only when a
// --resume flag was passed would make the default the duplicate, and a duplicate
// edit is a second changelog entry a human reads and a second chance for a name
// to resolve differently.
func TestReapplyingAPlanSkipsWhatAlreadyLanded(t *testing.T) {
	stub := newJiraStub(map[string]string{"ENG-1": listedAt, "ENG-2": listedAt})
	path := planFile(t, stub, "ENG-1", "ENG-2")
	ledger := ledgerIn(t)

	if _, err := runEditCmd(t, stub, ledger, nil, map[string]string{"apply": path}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	doc, err := runEditCmd(t, stub, ledger, nil, map[string]string{"apply": path})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}

	assertApplyCounts(t, doc, 0, 2, 0)
	if _, _, puts := stub.counts(); len(puts) != 2 {
		t.Errorf("puts = %v, want the two from the first run and none from the second", puts)
	}
}

// TestAFreshLedgerReappliesEverything is the other half of the test above, and
// the reason it is not asserting a coincidence.
//
// If the second run skipped because of something other than the ledger, the
// same plan against an empty ledger would also skip. It must not.
func TestAFreshLedgerReappliesEverything(t *testing.T) {
	stub := newJiraStub(map[string]string{"ENG-1": listedAt})
	path := planFile(t, stub, "ENG-1")

	if _, err := runEditCmd(t, stub, ledgerIn(t), nil, map[string]string{"apply": path}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	doc, err := runEditCmd(t, stub, ledgerIn(t), nil, map[string]string{"apply": path})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	assertApplyCounts(t, doc, 1, 0, 0)
}

// TestASystemicFailureDecidesTheExitOverAStaleRow is the rule for which cause
// reaches the caller when more than one row failed.
//
// A stale row is the expected outcome of two people touching one ticket. A
// permission error is systemic and makes every row after it suspect. Reporting
// the stale one because it happened first would be a tidy rule preferred to a
// true answer, and the caller would not learn that backing off or fixing
// permissions was the thing to do.
func TestASystemicFailureDecidesTheExitOverAStaleRow(t *testing.T) {
	stub := newJiraStub(map[string]string{
		"ENG-1": listedAt, "ENG-2": listedAt, "ENG-3": listedAt,
	})
	path := planFile(t, stub, "ENG-1", "ENG-2", "ENG-3")

	// ENG-1 is stale, and fails first. ENG-3 is refused by the server.
	stub.moveOn("ENG-1", "2026-08-04T11:41:55.008+0000")
	stub.putStatus["ENG-3"] = http.StatusForbidden

	_, err := runEditCmd(t, stub, ledgerIn(t), nil, map[string]string{"apply": path})
	if err == nil {
		t.Fatal("a run with two failed rows exited 0")
	}
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("err = %T, want PartiallyApplied", err)
	}
	if code := errs.Coerce(partial.Cause).Code; code == "STALE_WRITE" {
		t.Error("the stale row decided the exit over a permission failure, " +
			"so the caller is told about the expected problem and not the systemic one")
	}
	assertApplyCounts(t, partial.Doc, 1, 0, 2)
}

func assertApplyCounts(t *testing.T, doc *render.Doc, applied, skipped, failed int) {
	t.Helper()

	if doc == nil || doc.Record == nil {
		t.Fatal("no apply document")
	}
	if doc.Kind != issue.KindApply {
		t.Fatalf("kind = %q, want %q", doc.Kind, issue.KindApply)
	}
	for _, tc := range []struct {
		attr string
		want int
	}{
		{"applied", applied}, {"skipped", skipped}, {"failed", failed},
	} {
		got, _ := doc.Record.AttrValue(tc.attr)
		if got != fmt.Sprint(tc.want) {
			t.Errorf("%s = %q, want %d", tc.attr, got, tc.want)
		}
	}
}
