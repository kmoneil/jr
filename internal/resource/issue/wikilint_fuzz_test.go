package issue

import (
	"strings"
	"testing"
)

// FuzzWikiFindingsIsQuietUnlessItHasSomethingToSay is two properties.
//
// The first is what a fuzzer is for: this walks bytes with hand-rolled index
// arithmetic, including a nested loop that advances the outer index, and it
// must not panic or run away on any input a server or a caller can produce.
//
// The second is the one that keeps the feature worth having. Text with no brace
// in it cannot be ambiguous about braces, and a warning that fires on ordinary
// prose is worse than no warning at all, because it teaches the reader to skip
// the channel.
func FuzzWikiFindingsIsQuietUnlessItHasSomethingToSay(f *testing.F) {
	for _, seed := range []string{
		"",
		"ordinary prose with no markup at all",
		`{{/subjects/{subject\}}}`,
		"{code}\nif (x) {{{ y }}}\n{code}",
		"{code:go}\nfunc main() {}\n",
		"{noformat}raw{noformat}",
		"{{make test}}",
		"{{{{",
		"}}}}",
		"{}{}{}{}",
		"{code",
		"{code:",
		"{",
		"}",
		"{{",
		"h3. Heading\n* bullet\n# numbered",
		"{color:red}text{color}",
		"{panel:title=x}body{panel}",
		strings.Repeat("{", 500),
		strings.Repeat("{code}", 200),
		"\x00{{\x00}}\x00",
		"é{{é}}é",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := wikiFindings(s)

		if !strings.ContainsAny(s, "{}") && len(got) != 0 {
			t.Errorf("text with no brace produced %q", got)
		}
		// Every finding is shown to a person and has to read as a sentence
		// about their text, not as an empty string on stderr.
		for _, f := range got {
			if strings.TrimSpace(f) == "" {
				t.Errorf("an empty finding for %q", s)
			}
		}
	})
}
