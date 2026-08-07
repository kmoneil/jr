package adf_test

import (
	"runtime"
	"strconv"
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

// markedParagraph builds a paragraph of n inline text nodes, every other one
// carrying a mark so each becomes its own span.
//
// Spans are the axis that matters. TestPathologicalInputFinishes covers runs of
// a pathological *character*, which is a different shape: it goes in through
// FromMarkdown and stresses the tokenizer. The quadratic this guards against
// was on the way out, in the span renderer, and needed many marked nodes rather
// than many characters — so every case in that table passed while
// `renderInline` was copying its whole output buffer once per span.
func markedParagraph(n int) adf.Node {
	content := make([]adf.Node, 0, n)
	for i := range n {
		word := strconv.Itoa(i)
		if i > 0 {
			// Leading rather than trailing: markdown cannot represent text
			// ending in a space, and ToMarkdown refuses it rather than
			// trimming.
			word = " " + word
		}
		var marks []adf.Mark
		if i%2 == 1 {
			marks = []adf.Mark{{Type: "em"}}
		}
		content = append(content, adf.Node{Type: "text", Text: word, Marks: marks})
	}
	return adf.Node{
		Type: "doc", Version: 1,
		Content: []adf.Node{{Type: "paragraph", Content: content}},
	}
}

// bytesToConvert reports how much ToMarkdown allocates for one document.
//
// TotalAlloc is cumulative and unaffected by collection, so the delta is the
// exact volume allocated rather than a sample of what survived. That is what
// makes this assertion deterministic enough to gate a build: it measures work
// done, not memory held, and it does not depend on the machine's speed.
func bytesToConvert(t *testing.T, doc adf.Node) uint64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := adf.ToMarkdown(doc); err != nil {
		t.Fatalf("convert: %v", err)
	}
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestConversionIsLinearInSpanCount is the guard TestPathologicalInputFinishes
// could not be.
//
// That test asks whether conversion finishes inside ten seconds, which a
// quadratic comfortably does at the sizes it uses — 20000 characters is 4×10^8
// byte copies, well under a second. A ceiling that generous cannot tell a
// quadratic from a linear one; only the shape of the curve can.
//
// So this doubles the input and looks at the ratio. Linear is 2, quadratic is
// 4, and the bar sits at 3 — far enough from 2 to absorb the constant factors
// and the fixed overhead, far enough from 4 to fail on the real thing. The
// version this replaced measured 3.8 at these sizes.
//
// Allocated bytes rather than wall clock, deliberately. A timing assertion on a
// shared CI runner is a flaky assertion, and it was bytes that carried the
// signal anyway: allocation *count* stayed exactly linear throughout, so a
// guard watching allocs/op would have reported the quadratic as clean.
func TestConversionIsLinearInSpanCount(t *testing.T) {
	const n = 400

	// Warm the paths once, so first-call costs land outside both measurements.
	if _, err := adf.ToMarkdown(markedParagraph(n)); err != nil {
		t.Fatalf("warm: %v", err)
	}

	small := bytesToConvert(t, markedParagraph(n))
	large := bytesToConvert(t, markedParagraph(2*n))
	if small == 0 {
		t.Fatal("measured no allocation; the conversion did not run")
	}

	ratio := float64(large) / float64(small)
	t.Logf("%d spans: %d bytes; %d spans: %d bytes; ratio %.2f",
		n, small, 2*n, large, ratio)
	if ratio > 3 {
		t.Errorf("doubling the inline nodes multiplied allocation by %.2f, "+
			"want about 2 — conversion is superlinear in the number of spans", ratio)
	}
}
