// Package exitcode defines the process exit codes that form part of jr's
// public output contract.
//
// Codes are stable forever. New conditions get new codes; an existing code
// never changes meaning. Changing one is a major version bump. See
// docs/output-contract.md.
package exitcode

// Code is a process exit status.
type Code int

// The complete set of exit codes. Do not renumber.
const (
	// OK means the command succeeded and the result set is complete.
	OK Code = 0
	// Error means a generic runtime failure.
	Error Code = 1
	// Usage means bad flags, missing required input, or an unsupported
	// combination of options.
	Usage Code = 2
	// Partial means the command succeeded but the result set was truncated.
	Partial Code = 3
	// Auth means credentials are missing, invalid, or expired.
	Auth Code = 4
	// NotFound means a referenced issue, project, or board does not exist.
	NotFound Code = 5
	// Permission means the caller authenticated but is not authorized.
	Permission Code = 6
	// Conflict means a precondition failed: an invalid transition, a stale
	// write, or a version mismatch.
	Conflict Code = 7
	// RateLimit means the caller was throttled after the retry budget was
	// exhausted.
	RateLimit Code = 8
	// Remote means Jira returned a 5xx or malformed data.
	Remote Code = 9
	// Blocked means local policy refused the request: read-only mode, or a
	// destructive command without --yes.
	Blocked Code = 10
)

var names = map[Code]string{
	OK:         "OK",
	Error:      "ERROR",
	Usage:      "USAGE",
	Partial:    "PARTIAL",
	Auth:       "AUTH",
	NotFound:   "NOT_FOUND",
	Permission: "PERMISSION",
	Conflict:   "CONFLICT",
	RateLimit:  "RATE_LIMIT",
	Remote:     "REMOTE",
	Blocked:    "BLOCKED",
}

var descriptions = map[Code]string{
	OK:         "Success, result complete",
	Error:      "Generic runtime failure",
	Usage:      "Bad flags, missing required input, unsupported combination",
	Partial:    "Succeeded but result set was truncated",
	Auth:       "Missing, invalid, or expired credentials",
	NotFound:   "Referenced issue/project/board does not exist",
	Permission: "Authenticated but not authorized",
	Conflict:   "Precondition failed, transition invalid, version mismatch",
	RateLimit:  "Throttled after retry budget exhausted",
	Remote:     "Jira returned 5xx or malformed data",
	Blocked:    "Refused by local policy (read-only mode, missing --yes)",
}

// ordered lists every code in numeric order. All() returns a copy of it, and
// the package test asserts it stays in sync with names and descriptions.
var ordered = []Code{
	OK, Error, Usage, Partial, Auth,
	NotFound, Permission, Conflict, RateLimit, Remote, Blocked,
}

// Name returns the stable machine-readable name of the code, or "UNKNOWN" for
// a code this build does not define.
func (c Code) Name() string {
	if n, ok := names[c]; ok {
		return n
	}
	return "UNKNOWN"
}

// Description returns a one-line human explanation of the code.
func (c Code) Description() string {
	return descriptions[c]
}

// Int returns the code as an int, for os.Exit.
func (c Code) Int() int { return int(c) }

// String implements fmt.Stringer.
func (c Code) String() string { return c.Name() }

// All returns every defined code in numeric order.
func All() []Code {
	out := make([]Code, len(ordered))
	copy(out, ordered)
	return out
}
