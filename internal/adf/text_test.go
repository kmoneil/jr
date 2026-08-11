package adf_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

// TestFromTextContainsRatherThanConverts is the whole distinction. Cloud will
// not take a string where a document belongs, so the text is wrapped in one —
// and wrapping is exact, while interpreting would not be.
func TestFromTextContainsRatherThanConverts(t *testing.T) {
	doc, err := adf.FromText("**bold** and _em_ and `code`")
	if err != nil {
		t.Fatalf("FromText: %v", err)
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The characters survive verbatim...
	if !strings.Contains(string(encoded), `**bold** and _em_ and `+"`code`") {
		t.Errorf("the text was altered: %s", encoded)
	}
	// ...and nothing became a mark.
	for _, mark := range []string{"strong", "em\"", "code\"", "marks"} {
		if strings.Contains(string(encoded), mark) {
			t.Errorf("markdown was interpreted as %s: %s", mark, encoded)
		}
	}
}

// TestBlankLinesStartParagraphsAndSingleNewlinesDoNot covers the one structural
// decision. Collapsing a newline would join lines the caller separated;
// promoting it to a paragraph would add spacing they did not ask for.
func TestBlankLinesStartParagraphsAndSingleNewlinesDoNot(t *testing.T) {
	doc, err := adf.FromText("one\ntwo\n\nthree")
	if err != nil {
		t.Fatalf("FromText: %v", err)
	}

	if doc.Type != "doc" || doc.Version != adf.Version {
		t.Errorf("envelope = %+v", doc)
	}
	if len(doc.Content) != 2 {
		t.Fatalf("got %d paragraphs, want 2: %+v", len(doc.Content), doc.Content)
	}

	first := doc.Content[0]
	if len(first.Content) != 3 {
		t.Fatalf("first paragraph = %+v, want text, break, text", first.Content)
	}
	if first.Content[1].Type != "hardBreak" {
		t.Errorf("the single newline became %q, want hardBreak", first.Content[1].Type)
	}
	if first.Content[0].Text != "one" || first.Content[2].Text != "two" {
		t.Errorf("the lines were altered: %+v", first.Content)
	}
	if second := doc.Content[1]; len(second.Content) != 1 || second.Content[0].Text != "three" {
		t.Errorf("second paragraph = %+v", second.Content)
	}
}

// TestWindowsLineEndingsDoNotLeakIn covers a file written on Windows, which
// would otherwise leave a carriage return inside every line.
func TestWindowsLineEndingsDoNotLeakIn(t *testing.T) {
	doc, err := adf.FromText("one\r\ntwo")
	if err != nil {
		t.Fatalf("FromText: %v", err)
	}
	encoded, _ := json.Marshal(doc)
	if strings.Contains(string(encoded), `\r`) {
		t.Errorf("a carriage return survived: %s", encoded)
	}
}

// TestInvalidUTF8IsRefused covers the encoding rule. Substituting U+FFFD would
// put a character in Jira the caller never wrote, with no way to know.
func TestInvalidUTF8IsRefused(t *testing.T) {
	_, err := adf.FromText("valid \xff\xfe invalid")
	if err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "INVALID_ENCODING" {
		t.Errorf("code = %q, want INVALID_ENCODING", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
	}
}

// TestPlainTextIsLossyAndOnlyForDisplay pins what the extractor is for. It
// exists for a TSV cell, never for round-tripping — a value that went through
// it must not be sent back as if it were the original.
func TestPlainTextIsLossyAndOnlyForDisplay(t *testing.T) {
	doc := adf.Node{Type: "doc", Version: 1, Content: []adf.Node{
		{Type: "paragraph", Content: []adf.Node{
			{Type: "text", Text: "see "},
			{Type: "text", Text: "the docs"},
		}},
		{Type: "paragraph", Content: []adf.Node{{Type: "text", Text: "second"}}},
	}}

	got := adf.PlainText(doc)
	if got != "see the docs\n\nsecond" {
		t.Errorf("PlainText = %q", got)
	}

	// A round trip does not reproduce the input, which is why the doc comment
	// says so rather than leaving somebody to find out.
	back, err := adf.FromText(got)
	if err != nil {
		t.Fatalf("FromText: %v", err)
	}
	if len(back.Content[0].Content) == len(doc.Content[0].Content) {
		t.Skip("this input happens to survive; the guarantee is still not made")
	}
}

// TestEmptyTextIsStillADocument covers the degenerate case, so a caller who
// passes an empty body gets a well-formed document Jira will reject for the
// right reason rather than a malformed one it rejects for the wrong one.
func TestEmptyTextIsStillADocument(t *testing.T) {
	doc, err := adf.FromText("")
	if err != nil {
		t.Fatalf("FromText: %v", err)
	}
	if doc.Type != "doc" || len(doc.Content) != 1 {
		t.Errorf("empty text produced %+v", doc)
	}
	if len(doc.Content[0].Content) != 0 {
		t.Errorf("an empty paragraph carries %+v, want nothing", doc.Content[0].Content)
	}
}
