package adf_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/adf"
)

// TestPathologicalInputFinishes guards against a conversion that is quadratic
// in its input.
//
// Both directions take text somebody else wrote: a description comes from
// whoever can edit the issue, and a body comes from whoever ran the command.
// A parser that goes quadratic on a run of asterisks turns `issue get` into a
// hang on one issue, with no output and nothing to point at.
func TestPathologicalInputFinishes(t *testing.T) {
	cases := []struct{ name, markdown string }{
		{"deep quotes", strings.Repeat("> ", 5000) + "x"},
		{"asterisks", strings.Repeat("*", 20000)},
		{"backticks", strings.Repeat("`", 20000)},
		{"brackets", strings.Repeat("[", 20000)},
		{"pipes", strings.Repeat("|", 20000)},
		{"indented list", strings.Repeat("  ", 2000) + "- x"},
		{"wide table", strings.Repeat("| a ", 5000) + "|\n" + strings.Repeat("| --- ", 5000) + "|"},
		{"unclosed emphasis", strings.Repeat("*a ", 10000)},
		{"unclosed strike", strings.Repeat("~~a ", 10000)},
		{"escapes", strings.Repeat("\\*", 10000)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				doc, err := adf.FromMarkdown(c.markdown)
				if err != nil {
					return
				}
				// And back, because the read side takes the same abuse from a
				// description nobody here wrote.
				_, _ = adf.ToMarkdown(doc)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("converting %d bytes did not finish in 10s", len(c.markdown))
			}
		})
	}
}
