package issue

import (
	"strconv"
	"strings"
)

// Key is a parsed issue key.
//
// It is parsed rather than compared as a string because issue keys do not sort
// lexically: "IDO-1000" is less than "IDO-999" as text and greater as an issue.
// Anything that ordered keys as strings would page through a project in an
// order that looks right for the first nine issues and is wrong after that.
type Key struct {
	Project string
	Number  int
}

// ParseKey splits PROJ-123. It reports false for anything that is not an issue
// key, rather than guessing at a project or a number.
//
// Both halves are checked against a character set, not merely for being
// non-empty. A key becomes a URL path segment, and this used to accept
// "../../admin-1" as the project "../../ADMIN" — every caller that escaped the
// result was fine, and the one that concatenated it was not. The parser is the
// place to fix that, because it is the only place every caller shares.
func ParseKey(s string) (Key, bool) {
	project, number, found := strings.Cut(strings.TrimSpace(s), "-")
	if !found || !validProject(project) || !digits(number) {
		return Key{}, false
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 0 {
		return Key{}, false
	}
	return Key{Project: strings.ToUpper(project), Number: n}, true
}

// validProject reports whether s can be a Jira project key.
//
// Jira's default pattern is two or more uppercase letters. A site can widen it,
// and what widened patterns actually use is digits and underscores after a
// leading letter. Nothing outside that set is accepted — not because Jira would
// refuse it, but because a project key ends up in a URL path and a request that
// can be steered by its own argument is a different request.
func validProject(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

// digits reports whether s is one or more decimal digits and nothing else.
//
// strconv.Atoi alone is not enough: it accepts a leading sign, so "ENG-+1"
// would parse as ENG-1 — a different issue from the one that was typed.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// String renders the key.
func (k Key) String() string {
	return k.Project + "-" + strconv.Itoa(k.Number)
}

// Compare orders two keys: by project name, then by issue number.
//
// This is the order JQL's issuekey comparison uses, and the order this package
// verifies the server actually returned.
func (k Key) Compare(other Key) int {
	if k.Project != other.Project {
		return strings.Compare(k.Project, other.Project)
	}
	switch {
	case k.Number < other.Number:
		return -1
	case k.Number > other.Number:
		return 1
	default:
		return 0
	}
}

// Before reports whether k sorts before other.
func (k Key) Before(other Key) bool { return k.Compare(other) < 0 }
