package transport_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// streamFrom drives one streamed GET against h and returns the response.
func streamFrom(t *testing.T, h http.HandlerFunc) *transport.Response {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	client, err := transport.New(transport.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	resp, err := client.Do(context.Background(), transport.Request{
		Method: transport.MethodGet, Path: "/thing", Stream: true,
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(resp.Close)
	return resp
}

// endless writes b forever, flushing, so the response is chunked and declares
// no length. It stops when the client goes away.
func endless(b byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		block := make([]byte, 64*1024)
		for i := range block {
			block[i] = b
		}
		flusher, _ := w.(http.Flusher)
		for {
			if _, err := w.Write(block); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}
}

// TestAnUndeclaredStreamIsRefusedRatherThanTruncated is the fix.
//
// A response with no Content-Length is bounded by nothing at any layer: not by
// net/http, which only limits a body when a length was declared, and not by the
// per-attempt deadline, which deliberately ends at the first byte so a slow
// download is not killed mid-copy.
//
// It fails rather than ending, and that distinction is the point. io.LimitReader
// returns io.EOF, so a cap built on it writes a short file and reports success —
// the silent truncation this project exists to prevent, arriving through the
// door marked "safety limit".
//
// The read is capped low by reading only a slice at a time; the ceiling itself
// is 2 GiB and driving it literally would mean moving two gigabytes through a
// test. What is asserted is the shape: bytes flow, and then an error arrives
// rather than an EOF.
func TestAnUndeclaredStreamIsRefusedRatherThanTruncated(t *testing.T) {
	resp := streamFrom(t, endless('x'))

	if resp.Stream == nil {
		t.Fatal("the response did not stream")
	}
	// It still delivers: a bound that broke ordinary downloads would satisfy
	// every other assertion in this file.
	if _, err := io.CopyN(io.Discard, resp.Stream, 1<<20); err != nil {
		t.Fatalf("the stream did not deliver its first megabyte: %v", err)
	}

	// And the bound is attached. Removing the wrapper from handOver broke no
	// test until this line existed — the ceiling is 2 GiB, so nothing driving a
	// real response reaches it, and the whole fix could have been deleted with
	// the suite still green.
	limit, bounded := transport.StreamBoundForTest(resp.Stream)
	if !bounded {
		t.Fatal("an undeclared stream was handed over unbounded")
	}
	if want := int64(transport.MaxUndeclaredStreamBytesForTest) - (1 << 20); limit != want {
		t.Errorf("limit = %d after a megabyte, want %d", limit, want)
	}
}

// TestADeclaredStreamIsNotWrapped is the other side of the same wiring.
//
// The ceiling must not touch a body whose length the server stated, and a check
// that only ever confirms the wrapper is present would be satisfied by wrapping
// everything.
func TestADeclaredStreamIsNotWrapped(t *testing.T) {
	resp := streamFrom(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("abcd"))
	})

	if _, bounded := transport.StreamBoundForTest(resp.Stream); bounded {
		t.Error("a body that declared its length was wrapped in the ceiling; " +
			"net/http already bounds it, and capping a declared size would " +
			"refuse an attachment somebody legitimately stored")
	}
}

// TestTheBoundedBodyFailsInsteadOfEnding drives the wrapper's own boundary,
// which the 2 GiB ceiling makes impractical to reach through a real response.
//
// A reader that returns io.EOF at the limit and one that returns an error are
// indistinguishable to a caller counting bytes, and only one of them is
// correct: io.Copy reports nil for the first, so the caller writes a truncated
// file and calls it a download.
func TestTheBoundedBodyFailsInsteadOfEnding(t *testing.T) {
	body := transport.NewBoundedBodyForTest(io.NopCloser(strings.NewReader(
		strings.Repeat("y", 100),
	)), 10)

	n, err := io.Copy(io.Discard, body)

	if err == nil {
		t.Fatalf("copied %d bytes and reported success; a cap that ends the "+
			"stream is a cap that truncates silently", n)
	}
	if n != 10 {
		t.Errorf("delivered %d bytes before failing, want 10", n)
	}

	var structured *errs.Error
	if !errors.As(err, &structured) {
		t.Fatalf("error is not structured: %v", err)
	}
	if structured.Code != "UNBOUNDED_RESPONSE" {
		t.Errorf("code = %q, want UNBOUNDED_RESPONSE", structured.Code)
	}
	// Retrying cannot help: a server that streams without declaring a length
	// will do the same next time. This is the OFF_SITE_URL lesson applied
	// before it bites rather than after.
	if structured.Retryable {
		t.Error("the refusal advertises a retry that cannot work")
	}
}

// TestNetHTTPAlreadyBoundsADeclaredBody pins the measurement the decision not
// to build a --max-size flag rests on.
//
// The original card claimed io.Copy with no LimitReader let a server flood the
// caller. It does not, when a length is declared: net/http limits the body to
// Content-Length. An unasserted premise is one somebody re-litigates, and this
// one argued a flag off the command surface.
func TestNetHTTPAlreadyBoundsADeclaredBody(t *testing.T) {
	resp := streamFrom(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		for range 100 {
			if _, err := w.Write([]byte("0123456789")); err != nil {
				return
			}
		}
	})

	n, err := io.Copy(io.Discard, resp.Stream)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 10 {
		t.Errorf("read %d bytes for a declared 10; the body is not bounded by "+
			"its Content-Length", n)
	}
}

// TestAShortDeclaredBodyAlreadyFails is the other half of the same premise: a
// download that ends early is already an error, so nothing here needs to
// compare the byte count against the declared size afterwards.
func TestAShortDeclaredBodyAlreadyFails(t *testing.T) {
	resp := streamFrom(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("0123456789"))
	})

	_, err := io.Copy(io.Discard, resp.Stream)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a body shorter than its Content-Length gave %v, want "+
			"io.ErrUnexpectedEOF", err)
	}
}

// TestADeclaredLengthIsNotCapped keeps the ceiling off the case it must not
// touch.
//
// A server that says how much it is sending has told us what to expect, and
// net/http enforces it. Capping a declared length would refuse an attachment
// somebody legitimately stored, which is a policy decision nobody asked for.
func TestADeclaredLengthIsNotCapped(t *testing.T) {
	const size = 3 << 20
	resp := streamFrom(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3145728")
		block := make([]byte, 64*1024)
		for range size / len(block) {
			if _, err := w.Write(block); err != nil {
				return
			}
		}
	})

	n, err := io.Copy(io.Discard, resp.Stream)
	if err != nil {
		t.Fatalf("a declared body was interrupted: %v", err)
	}
	if n != size {
		t.Errorf("read %d bytes, want %d", n, size)
	}
}
