package adf_test

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
)

// verdictGolden is what FromMarkdown answers for every input in it, and it is
// the only thing in this package that can see a refusal move.
//
// FuzzMarkdownRoundTrips opens with `if err != nil { return }`, so **a refusal
// ends the property early and refusing more always looks greener**. A change
// that made the converter refuse every document in the world would leave that
// fuzzer permanently, silently happy, and no other test here asks the question
// either: they assert what specific inputs do, and a refusal set is a shape
// nobody looks at whole.
//
// That is not a hypothetical. Two of them have shipped:
//
//   - `flanks` once refused 25 inputs this converter had written correctly the
//     day before, because sideOf read a zero rune as "nothing there" and a NUL
//     inside a span is punctuation. Two sweeps at 600s a target said nothing.
//     It was found by diffing verdicts across two builds while pricing a
//     release, which is not a thing that happens on a schedule.
//   - The three-line change in `after` that this file arrived with moved 95
//     verdicts: 94 inputs it had been refusing for no reason became writable,
//     and **one input that used to convert now refuses**. The suite was green
//     for all 95, and the pull request claimed nothing had regressed.
//
// So the file is the record and the diff is the review. An input moving from
// REFUSED to OK is a fix; one moving the other way is the defect this exists to
// catch, and if it is intended it needs a sentence in the commit message saying
// why. Regenerate with `make golden`.
const verdictGolden = "markdown-verdicts.tsv"

// verdictEntry is one input and what FromMarkdown said about it.
type verdictEntry struct {
	input   string
	refused bool
}

func verdictName(refused bool) string {
	if refused {
		return "REFUSED"
	}
	return "OK"
}

// loadVerdicts reads the golden. The input is quoted, so a corpus entry holding
// a newline or a NUL is one line here.
func loadVerdicts(t *testing.T) []verdictEntry {
	t.Helper()

	path := filepath.Join("testdata", verdictGolden)
	f, err := os.Open(path) //nolint:gosec // a path from the test tree.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []verdictEntry
	scan := bufio.NewScanner(f)
	// A fuzz input can be longer than the default 64 KiB token.
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; scan.Scan(); line++ {
		text := scan.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		verdict, quoted, found := strings.Cut(text, "\t")
		if !found {
			t.Fatalf("%s:%d is not verdict<TAB>input", path, line)
		}
		input, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatalf("%s:%d input does not unquote: %v", path, line, err)
		}
		out = append(out, verdictEntry{input: input, refused: verdict == "REFUSED"})
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

// TestTheRefusalSetIsPinned is the second property over the corpus, and the one
// the round-trip fuzzer cannot express.
//
// It says nothing about whether a refusal is right. It says the set of them does
// not move without somebody noticing, which is the whole of what was missing:
// every refusal regression this package has had was invisible until a human
// diffed two builds by hand.
func TestTheRefusalSetIsPinned(t *testing.T) {
	entries := loadVerdicts(t)
	if len(entries) < 2000 {
		t.Fatalf("the verdict corpus holds %d inputs, which is too few to say "+
			"anything about a refusal set; reseed it from the fuzz cache",
			len(entries))
	}

	if *update {
		rewriteVerdicts(t, entries)
		return
	}

	var regressed, fixed []string
	for _, e := range entries {
		_, err := adf.FromMarkdown(e.input)
		refused := err != nil
		if refused == e.refused {
			continue
		}
		if refused {
			regressed = append(regressed, strconv.Quote(e.input))
			continue
		}
		fixed = append(fixed, strconv.Quote(e.input))
	}

	// Reported separately, because the two directions are different news and
	// running them together is how the important one gets skimmed past.
	if len(regressed) > 0 {
		t.Errorf("%d input(s) this converter used to write are now refused, "+
			"and no other test in this package can see that:\n  %s\n"+
			"A refusal ends the round-trip property early, so the fuzzer will "+
			"stay green over every one of them. If the refusals are right, run "+
			"`make golden` and say in the commit message why the document has "+
			"no spelling.",
			len(regressed), strings.Join(capped(regressed, 10), "\n  "))
	}
	if len(fixed) > 0 {
		t.Errorf("%d input(s) that used to be refused now convert:\n  %s\n"+
			"That is a fix and the golden is stale. Run `make golden`, and the "+
			"diff is what the pull request reviews.",
			len(fixed), strings.Join(capped(fixed, 10), "\n  "))
	}
}

// capped trims a list for a failure message and says how much it left out. A
// message that prints two thousand lines is a message nobody reads.
func capped(all []string, n int) []string {
	if len(all) <= n {
		return all
	}
	return append(all[:n:n], "… and "+strconv.Itoa(len(all)-n)+" more")
}

// rewriteVerdicts recomputes the verdict for every input the golden already
// holds, and keeps the input set exactly as it was.
//
// The inputs are not reseeded from the fuzz cache here, and that is deliberate:
// the cache is per-machine and CI restores it from a key that can miss, so a
// golden that reseeded itself would produce a different file on every machine
// and fail for the wrong reason. Growing the set is a deliberate act, done when
// a find arrives, the same way testdata/fuzz/ grows.
func rewriteVerdicts(t *testing.T, entries []verdictEntry) {
	t.Helper()

	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		_, err := adf.FromMarkdown(e.input)
		lines = append(lines, verdictName(err != nil)+"\t"+strconv.Quote(e.input))
	}
	// Sorted on the quoted input, so the file has one order whatever order the
	// inputs arrived in and a diff is only ever a verdict changing.
	sort.Slice(lines, func(a, b int) bool {
		_, x, _ := strings.Cut(lines[a], "\t")
		_, y, _ := strings.Cut(lines[b], "\t")
		return x < y
	})

	path := filepath.Join("testdata", verdictGolden)
	body := header + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// header explains the file to whoever opens it in a diff.
const header = `# What FromMarkdown answers for each input, one line per input.
#
# Generated by ` + "`make golden`" + `. The input set is not regenerated: it was
# seeded from the fuzz cache and grows by hand when a find arrives. See
# TestTheRefusalSetIsPinned for why a refusal moving in either direction is
# worth a line in a pull request.
`
