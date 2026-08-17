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
	// Absent rather than empty for a command that reached no site. An empty
	// attribute would read as "a site that is the empty string".
	if d.Site != "" {
		root.Attr("site", d.Site)
	}

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
		// And this one is present when there is a *next answer* to ask for,
		// which a complete result can have and a truncated one still does.
		container.LeafIf("next-since-token", c.NextSinceToken)
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
	//
	// Nothing is added around the section. A newline after `<![CDATA[` and one
	// before `]]>` used to frame the value, and both are element content: a
	// consumer parsing <description> got "\n\n" ahead of the text, and "\n\n"
	// plus the closing tag's indentation behind it. The contract describes this
	// element as `<text name="text" type="string"/>` and promises the value is
	// never escaped beyond XML minimums, so framing made every description,
	// comment body, worklog comment, and dry-run body the one place a value did
	// not survive its own round trip. An attribute always has; plain element
	// text always has.
	//
	// It cost a human reading raw XML a blank line before a fenced code block.
	// That is the audience this tool orders last, and it is the only thing the
	// framing bought.
	if len(n.Children) == 0 {
		b.WriteByte('>')
		if n.CDATA {
			w.raw(strings.Repeat("  ", depth))
			w.raw(b.String())
			w.raw("<![CDATA[")
			w.raw(escapeCDATA(n.Text))
			w.raw("]]></" + n.Name + ">\n")
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

// xmlTextEscaper covers the three characters that break the markup, plus the
// one that survives the write and not the parse.
//
// A carriage return is legal in XML — it is in the Char production, which is
// why `renderable` permits it — and it still does not round-trip. §2.11 requires
// a processor to translate #xD#xA and any lone #xD to #xA *before parsing*, so
// a raw CR on the wire reaches the consumer as a newline and the value it read
// is not the value Jira holds. `&#13;` is applied after that normalization and
// is the only spelling that survives.
//
// The attribute escaper below has always done this, for the same reason and
// with a comment saying so. Element text did not, so `--format json` returned
// "before\rafter" and `--format xml` parsed back as "before\nafter" — the same
// value meaning two things depending on a flag, which is the axis `renderable`
// exists to keep the formats on.
//
// Newline and tab are deliberately not escaped here. Neither is altered by
// §2.11 in element content — a newline is already a newline — and escaping them
// would make every multi-line description unreadable for no fidelity gained.
// The attribute escaper does escape them, because attribute-value normalization
// is a separate rule that turns both into a space.
var xmlTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\r", "&#13;",
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

// cdataEscaper carries the two sequences a CDATA section cannot hold.
//
// `]]>` ends the section, so it is split across two: `]]` finishes the first,
// `<![CDATA[` opens the second, and `>` starts it. The two halves concatenate
// back to `]]>` as text.
//
// A carriage return is the same problem wearing different clothes, and it is
// easy to miss because a CDATA section is otherwise verbatim. §2.11's line-end
// normalization runs on the raw input *before* parsing, so it applies inside
// CDATA too — and a numeric reference inside CDATA is not a reference, it is
// five literal characters. There is no escape available within a section, so
// the section has to end: close it, emit `&#13;` as ordinary element content
// where a reference does work, and open a new one.
//
// One Replacer for both, deliberately. It scans the source once and never
// re-examines what it inserted, so the `<![CDATA[` a CR replacement emits
// cannot be mistaken for source text by the `]]>` rule, and a `]]>` sitting
// beside a CR is handled without either rewriting the other's output.
var cdataEscaper = strings.NewReplacer(
	"]]>", "]]]]><![CDATA[>",
	"\r", "]]>&#13;<![CDATA[",
)

// escapeCDATA makes text safe to place inside a CDATA section, opening and
// closing further sections where the text cannot be carried by one.
func escapeCDATA(s string) string { return cdataEscaper.Replace(s) }

// writer wraps an io.Writer with indentation and a sticky error, so the node
// walk stays branch-free.
type writer struct {
	w   io.Writer
	err error
	// normalize rewrites every string on its way out, for a format that has one
	// rule applying to all of its output rather than to particular fields.
	//
	// It exists for markdown, which must not put a carriage return on a
	// terminal and has six places that write data-derived text — a table cell,
	// a title, a node's own text, a CDATA section, a list item, a diagnostic
	// field. Fixing six sites is how a seventh gets added without the rule, and
	// this project has already found that the copy which deviates is the bug.
	// raw is the single funnel every one of them goes through.
	//
	// Nil for the four contract formats, which escape per field because what is
	// legal depends on where the value sits: a tab is fine in XML element text
	// and must be a reference in an attribute.
	normalize func(string) string
}

func (w *writer) raw(s string) {
	if w.err != nil {
		return
	}
	if w.normalize != nil {
		s = w.normalize(s)
	}
	_, w.err = io.WriteString(w.w, s)
}

func (w *writer) line(depth int, s string) {
	w.raw(strings.Repeat("  ", depth))
	w.raw(s)
	w.raw("\n")
}
