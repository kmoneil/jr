package transport_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/transport"
)

// goneBody is what Cloud answered a second DELETE of a comment with on
// 2026-11-05. An issue, a worklog and a link were answered the same way, in
// their own words.
const goneBody = `{"errorMessages":["Can not find a comment for the id: 10503."],"errors":{}}`

// scriptedJira answers each request with the next of its answers: a status, or
// "drop" to close the connection once the request has been read, the way a
// proxy that gave up on Jira does.
func scriptedJira(t *testing.T, answers ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1))
		if n > len(answers) {
			t.Errorf("request %d, and the script has %d answers", n, len(answers))
			w.WriteHeader(http.StatusTeapot)
			return
		}
		switch answers[n-1] {
		case "drop":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("the test server cannot drop a connection")
				return
			}
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "404":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, goneBody)
		default:
			t.Errorf("no answer called %q", answers[n-1])
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestADeleteFoundGoneOnItsResendHasAnUnknownOutcome is t400. The transport
// sends a DELETE again after a 5xx, which HTTP allows, and Cloud answers a
// second delete of an issue, a comment, a worklog or a link with 404, measured
// 2026-11-05. So a delete Jira did behind a proxy that answered 503 was
// reported as NOT_FOUND, exit 5: the caller was told what they named did not
// exist, by the run that removed it. The 404 cannot tell that apart from a key
// that never existed, so the answer is OUTCOME_UNKNOWN, as for a write
// answered 5xx; and only when an earlier attempt may have been carried out.
func TestADeleteFoundGoneOnItsResendHasAnUnknownOutcome(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		answers []string
		code    string
		exit    exitcode.Code
	}{
		{
			name: "a 503 and then 404", method: http.MethodDelete,
			answers: []string{"503", "404"}, code: "OUTCOME_UNKNOWN", exit: exitcode.Remote,
		},
		{
			name: "a dropped connection and then 404", method: http.MethodDelete,
			answers: []string{"drop", "404"}, code: "OUTCOME_UNKNOWN", exit: exitcode.Remote,
		},
		{
			name: "a 429 then a 503 and then 404", method: http.MethodDelete,
			answers: []string{"429", "503", "404"}, code: "OUTCOME_UNKNOWN", exit: exitcode.Remote,
		},
		// A 429 is refused before Jira does anything, so the 404 after it is
		// about the resource, as if it had come first.
		{
			name: "a 429 and then 404", method: http.MethodDelete,
			answers: []string{"429", "404"}, code: "NOT_FOUND", exit: exitcode.NotFound,
		},
		{
			name: "404 at once", method: http.MethodDelete,
			answers: []string{"404"}, code: "NOT_FOUND", exit: exitcode.NotFound,
		},
		// A PUT changes a resource and never removes one, so its first attempt
		// cannot be why the second finds nothing there.
		{
			name: "a PUT answered 503 and then 404", method: http.MethodPut,
			answers: []string{"503", "404"}, code: "NOT_FOUND", exit: exitcode.NotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := scriptedJira(t, tc.answers...)
			c, _ := newTestClient(t, srv, transport.Options{Retries: 3})
			resp, err := c.Do(t.Context(), transport.Request{
				Method: tc.method, Path: "/rest/api/3/issue/ENG-1/comment/10503",
			})
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			if n := int(calls.Load()); n != len(tc.answers) {
				t.Fatalf("the server saw %d requests, and the script is %d long, so "+
					"this is not the history it names", n, len(tc.answers))
			}
			e := errs.Coerce(transport.Err(resp))
			if e.Code != tc.code || e.Exit != tc.exit || e.Retryable {
				t.Errorf("got %s exit %v retryable %t, want %s exit %v, not retryable",
					e.Code, e.Exit, e.Retryable, tc.code, tc.exit)
			}
			// Jira's words and the endpoint still reach the caller.
			if !strings.Contains(e.Detail, "Can not find a comment") ||
				!strings.Contains(e.Detail, "/comment/10503") {
				t.Errorf("detail = %q, want Jira's message and the endpoint", e.Detail)
			}
		})
	}
}

// TestADeleteWhoseDialFailedReadsItsNotFound keeps a failure that proves the
// request never left this process out of the doubt: a dial that failed sent
// nothing, so the 404 after it is about the resource.
func TestADeleteWhoseDialFailedReadsItsNotFound(t *testing.T) {
	srv, calls := scriptedJira(t, "404")
	var attempts atomic.Int32
	dialFirst := roundTripper(func(r *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	c, _ := newTestClient(t, srv, transport.Options{
		Retries: 3, HTTPClient: &http.Client{Transport: dialFirst},
	})
	resp, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodDelete, Path: "/rest/api/3/issue/ENG-1/comment/10503",
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if attempts.Load() != 2 || calls.Load() != 1 {
		t.Fatalf("%d attempts and %d reached the server, want 2 and 1, so the dial "+
			"failure was not the history", attempts.Load(), calls.Load())
	}
	if e := errs.Coerce(transport.Err(resp)); e.Code != "NOT_FOUND" {
		t.Errorf("got %s after a dial that sent nothing, want NOT_FOUND", e.Code)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
