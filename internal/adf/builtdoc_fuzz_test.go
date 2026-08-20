package adf_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
)

// FuzzMarkedParagraphConverts fuzzes documents built from *marks* rather than
// from markdown, which is the input space both existing targets are blind to.
//
// FuzzMarkdownRoundTrips seeds from markdown, so it can only ever reach
// documents markdown has a spelling for. That blind spot has already cost:
// three documents Cloud stores went out as `~~*a.*~~~~b~~` and read back with
// the tildes as text, and none of the three is reachable from markdown, because
// `~~*a.*b~~` is a different document — the emphasis cannot close in front of a
// word character, so the reader keeps the asterisks.
//
// FuzzToMarkdown takes raw bytes and hands them to Parse, which is a fine
// robustness check and a poor way to reach the property it claims. Measured on
// 2026-08-20 over its own corpus of 639 entries: 614 fail Parse and return
// before ToMarkdown is called at all, 8 are refused, and 17 convert. Twenty-five
// inputs reach the property, after however many hours of fuzzing have gone into
// it, because a mutation of a JSON document is almost never a JSON document.
//
// This one reaches it on every input by construction, and says so loudly if it
// ever does not: a generator that quietly produces documents Parse refuses is
// the same failure one layer up.
func FuzzMarkedParagraphConverts(f *testing.F) {
	// The seeds are the shapes that have actually gone wrong, so they are tried
	// on every run rather than waiting to be rediscovered. Each is a text and a
	// mark plan; see markPlan for the bits.
	for _, seed := range []struct {
		text  string
		marks []byte
	}{
		{"", nil},
		{"a", []byte{0}},
		// A span whose content opens on an escape, which is what made
		// `insideLive` miss a delimiter and cut a strike into four tildes.
		{`\*0`, []byte{markEm}},
		// Emphasis ending in a full stop with a word after it: the reader will
		// not close there, so the writer may not offer that spelling.
		{"a.b", []byte{markEm, 0}},
		// The strike-cut shape itself: em inside strike, then a bare strike
		// beside it, which is how `~~*a.*~~~~b~~` was built.
		{"a.b", []byte{markEm | markStrike, markStrike}},
		// A link, whose bracket is punctuation the flanking rules see.
		{"ab", []byte{markLink, 0}},
		// code is innermost and verbatim, so it interacts with every other mark
		// differently from the way they interact with each other.
		{"ab", []byte{markCode | markStrong, markStrong}},
		// Every mark at once, which nothing hand-written would think to try.
		{"abc", []byte{markStrong | markEm | markStrike | markCode | markLink}},
	} {
		f.Add(seed.text, seed.marks)
	}

	f.Fuzz(func(t *testing.T, text string, marks []byte) {
		// Both axes are bounded, and the reason is a measurement rather than a
		// precaution. Unbounded, the first two-minute run spent 51 consecutive
		// seconds at zero executions per second on a single input, because the
		// fuzzer had handed it a plan long enough to build tens of thousands of
		// spans. That is not this target's subject and it is already somebody
		// else's: TestConversionIsLinearInSpanCount doubles the span count and
		// asserts the shape of the curve, which is a better instrument for size
		// than a fuzzer with a deadline.
		//
		// What this target is for is mark *combinations*, and every shape that
		// has gone wrong so far needed two or three adjacent spans. Truncating
		// rather than skipping keeps a large input meaningful: it maps onto a
		// document in range instead of being thrown away and regenerated.
		if len(marks) > maxRuns {
			marks = marks[:maxRuns]
		}
		if runes := []rune(text); len(runes) > maxRunes {
			text = string(runes[:maxRunes])
		}

		// Invalid UTF-8 is skipped rather than built, and the reason is not
		// convenience. json.Marshal replaces an invalid byte with U+FFFD, so a
		// document built through it would not be the document this planned, and
		// the test would be about a substitution rather than about the writer.
		// Nothing is lost: Parse refuses invalid UTF-8, so no such document can
		// reach ToMarkdown on a real site either.
		if !utf8.ValidString(text) {
			return
		}

		doc := builtParagraph(text, marks)
		encoded, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("a built document did not encode: %v", err)
		}
		parsed, err := adf.Parse(encoded)
		if err != nil {
			// A skip here would be how this target quietly becomes the one it
			// was written to replace.
			t.Fatalf("the generator built a document Parse refuses: %v\n%s", err, encoded)
		}

		if _, err := adf.ToMarkdown(parsed); err != nil {
			if code := errs.Coerce(err).Code; code != "ADF_UNREPRESENTABLE" {
				t.Fatalf("code = %q, want ADF_UNREPRESENTABLE: %v\n%s", code, err, encoded)
			}
		}
	})
}

// The bounds on the generated document. See the note in the fuzz body: the size
// axis belongs to TestConversionIsLinearInSpanCount, and leaving it open here
// costs the whole time budget for one input.
const (
	maxRuns  = 64
	maxRunes = 512
)

// The mark plan's bits. Five of them, because spanMarks is link, strong, em and
// strike, and code is the fifth: it sits innermost and verbatim rather than in
// that list, which is exactly why it interacts with the others differently.
const (
	markStrong = 1 << iota
	markEm
	markStrike
	markCode
	markLink
)

// builtParagraph builds a one-paragraph document whose text is cut into runs,
// one per byte of the plan, each carrying the marks that byte names.
//
// It is total: every input produces a document Parse accepts. An empty plan or
// an empty text yields an empty paragraph, which is a document Jira stores and
// a shape worth converting.
func builtParagraph(text string, plan []byte) adf.Node {
	runs := splitRuns(text, len(plan))

	content := make([]adf.Node, 0, len(runs))
	for i, run := range runs {
		node := adf.Node{Type: "text", Text: run}
		if marks := marksFor(plan[i]); len(marks) > 0 {
			node.Marks = marks
		}
		content = append(content, node)
	}

	para := adf.Node{Type: "paragraph"}
	if len(content) > 0 {
		para.Content = content
	}
	return adf.Node{
		Type:    "doc",
		Version: adf.Version,
		Content: []adf.Node{para},
	}
}

// marksFor turns one plan byte into the marks it names, in a fixed order so the
// same byte always builds the same node.
//
// The order here is not spanMarks' order and does not need to be: the writer
// sorts what it is given, and a document whose marks arrive in an arbitrary
// order is exactly what Jira sends, because that order is an artefact of how
// the text was typed.
func marksFor(bits byte) []adf.Mark {
	var out []adf.Mark
	if bits&markLink != 0 {
		// A fixed href. Fuzzing the address is a different property, and one
		// FuzzToMarkdown already reaches through raw JSON.
		out = append(out, adf.Mark{
			Type:  "link",
			Attrs: map[string]any{"href": "https://example.invalid/a"},
		})
	}
	if bits&markStrong != 0 {
		out = append(out, adf.Mark{Type: "strong"})
	}
	if bits&markEm != 0 {
		out = append(out, adf.Mark{Type: "em"})
	}
	if bits&markStrike != 0 {
		out = append(out, adf.Mark{Type: "strike"})
	}
	if bits&markCode != 0 {
		out = append(out, adf.Mark{Type: "code"})
	}
	return out
}

// splitRuns cuts text into exactly n runs on rune boundaries, padding with
// empty runs when the text is shorter than the plan.
//
// Empty text nodes are deliberate rather than an accident of the arithmetic. A
// span carrying nothing is a document Jira stores, and it is what
// `*0**0*` came out of: the writer has to decide what a delimiter means with no
// content between two of them.
func splitRuns(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	runes := []rune(text)
	out := make([]string, 0, n)
	for i := range n {
		lo := i * len(runes) / n
		hi := (i + 1) * len(runes) / n
		out = append(out, string(runes[lo:hi]))
	}
	return out
}

// TestTheGeneratorReachesTheProperty is the number this target exists for, and
// it is asserted rather than measured once and believed.
//
// FuzzToMarkdown reaches ToMarkdown on 25 of its 639 corpus entries. A generator
// that drifted back toward that would leave a green fuzz target testing almost
// nothing, and nothing would say so, which is how the first one got there.
func TestTheGeneratorReachesTheProperty(t *testing.T) {
	plans := [][]byte{
		nil,
		{0},
		{markStrong},
		{markEm | markStrike, markStrike},
		{markCode | markStrong, markStrong, 0},
		{markStrong | markEm | markStrike | markCode | markLink},
		{255},
		{0, 0, 0, 0},
	}
	texts := []string{"", "a", "a.b", `\*0`, "0 0", strings.Repeat("x", 40)}

	built, reached := 0, 0
	for _, plan := range plans {
		for _, text := range texts {
			built++
			encoded, err := json.Marshal(builtParagraph(text, plan))
			if err != nil {
				t.Fatalf("a built document did not encode: %v", err)
			}
			if _, err := adf.Parse(encoded); err != nil {
				t.Errorf("Parse refused a generated document: %v\n%s", err, encoded)
				continue
			}
			reached++
		}
	}
	if reached != built {
		t.Errorf("%d of %d generated documents reach ToMarkdown; the generator "+
			"must be total, or this target becomes the one it replaced",
			reached, built)
	}
}

// markVerdicts pins, per text, how many of the 32 mark combinations the writer
// refuses. It is the going-quiet guard for this direction.
//
// The fuzz target above has the shape every target in this class has: a refusal
// carrying the right code is a pass, so a writer that refused everything would
// leave it permanently, silently green. `FuzzMarkdownRoundTrips` had that defect
// and two refusal regressions shipped under it, which is what
// markdown-verdicts.tsv exists for.
//
// This one needs no corpus file and no cache. The generator is total and
// deterministic, so the whole space of one-run mark combinations is 32 documents
// per text, enumerable here and reviewable in a diff.
var markVerdicts = []struct {
	text    string
	refused int
	why     string
}{
	{"a", 0, "nothing about a bare letter is unrepresentable under any mark"},
	{"a\nb", 0, "a newline inside a text node is a soft break, which markdown writes"},
	{"a`b", 0, "a backtick is escaped, or fenced by a longer run inside code"},
	{"a]b", 0, "a bracket is escaped; only a link address has nowhere to put one"},

	// The interesting row, and the reason both directions are pinned rather
	// than a single total. Text ending in a space has no markdown spelling,
	// because the reader trims it, so the writer refuses rather than dropping
	// it. Twenty-four of the thirty-two combinations convert anyway, and the
	// eight that do not are exactly the ones carrying neither `code` nor
	// `link`: backticks preserve a trailing space verbatim, and a link's text
	// sits inside brackets where the trim cannot reach it.
	{"a ", 8, "trailing space, representable only under code or link"},
	{" a", 8, "leading space, the same rule at the other end"},
}

// TestTheRefusedMarkCombinationsAreTheSameOnes fails when a refusal moves in
// either direction.
//
// Refusing more is the defect the fuzz target cannot see. Refusing less is a fix
// and a stale expectation, and the two are reported apart on purpose: reported
// together, the one that matters gets skimmed past.
func TestTheRefusedMarkCombinationsAreTheSameOnes(t *testing.T) {
	const combinations = 32

	for _, want := range markVerdicts {
		refused, converted := 0, 0
		for bits := range combinations {
			encoded, err := json.Marshal(builtParagraph(want.text, []byte{byte(bits)}))
			if err != nil {
				t.Fatalf("%q: encode: %v", want.text, err)
			}
			doc, err := adf.Parse(encoded)
			if err != nil {
				t.Fatalf("%q: the generator built a document Parse refuses: %v",
					want.text, err)
			}
			if _, err := adf.ToMarkdown(doc); err != nil {
				refused++
				continue
			}
			converted++
		}

		if refused+converted != combinations {
			t.Fatalf("%q: %d verdicts for %d combinations",
				want.text, refused+converted, combinations)
		}
		switch {
		case refused > want.refused:
			t.Errorf("%q: the writer now refuses %d of %d mark combinations and "+
				"refused %d; it converted these yesterday, and the fuzz target "+
				"cannot see a refusal spreading (%s)",
				want.text, refused, combinations, want.refused, want.why)
		case refused < want.refused:
			t.Errorf("%q: the writer now refuses %d of %d mark combinations and "+
				"refused %d. If that is a fix, update markVerdicts in the same "+
				"commit and say what became representable (%s)",
				want.text, refused, combinations, want.refused, want.why)
		}
	}
}
