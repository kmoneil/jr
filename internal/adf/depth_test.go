package adf_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
)

// TestADeeplyNestedDocumentIsRefusedRatherThanFollowed pins a bound this
// package relies on and does not own.
//
// adf.Node is recursive and both directions of the conversion recurse with it:
// Parse decodes `Content []Node` through encoding/json, and ToMarkdown walks
// the result through blockList and block. Neither has a depth limit of its own,
// which reads like a stack overflow waiting for a server that sends one.
//
// It is not, and this test is the measurement rather than the assumption.
// encoding/json's scanner refuses past its own nesting cap, so Parse answers
// MALFORMED_ADF in well under a millisecond and ToMarkdown never sees the
// document. Every call site reaching ToMarkdown comes through Parse, so there
// is no way around it.
//
// The bound is real and it belongs to the standard library, which is exactly
// why it is worth a test: nothing in this package says it depends on that, and
// a future decoder with a different cap would remove it silently. This fails if
// that happens, which is the whole of what it is for.
func TestADeeplyNestedDocumentIsRefusedRatherThanFollowed(t *testing.T) {
	// Far past the decoder's cap, so this stays true if the cap moves. Under a
	// megabyte of input, and it is refused in microseconds.
	doc, err := adf.Parse(nestedQuotes(50000))
	if err == nil {
		// Not a failure by itself: a decoder that accepted it would have to be
		// held to what the conversion then does, which is the other half of the
		// property and the reason this does not simply assert an error.
		if _, cerr := adf.ToMarkdown(doc); cerr == nil {
			t.Fatal("50000 levels of nesting were decoded and converted; " +
				"the recursion in this package is bounded by encoding/json and " +
				"by nothing else, so a decoder that accepts this needs a depth " +
				"limit here")
		}
		return
	}

	if code := errs.Coerce(err).Code; code != "MALFORMED_ADF" {
		t.Errorf("code = %q, want MALFORMED_ADF", code)
	}
}

// TestANormallyNestedDocumentStillConverts is the control. A refusal that
// applied to real documents would pass the test above and break the tool.
//
// A thousand levels is far past anything an editor produces: Jira's own
// containment rules allow nesting only through lists and table cells, and its
// editor caps list indentation well below this.
func TestANormallyNestedDocumentStillConverts(t *testing.T) {
	doc, err := adf.Parse(nestedQuotes(1000))
	if err != nil {
		t.Fatalf("a document 1000 levels deep was refused: %v", err)
	}
	out, err := adf.ToMarkdown(doc)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "x") {
		t.Errorf("the innermost paragraph did not survive:\n%.120s", out)
	}
}

// nestedQuotes builds a document whose only content is one paragraph at the
// bottom of depth blockquotes.
func nestedQuotes(depth int) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"doc","version":1,"content":[`)
	for range depth {
		b.WriteString(`{"type":"blockquote","content":[`)
	}
	b.WriteString(`{"type":"paragraph","content":[{"type":"text","text":"x"}]}`)
	for range depth {
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}
