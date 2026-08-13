package transport_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/transport"
)

// maxBuffered is the cap the client will hold for a response it is not
// streaming. It is a copy of an unexported constant, which is normally a thing
// to avoid, and it is here because the alternative is worse: exporting the
// number would let a caller build a request around it, and a test hook that
// lowers it would leave the production wiring — that receive passes this
// particular number — asserted by nothing. The end-to-end tests below cost 64
// MiB once each and prove the whole path.
const maxBuffered = 64 << 20

// TestABufferedResponsePastTheCapIsRefused is the regression for a silent
// truncation that arrived through the door marked "safety limit".
//
// The buffered path read with io.ReadAll over an io.LimitReader at the cap.
// That returns exactly the cap and a nil error for a body of any size beyond
// it, so a caller was handed a clipped body presented as a whole one. The
// streamed path already refuses this and boundedBody's comment says why
// LimitReader is the wrong primitive for it; the buffered path went on using
// it.
//
// What made it hard to see is that it usually failed loudly and wrongly: every
// consumer decodes JSON, so a clipped body surfaced as MALFORMED_SEARCH,
// "Jira returned a search result this tool cannot read", when what happened is
// that this client stopped reading.
func TestABufferedResponsePastTheCapIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprint(maxBuffered+1))
		_, _ = io.Copy(w, &zeros{remaining: maxBuffered + 1})
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, transport.Options{Retries: 0})

	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/rest/api/2/search",
	})
	if err == nil {
		defer resp.Close()
		t.Fatalf("a body of %d bytes was accepted, and %d of them came back",
			maxBuffered+1, len(resp.Body))
	}

	e, ok := errs.AsError(err)
	if !ok {
		t.Fatalf("the refusal is not structured: %v", err)
	}
	if e.Code != "RESPONSE_TOO_LARGE" {
		t.Errorf("code = %q, want RESPONSE_TOO_LARGE: %v", e.Code, err)
	}
	// Not retryable, and not exit 9. A response too large once is too large
	// again, and advertising a retry that cannot work spends a caller's budget
	// on a certainty. This is the reasoning errUnboundedStream already applies
	// to the streamed side of the same question.
	if e.Exit != exitcode.Error {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Error)
	}
	if e.Retryable {
		t.Error("a body that will be the same size next time is advertised as retryable")
	}
}

// TestABufferedResponseAtTheCapIsKept is the control. The check is off by one
// either way, and a cap that refused the largest legal body would be a cap that
// refused a body it was built to carry.
func TestABufferedResponseAtTheCapIsKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprint(maxBuffered))
		_, _ = io.Copy(w, &zeros{remaining: maxBuffered})
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, transport.Options{Retries: 0})

	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/rest/api/2/search",
	})
	if err != nil {
		t.Fatalf("a body of exactly the cap was refused: %v", err)
	}
	defer resp.Close()

	if len(resp.Body) != maxBuffered {
		t.Errorf("got %d bytes, want the whole %d", len(resp.Body), maxBuffered)
	}
}
