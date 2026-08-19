package adf_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
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
		{"a link", "see [the docs](https://example.invalid/x)"},
		{"a link with a title", `see [the docs](https://example.invalid/x "Docs")`},
		// Jira stores `code` beside a `link` and beside no other mark, which
		// two corpus entries show and a probe against the sandbox confirmed.
		// These were the only documents this tool could write and not read.
		{"a code span inside a link", "[`x`](https://example.invalid/a)"},
		{"a code span inside a link with a title", `[` + "`x`" + `](https://example.invalid/a "T")`},
		{"a code span as part of a link's text", "see [the `code` here](https://example.invalid/a)"},
		// The escape is the whole case: a title holding the quote that would
		// otherwise close it has to come back out as one character, not two.
		{"a link title holding a quote", `see [the docs](https://example.invalid/x "The \"Docs\"")`},
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
		// The escape is the case again, and the whitespace in front of it is
		// why: a line start is where a block can begin, and the read side finds
		// that start after whitespace markdown itself does not strip. Without
		// the escape these are a setext heading and a rule.
		// The span that reaches furthest is the one that opens, so the emphasis
		// over the whole phrase stays one span rather than becoming three with
		// the spaces falling out from between them.
		{"emphasis over a phrase with strong on each word", "_**one** **two** **three**_"},
		// A nested span flush against its parent's close. The writer spells the
		// outer mark with underscores so the two delimiters cannot merge into
		// one run; the reader takes a run apart whichever way it is written.
		{"emphasis inside strong, flush against the close", "__bold *and italic*__"},
		{"strong inside emphasis, flush against the close", "_italic **and bold**_"},
		// The writer spells the outer mark with underscores wherever its
		// content holds a live asterisk, so the run it closes with is its own.
		// A reader takes `**0*0***` apart correctly and this package will not
		// write it: the conservatism costs a spelling and never a document.
		{"a run that closes an emphasis and a strong", "0 __0*0*__ _0_**0**"},
		{"a setext underline behind a vertical tab", "0\\\n\v\\="},
		{"a setext underline behind a non-breaking space", "0\\\n\u00a0\\="},
		{"a rule behind a vertical tab", "0\\\n\v\\---"},
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

		// Jira's own content model, established by posting each of these to
		// the sandbox rather than read off the ADF documentation. Its refusal
		// is "INVALID_INPUT; comment: INVALID_INPUT", which names nothing.
		// The mark is named rather than called emphasis, because one of these
		// is not emphasis and the message used to say it was. That wording is
		// how a link on inline code went unnoticed: it is refused by a check
		// about emphasis, on a document with no emphasis in it.
		{"bold code", "**`x`**", "strong on inline code"},
		{"italic code", "*`x`*", "em on inline code"},
		{"struck code", "~~`x`~~", "strike on inline code"},
		{"a quote inside a quote", "> > deep", "a blockquote inside a blockquote"},
		{"a panel inside a quote", "> > [!INFO]\n> > x", "a panel inside a blockquote"},
		{"a table inside a quote", "> | a |\n> | --- |", "a table inside a blockquote"},
		{"a heading inside a quote", "> # x", "a heading inside a blockquote"},
		{"a rule inside a quote", "> ---", "a rule inside a blockquote"},
		{"a task list inside a quote", "> - [ ] x", "a task list inside a blockquote"},
		{"a table inside a panel", "> [!INFO]\n> | a |\n> | --- |", "a table inside a panel"},
		{"a quote inside a panel", "> [!INFO]\n> > x", "a blockquote inside a panel"},
		{"a panel inside a panel", "> [!INFO]\n> > [!NOTE]\n> > x", "a panel inside a panel"},
		{"a table inside a list item", "- x\n\n  | a |\n  | --- |", "a table inside a list item"},
		{"a rule inside a list item", "- x\n\n  ---", "a rule inside a list item"},
		{"a heading inside a list item", "- # x", "a heading inside a list item"},
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

// linkHref pulls the address off the first link mark in a document.
func linkHref(doc adf.Node) (string, bool) {
	for _, block := range doc.Content {
		for _, inline := range block.Content {
			for _, mark := range inline.Marks {
				if mark.Type != "link" {
					continue
				}
				href, ok := mark.Attrs["href"].(string)
				return href, ok
			}
		}
	}
	return "", false
}

// TestALinkAddressSurvivesEveryByte holds the two halves of the destination
// scanner to one subset. linkTarget decides which addresses go inside angle
// brackets on the way out; scanBareTarget decides where an unbracketed one
// ends on the way back in — at a space, a newline, or a `)` that nothing in
// the address opened. If those two ever disagree about a byte, an address goes
// out bare and comes back truncated, which is a link to somewhere else
// reported as the description.
//
// Sweeping the bytes is the point rather than listing the interesting ones.
// Tab is the case that prompted this: it is not a terminator here, which
// diverges from CommonMark and is safe only because linkTarget's set includes
// it — reasoning that holds today and is one edit from not holding. A list
// written by hand is a list of the bytes somebody already thought of.
func TestALinkAddressSurvivesEveryByte(t *testing.T) {
	for b := range 0x80 {
		if b == '\n' || b == '\r' {
			// linkTarget refuses a line ending outright: it is not an address,
			// and percent-encoding it is one-way.
			continue
		}
		href := "https://example.invalid/a" + string(rune(b)) + "b"
		quoted, err := json.Marshal(href)
		if err != nil {
			t.Fatalf("byte %#02x: marshal: %v", b, err)
		}
		doc := para(`{"type":"text","text":"x","marks":[{"type":"link","attrs":{"href":` +
			string(quoted) + `}}]}`)

		markdown, err := convert(t, doc)
		if err != nil {
			t.Errorf("byte %#02x: ToMarkdown: %v", b, err)
			continue
		}
		back, err := adf.FromMarkdown(markdown)
		if err != nil {
			t.Errorf("byte %#02x: FromMarkdown(%q): %v", b, markdown, err)
			continue
		}
		got, ok := linkHref(back)
		if !ok {
			t.Errorf("byte %#02x: %q did not come back as a link", b, markdown)
			continue
		}
		if got != href {
			t.Errorf("byte %#02x: the address changed\n sent %q\n  got %q\n  via %q",
				b, href, got, markdown)
		}
	}
}

// TestAMalformedDestinationIsNotAShortLink covers the refusals in each of the
// three destination scans. They matter more than they look: every one of them
// is a point where the scanner has already read a prefix of something that
// could have been an address, and the wrong answer is not an error but a link
// to that prefix. `[x](a b)` becoming a link to `a` is the failure this whole
// file exists to prevent, arriving through the parser instead of the renderer.
//
// A refused construct stays literal text, which is what CommonMark does too.
func TestAMalformedDestinationIsNotAShortLink(t *testing.T) {
	for _, markdown := range []string{
		"[x]",                            // no destination at all
		"[x](",                           // an unterminated bare address
		"[x](https://example.invalid/a",  // the same, having read a whole URL
		"[x](<https://example.invalid/a", // an unterminated angle address
		"[x](<a\nb>)",                    // a line ending inside the angle form
		"[x](a b)",                       // a second word that is not a title
		"[x](a 'unterminated)",           // a title whose quote never closes
		`[x](a "title" trailing)`,        // a title with something after it
		"[x](a(b)",                       // a `(` the closing paren balances
	} {
		doc, err := adf.FromMarkdown(markdown)
		if err != nil {
			t.Errorf("FromMarkdown(%q): %v", markdown, err)
			continue
		}
		if href, ok := linkHref(doc); ok {
			t.Errorf("FromMarkdown(%q) built a link to %q", markdown, href)
		}
	}
}

// TestWhitespaceMarkdownDoesNotCountSurvives is the Jira to markdown to Jira
// path for characters CommonMark does not call whitespace and Unicode does.
//
// The reader decided block structure with strings.TrimSpace, which trims the
// vertical tab, the form feed, NEL and the non-breaking space. A heading ending
// in a non-breaking space came back one character shorter, a table cell did
// too, and a line holding only one read as blank and split a paragraph in two.
// Silently, with no refusal and no warning, which is the one thing this package
// is not allowed to do to a value.
//
// Jira keeps the character: a heading ending in U+00A0 posted to the Cloud
// sandbox on 2026-08-16 came back with it. So the tool was dropping something
// the server stores, on the path a person performs by reading an issue, editing
// the body, and sending it back.
func TestWhitespaceMarkdownDoesNotCountSurvives(t *testing.T) {
	// The JSON escape rather than the character, because a raw vertical tab is
	// a control character and JSON forbids one inside a string literal. The
	// non-breaking space is not a control character and either form parses.
	const (
		nbsp = `\u00a0`
		vt   = `\u000b`
	)
	cases := []struct{ name, adf, want string }{{
		name: "a heading ending in a non-breaking space",
		adf:  wrap(`{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"x` + nbsp + `"}]}`),
		want: "x\u00a0",
	}, {
		name: "a heading ending in a vertical tab",
		adf:  wrap(`{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"x` + vt + `"}]}`),
		want: "x\v",
	}, {
		name: "a paragraph ending in a non-breaking space",
		adf:  para(`{"type":"text","text":"x` + nbsp + `"}`),
		want: "x\u00a0",
	}, {
		name: "a table cell ending in a non-breaking space",
		adf: wrap(`{"type":"table","content":[{"type":"tableRow","content":[` +
			`{"type":"tableHeader","content":[{"type":"paragraph","content":[` +
			`{"type":"text","text":"a` + nbsp + `"}]}]}]}]}`),
		want: "a\u00a0",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			markdown, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			back, err := adf.FromMarkdown(markdown)
			if err != nil {
				t.Fatalf("this package cannot read its own output: %v\n%q", err, markdown)
			}
			// Compared as documents, because PlainText is deliberately lossy
			// and trims the very character this is about.
			parsed, err := adf.Parse([]byte(c.adf))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			want, _ := json.Marshal(parsed)
			got, _ := json.Marshal(back)
			if string(got) != string(want) {
				t.Errorf("the round trip changed the document\n wrote %q\n sent %s\n back %s\n (the character at stake is %q)",
					markdown, want, got, c.want)
			}
		})
	}
}

// A line holding nothing but a character markdown does not count as whitespace
// is not a blank line, so it does not end a paragraph.
func TestALineOfNonMarkdownWhitespaceIsNotBlank(t *testing.T) {
	doc, err := adf.FromMarkdown("a\n\u00a0\nb")
	if err != nil {
		t.Fatalf("FromMarkdown: %v", err)
	}
	if n := len(doc.Content); n != 1 {
		t.Errorf("built %d blocks, want 1: a non-breaking space does not make a line blank", n)
	}
}

// TestTheTextIsAFixedPoint is the writer's stability contract, and it is the
// nightly sweep's second find of 2026-08-19.
//
// One conversion was not a fixed point. A mark on whitespace is dropped when
// that whitespace lands at the edge of a span, which is deliberate and
// documented; which span an edge belongs to is decided while writing, and two
// mark runs that overlap without nesting force a cut that can leave a marked
// space at the head of what is left. Only one such space lands there per
// conversion, so a document with two of them took three conversions to stop
// moving. A body read out of `issue get` was not the body you got by piping it
// back in, and nothing said which of the two was the answer.
//
// The last case is the guard rather than the fix, and it is why settling is
// anchored on the document. Settling looked free on every corpus this package
// has: six texts move and none loses a mark. Those corpora are markdown-shaped,
// and an ADF text node holding a newline is not. The newline is written, the
// reader joins the lines with a space because that is what a soft break is, and
// an unanchored settle adopted the join and lost the newline. So a conversion
// is settled through only when what it reads back is the document it was given.
func TestTheTextIsAFixedPoint(t *testing.T) {
	t.Run("markdown that took three conversions", func(t *testing.T) {
		const src = "__!_____!__ __!_____!_____!__ __0___"
		doc, err := adf.FromMarkdown(src)
		if err != nil {
			t.Fatalf("FromMarkdown: %v", err)
		}
		once, err := adf.ToMarkdown(doc)
		if err != nil {
			t.Fatalf("ToMarkdown: %v", err)
		}
		again, err := adf.FromMarkdown(once)
		if err != nil {
			t.Fatalf("this package cannot read its own output: %v", err)
		}
		twice, err := adf.ToMarkdown(again)
		if err != nil {
			t.Fatalf("ToMarkdown: %v", err)
		}
		if twice != once {
			t.Errorf("the text moved on the conversion after the first"+
				"\n--- was ---\n%s\n--- now ---\n%s", once, twice)
		}
	})

	t.Run("a marked space between overlapping runs", func(t *testing.T) {
		// strong over the first two nodes and em over the last three, which
		// markdown cannot nest. Built by hand because the cut that strands the
		// mark is the writer's, and no markdown asks for it directly.
		doc := para(`{"type":"text","text":"0","marks":[{"type":"strong"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"strong"},{"type":"em"}]},` +
			`{"type":"text","text":" ","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"}]}`)
		once, err := convert(t, doc)
		if err != nil {
			t.Fatalf("ToMarkdown: %v", err)
		}
		back, err := adf.FromMarkdown(once)
		if err != nil {
			t.Fatalf("this package cannot read its own output: %v", err)
		}
		twice, err := adf.ToMarkdown(back)
		if err != nil {
			t.Fatalf("ToMarkdown: %v", err)
		}
		if twice != once {
			t.Errorf("the text moved on the conversion after the first"+
				"\n--- was ---\n%q\n--- now ---\n%q", once, twice)
		}
	})

	t.Run("settling never costs the document a character", func(t *testing.T) {
		// The newline is content. Reading this text back joins the lines with a
		// space, so the document it reads back as is not the one it was given,
		// and the first version stands.
		doc := para(`{"type":"text","text":"a\n# second line"}`)
		got, err := convert(t, doc)
		if err != nil {
			t.Fatalf("ToMarkdown: %v", err)
		}
		if want := "a\n" + `\# second line`; got != want {
			t.Errorf("got %q, want %q; settling adopted a loss rather than "+
				"normalising a spelling", got, want)
		}
	})
}
