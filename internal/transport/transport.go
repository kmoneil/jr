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

	// Replayable marks a non-idempotent request safe to retry, which is true
	// when the caller holds an idempotency key. Without it, a POST is not
	// replayed after an upstream error, because the server may have processed
	// it before failing.
	Replayable bool
}

// Response is a completed exchange with the body already read.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
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
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var body io.Reader
	if len(r.Body) > 0 {
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
		req.Header.Set("Accept", "application/json")
	}
	if len(r.Body) > 0 && req.Header.Get("Content-Type") == "" {
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
		return nil, networkError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, errs.Remote("TRUNCATED_RESPONSE", "the response body could not be read").
			WithRequestID(requestID).Wrap(err)
	}
	elapsed := c.clock.Now().Sub(start)

	// Prefer the server's correlation id when it echoed one back, since that is
	// what Atlassian support can look up.
	id := requestID
	if echoed := resp.Header.Get(HeaderRequestID); echoed != "" {
		id = echoed
	}

	c.tracer.Trace(responseEvent(target, r.Method, resp.StatusCode, resp.Header,
		attempt, len(payload), elapsed, id))

	return &Response{
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      payload,
		Method:    r.Method,
		URL:       RedactURL(target),
		RequestID: id,
		Attempts:  attempt,
	}, nil
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
	if ref.IsAbs() {
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
		return errs.Remote("TIMEOUT", "the request timed out").
			WithRemedy("raise the timeout, or narrow the request").Wrap(err)
	}
	return errs.Runtime("CANCELED", "canceled").Wrap(err)
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
