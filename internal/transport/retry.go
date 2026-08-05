package transport

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Retry defaults. The budget is a flag (--retries); these are what it starts at.
const (
	DefaultRetries = 3
	// baseDelay is the first backoff interval. Each attempt doubles it.
	baseDelay = 500 * time.Millisecond
	// maxDelay caps a single wait, so a Retry-After of an hour does not become
	// an hour-long hang inside one command.
	maxDelay = 30 * time.Second
)

// Clock is the time source. It is an interface so a test can assert on the
// backoff schedule without spending the wall-clock time it describes.
type Clock interface {
	Now() time.Time
	// Sleep waits for d, or returns early if ctx is cancelled.
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// idempotentMethods are safe to replay by HTTP semantics: sending one twice has
// the same effect as sending it once.
var idempotentMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
	http.MethodPut:     true,
	http.MethodDelete:  true,
	http.MethodTrace:   true,
}

// shouldRetry decides whether a response is worth another attempt.
//
// The subtle case is a non-idempotent method. A POST that failed with a 503 may
// well have been processed before the error — retrying it is how one `issue
// create` becomes two issues. So a POST is replayed only when the failure is
// proof the request was not acted on (a 429 is refused before processing), or
// when the caller has said it is safe because it holds an idempotency key.
func shouldRetry(method string, status int, replayable bool) (bool, string) {
	switch {
	case status == http.StatusTooManyRequests:
		// Refused before processing, whatever the method.
		return true, "rate limited"
	case status < 500:
		return false, ""
	case idempotentMethods[method] || replayable:
		return true, "upstream error"
	default:
		return false, "non-idempotent request not replayed after an upstream error"
	}
}

// shouldRetryNetworkError decides whether a transport-level failure is worth
// another attempt. A cancelled context never is.
func shouldRetryNetworkError(ctx context.Context, method string, replayable bool, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// A connection-level failure may have happened after the server accepted
	// the request, so the same idempotency reasoning applies.
	return idempotentMethods[method] || replayable
}

// backoff returns how long to wait before attempt n (1-based), honoring
// Retry-After when the server sent one.
//
// Jitter matters more than it looks: without it, every client throttled at the
// same moment retries at the same moment, and the thundering herd re-creates
// the overload that caused the 429.
func backoff(attempt int, header http.Header, now time.Time, jitter float64) time.Duration {
	if d, ok := retryAfter(header, now); ok {
		return min(d, maxDelay)
	}

	exp := float64(baseDelay) * math.Pow(2, float64(attempt-1))
	// Full jitter: uniform over [0, exp]. It spreads a herd better than
	// adding a small fraction to a fixed delay.
	d := time.Duration(exp * jitter)
	return min(max(d, time.Millisecond), maxDelay)
}

// retryAfter parses the Retry-After header in either of its two forms: a count
// of seconds, or an HTTP date.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	v := header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(v); err == nil {
		d := when.Sub(now)
		if d < 0 {
			// A date already in the past means "retry now", not "wait
			// backwards".
			return 0, true
		}
		return d, true
	}
	return 0, false
}
