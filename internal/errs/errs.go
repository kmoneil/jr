// Package errs defines the structured error that every jr failure carries.
//
// An error is part of the output contract: it has a machine-stable Code, a
// retryable flag so an agent does not burn its budget on a syntax error, and
// an exit status. See docs/output-contract.md.
package errs

import (
	"errors"
	"fmt"

	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// Error is a structured, renderable failure.
//
// Code is machine-stable and never changes meaning once shipped. Message is a
// single line addressed to a human. Detail carries the offending input.
// Remedy, when set, states what to do instead.
type Error struct {
	Exit      exitcode.Code
	Code      string
	Message   string
	Detail    string
	Remedy    string
	Retryable bool
	RequestID string

	wrapped error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Detail)
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.wrapped }

// WithDetail attaches the offending input and returns the receiver.
func (e *Error) WithDetail(format string, args ...any) *Error {
	e.Detail = fmt.Sprintf(format, args...)
	return e
}

// WithRemedy attaches a suggested fix and returns the receiver.
func (e *Error) WithRemedy(format string, args ...any) *Error {
	e.Remedy = fmt.Sprintf(format, args...)
	return e
}

// WithRequestID attaches the X-Request-Id of the failing call and returns the
// receiver.
func (e *Error) WithRequestID(id string) *Error {
	e.RequestID = id
	return e
}

// Wrap attaches an underlying cause and returns the receiver. The cause is
// reachable via errors.Unwrap but is never rendered, so it cannot leak
// transport details into the output contract.
func (e *Error) Wrap(err error) *Error {
	e.wrapped = err
	return e
}

// New builds an error with an explicit exit status.
func New(exit exitcode.Code, code, format string, args ...any) *Error {
	return &Error{
		Exit:    exit,
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		// Only the transport marks an error retryable, and only for the
		// classes where retrying can actually succeed.
		Retryable: exit == exitcode.RateLimit || exit == exitcode.Remote,
	}
}

// Usage reports bad flags, missing required input, or an unsupported
// combination. Exit 2.
func Usage(code, format string, args ...any) *Error {
	return New(exitcode.Usage, code, format, args...)
}

// Runtime reports a generic failure with no better classification. Exit 1.
func Runtime(code, format string, args ...any) *Error {
	return New(exitcode.Error, code, format, args...)
}

// Auth reports missing, invalid, or expired credentials. Exit 4.
func Auth(code, format string, args ...any) *Error {
	return New(exitcode.Auth, code, format, args...)
}

// NotFound reports a referenced resource that does not exist. Exit 5.
func NotFound(code, format string, args ...any) *Error {
	return New(exitcode.NotFound, code, format, args...)
}

// Permission reports an authenticated but unauthorized caller. Exit 6.
func Permission(code, format string, args ...any) *Error {
	return New(exitcode.Permission, code, format, args...)
}

// Conflict reports a failed precondition or a stale write. Exit 7.
func Conflict(code, format string, args ...any) *Error {
	return New(exitcode.Conflict, code, format, args...)
}

// RateLimit reports throttling after the retry budget was exhausted. Exit 8.
func RateLimit(code, format string, args ...any) *Error {
	return New(exitcode.RateLimit, code, format, args...)
}

// Remote reports a 5xx or malformed data from Jira. Exit 9.
func Remote(code, format string, args ...any) *Error {
	return New(exitcode.Remote, code, format, args...)
}

// Blocked reports a refusal by local policy: read-only mode, or a destructive
// command invoked without --yes. Exit 10.
func Blocked(code, format string, args ...any) *Error {
	return New(exitcode.Blocked, code, format, args...)
}

// ExitOf returns the exit status carried by err, walking the wrap chain. A nil
// error is exitcode.OK; an error with no structured status is exitcode.Error,
// never a success.
func ExitOf(err error) exitcode.Code {
	if err == nil {
		return exitcode.OK
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Exit
	}
	return exitcode.Error
}

// Coerce returns err as an *Error, wrapping an unstructured error as a generic
// runtime failure so every failure path reaches the renderer with a code.
func Coerce(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	return Runtime("INTERNAL", "%s", err.Error()).Wrap(err)
}
