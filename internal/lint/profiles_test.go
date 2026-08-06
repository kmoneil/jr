package lint_test

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The two files this test holds to each other. The Makefile is the authority on
// what a profile is; the doc is what a reader believes.
const (
	makefilePath = "../../Makefile"
	profilesDoc  = "../../docs/build-profiles.md"
)

// profile is one shipped build.
type profile struct {
	name string
	// tags is the value of the Makefile's TAGS_<NAME>, which may be empty for
	// the ci profile.
	tags string
}

// TestTheProfileTableMatchesTheBinaries builds each shipped profile and counts
// what `jr schema` reports, then holds the table in docs/build-profiles.md to
// the answer.
//
// The counts in that table were four releases stale when this was written — 26,
// 25, 18, 17 against a real 54, 52, 35, 34 — because nothing asserted them. A
// number in a document that nothing checks is a number that was true once.
//
// It counts the *command surface* rather than bytes on purpose. The doc already
// says binary size is a poor proxy for compile-out, and the surface is the
// guarantee the profiles are actually sold on: a reader build does not list
// `issue create` because it does not contain it.
func TestTheProfileTableMatchesTheBinaries(t *testing.T) {
	profiles := profilesFromMakefile(t)
	documented := profileCountsFromDoc(t)

	if len(documented) != len(profiles) {
		t.Errorf("the Makefile ships %d profiles and the doc tabulates %d",
			len(profiles), len(documented))
	}

	bin := t.TempDir()
	for _, p := range profiles {
		want, ok := documented[p.name]
		if !ok {
			t.Errorf("profile %q ships and the doc's table does not mention it", p.name)
			continue
		}
		got := commandCount(t, bin, p)
		if got != want {
			t.Errorf("the %s profile has %d commands and docs/build-profiles.md "+
				"says %d; run `%s schema --format tsv | tail -n +2 | wc -l` and "+
				"fix the table", p.name, got, want, filepath.Join(bin, "jr-"+p.name))
		}
	}

	for name := range documented {
		if !slicesContainsProfile(profiles, name) {
			t.Errorf("the doc tabulates a %q profile the Makefile does not ship", name)
		}
	}
}

// TestTheDocumentedTagSetsAreTheOnesTheMakefileBuilds covers the other half of
// the same drift. A count is only meaningful next to the tags it was counted
// with, and the doc prints both.
func TestTheDocumentedTagSetsAreTheOnesTheMakefileBuilds(t *testing.T) {
	documented := profileTagsFromDoc(t)

	for _, p := range profilesFromMakefile(t) {
		got, ok := documented[p.name]
		if !ok {
			t.Errorf("the doc's shipped-profiles block does not list %q", p.name)
			continue
		}
		// The doc writes an empty tag set as "(none)", which is the readable
		// spelling of what the Makefile writes as nothing at all.
		want := p.tags
		if want == "" {
			want = "(none)"
		}
		if got != want {
			t.Errorf("the doc builds %s with %q and the Makefile uses %q",
				p.name, got, want)
		}
	}
}

// commandCount builds one profile and asks the binary what it contains.
//
// It runs the real binary rather than counting registrations in-process,
// because in-process is the one thing that cannot answer this question: a test
// binary is compiled with one tag set, and the whole point is what the other
// three contain.
func commandCount(t *testing.T, dir string, p profile) int {
	t.Helper()

	// --limit all rather than the default, so this measures the surface and not
	// whatever bound `schema` happens to carry.
	stdout := askBinary(t, buildProfile(t, dir, p), p, "schema", "--format", "tsv", "--limit", "all")

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("%s schema returned no rows:\n%s", p.name, stdout)
	}
	return len(lines) - 1 // the header is not a command
}

func stderrOf(err error) string {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return string(ee.Stderr)
	}
	return ""
}

// makefileTags matches `TAGS_FULL   := tui,prompt,...`, including the empty
// right-hand side the ci profile has.
var makefileTags = regexp.MustCompile(`^TAGS_([A-Z]+)\s*:=\s*(.*)$`)

// profilesFromMakefile reads the shipped tag sets from the Makefile, so this
// test has no second copy of them to go stale.
func profilesFromMakefile(t *testing.T) []profile {
	t.Helper()

	var out []profile
	for _, line := range readLines(t, makefilePath) {
		m := makefileTags.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, profile{
			name: strings.ToLower(m[1]),
			tags: strings.TrimSpace(m[2]),
		})
	}
	if len(out) == 0 {
		t.Fatal("no TAGS_* assignments found in the Makefile")
	}
	return out
}

// docTableRow matches `| \`full\`   | 54       | — |`.
var docTableRow = regexp.MustCompile(`^\|\s*` + "`" + `([a-z]+)` + "`" + `\s*\|\s*(\d+)\s*\|`)

// profileCountsFromDoc reads the command counts out of the profile table.
func profileCountsFromDoc(t *testing.T) map[string]int {
	t.Helper()

	out := map[string]int{}
	for _, line := range readLines(t, profilesDoc) {
		m := docTableRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s: %q has an unreadable count", profilesDoc, line)
		}
		out[m[1]] = n
	}
	if len(out) == 0 {
		t.Fatalf("%s: no profile table found; this test reads it by shape, so a "+
			"reformatted table silently asserts nothing", profilesDoc)
	}
	return out
}

// docBuildLine matches `make build-agent   # agent    mcp,write` and the bare
// `make build         # full     ...` for the full profile.
var docBuildLine = regexp.MustCompile(`^make build[a-z-]*\s+#\s*([a-z]+)\s+(.*)$`)

// profileTagsFromDoc reads the tag sets out of the shipped-profiles block.
func profileTagsFromDoc(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, line := range readLines(t, profilesDoc) {
		m := docBuildLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if name == "all" {
			// `make build-all` builds every profile and names no tag set.
			continue
		}
		out[name] = strings.TrimSpace(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("%s: no shipped-profiles block found", profilesDoc)
	}
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()

	f, err := os.Open(path) //nolint:gosec // both paths are constants in this file.
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

func slicesContainsProfile(profiles []profile, name string) bool {
	for _, p := range profiles {
		if p.name == name {
			return true
		}
	}
	return false
}
