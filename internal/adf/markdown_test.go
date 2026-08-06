package adf_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/adf"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
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

// quoteJSON is a JSON string literal for a test case, kept local so a case can
// be written as the text it is about.
func quoteJSON(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
