package lint_test

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
)

// semver is the shape a version has to have, written here rather than imported
// so this check does not share a definition with the thing it checks.
//
// MAJOR.MINOR.PATCH, an optional prerelease, an optional build metadata suffix.
// Deliberately not the full grammar — it does not reject a leading zero in a
// numeric prerelease identifier, which is a conformance detail no consumer of
// this string will act on. What it does reject is what actually shipped: a bare
// commit hash.
var semver = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?` +
		`(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`,
)

// TestTheStampedVersionIsSemver runs the script the Makefile stamps builds with
// and holds its answer to the shape a version has.
//
// `jr version` and the User-Agent both carry this string, and the User-Agent is
// what a Jira administrator sees in their access logs. It used to be whatever
// `git describe --tags --always --dirty` produced, which on a tree with no tags
// is a bare commit hash: `jr/786d271` names no release, sorts against nothing,
// and does not announce that it is not a version. Nothing checked, because
// nothing had ever looked at the value.
//
// This runs in the repository it is testing, so it exercises whichever case
// that tree is actually in — tagged, untagged, or dirty. The other cases are
// exercised by the script's own logic and named in its header comment.
func TestTheStampedVersionIsSemver(t *testing.T) {
	cmd := exec.Command("sh", "scripts/version.sh")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("scripts/version.sh: %v\n%s", err, stderrOf(err))
	}

	got := strings.TrimSpace(string(out))
	if got == "" {
		t.Fatal("scripts/version.sh printed nothing; a build would be stamped empty")
	}
	if !semver.MatchString(got) {
		t.Errorf("scripts/version.sh printed %q, which is not a semantic version.\n"+
			"This string reaches a Jira administrator's access log as the "+
			"User-Agent; a value that does not parse as a version tells them "+
			"nothing about which release is talking to them.", got)
	}
}

// TestTheDefaultVersionIsSemver covers the build the Makefile did not make.
//
// `go build ./cmd/jr` stamps nothing, so the binary reports whatever the
// package default is. That default used to be `0.1.0-dev`, which is a
// plausible-looking release number for a build that has no idea what it is.
// 0.0.0 is the honest answer, and it still has to parse.
func TestTheDefaultVersionIsSemver(t *testing.T) {
	if !semver.MatchString(buildinfo.Release) {
		t.Errorf("buildinfo.Release defaults to %q, which is not a semantic version",
			buildinfo.Release)
	}
	if strings.HasPrefix(buildinfo.Release, "0.0.0") {
		return
	}
	t.Errorf("buildinfo.Release defaults to %q, which claims a release number. "+
		"An unstamped build does not know what it is and should say so",
		buildinfo.Release)
}
