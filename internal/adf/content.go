package adf

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// This file is this package's answer to "are these the same document".
//
// It is not equality of the trees, and it is not `reflect.DeepEqual`. Three
// differences between two ADF documents are not differences in what they say,
// and a tree comparison reports all three:
//
//   - Mark order is an artefact of how the text was typed.
//   - Adjacent text nodes carrying the same marks are one run of content.
//   - A mark on whitespace cannot be written down. renderSpan moves edge
//     whitespace outside its span on purpose, because markdown cannot
//     emphasise a space, so counting that as a difference would be measuring
//     the design rather than a defect. Whitespace is projected with no marks.
//
// What is left is the thing a reader would notice: this character was bold and
// now it is not, this cell was here and now it is gone.
//
// It lived in survival_test.go first, which is where the definition was worked
// out and where `docs/invariants.md` cites it. It is product code now because
// `settle` needs the same question answered on every conversion, and the one
// thing this package cannot afford is two definitions of the same word in two
// files. The test still uses this one, through export_test.go, so the golden
// and the writer are held to a single answer by construction.

// contentKey reduces a document to what markdown is able to carry: which marks
// each non-whitespace character holds, in order, inside which blocks.
func contentKey(n Node) string {
	var b strings.Builder
	projectContent(n, &b)
	return b.String()
}

func projectContent(n Node, b *strings.Builder) {
	if n.Type == "text" {
		marks := markKey(n.Marks)
		for _, r := range n.Text {
			if unicode.IsSpace(r) {
				fmt.Fprintf(b, "%q\n", r)
				continue
			}
			fmt.Fprintf(b, "%q %s\n", r, marks)
		}
		return
	}
	if n.Type != "doc" {
		fmt.Fprintf(b, "<%s %s\n", n.Type, attrKey(n.Attrs))
	}
	for _, c := range n.Content {
		projectContent(c, b)
	}
	if n.Type != "doc" {
		fmt.Fprintf(b, ">%s\n", n.Type)
	}
}

// markKey is a mark set in an order that does not depend on how it was typed.
func markKey(marks []Mark) string {
	keys := make([]string, 0, len(marks))
	for _, m := range marks {
		raw, err := json.Marshal(m)
		if err != nil {
			keys = append(keys, m.Type)
			continue
		}
		keys = append(keys, string(raw))
	}
	sort.Strings(keys)
	// Joined on a separator JSON cannot contain, so a caller can split the set
	// back apart. A comma is in every mark that carries attributes.
	return strings.Join(keys, "\x1f")
}

// attrKey is a node's attributes, minus the two that are not content.
//
// localId is editor state that FromMarkdown renumbers per document, and an
// attribute whose value is the empty string is how ADF spells absent in the
// places this converter round-trips through a URI.
func attrKey(attrs map[string]any) string {
	if len(attrs) == 0 {
		return ""
	}
	kept := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if k == "localId" || v == "" {
			continue
		}
		kept[k] = v
	}
	if len(kept) == 0 {
		return ""
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return fmt.Sprintf("%v", kept)
	}
	return string(raw)
}
