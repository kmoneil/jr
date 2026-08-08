package registry

import (
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Flags holds parsed flag values for one invocation.
type Flags struct {
	strings map[string][]string
	ints    map[string]int
	bools   map[string]bool
	// defaulted names the flags whose value came from the declaration rather
	// than from the caller. See WasSet.
	defaulted map[string]bool
}

// NewFlags returns an empty flag set.
func NewFlags() Flags {
	return Flags{
		strings:   map[string][]string{},
		ints:      map[string]int{},
		bools:     map[string]bool{},
		defaulted: map[string]bool{},
	}
}

// SetString records a string value, appending for a repeatable flag.
func (f Flags) SetString(name, value string) {
	f.strings[name] = append(f.strings[name], value)
}

// SetInt records an int value.
func (f Flags) SetInt(name string, value int) { f.ints[name] = value }

// SetBool records a bool value.
func (f Flags) SetBool(name string, value bool) { f.bools[name] = value }

// MarkDefault records that a flag's value came from its declaration and not
// from the caller.
//
// The setters above are the caller's voice: harvest writes the effective value
// for every declared flag, and then says which of them nobody typed. Doing it
// this way round means a producer that only ever sets what it was given — the
// MCP binder, and every test — gets the true answer without knowing this method
// exists. The alternative, marking on the way in, would need the setters to
// know whether they were carrying an answer or a default, and forgetting to say
// so would silently report a default as the caller's choice.
func (f Flags) MarkDefault(name string) { f.defaulted[name] = true }

// WasSet reports whether the caller actually gave this flag.
//
// It exists because a flag's zero value and its absence are the same value at
// this layer, and for some flags they are different requests. `--page-size 0`
// meant 50: zero is the sentinel for "unset" inside the client, and Int could
// not tell an explicit one from an absent flag, so a value outside the range
// the flag's own help publishes was accepted and quietly replaced. Anything
// whose zero is a legitimate input has to ask this rather than read Int.
func (f Flags) WasSet(name string) bool {
	if f.defaulted[name] {
		return false
	}
	_, isString := f.strings[name]
	_, isInt := f.ints[name]
	_, isBool := f.bools[name]
	return isString || isInt || isBool
}

// String returns the last value of a string flag.
func (f Flags) String(name string) string {
	v := f.strings[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// StringSlice returns every value of a repeatable flag, in the order given.
func (f Flags) StringSlice(name string) []string { return f.strings[name] }

// Int returns an int flag.
func (f Flags) Int(name string) int { return f.ints[name] }

// Bool returns a bool flag.
func (f Flags) Bool(name string) bool { return f.bools[name] }

// Limit is a caller's requested result bound.
//
// It is a user intent, decoupled from the API page size: the client pages
// internally until it is satisfied. There is no offset — the upstream API is
// cursor-based, and exposing an offset that cannot be honored is how the
// incumbent shipped a silent lie.
type Limit struct {
	// N is the requested number of results. Meaningful only when All is false.
	N int
	// All requests the entire result set.
	All bool
}

// DefaultLimit is the bound applied when the caller does not pass --limit.
const DefaultLimit = 50

// ParseLimit resolves a --limit value. It accepts a positive integer or the
// word "all". Zero, a negative number, or anything else is a usage error.
func ParseLimit(s string) (Limit, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "all" {
		return Limit{All: true}, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return Limit{}, errs.Usage("INVALID_LIMIT", "--limit must be a positive integer or \"all\"").
			WithDetail("got %q", s).
			WithRemedy("try --limit 50 or --limit all")
	}
	if n < 1 {
		return Limit{}, errs.Usage("INVALID_LIMIT", "--limit must be at least 1").
			WithDetail("got %d", n).
			WithRemedy("use --limit all to request the entire result set")
	}
	return Limit{N: n}, nil
}

// Satisfied reports whether count results are enough to satisfy the limit.
func (l Limit) Satisfied(count int) bool {
	if l.All {
		return false
	}
	return count >= l.N
}

// Bound cuts a result set the caller already holds in full down to what was
// asked for, and reports whether it survived whole.
//
// It is for the fetch-everything-then-trim shape, and only that shape: the
// caller has paged the endpoint to exhaustion, so len(items) is the true size
// of the result set and comparing it against the limit is a real question.
// `board list`, `epic list`, `sprint list`, `project list` and the rest fetch
// the whole set before trimming because their endpoints document no ordering,
// and this is where they all say so in one voice.
//
// **A result the server bounded must not come through here.** If the limit
// reached the endpoint as maxResults, then len(items) cannot exceed it, the
// comparison below can never fire, and the set comes back marked complete
// however much was left behind. That is not hypothetical: `user list` pushed
// the caller's limit into the request and then ran this comparison against the
// response, so its truncation branch was unreachable and every bounded search
// reported itself exhaustive. Such a command asks the server for one row more
// than it wants and reports whether that row came back — see site.UserPage.
//
// It exists because eleven call sites had written these four lines out
// longhand, and the twelfth wrote them slightly differently and was wrong. The
// boundary is the part worth having in one place: exactly N results is a
// complete answer, not a truncated one, and N+1 is the first that is not.
func Bound[T any](l Limit, items []T) ([]T, bool) {
	if l.All || len(items) <= l.N {
		return items, true
	}
	return items[:l.N], false
}

// String renders the limit as the caller wrote it.
func (l Limit) String() string {
	if l.All {
		return "all"
	}
	return strconv.Itoa(l.N)
}
