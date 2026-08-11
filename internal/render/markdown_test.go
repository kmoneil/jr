//go:build render

package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/render"
)

func recordDoc() *render.Doc {
	return render.Record("issue.get", 1,
		render.El("issue").Attr("key", "ENG-101").
			Leaf("summary", "Login times out on SSO").
			Child(render.El("assignee").Attr("id", "712020").Leaf("display", "Ada Lovelace")).
			Child(render.El("description").SetCDATA("## Repro\n\nSteps with **bold**.")))
}

func collectionDoc(complete bool, token string) *render.Doc {
	return render.List("issue.list", 1, &render.Collection{
		Name: "issues", Complete: complete, NextPageToken: token,
		Items: []*render.Node{
			render.El("issue").Attr("key", "ENG-1").Leaf("summary", "First"),
		},
		Columns: []render.Column{
			{Header: "key", Path: "@key"},
			{Header: "summary", Path: "summary"},
		},
	})
}

func markdownOf(t *testing.T, d *render.Doc) string {
	t.Helper()
	var out strings.Builder
	if err := render.Write(&out, d, render.Markdown); err != nil {
		t.Fatalf("write: %v", err)
	}
	return out.String()
}

// TestMarkdownIsAvailableOnlyWithTheTag is one half of the pair. The other is
// in markdown_absent_test.go, which builds when this file does not.
func TestMarkdownIsAvailableOnlyWithTheTag(t *testing.T) {
	got, err := render.ParseFormat("markdown")
	if err != nil {
		t.Fatalf("a render build does not accept markdown: %v", err)
	}
	if got != render.Markdown {
		t.Errorf("ParseFormat = %q", got)
	}
	if !strings.Contains(strings.Join(render.FormatNames(), ","), "markdown") {
		t.Errorf("markdown is missing from FormatNames: %v", render.FormatNames())
	}
}

// TestMarkdownIsNeverADefault is the containment on an unversioned format. It
// has no stability promise, so nothing may receive it without asking.
func TestMarkdownIsNeverADefault(t *testing.T) {
	if got := render.DefaultFor(recordDoc()); got == render.Markdown {
		t.Error("a record defaults to markdown")
	}
	if got := render.DefaultFor(collectionDoc(true, "")); got == render.Markdown {
		t.Error("a collection defaults to markdown")
	}
}

// TestMarkdownRecordReadsAsADocument covers the shape a person sees.
func TestMarkdownRecordReadsAsADocument(t *testing.T) {
	got := markdownOf(t, recordDoc())

	for _, want := range []string{
		"# issue ENG-101",
		"| Field | Value |",
		"| summary | Login times out on SSO |",
		"## assignee 712020",
		"## description",
		// The description is already markdown, so it goes out verbatim: a
		// reader wants the formatting, not the backslashes that would preserve
		// its source.
		"Steps with **bold**.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

// TestMarkdownStatesTruncationInWords is the one thing a collection must never
// under-report. `complete="false"` is an attribute a reader skimming a table
// cannot see, so this format spells it out.
func TestMarkdownStatesTruncationInWords(t *testing.T) {
	truncated := markdownOf(t, collectionDoc(false, "cursor-42"))
	if !strings.Contains(truncated, "TRUNCATED") {
		t.Errorf("a truncated result does not say so:\n%s", truncated)
	}
	if !strings.Contains(truncated, "--page-token cursor-42") {
		t.Errorf("a truncated result does not say how to resume:\n%s", truncated)
	}

	complete := markdownOf(t, collectionDoc(true, ""))
	if strings.Contains(complete, "TRUNCATED") {
		t.Errorf("a complete result claims truncation:\n%s", complete)
	}
	if !strings.Contains(complete, "complete") {
		t.Errorf("a complete result does not say so:\n%s", complete)
	}
}

// TestMarkdownCellsCannotRestructureTheTable is the escaping rule. A pipe ends
// a cell and a newline ends a row, so an unescaped one silently produces a
// table with different columns than the data has — the same defect an
// unescaped tab is in TSV.
func TestMarkdownCellsCannotRestructureTheTable(t *testing.T) {
	doc := render.List("issue.list", 1, &render.Collection{
		Name: "issues", Complete: true,
		Items: []*render.Node{
			render.El("issue").Attr("key", "ENG-1").
				Leaf("summary", "a | b\nsecond line"),
		},
		Columns: []render.Column{
			{Header: "key", Path: "@key"},
			{Header: "summary", Path: "summary"},
		},
	})

	got := markdownOf(t, doc)
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		// Two columns means three unescaped pipes and no more.
		if n := strings.Count(line, "|") - strings.Count(line, `\|`); n != 3 {
			t.Errorf("row has %d structural pipes, want 3: %q", n, line)
		}
	}
	if strings.Contains(got, "a | b") {
		t.Errorf("an unescaped pipe reached a cell:\n%s", got)
	}
}

// TestMarkdownEmptyCollectionSaysSo covers the case a heading and a count with
// no table under it would leave looking like a rendering bug.
func TestMarkdownEmptyCollectionSaysSo(t *testing.T) {
	doc := render.List("issue.list", 1, &render.Collection{
		Name: "issues", Complete: true, Items: nil,
		Columns: []render.Column{{Header: "key", Path: "@key"}},
	})
	if got := markdownOf(t, doc); !strings.Contains(got, "No rows") {
		t.Errorf("an empty collection renders as nothing:\n%s", got)
	}
}

// TestMarkdownRendersDiagnostics stops it being half a format. A caller who
// passed --format markdown and hit a 404 is owed an error in the format they
// asked for.
func TestMarkdownRendersDiagnostics(t *testing.T) {
	var out strings.Builder
	e := errs.NotFound("NO_ISSUE", "ENG-999 does not exist").
		WithRemedy("check the key")
	if err := render.WriteError(&out, e, render.Markdown); err != nil {
		t.Fatalf("write error: %v", err)
	}
	for _, want := range []string{"NO_ISSUE", "ENG-999 does not exist", "check the key"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the diagnostic is missing %q:\n%s", want, out.String())
		}
	}
}

// TestAListItemKeepsItsLabelAndItsText covers the node shape the goldens did
// not contain: a list item with attributes *and* text.
//
// Found by running `jr version --format markdown`, not by a test. The version
// document lists eight tags as `<tag name="tui">Interactive terminal UI</tag>`,
// and the first version of this writer sent anything with attributes to the
// record renderer — which produced eight headings each holding a one-row table,
// and dropped every description, because the description is the node's text and
// that path rendered only attributes.
func TestAListItemKeepsItsLabelAndItsText(t *testing.T) {
	doc := render.Record("version", 1,
		render.El("version").Attr("app", "jr").
			Child(render.ListEl("tags", "tag",
				render.El("tag").Attr("name", "write").SetText("All mutating commands"),
				render.El("tag").Attr("name", "mcp").SetText("MCP server"))))

	got := markdownOf(t, doc)
	for _, want := range []string{
		"- **write** All mutating commands",
		"- **mcp** MCP server",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "### tag") {
		t.Errorf("a one-line list item became a section:\n%s", got)
	}
}

// TestARecordKeepsItsOwnText is the same defect at the other end: a node's text
// is not one of its children and was rendered by nothing.
func TestARecordKeepsItsOwnText(t *testing.T) {
	doc := render.Record("thing.get", 1,
		render.El("thing").Attr("id", "7").SetText("the text of the thing itself"))

	if got := markdownOf(t, doc); !strings.Contains(got, "the text of the thing itself") {
		t.Errorf("a record's own text was dropped:\n%s", got)
	}
}

// TestACollectionOfDocumentsRendersAsSections is the fix for a comment thread
// arriving in one table cell.
//
// The escaping was right — a raw newline ends the row and a raw pipe ends the
// cell — and the result was still unreadable, because the assumption one layer
// up was that rows are short. Every collection this writer was tested against
// had short cells; `issue comment list` is the first whose rows are prose.
func TestACollectionOfDocumentsRendersAsSections(t *testing.T) {
	body := "Reproduced on 9.4.\n\n```\nGET /rest/api/2/issue/ENG-1\n```\n\nThe **retry** loop swallows it."
	doc := render.List("issue.comment.list", 1, &render.Collection{
		Name: "comments", Complete: true,
		Items: []*render.Node{
			render.El("comment").Attr("id", "10001").
				Child(render.El("author").Attr("display", "Ada Lovelace")).
				Child(render.El("body").SetCDATA(body)),
		},
		Columns: []render.Column{
			{Header: "id", Path: "@id"},
			{Header: "body", Path: "body"},
		},
	})

	got := markdownOf(t, doc)
	if strings.Contains(got, "<br>") {
		t.Errorf("a document was flattened into a table cell:\n%s", got)
	}
	for _, want := range []string{
		"## comment 10001",
		"### body",
		// The fence survives, which is the entire point.
		"```\nGET /rest/api/2/issue/ENG-1\n```",
		// A child with attributes and no text is its attributes.
		"| author | Ada Lovelace |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	// The collection's own facts survive the change of shape.
	if !strings.Contains(got, "# comments") || !strings.Contains(got, "complete") {
		t.Errorf("the collection lost its heading or its count:\n%s", got)
	}
}

// TestACollectionWithoutDocumentsStaysATable is the direction that would break
// quietly. A rule that turned every collection into sections would pass the
// test above and make `issue list` unreadable for the opposite reason.
func TestACollectionWithoutDocumentsStaysATable(t *testing.T) {
	got := markdownOf(t, collectionDoc(true, ""))
	if !strings.Contains(got, "| key | summary |") {
		t.Errorf("a collection of short rows stopped being a table:\n%s", got)
	}
	if strings.Contains(got, "## issue") {
		t.Errorf("a collection of short rows became sections:\n%s", got)
	}
}
