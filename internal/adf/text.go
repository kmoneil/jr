package adf

import (
	"strings"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
)

// Version is the ADF schema version this package emits.
const Version = 1

// FromText encodes plain text as an ADF document.
//
// This is not markdown conversion, and the distinction is the whole point.
// Cloud's v3 API will not take a string where a document belongs, so text has
// to be *contained* in one — but containing it is exact, while interpreting it
// is not. Nothing here reads `**bold**` as anything but six characters, which
// is also what Data Center does with the same input: it goes to the server as
// typed and the server decides what it means.
//
// Turning markdown into real ADF marks is a different job with its own
// failure modes, and it lives in the write-side subset this package will grow.
//
// A blank line starts a paragraph. A single newline is a line break inside
// one, because collapsing it would join two lines the caller separated, and
// promoting it to a paragraph would add spacing they did not ask for.
func FromText(text string) (Node, error) {
	if !utf8.ValidString(text) {
		// Refused rather than replaced. Substituting U+FFFD would put a
		// character in Jira that the caller never wrote, and they would have no
		// way to know it happened.
		return Node{}, errs.Usage("INVALID_ENCODING", "the text is not valid UTF-8").
			WithRemedy("check the file or pipe it came from")
	}

	doc := Node{Type: "doc", Version: Version}

	// \r\n first, so a file written on Windows does not leave a stray carriage
	// return inside every line.
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	for block := range strings.SplitSeq(normalized, "\n\n") {
		doc.Content = append(doc.Content, paragraph(block))
	}
	return doc, nil
}

// paragraph builds one paragraph, with a hard break for each newline inside it.
func paragraph(block string) Node {
	p := Node{Type: "paragraph"}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if i > 0 {
			p.Content = append(p.Content, Node{Type: "hardBreak"})
		}
		if line != "" {
			// An empty line contributes its break and no text node: ADF has no
			// text node with an empty string, and a server that accepts one is
			// not one to rely on.
			p.Content = append(p.Content, Node{Type: "text", Text: line})
		}
	}
	return p
}

// PlainText extracts the text from an ADF document, for the places that need a
// scalar rather than a tree.
//
// It is deliberately lossy and says so: every mark, link target, and embedded
// media is dropped, and block structure becomes newlines. It exists for a TSV
// cell and a one-line summary, never for round-tripping — a value that went
// through here must not be sent back as if it were the original.
func PlainText(n Node) string {
	var b strings.Builder
	writeText(&b, n)
	return strings.TrimSpace(b.String())
}

func writeText(b *strings.Builder, n Node) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
	case "hardBreak":
		b.WriteString("\n")
	case "paragraph", "heading", "blockquote", "codeBlock":
		for _, c := range n.Content {
			writeText(b, c)
		}
		// A blank line, because that is what FromText reads back as a paragraph
		// break. One newline would mean the two functions disagreed about what
		// a paragraph is, and text that went out through one and came back
		// through the other would gain or lose structure.
		b.WriteString("\n\n")
		return
	case "listItem":
		for _, c := range n.Content {
			writeText(b, c)
		}
		b.WriteString("\n")
		return
	}
	for _, c := range n.Content {
		writeText(b, c)
	}
}
