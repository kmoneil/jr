package render

import (
	"io"
	"strconv"
	"strings"
)

const xmlDecl = `<?xml version="1.0" encoding="UTF-8"?>`

// writeXML emits the result envelope described in docs/output-contract.md.
func writeXML(w *writer, d *Doc) {
	w.line(0, xmlDecl)

	root := El("result").
		Attr("kind", d.Kind).
		Attr("v", strconv.Itoa(d.Version))

	switch {
	case d.Record != nil:
		root.Child(d.Record)
	default:
		c := d.Collection
		container := El(c.Name).
			Attr("count", strconv.Itoa(len(c.Items))).
			Attr("complete", strconv.FormatBool(c.Complete))
		for _, it := range c.Items {
			container.Child(it)
		}
		// Present if and only if the result set was truncated and the server
		// offered a cursor to resume from.
		container.LeafIf("next-page-token", c.NextPageToken)
		root.Child(container)
	}

	writeXMLNode(w, root, 0)
}

// writeXMLDiagnostic emits a stderr diagnostic: an <error> or a <warning>.
func writeXMLDiagnostic(w *writer, n *Node) {
	w.line(0, xmlDecl)
	writeXMLNode(w, n, 0)
}

func writeXMLNode(w *writer, n *Node, depth int) {
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(n.Name)
	for _, a := range n.Attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteString(`="`)
		b.WriteString(escapeXMLAttr(a.Value))
		b.WriteByte('"')
	}

	// An element with neither text nor children collapses to a self-closing
	// tag: <assignee id="712020:8f3a" display="Ada Lovelace"/>.
	if n.Text == "" && len(n.Children) == 0 {
		b.WriteString("/>")
		w.line(depth, b.String())
		return
	}

	// A leaf with text stays on one line unless it is mixed content, which is
	// emitted verbatim inside a CDATA section.
	if len(n.Children) == 0 {
		b.WriteByte('>')
		if n.CDATA {
			w.line(depth, b.String())
			w.raw("<![CDATA[\n")
			w.raw(escapeCDATA(n.Text))
			w.raw("\n]]>\n")
			w.line(depth, "</"+n.Name+">")
			return
		}
		b.WriteString(escapeXMLText(n.Text))
		b.WriteString("</")
		b.WriteString(n.Name)
		b.WriteByte('>')
		w.line(depth, b.String())
		return
	}

	b.WriteByte('>')
	w.line(depth, b.String())
	if n.Text != "" {
		w.line(depth+1, escapeXMLText(n.Text))
	}
	for _, c := range n.Children {
		writeXMLNode(w, c, depth+1)
	}
	w.line(depth, "</"+n.Name+">")
}

var xmlTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// xmlAttrEscaper also escapes the whitespace an XML parser would otherwise
// normalize to a space, so an attribute round-trips byte-for-byte.
var xmlAttrEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
	"\t", "&#9;",
	"\n", "&#10;",
	"\r", "&#13;",
)

func escapeXMLText(s string) string { return xmlTextEscaper.Replace(s) }
func escapeXMLAttr(s string) string { return xmlAttrEscaper.Replace(s) }

// escapeCDATA splits any literal "]]>" across two CDATA sections, which is the
// only way to carry that sequence inside one.
func escapeCDATA(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

// writer wraps an io.Writer with indentation and a sticky error, so the node
// walk stays branch-free.
type writer struct {
	w   io.Writer
	err error
}

func (w *writer) raw(s string) {
	if w.err != nil {
		return
	}
	_, w.err = io.WriteString(w.w, s)
}

func (w *writer) line(depth int, s string) {
	w.raw(strings.Repeat("  ", depth))
	w.raw(s)
	w.raw("\n")
}
