package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/transport"
)

// Every other test in this package injects a clock, a jitter source, and a
// tracer. That leaves the real implementations — the ones that actually ship —
// exercised by nothing. These cover them.

// TestRealClockSleeps is the production wait path. A Sleep that returned
// immediately would turn every backoff into a busy retry loop, and no test
// using a fake clock would notice.
func TestRealClockSleeps(t *testing.T) {
	var slept atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if slept.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		slept.Store(true)
	}))
	defer srv.Close()

	// No Clock: this exercises realClock.
	c, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: 2})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	start := time.Now()
	resp, err := c.Do(t.Context(), transport.Request{Method: http.MethodGet, Path: "/x"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusOK || resp.Attempts != 2 {
		t.Fatalf("status = %d, attempts = %d", resp.Status, resp.Attempts)
	}
	// Retry-After: 0 means the wait is real but negligible; the point is that
	// the real Sleep ran and returned.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a Retry-After of 0 took %s", elapsed)
	}
}

// TestRealClockSleepIsInterruptible asserts a cancelled context does not have
// to wait out the backoff. Without this, Ctrl-C during a long Retry-After hangs
// until the timer fires.
func TestRealClockSleepIsInterruptible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: 5})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := c.Do(ctx, transport.Request{Method: http.MethodGet, Path: "/x"}); err == nil {
		t.Fatal("a cancelled context still returned a response")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %s; the backoff was not interruptible", elapsed)
	}
}

// TestDefaultJitterIsInRange guards the contract backoff relies on: a jitter
// outside [0,1) would either collapse the spread or overshoot the schedule.
func TestDefaultJitterIsInRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	seen := map[time.Duration]bool{}
	for range 12 {
		clock := newFakeClock()
		// No Jitter: this exercises defaultJitter.
		c, err := transport.New(transport.Options{
			BaseURL: srv.URL, Retries: 1, Clock: clock,
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
			t.Fatalf("slept %v", clock.slept)
		}
		d := clock.slept[0]
		if d <= 0 || d > 500*time.Millisecond {
			t.Fatalf("waited %s, outside the first backoff window", d)
		}
		seen[d] = true
	}
	// Real randomness should not produce one value twelve times.
	if len(seen) < 6 {
		t.Errorf("only %d distinct waits from the real jitter source", len(seen))
	}
}

// TestDefaultTracerDiscards asserts a client with no tracer configured works
// and costs nothing.
func TestDefaultTracerDiscards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := transport.New(transport.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
}

func TestTracerFunc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var events []transport.Event
	c, err := transport.New(transport.Options{
		BaseURL: srv.URL,
		Tracer: transport.TracerFunc(func(e transport.Event) {
			events = append(events, e)
		}),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/x",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want a request and a response", len(events))
	}
	if events[0].Kind != transport.EventRequest || events[1].Kind != transport.EventResponse {
		t.Errorf("kinds = %q, %q", events[0].Kind, events[1].Kind)
	}
	if events[1].Status != http.StatusOK {
		t.Errorf("response event status = %d", events[1].Status)
	}
	if events[0].RequestID == "" {
		t.Error("the request event carries no correlation id")
	}
}

// TestRecordThenReplay closes the loop the fixture workflow depends on: what
// the recorder writes must be what the replayer can serve.
func TestRecordThenReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"Ada Lovelace"}`))
		case "/rest/api/3/issue":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"key":"ENG-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rec := transport.NewRecorder(nil, transport.Cloud)
	live, err := transport.New(transport.Options{
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Transport: rec},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := live.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/rest/api/3/myself",
	}); err != nil {
		t.Fatalf("record get: %v", err)
	}
	if _, err := live.Do(t.Context(), transport.Request{
		Method: http.MethodPost, Path: "/rest/api/3/issue",
		Body: []byte(`{"fields":{"summary":"x"}}`),
	}); err != nil {
		t.Fatalf("record post: %v", err)
	}

	// Persist and reload, so the JSON encoding is part of what is proven.
	path := t.TempDir() + "/recorded.json"
	if err := rec.Cassette().Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	cassette, err := transport.LoadCassette(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	replayer := transport.NewReplayer(cassette)
	replayed, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: replayer.Client(),
	})
	if err != nil {
		t.Fatalf("new replay client: %v", err)
	}

	resp, err := replayed.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/rest/api/3/myself",
	})
	if err != nil {
		t.Fatalf("replay get: %v", err)
	}
	if !strings.Contains(string(resp.Body), "Ada Lovelace") {
		t.Errorf("replayed body = %s", resp.Body)
	}

	resp, err = replayed.Do(t.Context(), transport.Request{
		Method: http.MethodPost, Path: "/rest/api/3/issue",
		Body: []byte(`{"fields":{"summary":"x"}}`),
	})
	if err != nil {
		t.Fatalf("replay post: %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Errorf("replayed status = %d", resp.Status)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("unplayed: %v", unplayed)
	}
}

// TestReplayerServesRepeatedCalls is what lets a retry test use a fixture: the
// same request asked for twice gets an answer twice.
func TestReplayerServesRepeatedCalls(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request:  transport.RecordedRequest{Method: http.MethodGet, Path: "/x"},
			Response: transport.RecordedResponse{Status: 200, Body: `{}`},
		}},
	}
	c, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(cassette).Client(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	for i := range 3 {
		if _, err := c.Do(t.Context(), transport.Request{
			Method: http.MethodGet, Path: "/x",
		}); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
}

func TestNonJSONBodyStillMatches(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.DataCenter,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: http.MethodPost, Path: "/x", Body: "plain   text  body",
			},
			Response: transport.RecordedResponse{Status: 200},
		}},
	}
	c, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(cassette).Client(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	// Whitespace differs; a non-JSON body is matched on its collapsed fields.
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodPost, Path: "/x", Body: []byte("plain text body"),
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
}

func TestMalformedQueryInAFixtureIsMatchedLiterally(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: http.MethodGet, Path: "/x", Query: "%zz=broken",
			},
			Response: transport.RecordedResponse{Status: 200},
		}},
	}
	replayer := transport.NewReplayer(cassette)
	// Reaching it through the RoundTripper directly, since a *url.URL cannot
	// carry an unparseable query built through url.Values.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://recorded.invalid/x?%zz=broken", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := replayer.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// TestRedactURLWithUsernameOnly pins the deliberate choice: the password is the
// secret and always goes, the username stays because "which account made this
// call" is the main question debug output exists to answer, and a URL with no
// password must not come back claiming one was redacted.
func TestRedactURLWithUsernameOnly(t *testing.T) {
	u, err := url.Parse("https://ada@acme.atlassian.invalid/x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := transport.RedactURL(u)
	if strings.Contains(got, transport.Redacted) {
		t.Errorf("a URL with no password reports one as redacted: %s", got)
	}
	if !strings.Contains(got, "ada") {
		t.Errorf("the username was dropped: %s", got)
	}
}

func TestPathWithInvalidEscapeIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/rest/%zz/broken",
	}); err == nil {
		t.Fatal("an unparseable path was accepted")
	}
}

func TestMethodIsRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, transport.Options{})
	if _, err := c.Do(t.Context(), transport.Request{Path: "/x"}); err == nil {
		t.Fatal("a request with no method was accepted")
	}
}

func TestNegativeRetryAfterFallsBackToBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "-5")
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

// TestRetryAfterInThePastMeansNow covers a server whose clock is behind ours.
func TestRetryAfterInThePastMeansNow(t *testing.T) {
	clock := newFakeClock()
	past := clock.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", past)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: 1, Clock: clock,
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
	if len(clock.slept) != 1 || clock.slept[0] != 0 {
		t.Errorf("slept %v, want no wait for a date already past", clock.slept)
	}
}

func TestTextTracerWritesHeaders(t *testing.T) {
	var out strings.Builder
	tracer := transport.NewTextTracer(&out)
	tracer.Trace(transport.Event{
		Kind:   transport.EventResponse,
		Method: http.MethodGet,
		URL:    "https://acme.atlassian.invalid/x",
		Status: 200,
		Header: http.Header{
			"Zebra":        {"last"},
			"Alpha":        {"first"},
			"Content-Type": {"application/json"},
		},
		Elapsed:   150 * time.Millisecond,
		Bytes:     42,
		RequestID: "abc",
	})

	got := out.String()
	// Sorted, so debug output is diffable between runs.
	alpha := strings.Index(got, "Alpha")
	contentType := strings.Index(got, "Content-Type")
	zebra := strings.Index(got, "Zebra")
	if alpha >= contentType || contentType >= zebra {
		t.Errorf("headers are not sorted:\n%s", got)
	}
	for _, want := range []string{"status=200", "bytes=42", "elapsed=150ms", "request-id=abc"} {
		if !strings.Contains(got, want) {
			t.Errorf("trace is missing %q:\n%s", want, got)
		}
	}
}

func TestErrorDetailCarriesTheDetailField(t *testing.T) {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	err := transport.Err(&transport.Response{
		Status: http.StatusBadRequest,
		Header: header,
		Body:   []byte(`{"message":"bad","detail":"field xyz is unknown"}`),
	})
	if !strings.Contains(err.Error(), "field xyz is unknown") {
		t.Errorf("the detail field was dropped: %v", err)
	}
}
