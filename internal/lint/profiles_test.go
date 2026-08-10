package lint_test

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
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

// TestTheGatesTodayColumnMatchesTheBinaries reads the third column of the tag
// table, which was the one column of that document nothing asserted.
//
// The doc says of it: "The right-hand column is not documentation that can
// drift. internal/lint/tags_test.go asserts it." That was half true.
// tags_test asserts whether a tag gates *any file*, which is what catches a tag
// listed as gating nothing once it starts gating something. It never read what
// the cell says. So `write` claimed 18 mutating verbs against a real 19, in a
// column captioned as un-driftable, in a document whose other two tables are
// asserted precisely because they went four releases stale.
//
// What the column claims is about a binary — what a build without the tag turns
// out not to contain — so it is measured that way, by building the full profile
// without each tag in turn and taking the difference. A command's RequiresTags
// is a declaration; the two agree only while the registration file is gated to
// match, and this is the check that would notice if it were not.
func TestTheGatesTodayColumnMatchesTheBinaries(t *testing.T) {
	documented := gatesTodayFromDoc(t)

	var full profile
	for _, p := range profilesFromMakefile(t) {
		if p.name == "full" {
			full = p
		}
	}
	if full.tags == "" {
		t.Fatal("the Makefile ships no full profile to measure against")
	}

	bin := t.TempDir()
	present := commandsIn(t, bin, full)

	for _, tag := range buildinfo.KnownTags {
		cell, ok := documented[tag]
		if !ok {
			t.Errorf("the tag table does not list %q", tag)
			continue
		}
		t.Run(tag, func(t *testing.T) {
			gates := gatedCommands(t, bin, full, present, tag)
			switch want := readGatesCell(t, cell); {
			case want.format != "":
				// A format is not visible in `jr schema`, so it is measured by
				// asking each build to use it: the tagged one accepts it and
				// the one without refuses it. That is the same thing a caller
				// finds out, rather than a proxy for it.
				if len(gates) > 0 {
					t.Errorf("the table says %s gates a format and it also gates %v",
						tag, gates)
				}
				assertFormatIsGated(t, bin, full, tag, want.format)
			case want.nothing:
				if len(gates) > 0 {
					t.Errorf("the table says %s gates nothing and it gates %v",
						tag, gates)
				}
			case want.commands != nil:
				if !slices.Equal(gates, want.commands) {
					t.Errorf("the table says %s gates %v and it gates %v",
						tag, want.commands, gates)
				}
			default:
				if len(gates) != want.count {
					t.Errorf("the table says %s gates %d commands and it gates "+
						"%d: %v", tag, want.count, len(gates), gates)
				}
			}
		})
	}

	for tag := range documented {
		if !slices.Contains(buildinfo.KnownTags, tag) {
			t.Errorf("the tag table lists %q, which is not a known tag", tag)
		}
	}
}

// gatesCell is one parsed "Gates today" cell. A cell names commands, counts
// them, or says the tag gates nothing.
type gatesCell struct {
	nothing  bool
	commands []string
	count    int
	// format is the output format a tag adds, for a tag that gates something
	// other than a command.
	format string
}

// docCommand matches a command named in a doc cell, e.g. `jr sprint close`.
var docCommand = regexp.MustCompile("`jr ([a-z]+(?: [a-z]+)*)`")

// docFormat matches an output format named in a doc cell, e.g.
// `--format markdown`.
//
// `render` is the first tag to gate something that is not a command, and the
// column had no way to say so: a cell is read by shape, and this shape did not
// exist. It cannot collide with docCommand, which requires the backticked text
// to begin "jr ".
var docFormat = regexp.MustCompile("`--format ([a-z]+)`")

// docCount matches the count in a cell like "the 18 mutating verbs".
var docCount = regexp.MustCompile(`\b(\d+)\b`)

// readGatesCell decides what one cell claims.
//
// "nothing" is tested first and deliberately: three of those four cells name a
// command as the thing that does not exist yet, as in "**nothing**, no `jr ui`
// yet", and reading them as a command list would assert the opposite of what they
// say.
func readGatesCell(t *testing.T, cell string) gatesCell {
	t.Helper()

	if strings.HasPrefix(strings.TrimSpace(cell), "**nothing**") {
		return gatesCell{nothing: true}
	}
	if m := docFormat.FindStringSubmatch(cell); m != nil {
		return gatesCell{format: m[1]}
	}
	if m := docCommand.FindAllStringSubmatch(cell, -1); m != nil {
		var out []string
		for _, one := range m {
			out = append(out, strings.ReplaceAll(one[1], " ", "."))
		}
		slices.Sort(out)
		return gatesCell{commands: out}
	}
	if m := docCount.FindStringSubmatch(cell); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unreadable count in %q", cell)
		}
		return gatesCell{count: n}
	}
	t.Fatalf("%s: cannot read %q as a count, a command list, or nothing; this "+
		"test reads the column by shape, so a reworded cell asserts nothing",
		profilesDoc, cell)
	return gatesCell{}
}

// assertFormatIsGated checks that a format exists in the full build and not in
// the same build without one tag.
//
// Both halves matter and the second is the one worth having. Asserting only
// that the tagged build accepts the format would pass just as well against a
// format that was never gated at all — which is the whole claim being made, and
// the reason the reader, agent, and ci profiles can be said not to emit it.
func assertFormatIsGated(t *testing.T, dir string, full profile, tag, format string) {
	t.Helper()

	kept := slices.DeleteFunc(strings.Split(full.tags, ","),
		func(s string) bool { return s == tag })
	without := profile{name: "full-no-" + tag, tags: strings.Join(kept, ",")}

	// `version` is the cheapest command that emits a document, needs no
	// network, and exists in every profile.
	askBinary(t, buildProfile(t, dir, full), full, "version", "--format", format)

	bin := buildProfile(t, dir, without)
	cmd := exec.Command(bin, "version", "--format", format) //nolint:gosec // a binary this test just built.
	cmd.Env = append(os.Environ(), "JIRA_FORMAT=", "JIRA_READONLY=")
	if out, err := cmd.Output(); err == nil {
		t.Errorf("%s accepts --format %s without the %s tag:\n%s",
			without.name, format, tag, out)
	} else if stderr := stderrOf(err); !strings.Contains(stderr, "INVALID_FORMAT") {
		t.Errorf("%s refused --format %s with something other than INVALID_FORMAT:\n%s",
			without.name, format, stderr)
	}
}

// gatedCommands returns the commands one tag gates, as the difference between
// the full build and the full build without that tag.
func gatedCommands(t *testing.T, dir string, full profile, present []string, tag string) []string {
	t.Helper()

	kept := slices.DeleteFunc(strings.Split(full.tags, ","),
		func(s string) bool { return s == tag })
	without := profile{name: "full-no-" + tag, tags: strings.Join(kept, ",")}

	var out []string
	remaining := commandsIn(t, dir, without)
	for _, name := range present {
		if !slices.Contains(remaining, name) {
			out = append(out, name)
		}
	}
	return out
}

// commandCount builds one profile and asks the binary what it contains.
//
// It runs the real binary rather than counting registrations in-process,
// because in-process is the one thing that cannot answer this question: a test
// binary is compiled with one tag set, and the whole point is what the other
// three contain.
func commandCount(t *testing.T, dir string, p profile) int {
	t.Helper()
	return len(commandsIn(t, dir, p))
}

// commandsIn builds one profile and returns the dotted command names it holds.
func commandsIn(t *testing.T, dir string, p profile) []string {
	t.Helper()

	// --limit all rather than the default, so this measures the surface and not
	// whatever bound `schema` happens to carry.
	stdout := askBinary(t, buildProfile(t, dir, p), p, "schema", "--format", "tsv", "--limit", "all")

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("%s schema returned no rows:\n%s", p.name, stdout)
	}
	out := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] { // the header is not a command
		name, _, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("%s schema wrote a row with no tab: %q", p.name, line)
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
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

// gatesTodayRow matches a row of the tag table:
//
//	| `write` | All mutating commands | the 18 mutating verbs |
//
// The first cell has to be a known tag. The profile-count table further down
// has the same shape with a backticked lowercase name in the same place, and a
// row of it would otherwise be read as a tag that gates `—`.
var gatesTodayRow = regexp.MustCompile("^\\|\\s*`([a-z]+)`\\s*\\|[^|]*\\|([^|]*)\\|")

// gatesTodayFromDoc reads the third column of the tag table, keyed by tag.
func gatesTodayFromDoc(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, line := range readLines(t, profilesDoc) {
		m := gatesTodayRow.FindStringSubmatch(line)
		if m == nil || !slices.Contains(buildinfo.KnownTags, m[1]) {
			continue
		}
		out[m[1]] = strings.TrimSpace(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("%s: no tag table found; this test reads it by shape, so a "+
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

// readmePath is the fourth document carrying a number about the command
// surface, and it was the only one nothing read.
const readmePath = "../../README.md"

// readmeCount matches the sentence the README states its surface in, wherever
// it says it:
//
//	62 commands in the full build, 40 in the reader.
//	complete and tested: 62 commands in the full build, 40 in the reader.
//	Everything else described in this README is built. 62 commands in the full
//
// The "in the reader" half is optional, because the third occurrence does not
// carry it.
var readmeCount = regexp.MustCompile(
	`(\d+) commands? in the full\s+build(?:, (\d+) in the reader)?`,
)

// TestTheReadmeSurfaceCountMatchesTheBinaries holds the README to the same
// binaries docs/build-profiles.md is held to.
//
// It says the number three times and every one of them was 60 against a real
// 62 the moment `sprint create` and `sprint start` landed. The sentence beside
// the third even said the lint asserts the tag table "rather than against this
// sentence", which was an accurate description of a gap: the profile table was
// gated and the README was not, so the document a new reader meets first was
// the one free to drift.
//
// Every occurrence is checked rather than the first, because three copies of a
// number are three chances to update two of them.
func TestTheReadmeSurfaceCountMatchesTheBinaries(t *testing.T) {
	profiles := profilesFromMakefile(t)
	bin := t.TempDir()

	counts := map[string]int{}
	for _, p := range profiles {
		if p.name == "full" || p.name == "reader" {
			counts[p.name] = commandCount(t, bin, p)
		}
	}
	if len(counts) != 2 {
		t.Fatalf("the Makefile ships %v; this test needs a full and a reader "+
			"profile to compare the README against", profiles)
	}

	body := strings.Join(readLines(t, readmePath), "\n")
	found := readmeCount.FindAllStringSubmatch(body, -1)
	if len(found) == 0 {
		t.Fatalf("%s: no surface count found; this test reads it by shape, so a "+
			"reworded sentence silently asserts nothing", readmePath)
	}

	for _, m := range found {
		full, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s: %q has an unreadable count", readmePath, m[0])
		}
		if full != counts["full"] {
			t.Errorf("%s says %q and the full build has %d commands",
				readmePath, strings.Join(strings.Fields(m[0]), " "), counts["full"])
		}
		if m[2] == "" {
			continue
		}
		reader, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s: %q has an unreadable reader count", readmePath, m[0])
		}
		if reader != counts["reader"] {
			t.Errorf("%s says %q and the reader build has %d commands",
				readmePath, strings.Join(strings.Fields(m[0]), " "), counts["reader"])
		}
	}
}
