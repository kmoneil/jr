package transport_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// zeros is an endless reader, so a test can serve more bytes than the process
// could hold without allocating them.
type zeros struct{ remaining int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.remaining <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), z.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	z.remaining -= n
	return int(n), nil
}

// TestAStreamedDownloadRunsInConstantMemory is the reason this exists. The
// buffered path caps a body at 64MB and holds all of it, which turns a large
// attachment into an error rather than a download.
//
// The body here is larger than that cap, so the buffered path could not carry
// it at all, and the heap is measured either side to show it was never held.
func TestAStreamedDownloadRunsInConstantMemory(t *testing.T) {
	const size = 96 << 20 // over maxResponseBytes, deliberately.

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = io.Copy(w, &zeros{remaining: size})
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, transport.Options{Retries: 0})

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/attachment", Stream: true,
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Close()

	if resp.Stream == nil {
		t.Fatal("a streamed request came back with no reader")
	}
	if len(resp.Body) != 0 {
		t.Errorf("the body was buffered as well: %d bytes", len(resp.Body))
	}

	// io.Discard is the caller's file in production. Copying is what proves the
	// stream is live rather than a reader over something already read.
	n, err := io.Copy(io.Discard, resp.Stream)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != size {
		t.Errorf("copied %d bytes, want %d", n, size)
	}

	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	// A generous bound: the point is that it is not proportional to the body,
	// not that it is any particular number.
	if growth := after.HeapAlloc; growth > 32<<20 {
		t.Errorf("heap grew to %d bytes carrying a %d byte body", growth, size)
	}
}

// TestAStreamedFailureIsBufferedAnyway covers the exception. An error body is
// small and is the only thing that names what went wrong; handing back an
// unread stream would leave the caller draining it to find out why its request
// failed, and transport.Err reading nothing.
func TestAStreamedFailureIsBufferedAnyway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"errorMessages":["Attachment does not exist"]}`)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, transport.Options{Retries: 0})
	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/attachment/1", Stream: true,
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Close()

	if resp.Stream != nil {
		t.Error("a failed request handed back an unread stream")
	}
	if !strings.Contains(string(resp.Body), "does not exist") {
		t.Errorf("body = %q, want the server's message", resp.Body)
	}
	// The normal mapping still works, which is the point of buffering it.
	if err := transport.Err(resp); err == nil {
		t.Error("a 404 was not turned into an error")
	} else if code := errs.Coerce(err).Code; code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", code)
	}
}

// TestADownloadDoesNotAskForJSON covers the header that would otherwise invite
// the server to answer an attachment request with a JSON error — a 406 that
// reads like a missing attachment.
func TestADownloadDoesNotAskForJSON(t *testing.T) {
	var accept atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept.Store(r.Header.Get("Accept"))
		_, _ = fmt.Fprint(w, "file contents")
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, transport.Options{Retries: 0})
	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet, Path: "/attachment/1", Stream: true,
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Close()
	_, _ = io.Copy(io.Discard, resp.Stream)

	if got := accept.Load(); got != "*/*" {
		t.Errorf("Accept = %q, want */*", got)
	}
}

// reopenable is an upload source that can be read again, which is what a retry
// needs. It counts how many times it was opened.
type reopenable struct {
	content string
	opens   atomic.Int32
}

func (s *reopenable) open() (io.ReadCloser, error) {
	s.opens.Add(1)
	return io.NopCloser(strings.NewReader(s.content)), nil
}

// TestAReopenedBodyIsSentWholeOnARetry is the upload half. The first attempt
// consumes the reader; without re-opening, the retry would send nothing and the
// server would accept an empty file.
func TestAReopenedBodyIsSentWholeOnARetry(t *testing.T) {
	var attempts atomic.Int32
	var received atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received.Store(string(body))
		if attempts.Add(1) == 1 {
			// 429 is refused before processing, so it is retried whatever the
			// method — which is exactly the case an upload has to survive.
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	source := &reopenable{content: "the whole file"}
	client, _ := newTestClient(t, srv, transport.Options{Retries: 2})
	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodPost, Path: "/attachments",
		BodySource: source.open,
		Header:     http.Header{"Content-Type": {"application/octet-stream"}},
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Fatalf("status = %d", resp.Status)
	}
	if got := received.Load(); got != "the whole file" {
		t.Errorf("the retry sent %q, want the whole body again", got)
	}
	if opens := source.opens.Load(); opens != 2 {
		t.Errorf("the source was opened %d times, want one per attempt", opens)
	}
}

// TestABodyThatCannotBeReopenedIsRefusedNotTruncated is the rule the card asks
// for. A body read from a pipe cannot be sent twice, and the failure mode
// without this is the worst kind: the retry succeeds, having uploaded nothing.
func TestABodyThatCannotBeReopenedIsRefusedNotTruncated(t *testing.T) {
	var attempts atomic.Int32
	var lastBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(string(body))
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	var opened atomic.Int32
	stdinLike := func() (io.ReadCloser, error) {
		if opened.Add(1) > 1 {
			return nil, errors.New("stdin has already been read")
		}
		return io.NopCloser(strings.NewReader("from a pipe")), nil
	}

	client, _ := newTestClient(t, srv, transport.Options{Retries: 2})
	_, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodPost, Path: "/attachments", BodySource: stdinLike,
	})
	if err == nil {
		t.Fatal("a body that cannot be re-sent was retried anyway")
	}

	e := errs.Coerce(err)
	if e.Code != "BODY_NOT_REPLAYABLE" {
		t.Fatalf("code = %q, want BODY_NOT_REPLAYABLE", e.Code)
	}
	if !strings.Contains(e.Remedy, "file") {
		t.Errorf("remedy = %q, want it to say what to do instead", e.Remedy)
	}
	// The second attempt never reached the server, so nothing empty was
	// accepted in place of the file.
	if n := attempts.Load(); n != 1 {
		t.Errorf("the server saw %d attempts, want only the first", n)
	}
	if got := lastBody.Load(); got != "from a pipe" {
		t.Errorf("the server received %q", got)
	}
}

// TestAByteBodyStillDefaultsToJSON keeps the existing behaviour intact: only a
// BodySource carries content of some other type, and only it skips the default.
func TestAByteBodyStillDefaultsToJSON(t *testing.T) {
	var contentType atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType.Store(r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, transport.Options{Retries: 0})
	if _, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodPost, Path: "/issue", Body: []byte(`{"a":1}`),
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if got := contentType.Load(); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// TestCloseIsSafeWhateverHappened covers the helper a caller defers before it
// knows whether this particular request streamed.
func TestCloseIsSafeWhateverHappened(t *testing.T) {
	var nilResp *transport.Response
	nilResp.Close()

	buffered := &transport.Response{Body: []byte("read already")}
	buffered.Close()
	buffered.Close()

	closed := &countingCloser{Reader: strings.NewReader("x")}
	streamed := &transport.Response{Stream: closed}
	streamed.Close()
	streamed.Close()
	if closed.closes != 1 {
		t.Errorf("the body was closed %d times, want exactly one", closed.closes)
	}
}

type countingCloser struct {
	io.Reader
	closes int
}

func (c *countingCloser) Close() error {
	c.closes++
	return nil
}

// TestAStreamedRequestReplaysFromACassette keeps the fixture path working for
// the resource that will use this. Every resource tests against a recording,
// and a download that only worked against a live server would be untestable
// under the one rule this project does not bend: tests never touch the network.
func TestAStreamedRequestReplaysFromACassette(t *testing.T) {
	cassette := &transport.Cassette{
		Deployment: transport.DataCenter,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: "GET", Path: "/rest/api/2/attachment/content/1",
			},
			Response: transport.RecordedResponse{
				Status: 200,
				Header: map[string][]string{"Content-Type": {"application/octet-stream"}},
				Body:   "recorded file contents",
			},
		}},
	}

	replayer := transport.NewReplayer(cassette)
	client, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	resp, err := client.Do(t.Context(), transport.Request{
		Method: transport.MethodGet,
		Path:   "/rest/api/2/attachment/content/1",
		Stream: true,
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Close()

	got, err := io.ReadAll(resp.Stream)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "recorded file contents" {
		t.Errorf("read %q", got)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the request was never played: %v", unplayed)
	}
}

// TestRelativeRefusesAnotherHost is the guard on a value the server chooses.
//
// Data Center reports an attachment's content as an absolute URL. Following it
// as given would hand the credential to whatever host it names, and unlike a
// bad --site that is not a mistake the caller made or could see.
func TestRelativeRefusesAnotherHost(t *testing.T) {
	client, err := transport.New(transport.Options{
		BaseURL: "https://jira.acme.invalid/jira",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	for _, tc := range []struct {
		name, raw, want string
	}{
		{
			name: "absolute on the configured site, under its context path",
			raw:  "https://jira.acme.invalid/jira/secure/attachment/10042/report.pdf",
			want: "/secure/attachment/10042/report.pdf",
		},
		{
			// The port is implied by the scheme on one side and written on the
			// other. They are one host, not two.
			name: "the same host with the default port spelled out",
			raw:  "https://jira.acme.invalid:443/jira/secure/attachment/1/a.txt",
			want: "/secure/attachment/1/a.txt",
		},
		{
			name: "a relative value is already what we want",
			raw:  "/rest/api/2/attachment/content/1",
			want: "/rest/api/2/attachment/content/1",
		},
		{
			name: "a query survives",
			raw:  "https://jira.acme.invalid/jira/secure/attachment/1/a.txt?v=2",
			want: "/secure/attachment/1/a.txt?v=2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.Relative(tc.raw)
			if err != nil {
				t.Fatalf("Relative(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("Relative(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	for _, tc := range []struct{ name, raw, code string }{
		{"another host entirely", "https://evil.invalid/steal", "OFF_SITE_URL"},
		{"a subdomain is not the same host", "https://x.jira.acme.invalid/jira/a", "OFF_SITE_URL"},
		{"a different scheme", "http://jira.acme.invalid/jira/a", "OFF_SITE_URL"},
		{"a different port", "https://jira.acme.invalid:8443/jira/a", "OFF_SITE_URL"},
		{"outside the context path", "https://jira.acme.invalid/other/a", "OFF_SITE_URL"},
		{"nothing at all", "  ", "NO_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Relative(tc.raw)
			if err == nil {
				t.Fatalf("Relative(%q) was accepted", tc.raw)
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

// TestRelativeDoesNotEchoTheURL keeps a server-supplied URL out of the error
// text. A URL is not obviously a secret, which is exactly why one carrying
// userinfo or a signed query parameter gets printed by accident.
func TestRelativeDoesNotEchoTheURL(t *testing.T) {
	client, err := transport.New(transport.Options{BaseURL: "https://jira.acme.invalid"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	const secret = "s3cr3t-signature"
	_, err = client.Relative("https://cdn.invalid/a?signature=" + secret)
	if err == nil {
		t.Fatal("an off-site URL was accepted")
	}
	e := errs.Coerce(err)
	for _, text := range []string{e.Message, e.Detail, e.Remedy} {
		if strings.Contains(text, secret) {
			t.Errorf("the refusal echoed the query: %q", text)
		}
	}
}
