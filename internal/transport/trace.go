package transport

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// EventKind classifies a trace event.
type EventKind string

// The trace event kinds.
const (
	EventRequest  EventKind = "request"
	EventResponse EventKind = "response"
	EventRetry    EventKind = "retry"
	EventFailure  EventKind = "failure"
)

// Event is one step of an HTTP exchange, already redacted.
//
// Redaction happens when the event is built, inside this package, not when it
// is formatted. A Tracer implementation can print every field it is given and
// still cannot leak a credential, because a credential never reaches it. That
// is the difference between a rule the debug formatter has to follow and a
// rule it cannot break.
type Event struct {
	Kind      EventKind
	Method    string
	URL       string
	Status    int
	Header    http.Header
	Attempt   int
	Elapsed   time.Duration
	Wait      time.Duration
	RequestID string
	Reason    string
	Bytes     int
}

// Tracer receives trace events. It is the only way this package reports what it
// did over the wire.
type Tracer interface {
	Trace(Event)
}

// TracerFunc adapts a function to Tracer.
type TracerFunc func(Event)

// Trace implements Tracer.
func (f TracerFunc) Trace(e Event) { f(e) }

// discardTracer drops every event. It is the default, so a client with no
// tracer configured costs nothing.
type discardTracer struct{}

func (discardTracer) Trace(Event) {}

// NewTextTracer returns a Tracer writing one line per event to w, which is
// always stderr — never stdout, which carries the result and nothing else.
func NewTextTracer(w io.Writer) Tracer {
	t := &textTracer{w: w}
	return t
}

type textTracer struct {
	mu sync.Mutex
	w  io.Writer
}

func (t *textTracer) Trace(e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "[http] %s", e.Kind)
	if e.Attempt > 0 {
		fmt.Fprintf(&b, " attempt=%d", e.Attempt)
	}
	if e.Method != "" {
		fmt.Fprintf(&b, " %s %s", e.Method, e.URL)
	}
	if e.Status != 0 {
		fmt.Fprintf(&b, " status=%d", e.Status)
	}
	if e.Bytes > 0 {
		fmt.Fprintf(&b, " bytes=%d", e.Bytes)
	}
	if e.Elapsed > 0 {
		fmt.Fprintf(&b, " elapsed=%s", e.Elapsed.Round(time.Millisecond))
	}
	if e.Wait > 0 {
		fmt.Fprintf(&b, " wait=%s", e.Wait.Round(time.Millisecond))
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " request-id=%s", e.RequestID)
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, " reason=%q", e.Reason)
	}
	b.WriteByte('\n')

	for _, name := range sortedHeaderNames(e.Header) {
		fmt.Fprintf(&b, "[http]   %s: %s\n", name, strings.Join(e.Header[name], ", "))
	}

	_, _ = io.WriteString(t.w, b.String())
}

func sortedHeaderNames(h http.Header) []string {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	// Sorted so debug output is diffable between runs.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// requestEvent builds a redacted request event.
func requestEvent(req *http.Request, attempt int, requestID string) Event {
	return Event{
		Kind:      EventRequest,
		Method:    req.Method,
		URL:       RedactURL(req.URL),
		Header:    RedactHeader(req.Header),
		Attempt:   attempt,
		RequestID: requestID,
	}
}

// responseEvent builds a redacted response event.
func responseEvent(u *url.URL, method string, status int, header http.Header,
	attempt, size int, elapsed time.Duration, requestID string,
) Event {
	return Event{
		Kind:      EventResponse,
		Method:    method,
		URL:       RedactURL(u),
		Status:    status,
		Header:    RedactHeader(header),
		Attempt:   attempt,
		Bytes:     size,
		Elapsed:   elapsed,
		RequestID: requestID,
	}
}
