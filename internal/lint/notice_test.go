package lint_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// noticePath is the file that says what a distributed binary carries with it.
// The other half of the comparison is goModPath, which vuln_test.go declares.
const noticePath = "../../NOTICE"

// platforms is every operating system somebody can build this for. The
// releases are linux and darwin, and windows is here because `go build` takes
// it and cobra reaches a module there that it reaches nowhere else: attribution
// that is correct only on the platforms we happen to ship is attribution that
// breaks the moment somebody builds their own.
var platforms = []string{"linux", "darwin", "windows"}

// noticeModule matches a module path where NOTICE lists one, which is at the
// start of an indented line with its licence beside it.
var noticeModule = regexp.MustCompile(`(?m)^\s{4}(\S+/\S+|\S+\.\S+/\S+)\s{2,}\S`)

// TestTheNoticeCarriesEveryModuleTheBinaryLinks holds NOTICE to what the
// compiler actually pulls in, for every shipped profile on every platform.
//
// golang.org/x/term was missing from it. internal/cli/env.go imports the module
// with no build tag, so every profile links it and every archive would have
// shipped a BSD-3-Clause binary without the BSD-3-Clause notice, in the one
// file whose entire job is to travel with that binary. NOTICE said "go.mod is
// the authority" and nothing read go.mod; the count sentence beside the list
// said four and go.mod said five.
//
// Over-listing is allowed and under-listing is not. A module named here that
// the build no longer reaches costs a reader one confusing line; a module the
// build reaches that is not named here is a licence obligation nobody met.
func TestTheNoticeCarriesEveryModuleTheBinaryLinks(t *testing.T) {
	notice := strings.Join(readLines(t, noticePath), "\n")

	var listed []string
	for _, m := range noticeModule.FindAllStringSubmatch(notice, -1) {
		listed = append(listed, m[1])
	}
	if len(listed) == 0 {
		t.Fatalf("%s: no module list found; this test reads it by shape, so a "+
			"reformatted list silently asserts nothing", noticePath)
	}

	for _, p := range profilesFromMakefile(t) {
		for _, goos := range platforms {
			for _, mod := range linkedModules(t, p, goos) {
				if !slices.Contains(listed, mod) {
					t.Errorf("%s does not list %s, which the %s profile links on %s",
						noticePath, mod, p.name, goos)
				}
			}
		}
	}
}

// TestTheDependencyCountsMatchGoMod checks the sentences that state the same
// thing in prose, in NOTICE and in the README.
//
// The list above it can be right while the sentence under it is wrong, and the
// sentence is the part somebody skims. Both said four direct dependencies
// against a real five.
func TestTheDependencyCountsMatchGoMod(t *testing.T) {
	direct, indirect := requiresFromGoMod(t)

	counts := regexp.MustCompile(`(?i)(\w+) direct dependenc(?:y|ies)(?: and (\w+) indirect)?`)
	for _, path := range []string{noticePath, readmePath} {
		body := strings.Join(readLines(t, path), "\n")
		found := counts.FindAllStringSubmatch(body, -1)
		if len(found) == 0 {
			continue
		}

		for _, m := range found {
			if got := spelled(m[1]); got != len(direct) {
				t.Errorf("%s says %q and go.mod requires %d directly: %v",
					path, m[0], len(direct), direct)
			}
			if m[2] == "" {
				continue
			}
			if got := spelled(m[2]); got != len(indirect) {
				t.Errorf("%s says %q and go.mod requires %d indirectly: %v",
					path, m[0], len(indirect), indirect)
			}
		}
	}
}

// linkedModules returns every module the compiler reaches for one profile on
// one platform, which is what a binary links and therefore what its licences
// have to cover.
//
// `go list` rather than go.mod, because go.mod names what the module depends
// on and this question is what the *binary* contains. The two differ: a
// requirement reached only by a test, or only under a build tag nothing in this
// profile sets, is in the first and not the second.
func linkedModules(t *testing.T, p profile, goos string) []string {
	t.Helper()

	cmd := goCmd(repoRoot, "list",
		"-tags", p.tags,
		"-deps",
		"-f", "{{if .Module}}{{.Module.Path}}{{end}}",
		"./cmd/jr")
	// GOARCH is fixed so this asks about one build rather than the host's, and
	// amd64 exists on all three platforms.
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list for profile %s on %s: %v\n%s", p.name, goos, err, stderrOf(err))
	}

	var mods []string
	for line := range strings.SplitSeq(string(out), "\n") {
		mod := strings.TrimSpace(line)
		if mod == "" || mod == "github.com/kmoneil/jr" {
			continue
		}
		if !slices.Contains(mods, mod) {
			mods = append(mods, mod)
		}
	}
	return mods
}

// requiresFromGoMod splits go.mod's requirements the way the prose does, into
// what this module asks for and what those bring with them.
func requiresFromGoMod(t *testing.T) (direct, indirect []string) {
	t.Helper()

	inBlock := false
	for _, line := range readLines(t, goModPath) {
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case inBlock && strings.HasPrefix(line, ")"):
			inBlock = false
			continue
		case !inBlock && !strings.HasPrefix(line, "require "):
			continue
		}

		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) < 2 || strings.HasPrefix(fields[0], "//") {
			continue
		}
		if strings.Contains(line, "// indirect") {
			indirect = append(indirect, fields[0])
			continue
		}
		direct = append(direct, fields[0])
	}

	if len(direct) == 0 {
		t.Fatalf("%s: no direct requirements parsed; this test reads it by shape", goModPath)
	}
	return direct, indirect
}

// spelled turns the number a sentence writes out into one that can be compared.
// A count in prose is spelled, and rewriting the prose to carry digits so a
// test can read it would be the test changing the document to suit itself.
func spelled(word string) int {
	words := []string{
		"zero", "one", "two", "three", "four", "five", "six", "seven",
		"eight", "nine", "ten", "eleven", "twelve",
	}
	return slices.Index(words, strings.ToLower(word))
}
