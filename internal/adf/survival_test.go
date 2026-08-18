package adf_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/kmoneil/jr/internal/adf"
)

// survivalGolden lists every input whose *document* does not survive being
// written and read back, and it is the third property over this corpus.
//
// The other two do not ask this question and cannot.
//
// FuzzMarkdownRoundTrips permits the first conversion to change the text, and
// the reason is real: emphasis has two spellings, so `_**x**_` and `***x***`
// are the same document written two ways and normalising between them is not a
// defect. But the allowance is not restricted to spelling. A first pass that
// moves a mark across a boundary, drops a node, or changes a table's shape
// converges just as readily as one that swaps delimiters, and the fuzzer
// asserts nothing until the second pass. Sixty-five inputs already in its own
// corpus lose something on that first pass, and it is green on all of them.
//
// TestTheRefusalSetIsPinned sees a document that stopped converting. It cannot
// see one that converts into something else.
//
// So this file is what a loss looks like in a diff. A line appearing is a
// document that used to survive and no longer does; a line disappearing is a
// fix. Regenerate with `make golden`.
const survivalGolden = "markdown-survives.tsv"

// projection reduces a document to what markdown is able to carry: which marks
// each non-whitespace character holds, in order, inside which blocks.
//
// This is the definition of "the same document" that the comparison needs, and
// it is deliberately not equality of the trees. Three differences are not
// losses and a tree comparison would report all three:
//
//   - Mark order is an artefact of how the text was typed.
//   - Adjacent text nodes carrying the same marks are one run of content.
//   - **A mark on whitespace cannot be written down.** renderSpan moves edge
//     whitespace outside its span on purpose, because markdown cannot
//     emphasise a space, so counting that as loss would be measuring the
//     design rather than a defect. Whitespace is projected with no marks.
//
// What is left is the thing a reader would notice: this character was bold and
// now it is not, this cell was here and now it is gone.
func projection(n adf.Node) string {
	var b strings.Builder
	project(n, &b)
	return b.String()
}

func project(n adf.Node, b *strings.Builder) {
	if n.Type == "text" {
		marks := markKey(n.Marks)
		for _, r := range n.Text {
			if unicode.IsSpace(r) {
				fmt.Fprintf(b, "%q\n", r)
				continue
			}
			fmt.Fprintf(b, "%q %s\n", r, marks)
		}
		return
	}
	if n.Type != "doc" {
		fmt.Fprintf(b, "<%s %s\n", n.Type, attrKey(n.Attrs))
	}
	for _, c := range n.Content {
		project(c, b)
	}
	if n.Type != "doc" {
		fmt.Fprintf(b, ">%s\n", n.Type)
	}
}

// markKey is a mark set in an order that does not depend on how it was typed.
func markKey(marks []adf.Mark) string {
	keys := make([]string, 0, len(marks))
	for _, m := range marks {
		raw, err := json.Marshal(m)
		if err != nil {
			keys = append(keys, m.Type)
			continue
		}
		keys = append(keys, string(raw))
	}
	sort.Strings(keys)
	// Joined on a separator JSON cannot contain, so lossClass can split the set
	// back apart. A comma is in every mark that carries attributes.
	return strings.Join(keys, "\x1f")
}

// attrKey is a node's attributes, minus the two that are not content.
//
// localId is editor state that FromMarkdown renumbers per document, and an
// attribute whose value is the empty string is how ADF spells absent in the
// places this converter round-trips through a URI.
func attrKey(attrs map[string]any) string {
	if len(attrs) == 0 {
		return ""
	}
	kept := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if k == "localId" || v == "" {
			continue
		}
		kept[k] = v
	}
	if len(kept) == 0 {
		return ""
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return fmt.Sprintf("%v", kept)
	}
	return string(raw)
}

// survives reports whether writing this document and reading it back gives the
// same document, and what changed when it does not.
func survives(doc adf.Node) (ok bool, class string) {
	md, err := adf.ToMarkdown(doc)
	if err != nil {
		// A document with no spelling is the refusal set's subject, not this
		// one's. Refusing is not losing.
		return true, ""
	}
	back, err := adf.FromMarkdown(md)
	if err != nil {
		return false, "unreadable"
	}
	was, now := projection(doc), projection(back)
	if was == now {
		return true, ""
	}
	return false, lossClass(was, now)
}

// lossClass names what differs, so a line in the golden says which kind of loss
// it is without anybody re-running the conversion.
//
// It is the set of node and mark types appearing on one side of the difference
// and not the other, which is coarse on purpose: the file is an index, and the
// diff of the projections is a thing the reader can regenerate.
func lossClass(was, now string) string {
	kinds := map[string]bool{}
	for _, line := range symmetricDifference(was, now) {
		switch {
		case strings.HasPrefix(line, "<"), strings.HasPrefix(line, ">"):
			name, _, _ := strings.Cut(strings.TrimLeft(line, "<>"), " ")
			kinds["node:"+name] = true
		default:
			_, marks, found := strings.Cut(line, " ")
			if !found || marks == "" {
				kinds["text"] = true
				continue
			}
			for _, m := range strings.Split(marks, "\x1f") {
				var parsed adf.Mark
				if err := json.Unmarshal([]byte(m), &parsed); err == nil {
					kinds["mark:"+parsed.Type] = true
					continue
				}
				kinds["mark"] = true
			}
		}
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "order"
	}
	return strings.Join(names, "+")
}

// symmetricDifference is the lines in one projection and not the other, counted
// so a line present twice on one side and once on the other still shows.
func symmetricDifference(was, now string) []string {
	count := map[string]int{}
	for _, line := range strings.Split(was, "\n") {
		count[line]++
	}
	for _, line := range strings.Split(now, "\n") {
		count[line]--
	}
	var out []string
	for line, n := range count {
		if n != 0 && line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

// survivalEntry is one input the writer loses something from, and what kind.
type survivalEntry struct {
	class string
	input string
}

// TestTheDocumentSurvivesTheWriter holds the loss set still.
//
// It says nothing about whether a loss is acceptable. Several in the file are
// deliberate — a leading hardBreak is trimmed, a mention gains the text its
// markdown spelling carries — and the file does not distinguish those from the
// ragged table that drops two cells of content. What it does is make either one
// visible the moment it changes, which is what nothing here could do.
func TestTheDocumentSurvivesTheWriter(t *testing.T) {
	inputs := loadVerdicts(t)
	if len(inputs) < 2000 {
		t.Fatalf("the verdict corpus holds %d inputs, too few to say anything "+
			"about a loss set", len(inputs))
	}

	var found []survivalEntry
	for _, e := range inputs {
		doc, err := adf.FromMarkdown(e.input)
		if err != nil {
			continue
		}
		ok, class := survives(doc)
		if !ok {
			found = append(found, survivalEntry{class: class, input: e.input})
		}
	}

	if *update {
		writeSurvivalGolden(t, found)
		return
	}

	want := loadSurvivalGolden(t)
	compareSurvival(t, want, found)
}

// documentedLosses are the documents Jira stores that this converter changes
// on purpose, each with the line of docs/output-contract.md that says so.
//
// The list is here rather than in a golden because five entries with a reason
// each is a thing somebody can read, and because the reason is the point: every
// one of these was a decision, and a decision that only exists as a row in a
// generated file is one the next person re-litigates.
//
// Nothing may be added to it without the sentence in the contract to match. An
// entry leaving it is a conversion that became exact, which is news.
var documentedLosses = map[string]string{
	"emoji": "an emoji becomes the character it stands for, so the node, its " +
		"shortName and its id are gone on the way back (output-contract.md: " +
		"\"An `emoji` becomes the character it stands for\")",
	"emoji with no character": "a custom emoji with no character becomes its " +
		"`:short-name:`, which reads back as those literal characters",
	"block card": "a blockCard becomes the bare URL it points at, which reads " +
		"back as an inlineCard inside a paragraph: block becomes inline",
	"embed card": "an embedCard becomes the bare URL too, and its layout is " +
		"presentation rather than content",
	"table with a short row": "a GFM table is rectangular, so a short row is " +
		"padded to the header width and reads back with the cells it gained. " +
		"The empty cells add nothing a reader sees; the opposite direction " +
		"drops content and is a-ragged-table-loses-its-extra-cells",
}

// TestEveryRealDocumentSurvivesTheWriter is the same property over documents
// Jira actually stored, held to the list above rather than to a golden.
//
// The adversarial corpus holds shapes nobody has typed. These are 247 documents
// a real server produced, so an unlisted loss here is a body somebody would read
// wrong, and it is the one place this package should be exact by default.
func TestEveryRealDocumentSurvivesTheWriter(t *testing.T) {
	var unexpected []string
	stillExact := map[string]bool{}
	for name := range documentedLosses {
		stillExact[name] = true
	}

	for _, e := range loadCorpus(t) {
		if len(e.ADF) == 0 {
			continue
		}
		doc, err := adf.Parse(e.ADF)
		if err != nil {
			continue
		}
		ok, class := survives(doc)
		if ok {
			continue
		}
		delete(stillExact, e.Name)
		if _, documented := documentedLosses[e.Name]; !documented {
			unexpected = append(unexpected, e.Name+" ("+class+")")
		}
	}
	sort.Strings(unexpected)

	if len(unexpected) > 0 {
		t.Errorf("%d document(s) Jira stored are changed by being written and "+
			"read back, and nothing says they should be:\n  %s\n"+
			"The round-trip fuzzer cannot see this: it permits the first "+
			"conversion to change the text and only asserts that the second "+
			"settles. If the change is deliberate, it needs a sentence in "+
			"docs/output-contract.md and a line in documentedLosses saying "+
			"which sentence.",
			len(unexpected), strings.Join(capped(unexpected, 10), "\n  "))
	}
	for name := range stillExact {
		t.Errorf("%q is listed in documentedLosses and now survives the "+
			"conversion exactly.\nThat is a fix: remove the entry, and check "+
			"whether docs/output-contract.md still describes the old behaviour.",
			name)
	}
}

func compareSurvival(t *testing.T, want, got []survivalEntry) {
	t.Helper()

	wantBy := make(map[string]string, len(want))
	for _, e := range want {
		wantBy[e.input] = e.class
	}
	gotBy := make(map[string]string, len(got))
	for _, e := range got {
		gotBy[e.input] = e.class
	}

	var newly, fixed, reclassified []string
	for _, e := range got {
		switch was, listed := wantBy[e.input]; {
		case !listed:
			newly = append(newly, e.class+"\t"+strconv.Quote(e.input))
		case was != e.class:
			reclassified = append(reclassified,
				strconv.Quote(e.input)+": "+was+" -> "+e.class)
		}
	}
	for _, e := range want {
		if _, still := gotBy[e.input]; !still {
			fixed = append(fixed, strconv.Quote(e.input))
		}
	}
	sort.Strings(newly)
	sort.Strings(fixed)
	sort.Strings(reclassified)

	// Three separate reports, because they are three different pieces of news
	// and the first is the only one that is bad.
	if len(newly) > 0 {
		t.Errorf("%d document(s) the writer used to carry are now changed by "+
			"it:\n  %s\n"+
			"Each is a body that goes into Jira saying one thing and comes out "+
			"saying another, and no other test in this package can see it. If "+
			"the change is intended, run `make golden` and say in the commit "+
			"message what markdown cannot hold.",
			len(newly), strings.Join(capped(newly, 10), "\n  "))
	}
	if len(fixed) > 0 {
		t.Errorf("%d document(s) that used to be changed now survive:\n  %s\n"+
			"That is a fix and the golden is stale. Run `make golden`, and the "+
			"diff is what the pull request reviews.",
			len(fixed), strings.Join(capped(fixed, 10), "\n  "))
	}
	if len(reclassified) > 0 {
		t.Errorf("%d document(s) lose something different than they did:\n  %s\n"+
			"The loss did not go away, it changed shape. Run `make golden` "+
			"once you know which of the two is the one you meant.",
			len(reclassified), strings.Join(capped(reclassified, 10), "\n  "))
	}
}

func loadSurvivalGolden(t *testing.T) []survivalEntry {
	t.Helper()

	path := filepath.Join("testdata", survivalGolden)
	f, err := os.Open(path) //nolint:gosec // a path from the test tree.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []survivalEntry
	scan := bufio.NewScanner(f)
	// A fuzz input can be longer than the default 64 KiB token.
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; scan.Scan(); line++ {
		text := scan.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		class, quoted, found := strings.Cut(text, "\t")
		if !found {
			t.Fatalf("%s:%d is not class<TAB>input", path, line)
		}
		input, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatalf("%s:%d input does not unquote: %v", path, line, err)
		}
		out = append(out, survivalEntry{class: class, input: input})
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

func writeSurvivalGolden(t *testing.T, entries []survivalEntry) {
	t.Helper()

	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.class+"\t"+strconv.Quote(e.input))
	}
	// Sorted on the quoted input, so a diff is only ever a line arriving,
	// leaving, or changing class.
	sort.Slice(lines, func(a, b int) bool {
		_, x, _ := strings.Cut(lines[a], "\t")
		_, y, _ := strings.Cut(lines[b], "\t")
		return x < y
	})

	path := filepath.Join("testdata", survivalGolden)
	body := survivalHeader + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const survivalHeader = `# Inputs whose document does not survive being written and read back.
#
# Generated by ` + "`make golden`" + ` from the inputs in markdown-verdicts.tsv.
# Only the losses are listed: an input arriving here used to convert without
# changing and no longer does, and one leaving is a fix.
#
# The class names what differs, as node and mark types. It is an index rather
# than a diagnosis: several of these are deliberate. See
# TestTheDocumentSurvivesTheWriter.
`
