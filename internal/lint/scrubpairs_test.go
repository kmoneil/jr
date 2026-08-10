package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// scrubPairPatterns are the shapes a JIRA_RECORD_SCRUB mapping is written in.
//
// A scrub target is a real identifier and its replacement is the fiction that
// stands for it in the committed fixtures. Either half alone is worth little:
// the real key names a project on somebody's instance and nothing here says
// which, and the fictional key is fictional. Joined by `=` they are the
// mapping, which turns every scrubbed fixture in this repository back into a
// recording of a named instance — so the pair is worth more than both halves
// and is the thing to refuse.
//
// Matched by shape and never by value. That is the decision this guard
// implements, not an approximation of a better one; see
// TestNoScrubPairIsCommitted.
var scrubPairPatterns = []*regexp.Regexp{
	// Two project keys, which is the form that leaked. Ten characters is
	// Atlassian's own limit on a key.
	regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}=[A-Z][A-Z0-9]{1,9}\b`),
	// Two display names. JIRA_RECORD_SCRUB takes a name as readily as a key,
	// and a name is the identifier identifiers_test.go says still rests on
	// review — so the one guard that can read prose covers the more sensitive
	// class as well, for one more pattern.
	regexp.MustCompile(`\b[A-Z][a-z]+ [A-Z][a-z]+=[A-Z][a-z]+`),
}

// scrubPairsSelfFile is this file, whose matches are the evidence that the
// patterns work rather than uses of them.
const scrubPairsSelfFile = "internal/lint/scrubpairs_test.go"

// examinedFloor is how many text files the walk must reach before a clean
// result is worth anything.
//
// The tree holds upwards of five hundred. A walk that reaches none reports
// exactly what a walk that reaches everything and finds nothing reports, and
// this project has twice shipped a check that passed over code it could not
// see. The floor is well under the real count so ordinary deletion does not
// trip it; it exists to catch a walk that has stopped walking.
const examinedFloor = 200

// TestScrubPairsAreFlagged pins what the guard is for, using the string that
// actually leaked.
func TestScrubPairsAreFlagged(t *testing.T) {
	for _, bad := range []string{
		"a short key cannot be listed bare — `ET=AGL` would rewrite `GET`",
		"JIRA_RECORD_SCRUB=ET=AGL",
		"// the sandbox is ORG=ENG in every fixture below",
		`JIRA_RECORD_SCRUB="Ada Lovelace=Test User"`,
		"recorded with Ada Lovelace=Sam Example",
	} {
		if _, found := scrubPair(bad); !found {
			t.Errorf("scrubPair(%q) found no mapping", bad)
		}
	}
}

// TestNonPairsAreNotFlagged is the other half, and matters more.
//
// A guard that cries wolf gets deleted, and the tree is full of `=`: shell
// assignments, make variables, query parameters, and the metavariable spelling
// of the scrub variable's own documentation. None of them name two identifiers,
// and every one of them has to keep passing.
func TestNonPairsAreNotFlagged(t *testing.T) {
	for _, safe := range []string{
		`JIRA_RECORD_SCRUB="from=to,..."`,
		"JIRA_READONLY=1",
		"GODEBUG=inittrace=1",
		"CGO_ENABLED=0 GOOS=linux go build",
		"FUZZTIME=30s make fuzz",
		"READER_MAX_BYTES=8388608",
		`project = "ENG" AND status = Open`,
		"?maxResults=50&startAt=0&validateQuery=true",
		"if ENG==SEC {",
		"exit 3, not exit 0",
		"a two-letter key inside `GET` and `Set-Cookie`",
	} {
		if got, found := scrubPair(safe); found {
			t.Errorf("scrubPair(%q) flagged %q, which names no mapping", safe, got)
		}
	}
}

// TestNoScrubPairIsCommitted refuses a mapping from a real identifier to the
// fiction that replaces it, anywhere in the tracked tree.
//
// The incident. `2d60a4f` took the sandbox's project key and its scrubbed form
// out of a fixture's provenance note. One commit later `cf17711` put the same
// mapping back — in prose, in internal/transport/scrub.go, in record.go, and in
// docs/architecture.md, as explanatory comments on the code that exists to
// prevent exactly that. `8b236ba` replaced the key with a fiction. Every guard
// in this tree was green throughout, because every one of them reads data:
// StemResidue and the residue patterns read recorded interactions and a comment
// is not an interaction, identifiers_test.go reads account ids and a project key
// is not one, hosts_test.go reads hostnames. Nothing read prose.
//
// Why shape and not value. The obvious guard greps for the real identifiers,
// which means writing them down in the repository whose entire scrubbing
// apparatus exists to keep them out — and if the list is untracked to avoid
// that, a fresh clone has no list, the check skips, and a skip is an assertion
// that ran nothing. A shape rule needs to know neither half. It also catches
// the mapping whether or not the values are real, which is the property that
// makes it enforceable: nobody has to adjudicate whether a particular key was
// invented, and there is no declared-fiction allowlist for a real pair to
// arrive through.
//
// What it cannot see, recorded rather than papered over. A single real key in
// prose, which is a smaller leak than the mapping and the price of needing no
// list. A mapping written in words — "ET scrubs to AGL" — or with an arrow
// between the halves; `=` is refused because it is the scrub variable's own
// notation, and an arrow between two uppercase words is ordinary English that
// this guard would refuse wrongly more often than rightly. Those still rest on
// review, the same footing identifiers_test.go leaves a display name on. The
// third option on the card was to leave all of it there; it was what was in
// force the morning of the incident and it did not hold for the length of one
// commit.
func TestNoScrubPairIsCommitted(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	examined := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		body, err := os.ReadFile(path) //nolint:gosec // walking this module's own tree.
		if err != nil {
			return err
		}
		// A binary matching by chance would be noise, and a file nobody can
		// read cannot carry an explanation to a reader either.
		if !utf8.Valid(body) {
			return nil
		}
		examined++

		rel, _ := filepath.Rel(root, path)
		if filepath.ToSlash(rel) == scrubPairsSelfFile {
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if got, found := scrubPair(line); found {
				t.Errorf("%s:%d holds the scrub mapping %q.\n"+
					"A real identifier joined to its replacement is what turns "+
					"the committed fixtures back into a recording of a named "+
					"instance, and git history keeps it. Name one side or the "+
					"other, or write the metavariable form `from=to` — the "+
					"spelling docs/architecture.md already uses.",
					rel, i+1, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if examined < examinedFloor {
		t.Errorf("read %d text files, below the floor of %d.\n"+
			"A walk that reaches nothing reports what a clean tree reports. "+
			"Either the tree moved under this test or skipDirs now excludes "+
			"most of it; fix the walk rather than the floor.",
			examined, examinedFloor)
	}
}

// scrubPair reports the first mapping in s, if there is one.
func scrubPair(s string) (string, bool) {
	for _, re := range scrubPairPatterns {
		if got := re.FindString(s); got != "" {
			return got, true
		}
	}
	return "", false
}
