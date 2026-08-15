package adf_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
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
// FromMarkdown runs before it returns: a span whose delimiter would merge into
// a run with the character beside it has two spellings and both are read as
// something the document does not say, so there is no third and it is refused.
// `after` reads the neighbouring text unescaped on purpose, which makes the
// refusal wider than it strictly has to be and never narrower.
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
