package adf_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/adf"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// roundTrip is the property the write subset is defined by: markdown that this
// package accepts converts to a document that converts back to the same
// markdown. Anything that does not is either a parser bug or a construct that
// should have been refused, and both are the same failure from a caller's
// side — a body that is not what they wrote.
func roundTrip(t *testing.T, markdown string) {
	t.Helper()

	doc, err := adf.FromMarkdown(markdown)
	if err != nil {
		t.Fatalf("FromMarkdown: %v", err)
	}
	// Through JSON, because that is how the document reaches Jira and comes
	// back — a shape that only survives in memory has not been tested.
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := adf.Parse(encoded)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, encoded)
	}
	got, err := adf.ToMarkdown(parsed)
	if err != nil {
		t.Fatalf("ToMarkdown: %v\n%s", err, encoded)
	}
	if got != markdown {
		t.Errorf("round trip changed the body\n--- sent ---\n%s\n--- got ---\n%s\n--- adf ---\n%s",
			markdown, got, encoded)
	}
}

func TestMarkdownSurvivesTheRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		markdown string
	}{
		{"a paragraph", "Just some text."},
		{"two paragraphs", "One.\n\nTwo."},
		{"a hard break", "One\\\ntwo."},
		{"every heading level", "# One\n\n## Two\n\n### Three\n\n#### Four\n\n##### Five\n\n###### Six"},
		{"emphasis", "*em* and **strong** and ***both***"},
		{"strikethrough", "~~gone~~ but not forgotten"},
		{"code span", "call `client.Do(req)` first"},
		{"a code span holding a backtick", "``a ` b``"},
		{"code inside strong", "**`x`**"},
		{"a link", "see [the docs](https://example.invalid/x)"},
		{"a link with a title", `see [the docs](https://example.invalid/x "Docs")`},
		{"a link whose address needs brackets", "[a](<https://example.invalid/a(b) c>)"},
		{"an autolink", "<https://example.invalid/x>"},
		{"a fenced code block", "```go\nclient.Do(req)\n```"},
		{"a code block with no language", "```\nplain\n```"},
		{"a code block holding a fence", "````\na ``` b\n````"},
		{"a rule", "one\n\n---\n\ntwo"},
		{"a blockquote", "> quoted\n>\n> twice"},
		{"a panel", "> [!WARNING]\n> mind the gap"},
		{"every panel type", "> [!INFO]\n> a\n\n> [!NOTE]\n> b\n\n> [!SUCCESS]\n> c\n\n> [!ERROR]\n> d\n\n> [!TIP]\n> e"},
		{"a bullet list", "- one\n- two"},
		{"an ordered list", "1. one\n2. two"},
		{"an ordered list starting elsewhere", "9. nine\n10. ten"},
		{"a nested list", "- outer\n\n  - inner"},
		{"a list holding a code block", "- item\n\n  ```go\n  x := 1\n  ```"},
		{"a task list", "- [x] done\n- [ ] todo"},
		{"a table", "| key | value |\n| --- | --- |\n| a | b |"},
		{"a table cell holding a pipe", "| jql |\n| --- |\n| a \\| b |"},
		{"a table cell holding a link", "| where |\n| --- |\n| [docs](https://example.invalid/x) |"},
		{"a mention", "thanks [@Ada Lovelace](jira-user:557058:abc)"},
		{"a status lozenge", "it is [Blocked](jira-status:red) on review"},
		{"a date", "due [2026-08-06](jira-date:1785974400000)"},
		{"an attachment", "![the crash](jira-media:c-1/uuid-1)"},
		{"an attachment with no collection", "![](jira-media:uuid-1)"},
		{"an external image", "![a](https://example.invalid/a.png)"},
		{"escaped punctuation stays text", `\*\*not bold\*\* and \[not a link\]`},
		{"an underscore inside a word", "customfield_10042 is the id"},
		{"a hash that is not a heading", `\# not a heading`},
		{"a number that is not a list", `1\. not a list`},
		{"an ampersand", `\&amp; stays four characters`},
		{"mixed blocks", "# Repro\n\n1. Start it\n2. Wait\n\n```go\nclient.Do(req)\n```\n\n> [!WARNING]\n> It hangs."},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { roundTrip(t, c.markdown) })
	}
}

// TestFromMarkdownRefusesWhatItCannotStore covers the other half. Every one of
// these has a plausible-looking wrong answer, and producing one is how the
// incumbent's markdown bugs happen.
func TestFromMarkdownRefusesWhatItCannotStore(t *testing.T) {
	cases := []struct {
		name     string
		markdown string
		says     string
	}{
		{"a setext heading", "Title\n=====", "setext heading"},
		{"a setext h2, which is also a rule", "Title\n-----", "setext heading"},
		{"an indented code block", "    x := 1", "indented code block"},
		{"an unclosed code fence", "```go\nx := 1", "never closed"},
		{"a lazily continued quote", "> quoted\nnot quoted", "no > in front"},
		{"a table with alignment", "| a |\n| :-- |\n| b |", "column alignment"},
		{"an image beside text", "see ![a](x.png) here", "beside other text"},
		{"emphasis around an image", "**![a](x.png)**", "emphasis around an image"},
		{"emphasis around a mention", "**[@Ada](jira-user:1)**", "emphasis around a mention"},
		{"an attachment written as a link", "[a](jira-media:uuid)", "written as a link"},
		{"a date that is not a stamp", "[x](jira-date:soon)", "stamped"},
		{"a task with two paragraphs", "- [ ] one\n\n  two\n", "more than one paragraph"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := adf.FromMarkdown(c.markdown)
			if err == nil {
				t.Fatal("the construct was converted rather than refused")
			}
			e := errs.Coerce(err)
			if e.Code != "MARKDOWN_UNSUPPORTED" {
				t.Errorf("code = %q, want MARKDOWN_UNSUPPORTED", e.Code)
			}
			if e.Exit != exitcode.Usage {
				t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
			}
			if !strings.Contains(e.Message, c.says) {
				t.Errorf("message %q does not name the construct (%q)", e.Message, c.says)
			}
			// Naming the line is the difference between a report somebody can
			// act on and one they have to bisect their own document to use.
			if !strings.HasPrefix(e.Detail, "line ") {
				t.Errorf("detail %q does not name a line", e.Detail)
			}
		})
	}
}

// TestFromMarkdownIsNotFromText pins the one place the two disagree, because
// the same string means different things through them and that is deliberate.
func TestFromMarkdownIsNotFromText(t *testing.T) {
	const input = "one\ntwo"

	text, err := adf.FromText(input)
	if err != nil {
		t.Fatalf("FromText: %v", err)
	}
	if len(text.Content[0].Content) != 3 || text.Content[0].Content[1].Type != "hardBreak" {
		t.Errorf("FromText should keep the line break: %+v", text.Content[0].Content)
	}

	parsed, err := adf.FromMarkdown(input)
	if err != nil {
		t.Fatalf("FromMarkdown: %v", err)
	}
	if got := parsed.Content[0].Content; len(got) != 1 || got[0].Text != "one two" {
		t.Errorf("FromMarkdown should join a soft break: %+v", got)
	}
}

// TestAnEmptyBodyIsStillADocument covers the degenerate case, so an empty
// --description reaches Jira as a document it rejects for the right reason.
func TestAnEmptyBodyIsStillADocument(t *testing.T) {
	doc, err := adf.FromMarkdown("")
	if err != nil {
		t.Fatalf("FromMarkdown: %v", err)
	}
	if doc.Type != "doc" || len(doc.Content) != 1 || doc.Content[0].Type != "paragraph" {
		t.Errorf("empty markdown produced %+v", doc)
	}
}
