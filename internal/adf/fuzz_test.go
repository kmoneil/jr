package adf_test

import (
	"encoding/json"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
)

// FuzzToMarkdown asserts the read side has exactly two outcomes for anything
// that parses as a document: markdown, or a refusal naming the construct.
//
// A panic or a third kind of error means a document somewhere on a real Cloud
// site takes `issue get` down, and the input that does it is not one anybody
// would think to write in a table.
func FuzzToMarkdown(f *testing.F) {
	for _, seed := range []string{
		`{"type":"doc","version":1,"content":[]}`,
		`{"type":"doc","version":1,"content":[{"type":"paragraph"}]}`,
		`{"type":"doc","content":[{"type":"heading","attrs":{"level":1e999}}]}`,
		`{"type":"doc","content":[{"type":"heading","attrs":{"level":2.5}}]}`,
		`{"type":"doc","content":[{"type":"text","text":"","marks":[{"type":"link"}]}]}`,
		`{"type":"doc","content":[{"type":"table","content":[{"type":"tableRow"}]}]}`,
		`{"type":"doc","content":[{"type":"date","attrs":{"timestamp":"-9223372036854775808"}}]}`,
		`{"type":"doc","content":[{"type":"media","attrs":{"id":") ("}}]}`,
		`{"type":"doc","content":[{"type":"orderedList","attrs":{"order":-1},` +
			`"content":[{"type":"listItem"}]}]}`,
		`{"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"\n"}}]}`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		doc, err := adf.Parse([]byte(raw))
		if err != nil {
			return
		}
		if _, err := adf.ToMarkdown(doc); err != nil {
			if code := errs.Coerce(err).Code; code != "ADF_UNREPRESENTABLE" {
				t.Fatalf("code = %q, want ADF_UNREPRESENTABLE: %v", code, err)
			}
		}
	})
}

// FuzzMarkdownRoundTrips is the property that catches an escaping bug, which is
// the whole failure mode of a converter like this one.
//
// Whatever a caller writes, the markdown this package produces from the
// document it built has to read back as the same document — otherwise text
// that survived one conversion means something else after two, and nothing in
// between reports a problem. Leading whitespace was found exactly this way:
// four spaces at the start of a paragraph came back out as an indented code
// block.
func FuzzMarkdownRoundTrips(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "# h", "**b**", "*i*", "`c`", "~~s~~",
		"a\\\nb", "- x", "1. x", "- [ ] x", "> q", "> [!INFO]\n> x",
		"| a |\n| --- |\n| b |", "```go\nx\n```", "---",
		"[t](u)", "<https://x.invalid>", "![a](jira-media:c/i)",
		"[@a](jira-user:1)", "[s](jira-status:red)", "[d](jira-date:0)",
		"    four spaces", "trailing ", "\\| pipe | row |", "a_b_c",
		"*a * b*", "**a\\`b**", "[a](<b c>)", "***x***", "- a\n\n  - b",

		// Crashers this fuzzer found, kept here rather than only in a corpus
		// file so the regression is visible in the source next to the fix.
		"trailing ", // built a document ToMarkdown then refused
		"[0]()",     // a link to nowhere, which ADF has no mark for
		"`0``0`",    // a code span written with three backticks, which
		// the block parser then read as a code fence
		"[](jira-user:)", // a mention of nobody
		"# \\#",          // a heading whose text is an escaped hash, eaten by
		// the closing-hash rule
		"[!](jira-user:0)", // a label kept with its escapes in it, which grew
		// a backslash on every conversion
		"0 \\\n0", // a space before a hard break, which markdown strips and
		// this parser was keeping
		"**0 *0*0**", // a span across three nodes, emitted as three spans and
		// five delimiter runs that read back as something else
		"# 0 \\#", // a heading ending in a hash, read back as decoration and
		// stripped
		"*0****0***", // a nested span flush against its parent's delimiter,
		// which merged into a run that reads back as something else
		"***both***", "***a* b *c***", "**\\*\\***",
		"*0****0****0*", // a bold word inside an italic sentence, taken apart
		// by a closer found inside a longer run
		"0) * * +\n0",        // nested empty items, which render as a rule
		"*0*__0__",           // two spans whose delimiters merged into a run of three
		"*0*___0__!___",      // a delimiter inside the span, which closed it early
		"*__0__ 0000000*",    // emphasis around a leading space, which opens nothing
		"**0\t*#",            // a hash escaped as though it began a line
		"*__0__ 0)*",         // a bracket after a digit, escaped as a list marker
		"[0](\\\r)",          // a link address holding a line break, encoded one way
		"![](jira-media://)", // an attachment id holding the scheme's separator
		"*0\t*\\*",           // a span whose trailing whitespace sits between its own
		// delimiter and the next one
		"*__0__\v0*", // whitespace markdown counts and this did not
		"[\f](0)",    // a link whose text is whitespace, moved out and lost
		"a\\\n",      // a line break at the end of a block, which writes as a
		// backslash and reads back as one
		"**0_0*", // an escaped asterisk counted as a live delimiter, and
		// an inert underscore taken for a closer
		"0\\\n\v\\=", // a setext underline behind a vertical tab, which the
		// writer read as mid-line and the reader trimmed away
		"*__0__ __0__ __0__*", // one mark over a run and a narrower one on each
		// word, which opened the narrow mark first and cut the wide one into
		// spans that shed the mark from a space per conversion
		"0 __0__*__0__ 0*__0__", // the writer's own spelling of a span ending
		// flush against its parent's close, which the reader could not take
		// apart because it looked for a closing run of one exact length
		"**bold *and italic***", // the same shape as anybody writes it
		"*foo**bar*",            // the rule of three, which is the only thing
		// standing between this and a nest nobody meant
		"\x00**0***", // a control character beside a delimiter, which the
		// writer treats as no word character and the flanking rules put in the
		// class that has neither an opener nor a closer beside it
		"0 __0!_*_*__000", // an underscore beside an escaped underscore, where
		// the escaping counted the neighbour as a word character and the
		// flanking rules count it as punctuation, so one of the two characters
		// the writer meant as text opened a span
		"00 ***********************0*********0*0***0*0************* 0",
		// a span whose content holds live delimiters, with a space either side
		// of it: the space said no neighbour could merge and the check that
		// says so ran before the two about the content, and hid them
		"*0*~~*0*~~0", // em over a strike and out the other side, written as one
		// span whose closing delimiter then sat between the strike's `~` and a
		// digit, where CommonMark lets nothing close
		"foo***bar***baz", // the same shape spelled the way it works, so the
		// check that refuses the one above has to keep accepting this
		"**0*****0** **0*****0**0", // strong holding an em and a plain word,
		// which the writer refused to spell at all rather than trying the other
		// way of cutting it. Held out of this list while it was open, because
		// the seeds run in `make test` and everybody's build would have been red
		"[0](0 \"\r\\>\r\")", // a link title spanning lines, escaped by a rule
		// that knew what ends a title and not what ends a paragraph, so the
		// second line of the title read as a block quote
		`[0](0 "a` + "\n" + `# b")`, // the same shape with a heading under it,
		// because the first one only proves the character it happens to hold
		"**!*~~*0~~*~~0~~ *0***", // an escaped asterisk at the very start of a
		// span, counted as a live delimiter because the scan for escapes began
		// after the backslash. The emphasis had no spelling left, the strike
		// around it was cut in two, and the `~~~~` that made came back as four
		// literal tildes
		"***~~!~~*~~0~~ *0***0", // the same `~~~~`, eight minutes into the sweep
		// that was checking the fix for the one above it. Cutting a strike is
		// what writes those four tildes whatever drove the writer to cut, so
		// the cut is gone: renderChoices opens the span with another mark
		// instead, or the document is refused
		"*\\!*<0:\\>", // emphasis in front of a card, which is written as a
		// link and therefore starts with a bracket. `after` reported the first
		// character of the link's text, the emphasis could not close in front
		// of it, and the document this package had just written was one it
		// then refused to write again
		"**0~~0~~0~~0~~0~~ ~~0*0_0~~0~~0*0**", // a strong span whose content
		// holds a live asterisk, which `insideLive` refuses and a reader pairs
		// internally. Found by a fuzz run against the commit before the writer
		// started asking the reader about the spellings its own rules refuse
		"*0***0*****0** **0*****0**0", // a strong span cut to one node, which
		// is a correct span and leaves an asterisk where the next one needed
		// it. The walk had already committed and could not go back, so a
		// document this converter's own reader had just built was refused
		"__!_____!__ __!_____!_____!__ __0___", // two whitespace nodes carrying
		// em inside overlapping mark runs. Markdown can only nest, so the cut
		// stranded one marked space per conversion and the text took three to
		// stop moving, one more than this target allows. See settle
		"0~~ ~~*~~0~~***!***!*", // three emphasis spans against each other,
		// where every one of them was keeping the asterisk clear for the next.
		// The first took the underscore, the second had an underscore on its
		// left and a predicted asterisk on its right and could take neither,
		// and a run this package had just written came back refused
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, source string) {
		doc, err := adf.FromMarkdown(source)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("a built document did not encode: %v", err)
		}
		parsed, err := adf.Parse(encoded)
		if err != nil {
			t.Fatalf("a built document did not parse: %v\n%s", err, encoded)
		}
		once, err := adf.ToMarkdown(parsed)
		if err != nil {
			// FromMarkdown checks this itself before returning, so reaching it
			// here means that check was removed or is not reached.
			t.Fatalf("FromMarkdown built what ToMarkdown refuses: %v\n%s", err, encoded)
		}

		// The output has to be readable by the parser that produced it, and it
		// has to settle.
		//
		// The first pass may normalise: emphasis has two spellings and this
		// package picks whichever cannot be misread beside its neighbours, so
		// `_**x**_` and `***x***` are the same document written two ways. What
		// it may not do is keep changing — drift is how a body that went
		// through twice stops saying what it said, and every escaping bug this
		// fuzzer found showed up as text that changed on every pass rather
		// than settling on the second.
		twice := once
		for pass := range 2 {
			again, err := adf.FromMarkdown(twice)
			if err != nil {
				t.Fatalf("this package cannot read its own output: %v\n--- wrote ---\n%s",
					err, twice)
			}
			next, err := adf.ToMarkdown(again)
			if err != nil {
				t.Fatalf("pass %d refuses what the one before wrote: %v", pass+2, err)
			}
			if pass > 0 && next != twice {
				t.Errorf("the text is still changing after two conversions"+
					"\n--- was ---\n%s\n--- now ---\n%s", twice, next)
			}
			twice = next
		}
	})
}
