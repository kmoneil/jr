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
// Both of these are read correctly. What refuses them is ToMarkdown, which
// FromMarkdown runs before it returns: a span has two spellings, each of them
// is read as something the document does not say, and there is no third. The
// asterisk merges into a run with the delimiter beside it; the underscore is
// inert where it lands, because a run against punctuation cannot close in front
// of a word character. `after` reads the neighbouring text unescaped on
// purpose, which makes the refusal wider than it strictly has to be and never
// narrower.
//
// Neither is rescued by writing the span narrower, which is what renderChoices
// does with `*0*~~*0*~~0` in the test below: the cut in `**a *b***c` falls on a
// space the mark covers, and a narrower span there would come back with the
// space unmarked.
//
// The alternative is what this package exists not to do: write it down anyway
// and hope. A refusal names the construct and offers --raw-body; the shape the
// old scanner produced for these was a paragraph full of asterisks and exit 0.
func TestEmphasisTheWriterCannotSpellIsRefused(t *testing.T) {
	for _, src := range []string{
		"**a *b***c",
		"foo******bar*********baz",
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
