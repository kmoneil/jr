package lint_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Where internal/cli keeps its golden files, from here.
const (
	cliGoldenDir   = "../cli/testdata"
	kindsGoldenDir = "../cli/testdata/kinds"
)

// TestEveryShippedProfileHasAGoldenSet asserts the four profile directories
// exist and hold the same files.
//
// The golden set used to be recorded against the `ci` build alone, and the
// assertion skipped under any other tag set — so three of four profiles were
// covered by a test that reported success having compared nothing. This runs in
// every build, including the one whose own set is being checked, because a
// profile whose goldens were never recorded must not be able to pass by being
// the profile nobody ran.
func TestEveryShippedProfileHasAGoldenSet(t *testing.T) {
	profiles := profilesFromMakefile(t)

	var reference []string
	var referenceName string
	for _, p := range profiles {
		dir := filepath.Join(cliGoldenDir, p.name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("the %s profile ships and has no golden set at %s: %v\n"+
				"run `make golden`", p.name, dir, err)
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		slices.Sort(names)
		if len(names) == 0 {
			t.Errorf("%s is empty, so the %s profile compares against nothing",
				dir, p.name)
			continue
		}
		if reference == nil {
			reference, referenceName = names, p.name
			continue
		}
		if !slices.Equal(names, reference) {
			t.Errorf("the %s and %s golden sets hold different files:\n  %s: %v\n  %s: %v\n"+
				"every profile records the same commands, so a file present in one "+
				"set and missing from another means that profile's run was skipped",
				referenceName, p.name, referenceName, reference, p.name, names)
		}
	}
}

// TestEveryKindEveryProfileEmitsHasAShapeGolden is the guarantee the card
// asked for, checked from outside any one build.
//
// A kind's schema is pinned once per version in internal/cli/testdata/kinds,
// and the test that compares it can only iterate the kinds its own binary
// emits. Under `make test` that is the `ci` profile, which emits none of the
// write kinds — so a new `issue.*` write kind with no golden would pass the
// default suite and be caught only by `make test-profiles`. This builds every
// shipped profile and asks each one what it emits, so the gap fails in every
// build rather than in one.
func TestEveryKindEveryProfileEmitsHasAShapeGolden(t *testing.T) {
	bin := t.TempDir()
	emitted := map[string]bool{}

	for _, p := range profilesFromMakefile(t) {
		for _, k := range contractKinds(t, bin, p) {
			emitted[k.name] = true
			golden := filepath.Join(kindsGoldenDir, fmt.Sprintf("%s.v%d.xml", k.name, k.version))
			if _, err := os.Stat(golden); err != nil {
				t.Errorf("the %s profile emits kind %s at v%d and %s does not exist; "+
					"run `make golden`", p.name, k.name, k.version, golden)
			}
		}
	}

	if len(emitted) == 0 {
		t.Fatal("no profile reported any output kind")
	}

	// The other direction. A superseded version is kept on purpose — it is the
	// record of what that version was — but a file naming a kind no build emits
	// at all is residue from a kind that was renamed or removed.
	entries, err := os.ReadDir(kindsGoldenDir)
	if err != nil {
		t.Fatalf("read %s: %v", kindsGoldenDir, err)
	}
	for _, e := range entries {
		name := goldenKindName.FindStringSubmatch(e.Name())
		if name == nil {
			t.Errorf("%s is not named <kind>.v<version>.xml, so nothing can "+
				"match it to a kind", filepath.Join(kindsGoldenDir, e.Name()))
			continue
		}
		if !emitted[name[1]] {
			t.Errorf("%s pins the shape of kind %q, which no shipped profile "+
				"emits", filepath.Join(kindsGoldenDir, e.Name()), name[1])
		}
	}
}

// goldenKindName matches `issue.comment.add.v2.xml`, capturing the kind.
var goldenKindName = regexp.MustCompile(`^(.+)\.v\d+\.xml$`)

// kind is one row of `jr contract --format tsv`.
type kind struct {
	name    string
	version int
}

// contractKinds builds one profile and asks the binary which kinds it emits.
//
// It runs the real binary for the same reason commandCount does: a test binary
// is compiled with one tag set, and the question here is what the other three
// contain.
func contractKinds(t *testing.T, dir string, p profile) []kind {
	t.Helper()

	stdout := askBinary(t, buildProfile(t, dir, p), p, "contract", "--format", "tsv")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("%s contract returned no rows:\n%s", p.name, stdout)
	}

	out := make([]kind, 0, len(lines)-1)
	for _, line := range lines[1:] { // the header is not a kind
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("%s contract row %q has no version column", p.name, line)
		}
		var version int
		if _, err := fmt.Sscanf(fields[1], "%d", &version); err != nil {
			t.Fatalf("%s contract row %q has an unreadable version: %v", p.name, line, err)
		}
		out = append(out, kind{name: fields[0], version: version})
	}
	return out
}

// buildProfile builds one shipped profile and returns the path to the binary.
func buildProfile(t *testing.T, dir string, p profile) string {
	t.Helper()

	out := filepath.Join(dir, "jr-"+p.name)
	build := exec.Command("go", "build", "-tags", p.tags, "-o", out, "./cmd/jr")
	// The module root, which is two up from internal/lint.
	build.Dir = "../.."
	if err := build.Run(); err != nil {
		t.Fatalf("build %s (tags=%q): %v\n%s", p.name, p.tags, err, stderrOf(err))
	}
	return out
}

// askBinary runs a built profile and returns its stdout.
func askBinary(t *testing.T, bin string, p profile, args ...string) string {
	t.Helper()

	cmd := exec.Command(bin, args...) //nolint:gosec // bin is a binary this test just built.
	// A stray config or a context in the environment must not reach a command
	// that only reads its own registry.
	cmd.Env = append(os.Environ(), "JIRA_FORMAT=", "JIRA_READONLY=")
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", p.name, strings.Join(args, " "), err, stderrOf(err))
	}
	return string(stdout)
}
