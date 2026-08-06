package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// accountIDish finds anything shaped like an Atlassian account id.
//
// It is deliberately **looser** than transport.CloudAccountID and does not
// import it. A guard that shares a definition with the thing it guards is blind
// wherever that definition is wrong — which this repository has already learned
// once, when the residue check reused the scrubber's own pattern and reported a
// file clean that still held a real identifier. This accepts any prefix
// separator, any case of hex, and a wider prefix charset, so it flags things the
// scrubber would not match.
var accountIDish = regexp.MustCompile(
	`\b[0-9A-Za-z][0-9A-Za-z._-]{0,15}:` +
		`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`,
)

// fabricated is every account id this repository is allowed to contain, and
// what each one is for.
//
// An account id is a stable global identifier for a person. One that reached a
// committed file stays in git history whatever a later commit does, so the
// check is an allowlist rather than a pattern for "looks real" — there is no
// such pattern, which is exactly why the last one survived review.
var fabricated = map[string]string{
	"000000:00000000-0000-0000-0000-000000000000": "the scrubber's own stand-in",
	"61403:9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2":  "scrub_test's realish payload; the five-digit prefix is the regression",
	"5:9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2":      "scrub_test's one-character prefix case",
	"418902:c7a4e10d-2f63-4b58-9e07-1d84af6b2e93": "scrub_test's ordinary six-digit prefix case",
	"712020:8f3a2b1c-0d4e-4a5f-9b6c-7d8e9f0a1b2c": "the assignee and format-cost fixtures' first user",
	"712020:1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d": "the same fixtures' second user",
}

// selfFile is this file, whose matches are the allowlist rather than uses of it.
const selfFile = "internal/lint/identifiers_test.go"

// skipDirs are not part of the committed tree, or are not text.
var skipDirs = map[string]bool{
	".git": true, "_plans": true, "_reviews": true, "_tmp": true,
	"bin": true, "dist": true, "vendor": true, "node_modules": true,
}

// TestNoRealAccountIDIsCommitted is the guard for a defect that shipped.
//
// internal/transport/scrub_test.go built its payload from a real accountId and
// a real display name, on the reasoning that proving the scrubber removes *that
// exact string* is worth more than proving it removes an invented one. The
// reasoning is sound and the consequence was not: the identifier went into git
// history, where no later commit can remove it.
//
// Nothing checked. The host in the same fixture was `.invalid` and
// hosts_test.go was satisfied, because a hostname guard looks at the domain and
// a person is in the local part and the account id. This closes that gap for
// the identifier that actually persists.
//
// What it cannot check is a name. There is no pattern for "this is a real
// person", so a display name still rests on review — the honest limit, recorded
// rather than papered over.
func TestNoRealAccountIDIsCommitted(t *testing.T) {
	seen := map[string]bool{}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
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
		// A file that is not text cannot be reviewed for this by eye either, and
		// a compiled binary matching by chance would be noise.
		if !utf8.Valid(body) {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(body), "\n") {
			for _, got := range accountIDish.FindAllString(line, -1) {
				if _, ok := fabricated[got]; !ok {
					t.Errorf("%s:%d holds %s, which is not a declared fabrication.\n"+
						"An account id names a person and git history keeps it. If this "+
						"one is invented, add it to fabricated with what it is for; if it "+
						"is real, replace it and do not rely on a later commit to remove it.",
						rel, i+1, got)
					continue
				}
				// Every entry appears in this file as its own map key, so
				// crediting them here would make the stale-entry check below
				// pass for anything ever written down — a guard that reports
				// success having checked nothing, which is the shape this
				// repository keeps finding on the wrong end of. It was written
				// that way first, and the stale check could never fire.
				if filepath.ToSlash(rel) != selfFile {
					seen[got] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// The other direction, so the list cannot outlive what it was written for.
	for id, why := range fabricated {
		if !seen[id] {
			t.Errorf("fabricated lists %s (%s), which no file contains; remove it", id, why)
		}
	}
}
