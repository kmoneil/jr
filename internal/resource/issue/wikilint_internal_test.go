package issue

import (
	"strings"
	"testing"
)

// The cases that must stay quiet matter more than the ones that fire. A warning
// channel is only worth having while everything in it is worth reading, and
// ordinary prose full of braces is the way to lose that.
func TestWikiFindings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		want  string // a substring of the expected finding, or "" for silence
		quiet bool
	}{
		{
			// The report. A balanced-count check passes this, because the
			// counts are balanced under both readings, which is the problem.
			name: "the reported construct",
			text: `{{/subjects/{subject\}}}, {{/subjects/{subject\}/versions}}`,
			want: "three or more braces",
		},
		{
			name: "an unclosed code block",
			text: "before\n{code:go}\nfunc main() {}\n",
			want: "{code} is opened and never closed",
		},
		{
			name: "an unclosed monospace span",
			text: "the value is {{config.yaml and then some prose",
			want: "monospace span is",
		},
		{
			// Inside a fence nothing is markup, so a brace run in a code
			// sample is literal. Firing here would fire on most issues that
			// paste a snippet, which is how a warning channel dies.
			name:  "a brace run inside a code block",
			text:  "{code}\nif (x) {{{ y }}}\n{code}\nprose after",
			quiet: true,
		},
		{
			name:  "a closed code block with parameters",
			text:  "{code:java}\nSystem.out.println();\n{code}",
			quiet: true,
		},
		{
			name:  "balanced monospace",
			text:  "run {{make test}} and then {{make lint}}",
			quiet: true,
		},
		{
			name:  "ordinary prose",
			text:  "Fix the importer so it stops dropping rows.\n\nh3. Background",
			quiet: true,
		},
		{
			name:  "a single brace pair is not monospace",
			text:  "the function is func() { return nil }",
			quiet: true,
		},
		{name: "empty", text: "", quiet: true},
		{
			name:  "noformat closed",
			text:  "{noformat}\nraw {{ text\n{noformat}",
			quiet: true,
		},
		{
			name: "noformat left open swallows the rest",
			text: "{noformat}\nraw text and no close",
			want: "{noformat} is opened and never closed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wikiFindings(tc.text)
			if tc.quiet {
				if len(got) != 0 {
					t.Errorf("want silence, got %q", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("want a finding containing %q, got none", tc.want)
			}
			if !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Errorf("findings %q do not mention %q", got, tc.want)
			}
		})
	}
}

// TestAnUnclosedFenceDoesNotAlsoReportBraces is about not piling on. An
// unclosed {code} already explains why everything after it looks wrong, and
// reporting the braces inside the region it swallowed as a second problem sends
// the reader to fix the wrong thing.
func TestAnUnclosedFenceDoesNotAlsoReportBraces(t *testing.T) {
	got := wikiFindings("{code}\nif (x) {{{ y }}}\nand no close")
	if len(got) != 1 {
		t.Fatalf("want exactly the fence finding, got %q", got)
	}
	if !strings.Contains(got[0], "never closed") {
		t.Errorf("got %q, want the unclosed fence", got[0])
	}
}
