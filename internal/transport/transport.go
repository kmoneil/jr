// Package transport is the single HTTP path to Jira.
//
// It owns retry with backoff and jitter, Retry-After, the request budget, and
// header redaction. Redaction lives here rather than in a debug formatter so no
// future code path can bypass it: a credential is replaced when a trace event is
// built, so it never reaches whatever is formatting the output. Nothing outside
// this package imports net/http, and a test in internal/lint asserts it.
//
// Every request carries an X-Request-Id, echoed into error output so a failure
// can be traced with Atlassian support.
package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Header names this package sets or reads.
const (
	HeaderRequestID = "X-Request-Id"
	HeaderUserAgent = "User-Agent"
)

// HTTP methods, re-exported so a caller can name one without importing
// net/http. That import is confined to this package so header redaction has
// exactly one place to hold, and a resource naming a method is not a reason to
// punch a hole in it.
const (
	MethodGet    = http.MethodGet
	MethodHead   = http.MethodHead
	MethodPost   = http.MethodPost
	MethodPut    = http.MethodPut
	MethodPatch  = http.MethodPatch
	MethodDelete = http.MethodDelete
)

// DefaultTimeout bounds a single HTTP attempt, not the whole retried call.
const DefaultTimeout = 30 * time.Second

// maxResponseBytes bounds a buffered response body. A Jira endpoint that
// returns more than this is either misbehaving or should have been streamed.
const maxResponseBytes = 64 << 20

// RequestInfo is what an Authorizer is told about the call it is signing. It
// carries no body and no headers, because nothing an Authorizer legitimately
// does needs them.
type RequestInfo struct {
	Method string
	// URL is the fully resolved target.
	URL string
}

// Authorizer supplies the credential headers for a request.
//
// It returns headers rather than mutating an *http.Request for two reasons.
// It keeps net/http confined to this package, so the redaction rule has one
// place to hold. And it means an Authorizer physically cannot rewrite the URL,
// change the method, or drop the body — none of which supplying a credential
// should be able to do.
type Authorizer interface {
	Authorize(ctx context.Context, req RequestInfo) (map[string]string, error)
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, req RequestInfo) (map[string]string, error)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, req RequestInfo) (map[string]string, error) {
	return f(ctx, req)
}

// Options configures a Client.
type Options struct {
	// BaseURL is the site root, e.g. https://your-site.atlassian.net.
	BaseURL string
	// Auth applies credentials. A nil Auth sends unauthenticated requests,
	// which is only useful against a public instance or in a test.
	Auth Authorizer
	// HTTPClient overrides the underlying client, and is how a recorded
	// fixture is replayed.
	HTTPClient *http.Client
	// RoundTripper wraps the transport without the caller building a client,
	// which is how recording is switched on from internal/cli. It exists so
	// that nothing outside this package has to import net/http — the rule that
	// keeps header redaction impossible to bypass. Ignored when HTTPClient is
	// set, because a caller that supplied a whole client has already decided.
	RoundTripper http.RoundTripper
	// Retries is the retry budget per request. Zero means DefaultRetries;
	// a negative value disables retrying.
	Retries int
	// MaxRequests caps total HTTP calls for one invocation. Zero means
	// unlimited. Exceeding it is a distinct error so the caller can turn it
	// into exit 3 with a resumable cursor rather than a generic failure.
	MaxRequests int
	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration
	// UserAgent identifies this build to the server.
	UserAgent string
	// Tracer receives redacted trace events. Nil discards them.
	Tracer Tracer
	// Clock is the time source. Nil uses the real one.
	Clock Clock
	// Jitter returns a value in [0,1) for backoff. Nil uses a real random
	// source; a test supplies a fixed one to get a deterministic schedule.
	Jitter func() float64
}

// Client is the HTTP path to one Jira site.
type Client struct {
	base      *url.URL
	http      *http.Client
	auth      Authorizer
	retries   int
	timeout   time.Duration
	userAgent string
	tracer    Tracer
	clock     Clock
	jitter    func() float64
	budget    *budget
}

// Request is one call to Jira, described in terms this package can retry.
//
// Body is bytes rather than a reader precisely so a retry can replay it. A
// reader would be consumed by the first attempt, and a retry would send an
// empty body — succeeding, and writing the wrong thing.
type Request struct {
	Method string
	// Path is relative to the site root, e.g. /rest/api/3/search/jql.
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte

	// BodySource supplies a request body too large to hold in memory — an
	// attachment upload, and nothing else so far. It takes precedence over
	// Body.
	//
	// It is a factory rather than a reader for the same reason Body is bytes:
	// a retry has to send the same content again, and a reader is spent by the
	// first attempt. A source that cannot be re-opened — stdin, a pipe —
	// returns an error on its second call, and the transport reports that
	// rather than sending a short body and calling it a success.
	BodySource func() (io.ReadCloser, error)

	// Stream asks for the response body as a reader instead of bytes.
	//
	// The caller owns it and must Close it. A response that is not a success is
	// buffered anyway, whatever this says: an error body is small, it is the
	// only thing that names what went wrong, and a caller cannot be expected to
	// drain a stream to find out why its request failed.
	Stream bool

	// Replayable marks a non-idempotent request safe to retry, which is true
	// when the caller holds an idempotency key. Without it, a POST is not
	// replayed after an upstream error, because the server may have processed
	// it before failing.
	Replayable bool
}

// Response is a completed exchange with the body already read, unless the
// request asked for a stream.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// Stream is the unread response body, set only when Request.Stream was
	// asked for and the status was a success. The caller owns it and must
	// Close it; Response.Close does that safely whether it is set or not.
	//
	// Body is empty when Stream is set. The two are never both populated,
	// because a body cannot be both read and unread and pretending otherwise
	// is how a caller ends up copying an empty file.
	Stream io.ReadCloser
	// Method and URL identify what was asked for. They are here so an error
	// can say which endpoint failed: a 404 that does not name the path it
	// tried is nearly useless, and "which URL" is the only question worth
	// asking about one. URL is already redacted.
	Method    string
	URL       string
	RequestID string
	// Attempts is how many HTTP calls this response cost, including retries.
	Attempts int
}

// Close releases a streamed body, and does nothing for a buffered one.
//
// It is safe on a nil Response and safe to call twice, so a caller can
// `defer resp.Close()` immediately after Do without first working out whether
// this particular request streamed.
func (r *Response) Close() {
	if r == nil || r.Stream == nil {
		return
	}
	_ = r.Stream.Close()
	r.Stream = nil
}

// Endpoint renders the request this response answered, for an error message.
func (r *Response) Endpoint() string {
	if r == nil || r.Method == "" {
		return ""
	}
	return r.Method + " " + r.URL
}

// New builds a Client.
func New(opt Options) (*Client, error) {
	if strings.TrimSpace(opt.BaseURL) == "" {
		return nil, errs.Usage("NO_SITE", "no Jira site configured").
			WithRemedy("run `jr auth login --site <host>`, or pass --site")
	}
	base, err := url.Parse(strings.TrimRight(opt.BaseURL, "/"))
	if err != nil || base.Host == "" {
		return nil, errs.Usage("INVALID_SITE", "%q is not a valid site URL", opt.BaseURL).
			WithRemedy("use a full URL, e.g. https://your-site.atlassian.net")
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, errs.Usage("INVALID_SITE",
			"site URL scheme %q is not http or https", base.Scheme)
	}

	c := &Client{
		base:      base,
		http:      opt.HTTPClient,
		auth:      opt.Auth,
		retries:   opt.Retries,
		timeout:   opt.Timeout,
		userAgent: opt.UserAgent,
		tracer:    opt.Tracer,
		clock:     opt.Clock,
		jitter:    opt.Jitter,
		budget:    newBudget(opt.MaxRequests),
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.retries == 0 {
		c.retries = DefaultRetries
	}
	if c.timeout == 0 {
		c.timeout = DefaultTimeout
	}
	if opt.HTTPClient == nil && opt.RoundTripper != nil {
		c.http = &http.Client{Transport: opt.RoundTripper}
	}
	if c.tracer == nil {
		c.tracer = discardTracer{}
	}
	if c.clock == nil {
		c.clock = realClock{}
	}
	if c.jitter == nil {
		c.jitter = defaultJitter
	}
	return c, nil
}

// Requests returns how many HTTP calls this client has made, including retries.
func (c *Client) Requests() int { return c.budget.spent() }

// Remaining returns how many calls the budget still allows, or -1 when
// unlimited.
func (c *Client) Remaining() int { return c.budget.remaining() }

// Do performs a request, retrying according to the policy in retry.go, and
// returns the response for any status. A non-2xx status is not an error here:
// use DoJSON, or map it with Err, so a caller that wants to inspect a 404
// itself can.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	target, err := c.resolve(r)
	if err != nil {
		return nil, err
	}
	requestID := newRequestID()

	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := c.budget.consume(); err != nil {
			if attempt > 1 && lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}

		resp, err := c.attempt(ctx, r, target, requestID, attempt)
		if err == nil {
			retry, reason := shouldRetry(r.Method, resp.Status, r.Replayable)
			if !retry || attempt > c.retries {
				resp.Attempts = attempt
				if retry && reason != "" {
					// The budget ran out while the status was still
					// retryable. Report the status, not a retry error.
					c.tracer.Trace(Event{
						Kind: EventFailure, Method: r.Method, URL: RedactURL(target),
						Status: resp.Status, Attempt: attempt, RequestID: requestID,
						Reason: "retry budget exhausted",
					})
				}
				return resp, nil
			}
			// Only a success streams, and a success is never retried — but the
			// two facts live in different functions, and a leaked connection
			// is not the way to find out they diverged.
			resp.Close()
			wait := backoff(attempt, resp.Header, c.clock.Now(), c.jitter())
			c.tracer.Trace(Event{
				Kind: EventRetry, Method: r.Method, URL: RedactURL(target),
				Status: resp.Status, Attempt: attempt, Wait: wait,
				RequestID: requestID, Reason: reason,
			})
			if err := c.clock.Sleep(ctx, wait); err != nil {
				return nil, canceled(err)
			}
			continue
		}

		lastErr = err
		if !shouldRetryNetworkError(ctx, r.Method, r.Replayable, err) || attempt > c.retries {
			return nil, err
		}
		wait := backoff(attempt, nil, c.clock.Now(), c.jitter())
		c.tracer.Trace(Event{
			Kind: EventRetry, Method: r.Method, URL: RedactURL(target),
			Attempt: attempt, Wait: wait, RequestID: requestID,
			Reason: "network error",
		})
		if err := c.clock.Sleep(ctx, wait); err != nil {
			return nil, canceled(err)
		}
	}
}

// attempt performs exactly one HTTP call.
func (c *Client) attempt(ctx context.Context, r Request, target *url.URL,
	requestID string, attempt int,
) (*Response, error) {
	// The per-attempt timeout bounds getting a response, not reading one.
	//
	// For a buffered request those are the same thing, and a deadline over the
	// whole exchange is right. For a streamed one they are not: the caller
	// reads the body after this function returns, and a 30-second deadline has
	// no idea how large the file is or how fast the link is. So a stream is
	// bounded to first byte and then handed over — the caller's own context
	// still governs, which is what makes Ctrl-C work on a long download.
	var (
		cancel   context.CancelFunc
		deadline *time.Timer
		expired  atomic.Bool
	)
	if r.Stream {
		ctx, cancel = context.WithCancel(ctx)
		deadline = time.AfterFunc(c.timeout, func() {
			// Recorded as well as acted on, because a cancelled context cannot
			// say afterwards whether it ran out of time or was interrupted,
			// and those need different messages.
			expired.Store(true)
			cancel()
		})
	} else {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	// handedOver is set when the response body becomes the caller's, at which
	// point cancelling here would kill the transfer mid-copy.
	handedOver := false
	defer func() {
		if deadline != nil {
			deadline.Stop()
		}
		if !handedOver {
			cancel()
		}
	}()

	var body io.Reader
	switch {
	case r.BodySource != nil:
		// Re-opened on every attempt, so a retry sends the same content rather
		// than the remains of the last one. A source that cannot do that says
		// so here, and the request fails instead of going out short.
		source, err := r.BodySource()
		if err != nil {
			return nil, errs.Runtime("BODY_NOT_REPLAYABLE",
				"the request body cannot be sent again for attempt %d", attempt).
				WithDetail("%s", err.Error()).
				WithRemedy("a body read from a pipe cannot be retried; write it " +
					"to a file first, or retry the command yourself").
				Wrap(err)
		}
		defer func() { _ = source.Close() }()
		body = source
	case len(r.Body) > 0:
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
	if err != nil {
		return nil, errs.Runtime("BAD_REQUEST", "cannot build the request").Wrap(err)
	}

	for name, values := range r.Header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	if req.Header.Get("Accept") == "" {
		// A download is not asking for JSON. Sending Accept: application/json
		// for an attachment invites the server to answer with a JSON error
		// instead of the file, which is a 406 that reads like a missing
		// attachment.
		if r.Stream {
			req.Header.Set("Accept", "*/*")
		} else {
			req.Header.Set("Accept", "application/json")
		}
	}
	// Only for a byte body: a BodySource carries content of some other type,
	// and the caller is the one that knows which.
	if len(r.Body) > 0 && r.BodySource == nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set(HeaderUserAgent, c.userAgent)
	}
	req.Header.Set(HeaderRequestID, requestID)

	// Credentials are applied last, so nothing above can overwrite them, and
	// nothing below reads them: the trace event is built from a redacted copy.
	if c.auth != nil {
		creds, err := c.auth.Authorize(ctx, RequestInfo{
			Method: r.Method,
			URL:    target.String(),
		})
		if err != nil {
			return nil, err
		}
		for name, value := range creds {
			req.Header.Set(name, value)
		}
	}

	c.tracer.Trace(requestEvent(req, attempt, requestID))

	start := c.clock.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.tracer.Trace(Event{
			Kind: EventFailure, Method: r.Method, URL: RedactURL(target),
			Attempt: attempt, RequestID: requestID, Reason: redactErrorText(err),
		})
		if expired.Load() {
			// The streamed path cancels rather than deadlines, so the context
			// alone would report this as an interruption.
			return nil, timedOut(err)
		}
		return nil, networkError(ctx, err)
	}
	// A streamed success hands the body to the caller unread, which is the
	// whole point: a 50MB attachment is copied to a file rather than held in
	// memory. Anything else is buffered, including a streamed request that
	// failed — the error body is small and is the only thing that says why.
	streaming := r.Stream && resp.StatusCode >= 200 && resp.StatusCode < 300

	var payload []byte
	if streaming {
		// Not deferred: ownership passes to the caller with the Response.
		payload = nil
	} else {
		defer func() { _ = resp.Body.Close() }()
		payload, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			return nil, errs.Remote("TRUNCATED_RESPONSE", "the response body could not be read").
				WithRequestID(requestID).Wrap(err)
		}
	}
	elapsed := c.clock.Now().Sub(start)

	// Prefer the server's correlation id when it echoed one back, since that is
	// what Atlassian support can look up.
	id := requestID
	if echoed := resp.Header.Get(HeaderRequestID); echoed != "" {
		id = echoed
	}

	// A streamed body has not been read, so its size is whatever the server
	// declared or unknown. Reporting len(payload) would trace every download
	// as zero bytes.
	size := len(payload)
	if streaming {
		size = int(resp.ContentLength)
	}
	c.tracer.Trace(responseEvent(target, r.Method, resp.StatusCode, resp.Header,
		attempt, size, elapsed, id))

	out := &Response{
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      payload,
		Method:    r.Method,
		URL:       RedactURL(target),
		RequestID: id,
		Attempts:  attempt,
	}
	if streaming {
		// The connection outlives this function, so its cancellation does too.
		// Closing the stream is what releases it, which is why Response.Close
		// exists and why a caller that forgets it leaks a connection rather
		// than merely a file handle.
		deadline.Stop()
		handedOver = true
		out.Stream = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	}
	return out, nil
}

// cancelOnClose ties a request's cancellation to the lifetime of its body, so
// the transfer is not killed when the function that started it returns.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(c.cancel)
	return err
}

// resolve builds the absolute URL for a request.
func (c *Client) resolve(r Request) (*url.URL, error) {
	if r.Method == "" {
		return nil, errs.Runtime("BAD_REQUEST", "request has no method")
	}
	ref, err := url.Parse(r.Path)
	if err != nil {
		return nil, errs.Runtime("BAD_REQUEST", "%q is not a valid path", r.Path).Wrap(err)
	}
	// IsAbs is Scheme != "", so it alone would let `//host/path` through: that
	// names a host and carries no scheme. JoinPath would then drop the host and
	// send the path to the configured site, which is safe and is not what the
	// value said. A path that names anywhere is not relative.
	if ref.IsAbs() || ref.Host != "" {
		// A caller passing an absolute URL would silently escape the
		// configured site, which is how a credential ends up sent to the
		// wrong host.
		return nil, errs.Runtime("BAD_REQUEST",
			"request path must be relative to the site, got %q", r.Path)
	}

	target := c.base.JoinPath(ref.Path)
	if len(r.Query) > 0 {
		target.RawQuery = r.Query.Encode()
	}
	return target, nil
}

// Err maps a non-2xx response to a structured error, or returns nil for a
// success. A caller that wants to handle a 404 itself checks Status instead.
func Err(resp *Response) error {
	if resp == nil {
		return errs.Remote("NO_RESPONSE", "no response from Jira")
	}
	if resp.Status >= 200 && resp.Status < 300 {
		return nil
	}
	return statusError(resp)
}

// networkError classifies a transport-level failure. The message never carries
// the URL verbatim, because a URL can hold credentials.
func networkError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return canceled(ctx.Err())
	}
	return errs.Remote("NETWORK", "cannot reach Jira").
		WithDetail("%s", redactErrorText(err)).
		WithRemedy("check connectivity, the site URL, and any proxy configuration").
		Wrap(err)
}

func canceled(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return timedOut(err)
	}
	return errs.Runtime("CANCELED", "canceled").Wrap(err)
}

// timedOut is the same error a deadline produces, for the streamed path, which
// cancels on a timer rather than carrying a deadline. Without it, running out
// of time on a download would report itself as an interruption — and the two
// call for different things: one is a timeout to raise, the other is a Ctrl-C
// nobody needs to be told about.
func timedOut(err error) error {
	return errs.Remote("TIMEOUT", "the request timed out").
		WithRemedy("raise the timeout, or narrow the request").Wrap(err)
}

// defaultJitter returns a value in [0,1) for backoff spreading. It does not
// need to be cryptographically random: it exists to break up a herd, not to
// resist prediction.
func defaultJitter() float64 { return mrand.Float64() }

// redactErrorText strips credentials out of an error string. A *url.Error
// stringifies to include the URL, so the raw text of a transport failure is not
// safe to print.
func redactErrorText(err error) string {
	if err == nil {
		return ""
	}
	urlErr, ok := errors.AsType[*url.Error](err)
	if !ok {
		return err.Error()
	}
	safe := "?"
	if u, parseErr := url.Parse(urlErr.URL); parseErr == nil {
		safe = RedactURL(u)
	}
	inner := "unknown error"
	if urlErr.Err != nil {
		inner = urlErr.Err.Error()
	}
	return urlErr.Op + " " + safe + ": " + inner
}

// newRequestID returns a random correlation id. It is not a UUID: nothing
// parses it, and a hex string is one dependency fewer.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; a fixed marker is better
		// than a panic in a CLI, and correlation is best-effort anyway.
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// Relative converts a URL the server supplied into a path this client will
// send, and refuses one pointing anywhere else.
//
// Data Center reports an attachment's content as an absolute URL. Following it
// as given would hand the credential to whatever host it names, which is the
// exact failure "a request path is relative, never absolute" exists to prevent
// — and the value comes from the server, so it is not a caller's mistake to
// make. Scheme, host, and port must all match the configured site.
//
// One rule decides what a value means, and it is the one every HTTP client
// uses: the reference is resolved against the site's origin. So a value naming
// a host is checked against the site whether or not it carries a scheme, and a
// path beginning with "/" is a complete server path — it already carries the
// context path if it is inside it, and gets the same trim an absolute URL does.
// Reading such a path as relative to the base instead is what made one URL
// spelled two ways produce two different requests, one of them a 404 with the
// context path in it twice.
//
// A reference naming neither a host nor an absolute path is relative to the
// base, which is what resolve joins it onto, and is returned as given.
func (c *Client) Relative(raw string) (string, error) {
	out, err := c.relative(raw)
	if err != nil {
		return "", err
	}

	// A postcondition, because the returned string is the whole contract:
	// resolve parses it again and refuses anything naming a scheme or a host.
	// A reference whose first segment holds a colon re-parses as a scheme, so
	// `"%3A` came back as `%22:` — accepted here and refused one layer down,
	// which is this function failing at the one thing it exists for. Refusing
	// rather than prefixing "./" keeps it from repairing a server's value.
	ref, err := url.Parse(out)
	if err != nil || ref.Scheme != "" || ref.Host != "" {
		return "", errs.Remote("MALFORMED_URL",
			"the server supplied a URL this tool cannot turn into a request path")
	}
	return out, nil
}

// relative is Relative without the postcondition, so the checks read in the
// order they apply rather than around a deferred one.
func (c *Client) relative(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errs.Runtime("NO_URL", "the server supplied no URL to follow")
	}

	ref, err := url.Parse(trimmed)
	if err != nil {
		// The raw value is not echoed: a URL is not obviously a secret, which
		// is exactly why one carrying userinfo or a signed query parameter
		// gets printed by accident.
		return "", errs.Remote("MALFORMED_URL",
			"the server supplied a URL this tool cannot parse").Wrap(err)
	}

	// A protocol-relative reference inherits the base's scheme — that is what
	// makes it a reference to a host rather than a path. Filling it in before
	// the checks also keeps sameHost's port defaulting honest: with no scheme
	// it cannot tell 443 from nothing, and the same host would compare unequal.
	if ref.Scheme == "" && ref.Host != "" {
		ref.Scheme = c.base.Scheme
	}

	// Scheme and host are checked before anything decides the value is
	// relative. url.URL.IsAbs is Scheme != "", so `//evil.invalid/steal` is not
	// absolute by that test while still naming another host — and taking the
	// relative path out of it rewrites a pointer at somebody else's server into
	// a path on our own. No credential left the site either way, because
	// RequestURI drops the host; two spellings of "go elsewhere" getting
	// opposite treatments is the defect.
	if ref.Scheme != "" && !strings.EqualFold(ref.Scheme, c.base.Scheme) {
		return "", offSite("the server pointed at another scheme, and this "+
			"tool will not follow it",
			"configured %s, supplied %s", c.base.Scheme, ref.Scheme)
	}
	if ref.Host != "" && !sameHost(ref, c.base) {
		return "", offSite("the server pointed at another host, and this tool "+
			"will not follow it",
			"configured %s, supplied %s", c.base.Host, ref.Host)
	}

	if seg, odd := oddSegment(ref.Path); odd {
		return "", offSite("the server supplied a URL this tool will not "+
			"resolve on its behalf", "the segment %q in %s", seg, ref.Path)
	}

	path := ref.EscapedPath()
	if ref.Host == "" && ref.Scheme == "" && !strings.HasPrefix(path, "/") {
		return ref.RequestURI(), nil
	}

	// The site may be served under a context path — a Data Center instance
	// often is — and the base already carries it. Returning the whole path
	// would repeat it; returning the remainder keeps resolve's JoinPath honest.
	if base := strings.TrimSuffix(c.base.EscapedPath(), "/"); base != "" {
		rest, ok := underPath(path, base)
		if !ok {
			return "", offSite("the server pointed outside the configured "+
				"context path",
				"configured %s, supplied %s", base, path)
		}
		path = rest
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if ref.RawQuery != "" {
		path += "?" + ref.RawQuery
	}
	return path, nil
}

// offSite is the refusal for a server-supplied URL that points away from the
// configured site. The detail names the one part that differs and never the
// whole URL, which can carry userinfo or a signed query parameter.
func offSite(message, detail string, args ...any) error {
	return errs.Remote("OFF_SITE_URL", "%s", message).
		WithDetail(detail, args...).
		WithRemedy("report this: a credential is not sent anywhere but the " +
			"configured site")
}

// oddSegment finds a path segment this tool will not resolve on the server's
// behalf, and reports whether it found one.
//
// "." and ".." are cleaned by resolve's JoinPath — after the containment check
// has already looked at the path — so /jira/../../secure/x passes a check it
// then leaves anyway. An empty interior segment is the same escape spelled
// differently: /jira//x is inside the context path and trims to //x, which
// re-parsed names the host x rather than the path /x. Neither is normalised
// here, because normalising is this tool deciding what the server meant.
//
// A leading empty segment is every absolute path, and a trailing one is a
// trailing slash. Only an interior one is odd.
//
// The argument is the decoded path, so ..%2f and %2e%2e arrive as segments
// rather than as a way past this.
func oddSegment(p string) (string, bool) {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		switch {
		case seg == "." || seg == "..":
			return seg, true
		case seg == "" && i > 0 && i < len(segs)-1:
			return seg, true
		}
	}
	return "", false
}

// underPath reports whether path is inside base, and returns the remainder.
//
// The comparison is by segment. A plain prefix match would read /jiraxyz/a as
// inside /jira and hand back a path the server never named.
func underPath(path, base string) (string, bool) {
	if path == base {
		return "/", true
	}
	if rest, ok := strings.CutPrefix(path, base+"/"); ok {
		return "/" + rest, true
	}
	return "", false
}

// sameHost compares two URLs by host and port, defaulting the port from the
// scheme so https://x and https://x:443 are one host rather than two.
func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(withPort(a), withPort(b))
}

func withPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Hostname() + ":" + u.Port()
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return u.Hostname() + ":443"
	case "http":
		return u.Hostname() + ":80"
	}
	return u.Hostname()
}
