package transport_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/transport"
)

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // test-controlled path.
	return string(b), err
}

// fakeClock records what it was asked to wait for instead of waiting, so a
// backoff schedule can be asserted without spending it.
type fakeClock struct {
	now    time.Time
	slept  []time.Duration
	cancel error
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	if c.cancel != nil {
		return c.cancel
	}
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

// newTestClient builds a client against srv with deterministic timing.
func newTestClient(t *testing.T, srv *httptest.Server, opt transport.Options) (
	*transport.Client, *fakeClock,
) {
	t.Helper()
	clock := newFakeClock()
	opt.BaseURL = srv.URL
	opt.Clock = clock
	if opt.Jitter == nil {
		// A fixed jitter makes the backoff schedule exact. The randomness is
		// tested separately.
		opt.Jitter = func() float64 { return 1.0 }
	}
	c, err := transport.New(opt)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c, clock
}

func TestNewRejectsBadSites(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"whitespace":  "   ",
		"no scheme":   "acme.atlassian.invalid",
		"bad scheme":  "ftp://acme.atlassian.invalid",
		"file scheme": "file:///etc/passwd",
	}
	for name, site := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := transport.New(transport.Options{BaseURL: site})
			if err == nil {
				t.Fatalf("New accepted %q", site)
			}
			if errs.ExitOf(err) != exitcode.Usage {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
			}
		})
	}
}

// TestAbsolutePathIsRefused is the guard against sending a credential to
// another host. A caller that builds a path from a server-supplied value must
// not be able to redirect the request off-site.
func TestAbsolutePathIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	_, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet,
		Path:   "https://evil.invalid/steal",
	})
	if err == nil {
		t.Fatal("an absolute path was accepted, which would send credentials off-site")
	}
	if !strings.Contains(err.Error(), "relative") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestRequestIDIsSentAndEchoed(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get(transport.HeaderRequestID)
		w.Header().Set(transport.HeaderRequestID, "server-assigned-id")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	resp, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if sent == "" {
		t.Error("no X-Request-Id was sent")
	}
	// The server's id is what support can look up, so it wins.
	if resp.RequestID != "server-assigned-id" {
		t.Errorf("RequestID = %q, want the server's", resp.RequestID)
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(transport.HeaderRequestID)
		if seen[id] {
			t.Errorf("request id %q was reused", id)
		}
		seen[id] = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	for range 5 {
		if _, err := c.Do(t.Context(), transport.Request{
			Method: http.MethodGet, Path: "/x",
		}); err != nil {
			t.Fatalf("do: %v", err)
		}
	}
	if len(seen) != 5 {
		t.Errorf("saw %d distinct ids, want 5", len(seen))
	}
}

func TestQueryAndBodyAreSent(t *testing.T) {
	var gotQuery, gotBody, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotType = r.Header.Get("Content-Type")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	_, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPost,
		Path:   "/rest/api/3/issue",
		Query:  url.Values{"expand": {"names"}},
		Body:   []byte(`{"fields":{}}`),
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotQuery != "expand=names" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotBody != `{"fields":{}}` {
		t.Errorf("body = %q", gotBody)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want a JSON default", gotType)
	}
}

// TestRetriesUntilSuccess covers the ordinary case: a transient 503 followed by
// a 200.
func TestRetriesUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, clock := newTestClient(t, srv, transport.Options{Retries: 3})
	resp, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d", resp.Status)
	}
	if resp.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", resp.Attempts)
	}
	// Exponential: 500ms then 1s, at jitter 1.0.
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if len(clock.slept) != len(want) {
		t.Fatalf("slept %v, want %v", clock.slept, want)
	}
	for i := range want {
		if clock.slept[i] != want[i] {
			t.Errorf("wait %d = %s, want %s", i, clock.slept[i], want[i])
		}
	}
}

// TestExhaustedRetriesNeverExitZero is the spec's rule: a request that kept
// failing must surface as 8 or 9, never as a success.
func TestExhaustedRetriesNeverExitZero(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   exitcode.Code
		code   string
	}{
		{"rate limited", http.StatusTooManyRequests, exitcode.RateLimit, "RATE_LIMITED"},
		{"upstream error", http.StatusServiceUnavailable, exitcode.Remote, "UPSTREAM_ERROR"},
		{"bad gateway", http.StatusBadGateway, exitcode.Remote, "UPSTREAM_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			c, _ := newTestClient(t, srv, transport.Options{Retries: 2})
			resp, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			// Do returns the response; Err is what maps it.
			err = transport.Err(resp)
			if err == nil {
				t.Fatal("a persistently failing request reported success")
			}
			if errs.ExitOf(err) != tc.want {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), tc.want)
			}
			e := errs.Coerce(err)
			if e.Code != tc.code {
				t.Errorf("code = %q, want %q", e.Code, tc.code)
			}
			if !e.Retryable {
				t.Errorf("%s is not marked retryable", e.Code)
			}
			if got := calls.Load(); got != 3 {
				t.Errorf("made %d attempts, want 3 (initial plus 2 retries)", got)
			}
		})
	}
}

func TestRetriesDisabled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{Retries: -1})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("made %d attempts with retries disabled, want 1", got)
	}
}

// TestPostIsNotReplayedAfterAnUpstreamError is the rule that stops one
// `issue create` becoming two issues. A 503 does not prove the server failed
// to process the request.
func TestPostIsNotReplayedAfterAnUpstreamError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{Retries: 3})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPost, Path: "/rest/api/3/issue", Body: []byte(`{}`),
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("a POST was replayed %d times after a 503; it may have created duplicates", got)
	}
}

// TestPostIsReplayedAfterRateLimiting is the exception: a 429 is refused before
// processing, so replaying it cannot duplicate anything.
func TestPostIsReplayedAfterRateLimiting(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{Retries: 3})
	resp, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPost, Path: "/rest/api/3/issue", Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.Status)
	}
}

// TestReplayablePostIsRetried covers a caller holding an idempotency key, for
// whom a duplicate is not possible.
func TestReplayablePostIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{Retries: 3})
	resp, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPost, Path: "/x", Body: []byte(`{}`), Replayable: true,
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Errorf("status = %d", resp.Status)
	}
	if resp.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", resp.Attempts)
	}
}

// TestBodyIsReplayedIntact catches the classic retry bug: a body consumed by
// the first attempt, so the retry sends nothing and quietly writes the wrong
// thing.
func TestBodyIsReplayedIntact(t *testing.T) {
	const payload = `{"fields":{"summary":"retry me"}}`
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		bodies = append(bodies, string(b))
		if len(bodies) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{Retries: 3})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPut, Path: "/x", Body: []byte(payload),
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("saw %d attempts, want 3", len(bodies))
	}
	for i, got := range bodies {
		if got != payload {
			t.Errorf("attempt %d sent %q, want the original body", i+1, got)
		}
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, clock := newTestClient(t, srv, transport.Options{Retries: 2})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(clock.slept) != 1 || clock.slept[0] != 7*time.Second {
		t.Errorf("slept %v, want the server's Retry-After of 7s", clock.slept)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	clock := newFakeClock()
	when := clock.Now().Add(12 * time.Second).UTC().Format(http.TimeFormat)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", when)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: 2, Clock: clock,
		Jitter: func() float64 { return 1.0 },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(clock.slept) != 1 {
		t.Fatalf("slept %v, want one wait", clock.slept)
	}
	// The date is a whole number of seconds ahead; allow a second of slack for
	// the format's resolution.
	if clock.slept[0] < 11*time.Second || clock.slept[0] > 12*time.Second {
		t.Errorf("slept %s, want about 12s", clock.slept[0])
	}
}

// TestRetryAfterIsCapped stops a server telling us to wait an hour from turning
// one command into an hour-long hang.
func TestRetryAfterIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, clock := newTestClient(t, srv, transport.Options{Retries: 1})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(clock.slept) != 1 {
		t.Fatalf("slept %v", clock.slept)
	}
	if clock.slept[0] > 30*time.Second {
		t.Errorf("slept %s, which is longer than any single wait should be", clock.slept[0])
	}
}

func TestGarbageRetryAfterFallsBackToBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "soon-ish")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, clock := newTestClient(t, srv, transport.Options{Retries: 1})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(clock.slept) != 1 || clock.slept[0] != 500*time.Millisecond {
		t.Errorf("slept %v, want the exponential default", clock.slept)
	}
}

// TestJitterSpreadsRetries asserts the wait actually varies. Without jitter,
// every client throttled at the same instant retries at the same instant, and
// the herd re-creates the overload.
func TestJitterSpreadsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	seen := map[time.Duration]bool{}
	for i := range 8 {
		clock := newFakeClock()
		// A different jitter per client, as a real random source would give.
		jitter := float64(i+1) / 10
		c, err := transport.New(transport.Options{
			BaseURL: srv.URL, Retries: 1, Clock: clock,
			Jitter: func() float64 { return jitter },
		})
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		if _, err := c.Do(t.Context(), transport.Request{
			Method: http.MethodGet, Path: "/x",
		}); err != nil {
			t.Fatalf("do: %v", err)
		}
		seen[clock.slept[0]] = true
	}
	if len(seen) < 5 {
		t.Errorf("only %d distinct waits across 8 clients; retries are not being spread", len(seen))
	}
}

func TestBudgetStopsRunawayPagination(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{MaxRequests: 3})
	for i := range 3 {
		if _, err := c.Do(t.Context(), transport.Request{
			Method: http.MethodGet, Path: "/x",
		}); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}

	_, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("the request budget was exceeded without an error")
	}
	if !transport.IsBudgetExceeded(err) {
		t.Errorf("IsBudgetExceeded = false for %v", err)
	}
	// Exit 3, not a generic failure: hitting the budget means "there is more",
	// and the caller turns it into a partial result with a resumable cursor.
	if errs.ExitOf(err) != exitcode.Partial {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Partial)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("made %d requests despite a budget of 3", got)
	}
	if c.Requests() != 3 || c.Remaining() != 0 {
		t.Errorf("Requests = %d, Remaining = %d", c.Requests(), c.Remaining())
	}
}

// TestRetriesCountAgainstTheBudget matters because a retry is another request
// from the server's point of view; a budget that ignored them would bound
// nothing.
func TestRetriesCountAgainstTheBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{Retries: 10, MaxRequests: 2})
	_, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("retrying past the budget was allowed")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("made %d requests despite a budget of 2", got)
	}
}

func TestUnlimitedBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	if c.Remaining() != -1 {
		t.Errorf("Remaining = %d, want -1 for unlimited", c.Remaining())
	}
	for range 20 {
		if _, err := c.Do(t.Context(), transport.Request{
			Method: http.MethodGet, Path: "/x",
		}); err != nil {
			t.Fatalf("do: %v", err)
		}
	}
	if c.Requests() != 20 {
		t.Errorf("Requests = %d", c.Requests())
	}
}

func TestCancellationStopsRetrying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	clock := newFakeClock()
	clock.cancel = context.Canceled

	c, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: 5, Clock: clock,
		Jitter: func() float64 { return 1.0 },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err == nil {
		t.Fatal("a cancelled context did not stop the retry loop")
	}
}

func TestAuthorizerErrorIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	want := errs.Auth("NO_CREDENTIALS", "no credentials for this site")
	c, _ := newTestClient(t, srv, transport.Options{
		Auth: transport.AuthorizerFunc(
			func(context.Context, transport.RequestInfo) (map[string]string, error) {
				return nil, want
			},
		),
	})
	_, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("an authorizer failure was swallowed")
	}
	if errs.ExitOf(err) != exitcode.Auth {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Auth)
	}
}

func TestUserAgentIsSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(transport.HeaderUserAgent)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{UserAgent: "jr/0.1.0-dev (reader)"})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if got != "jr/0.1.0-dev (reader)" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestNetworkFailureIsRemote(t *testing.T) {
	c, err := transport.New(transport.Options{BaseURL: "http://127.0.0.1:1", Retries: -1})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("connecting to a closed port succeeded")
	}
	if errs.ExitOf(err) != exitcode.Remote {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Remote)
	}
	if e := errs.Coerce(err); !e.Retryable {
		t.Error("a network failure is not marked retryable")
	}
}

func TestTimeoutIsRemote(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(block); srv.Close() }()

	c, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: -1, Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("a hung request did not time out")
	}
	if got := errs.Coerce(err).Code; got != "TIMEOUT" {
		t.Errorf("code = %q, want TIMEOUT", got)
	}
}

func TestPathJoining(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, path := range []string{"/rest/api/3/myself", "rest/api/3/myself"} {
		c, _ := newTestClient(t, srv, transport.Options{})
		if _, err := c.Do(t.Context(), transport.Request{
			Method: http.MethodGet, Path: path,
		}); err != nil {
			t.Fatalf("do(%q): %v", path, err)
		}
		if got != "/rest/api/3/myself" {
			t.Errorf("path %q resolved to %q", path, got)
		}
	}
}

func TestTrailingSlashOnBaseURL(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := transport.New(transport.Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/rest/api/3/myself",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if got != "/rest/api/3/myself" {
		t.Errorf("path = %q, want no doubled slash", got)
	}
}

func TestConcurrentRequestsShareTheBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{MaxRequests: 50})

	const workers = 8
	done := make(chan struct{}, workers)
	for range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 10 {
				_, _ = c.Do(t.Context(), transport.Request{
					Method: http.MethodGet, Path: "/x",
				})
			}
		}()
	}
	for range workers {
		<-done
	}
	if got := c.Requests(); got != 50 {
		t.Errorf("Requests = %d, want exactly the budget of 50", got)
	}
}

func TestErrOnNilAndSuccess(t *testing.T) {
	if err := transport.Err(nil); err == nil {
		t.Error("Err(nil) reported success")
	}
	for _, status := range []int{200, 201, 204} {
		if err := transport.Err(&transport.Response{Status: status}); err != nil {
			t.Errorf("Err(%d) = %v, want nil", status, err)
		}
	}
}

func ExampleClient_Do() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountId":"712020:8f3a"}`))
	}))
	defer srv.Close()

	c, err := transport.New(transport.Options{BaseURL: srv.URL})
	if err != nil {
		panic(err)
	}
	resp, err := c.Do(context.Background(), transport.Request{
		Method: http.MethodGet,
		Path:   "/rest/api/3/myself",
	})
	if err != nil {
		panic(err)
	}
	if err := transport.Err(resp); err != nil {
		panic(err)
	}
	fmt.Println(resp.Status, string(resp.Body))
	// Output: 200 {"accountId":"712020:8f3a"}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
