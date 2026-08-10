package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The fuzz counts in the published documents have drifted four times: the
// README's JQL sentence said three against four, the architecture table said
// two `adf` fuzzers against three, and the total in the planning notes was
// wrong twice in two days — 11 against 17, then 17 against 18. Every one was
// found by somebody running `make fuzz` for another reason and reading the
// summary line, and every one was fixed by retyping the number, which buys
// exactly one day.
//
// This is the same defect the profile counts had and the error-code table had,
// and both were closed the same way: by making the document readable by a test.
//
// The counts are read from the source rather than from `go test -list`,
// which is deliberate and is the reasoning complexity_test.go already uses.
// Parsing sees a target inside a file that a build tag would exclude, and a
// tagged target is exactly what this must not miss — `make fuzz` once swept
// past internal/workflow entirely because the untagged build reported no
// targets there. Verified equal to `go test -list` under the full tag set at
// the time of writing: 20 targets, same per-package split.
//
// Not asserted here: `_plans/progress.md`, which carries the total and drifted
// twice. It is gitignored, so a test reading it fails in a fresh clone — the
// numbers a *published* document states are the ones a reader can be hurt by,
// and those are the ones below.

// fuzzTarget matches a fuzz entry point. The signature is what makes it one, so
// a helper named FuzzSomething is not counted and a target is not missed for
// being named unusually.
var fuzzTarget = regexp.MustCompile(`(?m)^func (Fuzz\w*)\(\w+ \*testing\.F\)`)

// wholeTree is the pkg of the claim that counts every target rather than one
// package's. Without it, a target added to a package no document mentions
// changes nothing and nothing notices — which is most of them: two packages
// carry a published count and ten hold targets.
const wholeTree = "*"

// countClaim is a sentence in a published document that states how many fuzz
// targets a package has, or wholeTree has in total.
type countClaim struct {
	doc string
	pkg string
	// sentence must match exactly once. A rewording that stops it matching is a
	// failure and not a silent pass: the profile-count test learned that a cell
	// whose shape the assertion does not know reads as unparseable, and the
	// only safe answer is to say so.
	sentence *regexp.Regexp
}

func countClaims() []countClaim {
	return []countClaim{{
		doc:      "../../README.md",
		pkg:      "internal/jql",
		sentence: regexp.MustCompile(`(?i)\b(\w+) fuzzers back it up`),
	}, {
		doc:      "../../docs/architecture.md",
		pkg:      "internal/adf",
		sentence: regexp.MustCompile(`(?i)round-trip property test, (\w+) fuzzers`),
	}, {
		doc:      "../../docs/architecture.md",
		pkg:      wholeTree,
		sentence: regexp.MustCompile(`\Qmake fuzz\E` + "`" + `, all (\w+) targets`),
	}}
}

// numberWords are the spellings the documents use. A count written as a word
// nobody listed fails rather than being guessed at.
var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
}

// TestTheDocumentedFuzzCountsAreTheOnesInTheTree holds each published count to
// the targets that exist.
func TestTheDocumentedFuzzCountsAreTheOnesInTheTree(t *testing.T) {
	byPackage := fuzzTargetsByPackage(t)

	for _, claim := range countClaims() {
		body := readDoc(t, claim.doc)
		found := claim.sentence.FindAllStringSubmatch(body, -1)
		if len(found) != 1 {
			t.Errorf("%s: the sentence stating %s's fuzz count matched %d times, "+
				"want 1 — it was reworded, and this assertion now reads nothing. "+
				"Update the pattern in countClaims or restore the wording",
				claim.doc, claim.pkg, len(found))
			continue
		}

		want, ok := countIn(found[0][1])
		if !ok {
			t.Errorf("%s: %q is not a number this test can read, so the claim "+
				"about %s cannot be checked. Write it as a numeral, or add the "+
				"word to numberWords", claim.doc, found[0][1], claim.pkg)
			continue
		}

		targets := byPackage[claim.pkg]
		subject := claim.pkg
		if claim.pkg == wholeTree {
			targets = nil
			for _, pkg := range slices.Sorted(maps(byPackage)) {
				targets = append(targets, byPackage[pkg]...)
			}
			subject = "the tree"
		}
		if got := len(targets); got != want {
			t.Errorf("%s says %s has %d fuzz targets and it has %d: %s",
				claim.doc, subject, want, got, strings.Join(targets, ", "))
		}
	}
}

// TestTheDocumentedFuzzTimeIsTheMakefileDefault holds the other number the
// documents state about the sweep.
//
// It is here because it drifted the same way and was found by the same look:
// the README said 30s against a Makefile default of 60s, while the
// architecture table said 60s. Two documents describing one variable, one of
// them wrong, and nothing able to tell.
func TestTheDocumentedFuzzTimeIsTheMakefileDefault(t *testing.T) {
	makefile := readDoc(t, "../../Makefile")
	def := regexp.MustCompile(`(?m)^FUZZTIME \?= (\S+)$`).FindStringSubmatch(makefile)
	if def == nil {
		t.Fatal("the Makefile no longer sets a FUZZTIME default the way this reads it")
	}
	want := def[1]

	for _, claim := range []struct {
		doc      string
		sentence *regexp.Regexp
	}{
		{"../../README.md", regexp.MustCompile(`FUZZTIME each \(default (\S+?)\)`)},
		{"../../docs/architecture.md", regexp.MustCompile(`Every PR, (\S+) per target`)},
	} {
		body := readDoc(t, claim.doc)
		found := claim.sentence.FindAllStringSubmatch(body, -1)
		if len(found) != 1 {
			t.Errorf("%s: the sentence stating the fuzz duration matched %d times, "+
				"want 1 — it was reworded and this assertion now reads nothing",
				claim.doc, len(found))
			continue
		}
		if got := found[0][1]; got != want {
			t.Errorf("%s says the sweep runs %s per target and the Makefile "+
				"defaults to %s", claim.doc, got, want)
		}
	}
}

// countIn reads a count written either as a numeral or as one of the words the
// documents use.
func countIn(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	n, ok := numberWords[strings.ToLower(s)]
	return n, ok
}

// fuzzTargetsByPackage returns the fuzz entry points in each package, keyed by
// the package's path relative to the module root.
func fuzzTargetsByPackage(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// bin/ holds build output and _plans is gitignored working notes;
			// neither is source this should read.
			if name := d.Name(); name == "bin" || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // walking this module's own tree.
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(repoRoot, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		pkg := filepath.ToSlash(rel)
		for _, m := range fuzzTarget.FindAllStringSubmatch(string(body), -1) {
			out[pkg] = append(out[pkg], m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A walk that found nothing would pass every claim that happens to say
	// zero, and say nothing about the rest.
	total := 0
	for _, targets := range out {
		total += len(targets)
	}
	if total < 15 {
		t.Fatalf("found only %d fuzz targets in the whole tree, so this walked "+
			"the wrong one", total)
	}
	return out
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // a path in this module's own tree.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
