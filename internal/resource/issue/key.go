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
func ParseKey(s string) (Key, bool) {
	project, number, found := strings.Cut(strings.TrimSpace(s), "-")
	if !found || project == "" || number == "" {
		return Key{}, false
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 0 {
		return Key{}, false
	}
	return Key{Project: strings.ToUpper(project), Number: n}, true
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
