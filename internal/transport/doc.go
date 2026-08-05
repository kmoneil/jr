// Package transport is the single HTTP path to Jira.
//
// It owns retry with backoff and jitter, Retry-After, the request budget, and
// header redaction. Redaction lives here rather than in a debug formatter so no
// future code path can bypass it: an Authorization, Cookie, or
// Proxy-Authorization header is unreachable from anything that logs.
//
// Every request carries an X-Request-Id, echoed into error output.
package transport
