//go:build write

package issue_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// instantClock lets the transport retry without waiting for its backoff.
type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }

func (instantClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// retryingConn is a transport at the retry budget a caller actually runs with.
// Every other conversation in this package is built with Retries: -1, which is
// why a keyed write replayed by the transport never showed up in one.
func retryingConn(t *testing.T, base string) *transport.Client {
	t.Helper()
	conn, err := transport.New(transport.Options{
		BaseURL: base, Retries: 3,
		Clock: instantClock{}, Jitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn
}

// TestAKeyedCreateJiraMayHaveMadeIsNotSentAgain is §6.3 at the default retry
// budget. Jira makes the issue, the proxy in front of it answers 503, and the
// create used to be sent again because the key marked it replayable: two
// issues, exit 0, the second reported. Measured before the fix with this
// server, and kept as the probe in _plans/probes.
func TestAKeyedCreateJiraMayHaveMadeIsNotSentAgain(t *testing.T) {
	var created atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/2/issue" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := created.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"errorMessages":["upstream timed out"]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":"1000%d","key":"ENG-20%d"}`, n, n)
	}))
	t.Cleanup(srv.Close)
	conn := retryingConn(t, srv.URL)
	ledger := &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")}

	create := func() error {
		t.Helper()
		cmd, _ := registry.Lookup("issue.create")
		flags := registry.NewFlags()
		flags.SetString("type", "Bug")
		flags.SetString("summary", "a summary")
		flags.SetString("idempotency-key", "deploy-42")
		inv := &registry.Invocation{
			Jira: &stubSession{
				doer: &stubDoer{body: catalogueJSON}, conn: conn,
				kind: site.DataCenter, ledger: ledger,
			},
			Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
		}
		if err := cmd.Validate(t.Context(), inv); err != nil {
			t.Fatalf("validate: %v", err)
		}
		_, err := cmd.Run(t.Context(), inv)
		return err
	}

	err := create()
	if n := created.Load(); n != 1 {
		t.Errorf("Jira made %d issues for one keyed create", n)
	}
	if err == nil {
		t.Fatal("a create Jira answered 503 for was reported as a success")
	}
	e := errs.Coerce(err)
	if e.Code != "OUTCOME_UNKNOWN" || e.Retryable {
		t.Errorf("got %s retryable %t, want OUTCOME_UNKNOWN, not retryable", e.Code, e.Retryable)
	}
	if !strings.Contains(e.Remedy, "check whether it happened") {
		t.Errorf("remedy = %q, want it to say to check first", e.Remedy)
	}

	// The claim stays held, because the first attempt may have made the issue,
	// so the same key is refused rather than allowed to make another.
	if code := errs.Coerce(create()).Code; code != "IDEMPOTENT_IN_FLIGHT" {
		t.Errorf("a retry with the same key = %s, want IDEMPOTENT_IN_FLIGHT", code)
	}
	if n := created.Load(); n != 1 {
		t.Errorf("the retry made another issue: %d in all", n)
	}
}

// TestAKeyedMoveJiraMayHaveAppliedIsNotSentAgain is the same class on a
// transition. Jira moves the issue and the answer is a 503; a replay is
// refused from the new status, so the move that happened used to be reported
// as a BAD_REQUEST, with the claim held against the caller's retry.
func TestAKeyedMoveJiraMayHaveAppliedIsNotSentAgain(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/transitions"):
			_, _ = io.WriteString(w, `{"transitions":[{"id":"31","name":"Done",`+
				`"to":{"id":"6","name":"Done","statusCategory":{"key":"done","name":"Done"}}}]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transitions"):
			if posts.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"errorMessages":["upstream timed out"]}`)
				return
			}
			// The issue is in Done now, and Done is not a transition out of Done.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errorMessages":["It seems that you have `+
				`tried to perform a workflow operation (Done) that is not valid for `+
				`the current state of this issue (ENG-1)."]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	conn := retryingConn(t, srv.URL)

	cmd, _ := registry.Lookup("issue.move")
	flags := registry.NewFlags()
	flags.SetString("idempotency-key", "deploy-42")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, metaClient: conn,
			kind:   site.DataCenter,
			ledger: &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")},
		},
		Args: []string{"ENG-1", "Done"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	_, err := cmd.Run(t.Context(), inv)

	if n := posts.Load(); n != 1 {
		t.Errorf("the transition reached Jira %d times; Jira had applied the first", n)
	}
	if err == nil {
		t.Fatal("a move Jira answered 503 for was reported as a success")
	}
	if e := errs.Coerce(err); e.Code != "OUTCOME_UNKNOWN" || e.Retryable {
		t.Errorf("got %s retryable %t, want OUTCOME_UNKNOWN, not retryable: %v",
			e.Code, e.Retryable, err)
	}
}
