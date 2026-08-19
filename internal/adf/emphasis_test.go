package adf_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
)

// sketch renders a document's inline content as one line: each text node with
// the marks it carries, sorted, so a table of expectations is something a
// reader can check against the CommonMark specification without decoding JSON.
//
// The marks are sorted because ADF stores them as a set and this package emits
// its own order; an expectation that pinned the order would be pinning an
// artefact of which delimiter closed first.
func sketch(t *testing.T, src string) string {
	t.Helper()

	doc, err := adf.FromMarkdown(src)
	if err != nil {
		return "REFUSED"
	}
	var b strings.Builder
	for _, block := range doc.Content {
		for _, n := range block.Content {
			if n.Type != "text" {
				fmt.Fprintf(&b, "<%s>", n.Type)
				continue
			}
			marks := make([]string, 0, len(n.Marks))
			for _, m := range n.Marks {
				marks = append(marks, m.Type)
			}
			if len(marks) == 0 {
				b.WriteString(n.Text)
				continue
			}
			sort.Strings(marks)
			fmt.Fprintf(&b, "[%s:%s]", strings.Join(marks, "+"), n.Text)
		}
	}
	return b.String()
}

// TestEmphasisFollowsCommonMark is the reader's half of the emphasis contract,
// and the table is CommonMark's section 6.2 rather than this package's history.
//
// It exists because the scanner this replaced matched a closing run of exactly
// the length it had opened with, which cannot express a nested span that ends
// flush against its parent's close. `**bold *and italic***` came out as one
// text node with the asterisks still in it: marks dropped, no refusal, exit 0.
// A round-trip fuzzer found it from the far side, as the writer refusing its
// own output, and a table like this one would have found it in a second by
// asking the reader directly.
//
// ADF stores marks as a set on a text node, so nesting that CommonMark spells
// as one element inside another arrives here as two marks on the same text —
// and emphasis inside emphasis arrives as emphasis, which is the one place the
// model has less to say than the markup does.
func TestEmphasisFollowsCommonMark(t *testing.T) {
	cases := []struct{ src, want string }{
		// A run closes what it can and keeps the rest. These are the shapes
		// the old scanner read as text.
		{"**bold *and italic***", "[strong:bold ][em+strong:and italic]"},
		{"*italic **and bold***", "[em:italic ][em+strong:and bold]"},
		{"**0*0***", "[strong:0][em+strong:0]"},
		{"*foo**bar***", "[em:foo][em+strong:bar]"},
		{"**foo*bar***", "[strong:foo][em+strong:bar]"},

		// Emphasis, strong, and both.
		{"*foo bar*", "[em:foo bar]"},
		{"_foo bar_", "[em:foo bar]"},
		{"**foo bar**", "[strong:foo bar]"},
		{"__foo bar__", "[strong:foo bar]"},
		{"***foo***", "[em+strong:foo]"},
		{"foo***bar***baz", "foo[em+strong:bar]baz"},

		// Flanking. A delimiter needs something other than whitespace on the
		// inside, and punctuation on the outside changes the answer.
		{"a * foo bar*", "a * foo bar*"},
		{"** foo bar**", "** foo bar**"},
		{"_ foo bar_", "_ foo bar_"},
		{`a*"foo"*`, `a*"foo"*`},
		{`a_"foo"_`, `a_"foo"_`},
		{"foo*bar*", "foo[em:bar]"},
		{"5*6*78", "5[em:6]78"},
		{"*foo*bar", "[em:foo]bar"},
		{"**foo**bar", "[strong:foo]bar"},
		{"_(bar)_.", "[em:(bar)]."},

		// The underscore's extra rule, which is why a custom field id is not
		// half emphasised.
		{"foo_bar_", "foo_bar_"},
		{"5_6_78", "5_6_78"},
		{"customfield_10042", "customfield_10042"},
		{"_foo_bar_baz_", "[em:foo_bar_baz]"},
		{"foo__bar__", "foo__bar__"},

		// Nesting, and the runs that only partly spend themselves.
		{"*foo**bar**baz*", "[em:foo][em+strong:bar][em:baz]"},
		{"*foo **bar** baz*", "[em:foo ][em+strong:bar][em: baz]"},
		{"**foo *bar* baz**", "[strong:foo ][em+strong:bar][strong: baz]"},
		{"__foo, __bar__, baz__", "[strong:foo, bar, baz]"},

		// The rule of three: where either run could open and close, a pair
		// whose original lengths sum to a multiple of three is refused unless
		// both lengths are. It is what keeps this one phrase rather than a
		// nest.
		{"*foo**bar*", "[em:foo**bar]"},

		// A run that pairs with nothing is the characters it is made of.
		{"**", "**"},
		{"a * b", "a * b"},
		{"*a", "*a"},
		{"a*", "a*"},
		{`\*not emphasis\*`, "*not emphasis*"},
	}

	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			if got := sketch(t, c.src); got != c.want {
				t.Errorf("FromMarkdown(%q)\n got  %s\n want %s", c.src, got, c.want)
			}
		})
	}
}

// TestEmphasisTheWriterCannotSpellIsRefused covers the boundary the table above
// runs into, so the next reader does not mistake it for a parser bug.
//
// It is read correctly. What refuses it is ToMarkdown, which FromMarkdown runs
// before it returns: the span has two spellings, each of them is read as
// something the document does not say, and there is no third. The asterisk
// merges into a run with the delimiter beside it; the underscore is inert where
// it lands, because a run against punctuation cannot close in front of a word
// character.
//
// It is not rescued by writing the span narrower, which is what renderChoices
// does with `*0*~~*0*~~0` in the test below: the cut in `**a *b***c` falls on a
// space the mark covers, and a narrower span there would come back with the
// space unmarked.
//
// The alternative is what this package exists not to do: write it down anyway
// and hope. A refusal names the construct and offers --raw-body; the shape the
// old scanner produced for this was a paragraph full of asterisks and exit 0.
//
// `foo******bar*********baz` used to be here and is not a refusal. It was one
// only because `after` read the neighbouring text unescaped, which made every
// refusal wider than it had to be — the three asterisks after the strong span
// are literal, they go out as `\*\*\*`, and a backslash merges with nothing. It
// is written `foo**bar**\*\*\*baz` and reads back as what it came from. See
// TestADelimiterIsWrittenOnlyWhereItCanBeRead, "a literal delimiter after a
// span is not a collision".
func TestEmphasisTheWriterCannotSpellIsRefused(t *testing.T) {
	for _, src := range []string{
		"**a *b***c",
	} {
		t.Run(src, func(t *testing.T) {
			if got := sketch(t, src); got != "REFUSED" {
				t.Errorf("FromMarkdown(%q) = %s, want a refusal", src, got)
			}
		})
	}
}

// TestADelimiterIsWrittenOnlyWhereItCanBeRead is the writer's half of the
// flanking rules, and it is the nightly fuzzer's find of 2026-08-16.
//
// A delimiter that cannot flank is not ambiguous, it is inert: the reader keeps
// it as text and the span disappears. Emphasis over `0` and a struck `0` used
// to be written as one span, `*0~~0~~*0`, whose closing asterisk sits between
// the strike's `~` and a digit: punctuation on one side and a word character on
// the other, which is the one combination CommonMark lets nothing close. It
// read back as two literal asterisks and no emphasis, exit 0, from this
// package's own output.
//
// The cases are documents rather than markdown because the failure is on the
// way out. A document arrives from Jira and there may be no markdown that
// produces it, so the reader cannot be asked to set the test up.
func TestADelimiterIsWrittenOnlyWhereItCanBeRead(t *testing.T) {
	cases := []struct{ name, adf, want, says string }{{
		name: "emphasis reaching across a strike",
		adf: para(`{"type":"text","text":"0","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strike"}]},` +
			`{"type":"text","text":"0"}`),
		// One span cannot close after the `~`, so the em is written over each
		// node instead and the strike goes outside the second. Which is how
		// `*0*~~*0*~~0` was written in the first place: the document is the one
		// that input builds, and this is the same document spelled again.
		want: "_0_~~*0*~~0",
		says: "[em:0][em+strike:0]0",
	}, {
		// The strike is not emphasis and `~~` has no flanking rules at all in
		// GFM, but both used to go through the emphasis choice and inherit its
		// refusal. Reading `*0~~0~~*0` back produced exactly this document, and
		// writing it down again failed with an error about emphasis the
		// document does not contain.
		name: "a strike beside an asterisk",
		adf: para(`{"type":"text","text":"*0"},` +
			`{"type":"text","text":"0","marks":[{"type":"strike"}]},` +
			`{"type":"text","text":"*0"}`),
		want: `\*0~~0~~\*0`,
		says: "*0[strike:0]*0",
	}, {
		// The check that refuses the first case has to keep accepting this one:
		// the two delimiters are flush on both sides, which is one run of three
		// to a reader, and what flanks it is what is outside the pair.
		name: "emphasis and strong over one word",
		adf: para(`{"type":"text","text":"foo"},` +
			`{"type":"text","text":"bar","marks":[{"type":"em"},{"type":"strong"}]},` +
			`{"type":"text","text":"baz"}`),
		want: "foo***bar***baz",
		says: "foo[em+strong:bar]baz",
	}, {
		// Punctuation on the far side of the delimiter is the other half of the
		// rule, and it is why the characters either side are read as runes: the
		// last byte of `»` is not a word character and treating it as one would
		// refuse a document CommonMark can spell.
		name: "emphasis ending in punctuation, punctuation after",
		adf: para(`{"type":"text","text":"a.","marks":[{"type":"strong"}]},` +
			`{"type":"text","text":"»"}`),
		want: "**a.**»",
		says: "[strong:a.]»",
	}, {
		// A control character is punctuation to the flanking rules and not
		// whitespace, and whitespace is the one class that can neither open nor
		// close. The check that asks this reads a zero rune as "nothing there",
		// which is right for what sits outside a span and wrong for what sits
		// inside one, because a span's content is never empty and a zero from it
		// is a literal NUL. Sharing one function for both refused 25 inputs from
		// the fuzz corpus that this converter had written correctly the day
		// before, and no fuzzer could have reported it: a refusal ends the
		// round-trip property early, so refusing more always looks greener.
		name: "emphasis over a control character",
		adf:  para(`{"type":"text","text":"\u0000","marks":[{"type":"em"}]}`),
		want: "*\x00*",
		says: "[em:\x00]",
	}, {
		// The find of 2026-08-17, nineteen seconds into the fuzz job on a push
		// that changed nothing in this package.
		//
		// The literal asterisk after the emphasis is written `\*`, and a
		// backslash merges with nothing, so the emphasis can close against it.
		// `after` read the *unescaped* text and saw an asterisk, so the span was
		// refused, renderChoices answered by cutting the strike in two, and the
		// two halves came out as `~~00*a*~~~~\*~~`. A reader takes `~~~~` for
		// four literal tildes, so the output said something the document did
		// not, and the fuzzer saw it on the pass after that, when those tildes
		// came back escaped.
		name: "a literal delimiter after a span is not a collision",
		adf: para(`{"type":"text","text":"00","marks":[{"type":"strike"}]},` +
			`{"type":"text","text":"a","marks":[{"type":"strike"},{"type":"em"}]},` +
			`{"type":"text","text":"*","marks":[{"type":"strike"}]}`),
		want: `~~00*a*\*~~`,
		says: "[strike:00][em+strike:a][strike:*]",
	}, {
		// The find of 2026-08-18, and the same ending as the case above it
		// reached from the other side of the same question: what is actually
		// written where the delimiter goes.
		//
		// The emphasised node holds `*0`, which is written `\*0`, an escaped
		// asterisk and a digit. insideLive asks whether a live delimiter sits
		// strictly inside the span and would close it early, and it began its
		// scan at the first byte strictly inside, so the backslash at index 0
		// was never consumed and the asterisk it escapes was counted as live.
		// The asterisk spelling was refused for a collision that is not there;
		// the underscore cannot close in front of the digit that follows; with
		// no spelling left, renderChoices cut the strike back to the first
		// node. That wrote `~~_\*0_~~~~0~~`, where the four tildes are two
		// delimiters meeting and a reader takes them for text.
		name: "an escaped delimiter at the start of a span is not a closer",
		adf: para(`{"type":"text","text":"*0","marks":[{"type":"strike"},{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"strike"}]}`),
		want: `~~*\*0*0~~`,
		says: "[em+strike:*0][strike:0]",
	}, {
		// The same document with the strike taken off it, which is where the
		// mistake starts. There is nothing to cut here, so the old code had
		// nowhere to put the span and refused the document outright: a
		// paragraph Jira stores, reported as having no spelling, over a
		// delimiter that is text.
		name: "a span opening on an escaped delimiter has a spelling",
		adf: para(`{"type":"text","text":"*0","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0"}`),
		want: `*\*0*0`,
		says: "[em:*0]0",
	}, {
		// The third find of 2026-08-18, and the same sentence about `after` as
		// the case above: what a span sits against is the character that will
		// be written there. A text node carrying a link is written starting
		// with the link's `[`, and `after` reported the first character of the
		// node's text instead. Emphasis ending in punctuation cannot close in
		// front of a word character and can close in front of a bracket, so a
		// document Cloud stores was refused over a character that is never
		// written where the model put it. spanMarks decides the order, and a
		// link is outermost, so the bracket is what lands there even when the
		// node carries emphasis of its own.
		name: "a link after a span is its bracket, not its text",
		adf: para(`{"type":"text","text":"a.","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"link",` +
			`"attrs":{"href":"https://x.invalid/a"}}]}`),
		want: "*a.*[0](https://x.invalid/a)",
		says: "[em:a.][link:0]",
	}, {
		// The same rule from the other side: two spans of one mark with nothing
		// between them are one span, and writing them as two puts their
		// delimiters against each other. That is what the cut above produced, so
		// it is asserted directly — a future change that brings the cut back
		// fails here rather than in a fuzz sweep somebody has to reproduce.
		name: "adjacent strikes are one span",
		adf: para(`{"type":"text","text":"a","marks":[{"type":"strike"}]},` +
			`{"type":"text","text":"b","marks":[{"type":"strike"}]}`),
		want: "~~ab~~",
		says: "[strike:ab]",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("ToMarkdown = %q, want %q", got, c.want)
			}
			// What the markdown says is the point; the spelling above is only
			// how this package happens to say it.
			if says := sketch(t, got); says != c.says {
				t.Errorf("%q reads back as %s, want %s", got, says, c.says)
			}
		})
	}
}

// TestAStrikeSpanIsNeverCut is the guard on the cut, and the shape behind
// three fuzz finds in two days.
//
// A strike has one spelling. GFM gives `~~` no flanking rules, so nothing
// beside it can make it inert, and the only thing that changes what it means is
// another `~~` flush against it, which a reader takes for four literal tildes.
// A cut leaves the rest of the mark to open its own span at the cut with
// nothing in between, so a cut strike writes exactly that.
//
// renderChoices used to cut anyway, and the cut is what turned each of those
// finds from a refusal into a wrong document. Refusing the cut is not the same
// as refusing the document: renderChoices moves on to the next way of opening
// the span, and where the node carries another mark that reaches as far, the
// document has a spelling with that mark on the outside instead. The first two
// cases here take it. The third has nothing else at the node the span opens on,
// so it is refused, which is the answer every other unwritable document gets.
//
// The documents are built by hand because no markdown produces them.
// `~~*a.*b~~` is not the first one: the emphasis cannot close in front of a
// word character, so a reader keeps the asterisks as text.
// FuzzMarkdownRoundTrips starts from markdown and therefore cannot reach these
// at all, which is why they converted wrongly under two clean sweeps of the
// shape.
func TestAStrikeSpanIsNeverCut(t *testing.T) {
	for _, c := range []struct{ name, adf, want, says string }{{
		// The emphasis cannot close in front of `b`, so the strike cannot be
		// written around it. Written with the emphasis outside instead, both
		// marks land where the document put them.
		name: "emphasis inside a strike, word after",
		adf: para(`{"type":"text","text":"a.","marks":[{"type":"strike"},{"type":"em"}]},` +
			`{"type":"text","text":"b","marks":[{"type":"strike"}]}`),
		want: "*~~a.~~*~~b~~",
		says: "[em+strike:a.][strike:b]",
	}, {
		name: "strong inside a strike, word after",
		adf: para(`{"type":"text","text":"a.","marks":[{"type":"strike"},{"type":"strong"}]},` +
			`{"type":"text","text":"b","marks":[{"type":"strike"}]}`),
		want: "**~~a.~~**~~b~~",
		says: "[strike+strong:a.][strike:b]",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				// It used to write `~~*a.*~~~~b~~`, which reads back with the
				// four tildes as text on the node after the cut.
				t.Errorf("ToMarkdown = %q, want %q", got, c.want)
			}
			if says := sketch(t, got); says != c.says {
				t.Errorf("%q reads back as %s, want %s", got, says, c.says)
			}
		})
	}

	// The same shape with a plain struck node in front of it. The span opens on
	// a node carrying nothing but the strike, so there is no other mark to put
	// outside and no spelling at all.
	t.Run("nothing else to put outside", func(t *testing.T) {
		doc := para(`{"type":"text","text":"0","marks":[{"type":"strike"}]},` +
			`{"type":"text","text":"a.","marks":[{"type":"strike"},{"type":"em"}]},` +
			`{"type":"text","text":"b","marks":[{"type":"strike"}]}`)
		got, err := convert(t, doc)
		if err == nil {
			t.Fatalf("ToMarkdown = %q, want a refusal; it reads back as %s",
				got, sketch(t, got))
		}
		if code := errs.Coerce(err).Code; code != "ADF_UNREPRESENTABLE" {
			t.Errorf("code = %q, want ADF_UNREPRESENTABLE", code)
		}
	})
}

// TestEmphasisWithNoSpellingIsRefusedRatherThanWrittenWrong is the case where
// no spelling exists, which is a real limit of markdown and not of this
// package: emphasis cannot end in punctuation and be followed by a word
// character, in either form, at any width.
//
// It is a refusal a caller can hit on a document Jira stored, which is the
// price of the rule above. The alternative is `**a.**b`, which every reader
// takes as five characters of text and a bold that never happened.
func TestEmphasisWithNoSpellingIsRefusedRatherThanWrittenWrong(t *testing.T) {
	doc := para(`{"type":"text","text":"a.","marks":[{"type":"strong"}]},` +
		`{"type":"text","text":"b"}`)

	got, err := convert(t, doc)
	if err == nil {
		t.Fatalf("ToMarkdown = %q, want a refusal", got)
	}
	structured := errs.Coerce(err)
	if structured.Code != "ADF_UNREPRESENTABLE" {
		t.Errorf("code = %q, want ADF_UNREPRESENTABLE", structured.Code)
	}
	if !strings.Contains(structured.Remedy, "--raw-body") {
		t.Errorf("remedy %q leaves the caller with no way to read the body",
			structured.Remedy)
	}
}

// TestTheAsteriskIsYieldedUntilThereIsNoRoom is the nightly sweep's find of
// 2026-08-19, and the class it belongs to is wider than the input that found
// it.
//
// Every emphasis span writes itself expecting the next one to open with an
// asterisk, because `opensWith` names one on its behalf before the next span
// has chosen anything. So a span with an emphasis neighbour takes the
// underscore and leaves the asterisk for it. That is worth doing — an
// underscore goes inert between word characters, so a span that has to close in
// front of one has only the asterisk, and `_a_**b**c` is written rather than
// refused because the first span handed it along.
//
// It is also unsatisfiable at three. The first yields the asterisk and takes
// the underscore, the second has an underscore on its left and a predicted
// asterisk on its right, and neither character is left. That is not a limit of
// markdown: `_a_**b**_c_` is spelled below, and CommonMark reads it back as the
// three spans it came from. Every document of the shape was refused until the
// sweep reported one with a strike in it, as this package being unable to read
// `0 ~~*0*~~__\!__*\!*`, which it had written itself one conversion earlier.
//
// The fix is that the yielding is a preference and gets given up: inlineList
// writes the run a second time without it, and only when the first attempt
// found no spelling for some span in it. So the two-span cases here are
// unchanged, and the rest are documents that used to have no spelling at all.
func TestTheAsteriskIsYieldedUntilThereIsNoRoom(t *testing.T) {
	em := func(text string) string {
		return `{"type":"text","text":"` + text + `","marks":[{"type":"em"}]},`
	}
	strong := func(text string) string {
		return `{"type":"text","text":"` + text + `","marks":[{"type":"strong"}]},`
	}

	for _, c := range []struct{ name, adf, want, says string }{{
		// Two spans, which the yielding has always been able to satisfy: the
		// first takes the underscore and the second keeps the asterisk. Here to
		// pin that the second attempt is not reached and changes nothing.
		name: "two spans against each other",
		adf:  para(strings.TrimSuffix(em("a")+strong("b"), ",")),
		want: "_a_**b**",
		says: "[em:a][strong:b]",
	}, {
		// The reason the yielding is worth keeping. The strong span has to
		// close in front of `c`, where an underscore is inert, so it needs the
		// asterisk and the em in front of it must not take one.
		name: "yielding is what makes the neighbour writable",
		adf: para(em("a") + strong("b") +
			`{"type":"text","text":"c"}`),
		want: "_a_**b**c",
		says: "[em:a][strong:b]c",
	}, {
		// Three, which no amount of yielding satisfies. Written on the second
		// attempt, where each span looks at the delimiter actually beside it
		// rather than at one nobody has chosen.
		name: "three spans against each other",
		adf:  para(strings.TrimSuffix(em("a")+strong("b")+em("c"), ",")),
		want: "*a*__b__*c*",
		says: "[em:a][strong:b][em:c]",
	}, {
		name: "four spans against each other",
		adf:  para(strings.TrimSuffix(em("a")+strong("b")+em("c")+strong("d"), ",")),
		want: "*a*__b__*c*__d__",
		says: "[em:a][strong:b][em:c][strong:d]",
	}, {
		// The document the sweep found, which is the same three spans with a
		// strike inside the first and an escaped character in the other two.
		name: "the sweep's own document",
		adf: para(`{"type":"text","text":"0 "},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strike"}]},` +
			`{"type":"text","text":"!","marks":[{"type":"strong"}]},` +
			`{"type":"text","text":"!","marks":[{"type":"em"}]}`),
		want: `0 *~~0~~*__\!__*\!*`,
		says: "0 [em+strike:0][strong:!][em:!]",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("ToMarkdown = %q, want %q", got, c.want)
			}
			if says := sketch(t, got); says != c.says {
				t.Errorf("%q reads back as %s, want %s", got, says, c.says)
			}
		})
	}
}

// TestASpanIsReconsideredWhenTheRestOfTheRunCannotBeWritten is the nightly
// sweep's third find of 2026-08-19, and the third defect in one enumeration.
//
// `renderChoices` lists the ways to open the span at one position and takes the
// first that can be written. Nothing goes back. So a span that is locally
// correct can leave the rest of the run with no spelling, and the document is
// refused over a choice made three nodes earlier.
//
// Here the strong span over the last two nodes has no spelling, so it is cut to
// one and written `***0***`. That is a correct span. It leaves an asterisk
// against the node after it, where `**` merges into a run of three and `__`
// cannot close between two word characters, and the run is refused. Opening the
// `em` on that node instead writes `_**0**_`, the next span takes `**`, and the
// document comes out as the text its own reader was handed.
//
// The document is built by hand because the failure is on the way out, and the
// spelling below is the one `*0***0*****0** **0*****0**0` reads back as: this
// converter refused a document that its own reader had just built from text
// this converter had just written.
//
// What the search does not reach is worth naming, because the first estimate of
// it was wrong. Twelve inputs in the verdict corpus are still refused this way,
// and raising the budget by a factor of a hundred thousand does not move any of
// them: the enumeration genuinely runs out. They need a spelling this writer
// has no vocabulary for, where one delimiter run closes one mark and opens
// another, which is the rule of three the parent card has been naming since
// 2026-08-15. A search finds an assignment of marks to spans; it cannot invent
// a spelling the writer never generates.
func TestASpanIsReconsideredWhenTheRestOfTheRunCannotBeWritten(t *testing.T) {
	doc := para(`{"type":"text","text":"0","marks":[{"type":"em"}]},` +
		`{"type":"text","text":"0","marks":[{"type":"strong"}]},` +
		`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]},` +
		`{"type":"text","text":" "},` +
		`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]},` +
		`{"type":"text","text":"0","marks":[{"type":"strong"}]},` +
		`{"type":"text","text":"0"}`)

	got, err := convert(t, doc)
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	if want := `*0*__0*0*__ _**0**_**0**0`; got != want {
		t.Errorf("ToMarkdown = %q, want %q", got, want)
	}
	if says, want := sketch(t, got),
		"[em:0][strong:0][em+strong:0] [em+strong:0][strong:0]0"; says != want {
		t.Errorf("%q reads back as %s, want %s", got, says, want)
	}
}

// TestANestedSpanTakesTheCharacterItsParentNeedsToLeave is the third thing the
// writer chooses while writing a run, and the last of the three to be searched.
//
// A span picks between `*` and `_` by taking the first that can be read back
// where it sits, which is correct about that span and says nothing about the
// span around it. `*_00_0 __0__*` is em over two nodes with strong on the
// second: writing the strong `**0**` is correct, and it leaves the em's content
// ending in a live asterisk, where neither of the em's own spellings can be
// read back. Writing it `__0__` costs the strong span nothing and leaves the em
// its asterisk.
//
// Nothing about that is visible from inside the strong span. It is visible only
// to something enumerating both, which is why the search yields every way of
// writing a span's content rather than the first, and why it does that in a
// second pass: the first pass allows only each span's first workable character,
// so a document that already had a spelling keeps the one it had.
func TestANestedSpanTakesTheCharacterItsParentNeedsToLeave(t *testing.T) {
	for _, c := range []struct{ name, adf, want, says string }{{
		name: "strong inside em, and the em needs the asterisk",
		adf: para(`{"type":"text","text":"_00_0 ","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]}`),
		want: `*\_00_0 __0__*`,
		says: "[em:_00_0 ][em+strong:0]",
	}, {
		name: "strong on both ends of an em",
		adf: para(`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]},` +
			`{"type":"text","text":" __0_0 ","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]}`),
		want: `*__0__ \_\_0_0 __0__*`,
		says: "[em+strong:0][em: __0_0 ][em+strong:0]",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("ToMarkdown = %q, want %q", got, c.want)
			}
			if says := sketch(t, got); says != c.says {
				t.Errorf("%q reads back as %s, want %s", got, says, c.says)
			}
		})
	}
}

// TestTheWriterAsksTheReaderWhenItsOwnRulesRefuse is the last of the emphasis
// refusals, and the first time this package answers a question about the reader
// by asking the reader.
//
// `merges` holds two checks that are approximations rather than rules.
// `insideLive` refuses a `**` span whose content holds a live asterisk, and the
// flush test refuses one whose content starts or ends with the delimiter on one
// side only. Both are right about a span delimited by a single character and
// both are conservative about one delimited by two, because a live asterisk
// inside is only a collision when it does not pair with something else inside:
// `**a*b*c**` is fine and `**a*bc**` is not, and what decides is the reader's
// delimiter pairing.
//
// There is no rule to add here that is not that algorithm written a second
// time, which is the whole subject of
// `the-writer-guesses-what-the-reader-will-say`. So the last pass of the search
// drops those two checks, and every candidate it generates that way is read
// back and compared against the nodes it was written from. A candidate the
// reader does not agree with is not written; it is the next candidate's turn.
//
// It is affordable because of where it sits. The greedy walk answers almost
// every document, the search runs only when the walk refused, and this pass
// runs only when every strict spelling in the search has been tried.
func TestTheWriterAsksTheReaderWhenItsOwnRulesRefuse(t *testing.T) {
	for _, c := range []struct{ name, adf, want, says string }{{
		// A strong run and an em run that overlap without nesting, with a
		// marked space inside the em. Every strict spelling collides.
		name: "overlapping runs with a marked space",
		adf: para(`{"type":"text","text":"0","marks":[{"type":"strong"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]},` +
			`{"type":"text","text":" ","marks":[{"type":"em"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]},` +
			`{"type":"text","text":"0"}`),
		want: `__0*0*__ ***0***0`,
		// The space keeps no mark, which is the whitespace rule in
		// docs/output-contract.md and not a loss this pass introduces.
		says: "[strong:0][em+strong:0] [em+strong:0]0",
	}, {
		// A strong span whose content holds a live asterisk, which is exactly
		// what insideLive refuses and exactly what a reader pairs internally.
		name: "a live asterisk inside a strong span",
		adf: para(`{"type":"text","text":"0","marks":[{"type":"strong"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]},` +
			`{"type":"text","text":"0_0","marks":[{"type":"strong"}]},` +
			`{"type":"text","text":"0","marks":[{"type":"em"},{"type":"strong"}]}`),
		want: `__0*0*__**0_0**__*0*__`,
		says: "[strong:0][em+strong:0][strong:0_0][em+strong:0]",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got, err := convert(t, c.adf)
			if err != nil {
				t.Fatalf("ToMarkdown: %v", err)
			}
			if got != c.want {
				t.Errorf("ToMarkdown = %q, want %q", got, c.want)
			}
			if says := sketch(t, got); says != c.says {
				t.Errorf("%q reads back as %s, want %s", got, says, c.says)
			}
		})
	}
}
