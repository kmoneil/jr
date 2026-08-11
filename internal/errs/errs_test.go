package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

func TestConstructorsCarryTheirExitStatus(t *testing.T) {
	cases := []struct {
		build func(string, string, ...any) *errs.Error
		want  exitcode.Code
	}{
		{errs.Runtime, exitcode.Error},
		{errs.Usage, exitcode.Usage},
		{errs.Auth, exitcode.Auth},
		{errs.NotFound, exitcode.NotFound},
		{errs.Permission, exitcode.Permission},
		{errs.Conflict, exitcode.Conflict},
		{errs.RateLimit, exitcode.RateLimit},
		{errs.Remote, exitcode.Remote},
		{errs.Blocked, exitcode.Blocked},
	}
	for _, tc := range cases {
		e := tc.build("CODE", "message")
		if e.Exit != tc.want {
			t.Errorf("%s exits %v, want %v", e.Code, e.Exit, tc.want)
		}
		if errs.ExitOf(e) != tc.want {
			t.Errorf("ExitOf(%s) = %v, want %v", e.Code, errs.ExitOf(e), tc.want)
		}
	}
}

// TestRetryableOnlyWhereRetryingCanWork is the flag that stops an agent burning
// its budget on a syntax error and stops it giving up on a 503.
func TestRetryableOnlyWhereRetryingCanWork(t *testing.T) {
	retryable := []*errs.Error{
		errs.RateLimit("THROTTLED", "throttled"),
		errs.Remote("UPSTREAM", "upstream failed"),
	}
	for _, e := range retryable {
		if !e.Retryable {
			t.Errorf("%s is not marked retryable, so an agent would give up on it", e.Code)
		}
	}

	terminal := []*errs.Error{
		errs.Usage("JQL_SYNTAX", "unclosed quote"),
		errs.Auth("NO_CREDENTIALS", "no credentials"),
		errs.NotFound("NO_ISSUE", "no such issue"),
		errs.Permission("FORBIDDEN", "forbidden"),
		errs.Conflict("STALE", "stale write"),
		errs.Blocked("READONLY", "read-only mode"),
		errs.Runtime("INTERNAL", "internal"),
	}
	for _, e := range terminal {
		if e.Retryable {
			t.Errorf("%s is marked retryable; an agent would retry a failure that cannot succeed",
				e.Code)
		}
	}
}

func TestBuildersAreChainable(t *testing.T) {
	e := errs.Usage("JQL_SYNTAX", "Unclosed quote at position %d", 34).
		WithDetail("project = ENG AND summary ~ %q", "unclosed").
		WithRemedy("quote the whole expression").
		WithRequestID("req-1")

	if e.Message != "Unclosed quote at position 34" {
		t.Errorf("Message = %q", e.Message)
	}
	if e.Detail != `project = ENG AND summary ~ "unclosed"` {
		t.Errorf("Detail = %q", e.Detail)
	}
	if e.Remedy != "quote the whole expression" {
		t.Errorf("Remedy = %q", e.Remedy)
	}
	if e.RequestID != "req-1" {
		t.Errorf("RequestID = %q", e.RequestID)
	}
}

func TestErrorStringIncludesTheCode(t *testing.T) {
	bare := errs.NotFound("NO_ISSUE", "no issue ENG-1")
	if got := bare.Error(); got != "NO_ISSUE: no issue ENG-1" {
		t.Errorf("Error() = %q", got)
	}
	detailed := errs.NotFound("NO_ISSUE", "no issue").WithDetail("ENG-1")
	if got := detailed.Error(); got != "NO_ISSUE: no issue (ENG-1)" {
		t.Errorf("Error() = %q", got)
	}
}

func TestWrapPreservesTheChainWithoutRenderingIt(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	e := errs.Remote("UPSTREAM", "cannot reach Jira").Wrap(cause)

	if !errors.Is(e, cause) {
		t.Error("Wrap did not preserve the cause for errors.Is")
	}
	// The cause is reachable but never rendered, so a transport detail cannot
	// leak into the output contract.
	if got := e.Error(); got != "UPSTREAM: cannot reach Jira" {
		t.Errorf("Error() leaks the wrapped cause: %q", got)
	}
}

func TestExitOfWalksTheChain(t *testing.T) {
	inner := errs.Conflict("STALE", "stale write")
	wrapped := fmt.Errorf("updating issue: %w", inner)
	if got := errs.ExitOf(wrapped); got != exitcode.Conflict {
		t.Errorf("ExitOf(wrapped) = %v, want %v", got, exitcode.Conflict)
	}
}

// TestUnstructuredFailuresNeverLookLikeSuccess is why ExitOf defaults to 1: an
// error with no status must not fall through to 0.
func TestUnstructuredFailuresNeverLookLikeSuccess(t *testing.T) {
	if got := errs.ExitOf(errors.New("boom")); got != exitcode.Error {
		t.Errorf("ExitOf(plain error) = %v, want %v", got, exitcode.Error)
	}
	if got := errs.ExitOf(nil); got != exitcode.OK {
		t.Errorf("ExitOf(nil) = %v, want %v", got, exitcode.OK)
	}
}

func TestCoerceGivesEveryFailureACode(t *testing.T) {
	if errs.Coerce(nil) != nil {
		t.Error("Coerce(nil) returned an error")
	}

	structured := errs.Auth("NO_CREDENTIALS", "no credentials")
	if got := errs.Coerce(structured); got != structured {
		t.Error("Coerce rewrapped an already-structured error")
	}

	cause := errors.New("boom")
	got := errs.Coerce(cause)
	if got.Code != "INTERNAL" {
		t.Errorf("Coerce(plain).Code = %q, want INTERNAL", got.Code)
	}
	if got.Exit != exitcode.Error {
		t.Errorf("Coerce(plain).Exit = %v, want %v", got.Exit, exitcode.Error)
	}
	if !errors.Is(got, cause) {
		t.Error("Coerce dropped the cause")
	}
}
