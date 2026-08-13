package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// releaseDocs is every hand-written markdown document at the root and under
// docs/, found rather than listed: a worked example can appear in any of them,
// and a list would have to be remembered by the person adding the next one.
//
// Two are excluded by name. CHANGELOG.md names releases because that is the
// whole document, and docs/commands.md is generated.
func releaseDocs(t *testing.T) []string {
	t.Helper()

	var found []string
	for _, pattern := range []string{"../../*.md", "../../docs/*.md"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		for _, m := range matches {
			switch filepath.Base(m) {
			case "CHANGELOG.md", "commands.md":
				continue
			}
			found = append(found, m)
		}
	}
	if len(found) == 0 {
		t.Fatal("no documents found; this test would pass by reading nothing")
	}
	return found
}

// releaseClaim matches a release version printed as though it were this build's:
// `release="0.1.0"`, or the display string `jr 0.1.0 (full; …)`.
var releaseClaim = regexp.MustCompile(`release="(\d+\.\d+\.\d+[^"]*)"|\bjr (\d+\.\d+\.\d+[^ ]*) \(`)

// illustrative are the two version strings a document may print, and both are
// safe for the same reason: no build of this tree produces either, so neither
// can be read as a claim about what the current release is.
//
//   - `1.2.0` is the house placeholder, already held to the semver grammar and
//     to the shipped profile tags by TestTheWorkedVersionExamplesAreOnesTheCodeCouldPrint.
//   - `0.0.0-untagged+<sha>` is what every clone prints, so an on-ramp showing
//     it is showing the reader their own output rather than somebody's release.
var illustrative = regexp.MustCompile(`^1\.2\.0$|^0\.0\.0-untagged`)

// TestNoDocumentPrintsARelease keeps a worked example from naming a version.
//
// `docs/getting-started.md` showed `jr version` printing 0.1.0 and went on
// showing it through 0.1.1 and 0.2.0. Nothing caught it, and nothing of the
// existing shape could: `internal/lint/kindversions_test.go` compares a
// document's `v="N"` against the binary because the schema version is compiled
// in, and the release version lives in a git tag that does not exist while the
// tests run. There is nothing here to compare against, so the rule is that
// there is nothing to compare: a document does not claim a release at all.
//
// `CHANGELOG.md` is exempt and is not read here: naming releases is the whole
// document. So is `docs/commands.md`, which is generated.
func TestNoDocumentPrintsARelease(t *testing.T) {
	for _, doc := range releaseDocs(t) {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		for _, m := range releaseClaim.FindAllStringSubmatch(string(body), -1) {
			claimed := m[1] + m[2]
			if illustrative.MatchString(claimed) {
				continue
			}
			t.Errorf("%s prints release %s, which is true of one build and "+
				"will be wrong at the next tag; use the 1.2.0 placeholder, or "+
				"the untagged form a clone produces", doc, claimed)
		}
	}
}
