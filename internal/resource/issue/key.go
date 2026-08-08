package issue

import (
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/site"
)

// Key is a parsed issue key.
//
// It is parsed rather than compared as a string because issue keys do not sort
// lexically: "ENG-1000" is less than "ENG-999" as text and greater as an issue.
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
// The grammar lives in internal/site because `project get` asks the same
// question about its own first argument, and a resource may not import another
// resource. It used to be written out here and nowhere else, so those four
// commands had no local check at all and `project get ../etc` cost a round trip
// to be told what the string already said.
func validProject(s string) bool { return site.ValidProjectKey(s) }

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
