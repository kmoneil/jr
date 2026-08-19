package adf_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

// convert parses a document and renders it, which is the path a description
// takes. Both halves are in the test so a change to either shows up here.
func convert(t *testing.T, doc string) (string, error) {
	t.Helper()
	parsed, err := adf.Parse([]byte(doc))
	if err != nil {
		return "", err
	}
	return adf.ToMarkdown(parsed)
}

// wrap puts content inside a minimal document, so a case can state the node it
// is about and nothing else.
func wrap(content string) string {
	return `{"type":"doc","version":1,"content":[` + content + `]}`
}

// para wraps inline content in a paragraph.
func para(content string) string {
	return wrap(`{"type":"paragraph","content":[` + content + `]}`)
}

func TestToMarkdownConvertsBlocks(t *testing.T) {
	cases := []struct {
		name string
		adf  string
		want string
	}{{
		name: "paragraphs are separated by a blank line",
		adf: wrap(`{"type":"paragraph","content":[{"type":"text","text":"one"}]},
			{"type":"paragraph","content":[{"type":"text","text":"two"}]}`),
		want: "one\n\ntwo",
	}, {
		name: "heading carries its level",
		adf:  wrap(`{"type":"heading","attrs":{"level":3},"content":[{"type":"text","text":"Repro"}]}`),
		want: "### Repro",
	}, {
		name: "hard break is a backslash, which survives whitespace trimming",
		adf:  para(`{"type":"text","text":"one"},{"type":"hardBreak"},{"type":"text","text":"two"}`),
		want: "one\\\ntwo",
	}, {
		name: "blockquote prefixes every line",
		adf: wrap(`{"type":"blockquote","content":[
			{"type":"paragraph","content":[{"type":"text","text":"one"}]},
			{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}`),
		want: "> one\n>\n> two",
	}, {
		name: "rule",
		adf:  wrap(`{"type":"rule"}`),
		want: "---",
	}, {
		name: "code block keeps its language and its text verbatim",
		adf: wrap(`{"type":"codeBlock","attrs":{"language":"go"},
			"content":[{"type":"text","text":"client.Do(req)  // **not** markdown"}]}`),
		want: "```go\nclient.Do(req)  // **not** markdown\n```",
	}, {
		name: "code block fences wider than the backticks inside it",
		adf: wrap(`{"type":"codeBlock","content":[
			{"type":"text","text":"a ` + "```" + ` b"}]}`),
		want: "````\na ``` b\n````",
	}, {
		name: "bullet list",
		adf: wrap(`{"type":"bulletList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]}`),
		want: "- one\n- two",
	}, {
		name: "ordered list starts where ADF says it does",
		adf: wrap(`{"type":"orderedList","attrs":{"order":9},"content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"nine"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"ten"}]}]}]}`),
		want: "9. nine\n10. ten",
	}, {
		name: "a nested list is indented under its item",
		adf: wrap(`{"type":"bulletList","content":[
			{"type":"listItem","content":[
				{"type":"paragraph","content":[{"type":"text","text":"outer"}]},
				{"type":"bulletList","content":[
					{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"inner"}]}]}]}]}]}`),
		want: "- outer\n\n  - inner",
	}, {
		name: "task list becomes checkboxes",
		adf: wrap(`{"type":"taskList","attrs":{"localId":"x"},"content":[
			{"type":"taskItem","attrs":{"localId":"a","state":"DONE"},"content":[{"type":"text","text":"done"}]},
			{"type":"taskItem","attrs":{"localId":"b","state":"TODO"},"content":[{"type":"text","text":"todo"}]}]}`),
		want: "- [x] done\n- [ ] todo",
	}, {
		name: "panel keeps its own type rather than being mapped onto GitHub's five",
		adf: wrap(`{"type":"panel","attrs":{"panelType":"success"},"content":[
			{"type":"paragraph","content":[{"type":"text","text":"shipped"}]}]}`),
		want: "> [!SUCCESS]\n> shipped",
	}, {
		name: "table with a header row",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"key"}]}]},
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"value"}]}]}]},
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}]}`),
		want: "| key | value |\n| --- | --- |\n| a | b |",
	}, {
		name: "a pipe inside a cell is escaped rather than closing it",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"jql"}]}]}]},
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a | b"}]}]}]}]}`),
		want: "| jql |\n| --- |\n| a \\| b |",
	}, {
		name: "a short row is padded to the header's width",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]},
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"only"}]}]}]}]}`),
		want: "| a | b |\n| --- | --- |\n| only |  |",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("got:\n%s\n\nwant:\n%s", got, c.want)
			}
		})
	}
}

func TestToMarkdownConvertsInline(t *testing.T) {
	cases := []struct {
		name string
		adf  string
		want string
	}{{
		name: "marks nest in a fixed order, and two nodes carrying the same set join",
		adf: para(`{"type":"text","text":"x","marks":[{"type":"em"},{"type":"strong"}]},
			{"type":"text","text":"y","marks":[{"type":"strong"},{"type":"em"}]}`),
		want: "***xy***",
	}, {
		name: "a different mark set does not join",
		adf: para(`{"type":"text","text":"x","marks":[{"type":"strong"}]},
			{"type":"text","text":"y"}`),
		want: "**x**y",
	}, {
		name: "two links to different places do not join",
		adf: para(`{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://a.invalid"}}]},
			{"type":"text","text":"b","marks":[{"type":"link","attrs":{"href":"https://b.invalid"}}]}`),
		want: "[a](https://a.invalid)[b](https://b.invalid)",
	}, {
		name: "strike",
		adf:  para(`{"type":"text","text":"gone","marks":[{"type":"strike"}]}`),
		want: "~~gone~~",
	}, {
		name: "code is verbatim inside the span, not escaped",
		adf:  para(`{"type":"text","text":"a*b","marks":[{"type":"code"}]}`),
		want: "`a*b`",
	}, {
		name: "a code span holding backticks widens its own fence",
		adf:  para(`{"type":"text","text":"a ` + "`" + ` b","marks":[{"type":"code"}]}`),
		want: "``a ` b``",
	}, {
		name: "code sits inside the other marks",
		adf:  para(`{"type":"text","text":"x","marks":[{"type":"code"},{"type":"strong"}]}`),
		want: "**`x`**",
	}, {
		name: "link",
		adf: para(`{"type":"text","text":"docs","marks":[
			{"type":"link","attrs":{"href":"https://example.invalid/x"}}]}`),
		want: "[docs](https://example.invalid/x)",
	}, {
		name: "a destination holding the bracket that would close it goes in angle brackets",
		adf: para(`{"type":"text","text":"docs","marks":[
			{"type":"link","attrs":{"href":"https://example.invalid/a(b) c"}}]}`),
		want: "[docs](<https://example.invalid/a(b) c>)",
	}, {
		name: "mention carries the id markdown has nowhere else to put",
		adf:  para(`{"type":"mention","attrs":{"id":"557058:abc","text":"@Ada"}}`),
		want: "[@Ada](jira-user:557058:abc)",
	}, {
		name: "status carries its colour",
		adf:  para(`{"type":"status","attrs":{"text":"Blocked","color":"red"}}`),
		want: "[Blocked](jira-status:red)",
	}, {
		name: "emoji is the character it stands for",
		adf:  para(`{"type":"emoji","attrs":{"shortName":":smile:","id":"1f604","text":"😄"}}`),
		want: "😄",
	}, {
		name: "a date chip shows the day and keeps the stamp",
		adf:  para(`{"type":"date","attrs":{"timestamp":"1785974400000"}}`),
		want: "[2026-08-06](jira-date:1785974400000)",
	}, {
		name: "a date with a time shows the time rather than rounding down",
		adf:  para(`{"type":"date","attrs":{"timestamp":"1785999600000"}}`),
		want: "[2026-08-06T07:00:00Z](jira-date:1785999600000)",
	}, {
		name: "an attachment keeps its media id",
		adf: wrap(`{"type":"mediaSingle","attrs":{"layout":"center","width":50},"content":[
			{"type":"media","attrs":{"id":"uuid-1","type":"file","collection":"c-1","alt":"the crash"}}]}`),
		want: "![the crash](jira-media:c-1/uuid-1)",
	}, {
		name: "an external image keeps its URL",
		adf: wrap(`{"type":"mediaSingle","attrs":{"layout":"center"},"content":[
			{"type":"media","attrs":{"type":"external","url":"https://example.invalid/a.png","alt":"a"}}]}`),
		want: "![a](https://example.invalid/a.png)",
	}, {
		name: "an inline card is its link",
		adf:  para(`{"type":"inlineCard","attrs":{"url":"https://example.invalid/x"}}`),
		want: "<https://example.invalid/x>",
	}, {
		name: "a card whose URL cannot be an autolink becomes an ordinary link",
		adf:  para(`{"type":"inlineCard","attrs":{"url":"https://example.invalid/a b"}}`),
		want: "[https://example.invalid/a b](<https://example.invalid/a b>)",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("got:\n%s\n\nwant:\n%s", got, c.want)
			}
		})
	}
}

// TestTextIsEscapedSoItReadsBackAsItself covers the half of the conversion that
// is invisible when it works. Markdown that came out of a Jira description has
// to mean what the description said, not what the punctuation in it spells.
func TestTextIsEscapedSoItReadsBackAsItself(t *testing.T) {
	cases := []struct{ text, want string }{
		{"**not bold**", `\*\*not bold\*\*`},
		{"a_b_c", "a_b_c"},        // intraword: inert in CommonMark
		{"_leading", `\_leading`}, // flanking: would open emphasis
		{"customfield_10042", "customfield_10042"},
		{"# not a heading", `\# not a heading`},
		{"mid # hash", "mid # hash"},
		{"1. not a list", `1\. not a list`},
		{"v1.2 is fine", "v1.2 is fine"},
		{"- not an item", `\- not an item`},
		// A pipe closes a cell only inside a table, and the cell escapes it
		// there. Escaping it everywhere would mark up every quoted JQL string.
		{"a | b", "a | b"},
		{"&amp;", `\&amp;`}, // an entity reference would resolve to "&"
		// The bracket is escaped, so the parentheses are already inert.
		{"[link](x)", `\[link\](x)`},
		{"a\n# second line", "a\n" + `\# second line`},
	}

	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			got, err := convert(t, para(`{"type":"text","text":`+quoteJSON(c.text)+`}`))
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestALinkTitleIsEscapedLikeAnyOtherText is the same rule as the test above,
// for the one piece of text that had its own escaper.
//
// A title escaped the backslash and the quote and nothing else: every character
// that can end the title early, and none of the ones that can end the
// *paragraph* early. A title may span lines, and the block parser reads a line
// inside one as a line: `"a\n> b"` puts a block quote under a paragraph, the
// paragraph comes apart, and the link is never built. The round-trip fuzzer
// found it as the writer refusing its own output, at 78 seconds, on
// `[0](0 "\r\\>\r")`.
//
// Jira Cloud stores a newline in a link title. Posted one to the sandbox and
// read it back: the document is one the server hands over, not only one a
// caller can construct, which is why this is escaped rather than refused.
func TestALinkTitleIsEscapedLikeAnyOtherText(t *testing.T) {
	cases := []struct{ name, title, want string }{
		{"a quote", `He said "no"`, `He said \"no\"`},
		{"a backslash", `a\b`, `a\\b`},
		{"a backslash in front of a quote", `a\"b`, `a\\\"b`},
		{"a line starting a block quote", "a\n> b", "a\n\\> b"},
		{"a line starting a heading", "a\n# b", "a\n\\# b"},
		{"a line starting an ordered list", "a\n1. b", "a\n1\\. b"},
		{"a line starting a fence", "a\n```", "a\n\\`\\`\\`"},
		// The ordinary case, which is what every title in the corpus looks
		// like. It has to come out untouched or the golden moves for nothing.
		{"nothing to escape", "Docs", "Docs"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := para(`{"type":"text","text":"x","marks":[{"type":"link","attrs":` +
				`{"href":"https://example.invalid/x","title":` + quoteJSON(c.title) + `}}]}`)
			got, err := convert(t, doc)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			want := `[x](https://example.invalid/x "` + c.want + `")`
			if got != want {
				t.Errorf("ToMarkdown\n got  %q\n want %q", got, want)
			}

			// The escaping is only right if it comes back as what went in.
			back, err := adf.FromMarkdown(got)
			if err != nil {
				t.Fatalf("this package cannot read its own output: %v\n%s", err, got)
			}
			marks := back.Content[0].Content[0].Marks
			if len(marks) != 1 || marks[0].Attrs["title"] != c.title {
				t.Errorf("title came back as %#v, want %q", marks, c.title)
			}
		})
	}
}

// TestALinkTitleHoldingABlankLineIsRefused is where escaping runs out.
//
// A backslash fixes a line that starts something. It cannot fix a line that is
// nothing: a blank line ends the paragraph, there is no character to escape,
// and CommonMark says in as many words that a title may not contain one. The
// round-trip fuzzer found it at 257 seconds, on a link whose title was two
// carriage returns, and it predates the escaping fix beside it.
func TestALinkTitleHoldingABlankLineIsRefused(t *testing.T) {
	for _, c := range []struct{ name, title string }{
		{"two newlines", "a\n\nb"},
		{"a line of spaces", "a\n   \nb"},
		{"nothing but newlines", "\n\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := para(`{"type":"text","text":"x","marks":[{"type":"link","attrs":` +
				`{"href":"https://example.invalid/x","title":` + quoteJSON(c.title) + `}}]}`)
			got, err := convert(t, doc)
			if err == nil {
				t.Fatalf("ToMarkdown = %q, want a refusal", got)
			}
			if code := errs.Coerce(err).Code; code != "ADF_UNREPRESENTABLE" {
				t.Errorf("code = %q, want ADF_UNREPRESENTABLE", code)
			}
			if says := errs.Coerce(err).Message; !strings.Contains(says, "blank line") {
				t.Errorf("message %q does not name the blank line", says)
			}
		})
	}
}

// A title that spans lines without a blank one is still written, which is the
// case the test above must not have swallowed.
func TestALinkTitleSpanningLinesIsStillWritten(t *testing.T) {
	doc := para(`{"type":"text","text":"x","marks":[{"type":"link","attrs":` +
		`{"href":"https://example.invalid/x","title":"a\nb"}}]}`)
	got, err := convert(t, doc)
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if want := "[x](https://example.invalid/x \"a\nb\")"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestUnrepresentableIsRefusedByName is the rule the card exists for. A
// construct markdown cannot hold is an error naming it, never a best effort
// that reads like the real thing.
func TestUnrepresentableIsRefusedByName(t *testing.T) {
	cases := []struct {
		name string
		adf  string
		says string
	}{{
		name: "underline",
		adf:  para(`{"type":"text","text":"x","marks":[{"type":"underline"}]}`),
		says: "underlined",
	}, {
		name: "coloured text",
		adf:  para(`{"type":"text","text":"x","marks":[{"type":"textColor","attrs":{"color":"#ff0000"}}]}`),
		says: "coloured",
	}, {
		name: "superscript",
		adf:  para(`{"type":"text","text":"x","marks":[{"type":"subsup","attrs":{"type":"sup"}}]}`),
		says: "superscript",
	}, {
		name: "a mark this converter has never heard of",
		adf:  para(`{"type":"text","text":"x","marks":[{"type":"sparkle"}]}`),
		says: `"sparkle" mark`,
	}, {
		name: "a two-column layout",
		adf:  wrap(`{"type":"layoutSection","content":[{"type":"layoutColumn","attrs":{"width":50},"content":[]}]}`),
		says: "multi-column",
	}, {
		name: "a collapsible section",
		adf:  wrap(`{"type":"expand","attrs":{"title":"more"},"content":[]}`),
		says: "collapsible",
	}, {
		name: "a merged table cell",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","attrs":{"colspan":2},"content":[
					{"type":"paragraph","content":[{"type":"text","text":"wide"}]}]}]}]}`),
		says: "spanning",
	}, {
		name: "a table cell holding a list",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[
					{"type":"bulletList","content":[{"type":"listItem","content":[]}]}]}]}]}`),
		says: "more than a single paragraph",
	}, {
		name: "a table with no header row",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]}]}]}`),
		says: "no header row",
	}, {
		name: "a table row wider than its header",
		adf: wrap(`{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"h"}]}]}]},
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}]}`),
		says: "under a header row of 1",
	}, {
		name: "a custom panel, whose colour is content",
		adf:  wrap(`{"type":"panel","attrs":{"panelType":"custom","panelColor":"#ff0000"},"content":[]}`),
		says: `"custom" panel`,
	}, {
		name: "a macro",
		adf:  wrap(`{"type":"extension","attrs":{"extensionKey":"k","extensionType":"t"},"content":[]}`),
		says: "macro",
	}, {
		name: "a node type this converter has never heard of",
		adf:  wrap(`{"type":"hologram"}`),
		says: `"hologram" node`,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := convert(t, c.adf)
			if err == nil {
				t.Fatal("the construct was converted rather than refused")
			}
			e := errs.Coerce(err)
			if e.Code != "ADF_UNREPRESENTABLE" {
				t.Errorf("code = %q, want ADF_UNREPRESENTABLE", e.Code)
			}
			if e.Exit != exitcode.Usage {
				t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
			}
			if !strings.Contains(e.Message, c.says) {
				t.Errorf("message %q does not name the construct (%q)", e.Message, c.says)
			}
			// Every refusal points at the flag that gives the caller the
			// document anyway. A refusal with no way forward is a dead end.
			if !strings.Contains(e.Remedy, "--raw-body") {
				t.Errorf("remedy %q does not offer --raw-body", e.Remedy)
			}
			// The path says where in the document to look, which is the
			// difference between a fixable report and a shrug.
			if !strings.HasPrefix(e.Detail, "at doc") {
				t.Errorf("detail %q does not locate the construct", e.Detail)
			}
		})
	}
}

// TestUnknownNodeFieldIsRefusedRatherThanDropped covers the schema-drift case.
// Ignoring a field Atlassian added would mean converting a document while
// silently leaving part of it out and calling the result the description.
func TestUnknownNodeFieldIsRefusedRatherThanDropped(t *testing.T) {
	_, err := convert(t, wrap(`{"type":"paragraph","sparkles":true,"content":[]}`))
	if err == nil {
		t.Fatal("an unmodelled field was ignored")
	}
	e := errs.Coerce(err)
	if e.Code != "MALFORMED_ADF" {
		t.Errorf("code = %q, want MALFORMED_ADF", e.Code)
	}
	if !strings.Contains(e.Remedy, "--raw-body") {
		t.Errorf("remedy %q does not offer --raw-body", e.Remedy)
	}
}

// TestParseRefusesWhatIsNotOneDocument covers the two shapes that are not a
// document at all, so neither reaches the converter as an empty one.
func TestParseRefusesWhatIsNotOneDocument(t *testing.T) {
	for _, raw := range []string{
		`{"type":"paragraph"}`,
		`{"type":"doc","version":1,"content":[]} {"type":"doc"}`,
		`"a wiki string"`,
	} {
		if _, err := adf.Parse([]byte(raw)); err == nil {
			t.Errorf("Parse(%s) was accepted", raw)
		}
	}
}

// nestedDocument builds a document `levels` paragraphs deep. Each level is an
// object inside an array, so the JSON nesting depth is about twice the count.
func nestedDocument(levels int) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"doc","version":1,"content":[`)
	for range levels {
		b.WriteString(`{"type":"paragraph","content":[`)
	}
	b.WriteString(`{"type":"text","text":"x"}`)
	for range levels {
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// nestedLists builds `levels` of bulletList inside listItem, which is a shape
// ADF genuinely permits and ToMarkdown therefore has to recurse through.
func nestedLists(levels int) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"doc","version":1,"content":[`)
	for range levels {
		b.WriteString(`{"type":"bulletList","content":[{"type":"listItem","content":[`)
	}
	b.WriteString(`{"type":"paragraph","content":[{"type":"text","text":"x"}]}`)
	for range levels {
		b.WriteString(`]}]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// TestParseBoundsNesting states a bound this package inherits rather than
// declares. Nothing here counts depth: what refuses a pathologically nested
// document is encoding/json's own limit, which is why the detail reads
// "exceeded max depth" in words this package did not write.
//
// That inheritance is the reason to pin it. Every other bound in this tool is
// stated — the 64MB response cap, --max-requests — and an undocumented one is
// a guarantee only until somebody changes the decoder. Both fuzz targets seed
// shallow documents, so neither reaches this.
//
// The two halves are equally load-bearing. The refusal is what makes the
// recursion below safe, so the deep-list case asserts the other direction: a
// document Parse accepts converts without exhausting the stack. If the bound
// ever moved up, this is what would notice.
func TestParseBoundsNesting(t *testing.T) {
	for _, levels := range []int{10_000, 100_000, 500_000} {
		_, err := adf.Parse(nestedDocument(levels))
		if err == nil {
			t.Errorf("a document %d deep was accepted", levels)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "MALFORMED_ADF" {
			t.Errorf("%d deep: code = %q, want MALFORMED_ADF", levels, e.Code)
		}
		if !strings.Contains(e.Detail, "max depth") {
			t.Errorf("%d deep: detail %q does not name the bound that fired", levels, e.Detail)
		}
	}

	// Just inside the bound, in the deepest shape ADF allows: four JSON
	// containers per level, so this is a little under the ceiling the loop
	// above is over.
	doc, err := adf.Parse(nestedLists(2400))
	if err != nil {
		t.Fatalf("a list 2400 deep was refused, so the bound is tighter than a document can be: %v", err)
	}
	markdown, err := adf.ToMarkdown(doc)
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if _, err := adf.FromMarkdown(markdown); err != nil {
		t.Fatalf("FromMarkdown: %v", err)
	}
}

// quoteJSON is a JSON string literal for a test case, kept local so a case can
// be written as the text it is about.
func quoteJSON(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// TestARowWiderThanItsHeaderIsRefusedRatherThanTruncated covers a silent loss
// that shipped, and the two halves of it that were hiding each other.
//
// `table` takes its width from the header row and `tableLine` writes exactly
// that many cells, so a body row holding more had every cell past the width
// dropped, with err nil and exit 0. tableLine's own doc comment reasons about
// the short row, which it pads; the long row went through the same loop and
// fell off the end of it.
//
// The parse side is the same rule from the other direction and needs no second
// check. FromMarkdown builds each row from its own pipe count with no reference
// to the header, so it will build two cells under a one-cell header, which GFM
// says is a one-column table whose second cell does not exist. Its closing
// self-check calls ToMarkdown, which is why one refusal covers both: the
// converter kept a cell GFM discards and then dropped it again on the way out,
// and the round trip came back looking clean.
//
// The short row is deliberately still accepted in both directions. Padding one
// adds empty cells and invents nothing a reader can see; dropping one loses
// what somebody typed.
func TestARowWiderThanItsHeaderIsRefusedRatherThanTruncated(t *testing.T) {
	const wide = `{"type":"table","content":[
		{"type":"tableRow","content":[
			{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"h"}]}]}]},
		{"type":"tableRow","content":[
			{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"kept"}]}]},
			{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"dropped"}]}]},
			{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"also"}]}]}]}]}`

	t.Run("the writer refuses it and names the row", func(t *testing.T) {
		got, err := convert(t, wrap(wide))
		if err == nil {
			t.Fatalf("a row of 3 cells under a header of 1 converted to %q, "+
				"which is two cells of content the reader never sees", got)
		}
		for _, want := range []string{"3 cells", "header row of 1", "tableRow 2"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q:\n%v", want, err)
			}
		}
	})

	t.Run("the markdown is refused by the same check", func(t *testing.T) {
		if _, err := adf.FromMarkdown("| a |\n| --- |\n| b | c |"); err == nil {
			t.Error("markdown holding a row wider than its header was accepted, " +
				"so a cell GFM discards went to Jira as content")
		}
	})

	t.Run("a short row is still written, padded", func(t *testing.T) {
		const short = `{"type":"table","content":[
			{"type":"tableRow","content":[
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
				{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]},
			{"type":"tableRow","content":[
				{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"only"}]}]}]}]}`
		got, err := convert(t, wrap(short))
		if err != nil {
			t.Fatalf("a short row was refused, which loses nothing and should "+
				"pad: %v", err)
		}
		if want := "| a | b |\n| --- | --- |\n| only |  |"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a rectangular table is untouched", func(t *testing.T) {
		if _, err := adf.FromMarkdown("| a | b |\n| --- | --- |\n| c | d |"); err != nil {
			t.Errorf("an ordinary table was refused: %v", err)
		}
	})
}

// TestACodeFenceIsSeparatedFromItsLanguage is the nightly target's find on
// `~~~ ~`+"`"+`000000\r~~~`, and it is a defect in the writer rather than in the
// reader that reported it.
//
// The opening line of a fenced block is a run of the fence character and then
// the info string, so a language beginning with that character is part of the
// fence. `~~~` in front of a language of "~x" is a fence of four and a language
// of "x": one character of content gone, and a closing fence of three that no
// longer closes anything. The document came back as "a code fence that is never
// closed", pointing at a line nobody wrote.
//
// The tilde only becomes the fence character when the language holds a backtick,
// which is why the first two cases are here: they are the same language shape
// against a backtick fence, where nothing runs together and nothing may change.
func TestACodeFenceIsSeparatedFromItsLanguage(t *testing.T) {
	for _, c := range []struct{ name, lang, want string }{
		{"an ordinary language is untouched", "go", "```go\nx\n```"},
		{"a tilde is not the fence character here", "~tilde", "```~tilde\nx\n```"},
		{"a backtick sends the fence to tildes", "`tick", "~~~`tick\nx\n~~~"},
		{"and then a leading tilde needs the gap", "~`both", "~~~ ~`both\nx\n~~~"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := wrap(`{"type":"codeBlock","attrs":{"language":` + quoteJSON(c.lang) +
				`},"content":[{"type":"text","text":"x"}]}`)
			got, err := convert(t, doc)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("ToMarkdown = %q, want %q", got, c.want)
			}
			back, err := adf.FromMarkdown(got)
			if err != nil {
				t.Fatalf("this package cannot read its own output: %v", err)
			}
			if lang, _ := back.Content[0].Attrs["language"].(string); lang != c.lang {
				t.Errorf("the language came back as %q, want %q", lang, c.lang)
			}
		})
	}
}
