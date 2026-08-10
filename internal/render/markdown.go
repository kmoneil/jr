//go:build render

package render

import (
	"strconv"
	"strings"
)

// Markdown is a human-readable rendering of a result.
//
// It is **not** part of the output contract. The other four formats are
// versioned, goldened, and safe to parse; this one exists so a person reading
// an issue on a terminal does not have to read XML, and it may change in any
// release. docs/output-contract.md says so in the same words.
//
// That makes it the one format with no stability promise, which is a real
// exception in a project whose premise is that output shape is a public API.
// Two things keep the exception contained: it is never a default, so nothing
// receives it without asking, and it only exists in a build carrying the
// `render` tag, so the agent, reader, and ci profiles cannot emit it at all.
const Markdown Format = "markdown"

func init() { registerFormat(Markdown, writeMarkdown, writeMarkdownDiagnostic) }

// registerFormat adds a format that only some builds carry.
//
// It appends to `formats`, which is what ParseFormat, FormatNames, and
// isKnownFormat all read — so a build without this tag refuses the name, omits
// it from `--format`, and does not advertise it in the MCP tool schema, with no
// branch anywhere saying so.
//
// One call sets the list entry and both writers, so those cannot disagree. It
// lives here rather than beside the maps in render.go because an untagged build
// has nothing to register and `make lint-untagged` correctly calls an unused
// function unused. If a second tagged format ever arrives behind a different
// tag, this moves to a file carrying both tags rather than being duplicated.
func registerFormat(f Format, doc func(*writer, *Doc), diagnostic func(*writer, *Node)) {
	formats = append(formats, f)
	extraDoc[f] = doc
	extraDiagnostic[f] = diagnostic
}

// lineEndings collapses a carriage return into the newline it means.
//
// This is the one place markdown alters a value, and it is a *presentation*
// decision rather than a fidelity one. §3.2 puts this format outside the
// contract and says not to parse it, so nothing here is being mis-read — but a
// terminal reading a CR returns the cursor to column 0, and what follows
// overwrites what came before. "Closed as duplicate\rDO NOT MERGE" displays as
// the second half alone, with the first half present in the data and absent
// from the screen. The source is an issue summary, written by whoever can file
// a ticket.
//
// Normalising rather than refusing, because UNRENDERABLE_VALUE is for a
// character no format can carry and four of the five carry this one perfectly.
// A format with no promises must not be the thing that fails a command. And
// rather than escaping, because a `\r` in the middle of prose is noise to the
// reader this format exists for.
//
// This is markdown's second documented lossy case, beside a leaf that carries
// both text and attributes rendering only the text. Both have the same shape of
// justification: what is dropped here is intact in the other four formats,
// which is where anything parsing should be looking.
//
// CRLF is listed first so the pair collapses to one newline rather than two.
// A Replacer matches the earlier pattern at a given position.
var lineEndings = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// writeMarkdown emits a document as markdown.
//
// One writer covers every kind, because a Doc is a tree of Nodes and nothing
// here knows what an issue is — the same reason the XML writer needs no
// per-kind code.
func writeMarkdown(w *writer, d *Doc) {
	w.normalize = lineEndings.Replace
	if d.Record != nil {
		writeMarkdownRecord(w, d.Record, 1)
		return
	}
	writeMarkdownCollection(w, d)
}

// writeMarkdownCollection emits a table of the declared columns, or a section
// per item when the items carry documents.
//
// The columns are the ones TSV uses, so the two agree about what a row is and a
// caller switching between them sees the same fields in the same order.
func writeMarkdownCollection(w *writer, d *Doc) {
	c := d.Collection

	w.raw("# " + c.Name + "\n\n")
	w.raw(countLine(c) + "\n\n")

	if len(c.Items) == 0 {
		// A heading and a count with no table under it reads as a rendering
		// bug. Say the thing that is true instead.
		w.raw("_No rows._\n")
		return
	}

	if holdsDocuments(c) {
		for _, item := range c.Items {
			writeMarkdownRecord(w, item, 2)
		}
		return
	}

	headers := make([]string, 0, len(c.Columns))
	for _, col := range c.Columns {
		headers = append(headers, col.Header)
	}
	w.raw(markdownRow(headers))
	w.raw(markdownRule(len(headers)))

	for _, item := range c.Items {
		row := make([]string, 0, len(c.Columns))
		for _, col := range c.Columns {
			v, _ := item.Lookup(col.Path)
			row = append(row, v)
		}
		w.raw(markdownRow(row))
	}
}

// holdsDocuments reports whether a collection's items carry mixed content, in
// which case a table is the wrong shape for them.
//
// `issue comment list` was rendering a whole thread into one cell — newlines
// replaced by `<br>`, the code fence destroyed — and the escaping was correct:
// a raw newline ends the row and a raw pipe ends the cell. The defect was one
// layer up, in assuming rows are short, which every collection tested when this
// writer was written happens to be.
//
// The record path already handles a CDATA child properly, as its own section
// with the markdown verbatim. So the two paths disagreed about the same node
// shape, and this is which one wins.
//
// It reads the data rather than a list of command names, and that is deliberate.
// `issue list` rows carry no CDATA today — `DefaultFields()` has no description,
// and `--field description` adds nothing because ExtraFieldNames drops what the
// package already models — so the flip cannot fire there by surprise. If one
// ever did carry prose, sections would be the right answer rather than a
// regression, because a table still could not hold it.
func holdsDocuments(c *Collection) bool {
	for _, item := range c.Items {
		for _, child := range item.Children {
			if child.CDATA {
				return true
			}
		}
	}
	return false
}

// countLine states the completeness of a result set in words.
//
// `complete="false"` is the single most important thing a collection carries,
// and a reader skimming a table will not notice an attribute they cannot see.
// It is spelled out, and a truncated result says how to get the rest.
func countLine(c *Collection) string {
	n := strconv.Itoa(len(c.Items))
	if c.Complete {
		return n + " rows, complete."
	}
	line := "**" + n + " rows, TRUNCATED** — this is not the whole result set."
	if c.NextPageToken != "" {
		line += " Resume with `--page-token " + c.NextPageToken + "`."
	}
	return line
}

// writeMarkdownRecord emits one node: a heading, a table of its scalar values,
// then a section per child that has structure of its own.
//
// depth is the heading level, so a nested node reads as a subsection rather
// than as a second document.
func writeMarkdownRecord(w *writer, n *Node, depth int) {
	w.raw(strings.Repeat("#", depth) + " " + markdownTitle(n) + "\n\n")

	// Attributes and leaf children are both "a name and a value" to a reader,
	// however differently they are spelled in XML.
	type pair struct{ name, value string }
	var scalars []pair
	var sections []*Node

	for _, a := range n.Attrs {
		scalars = append(scalars, pair{a.Name, a.Value})
	}
	for _, c := range n.Children {
		switch {
		case c.CDATA, c.ListOf != "", len(c.Children) > 0:
			sections = append(sections, c)
		default:
			scalars = append(scalars, pair{c.Name, markdownScalar(c)})
		}
	}

	if len(scalars) > 0 {
		w.raw(markdownRow([]string{"Field", "Value"}))
		w.raw(markdownRule(2))
		for _, s := range scalars {
			w.raw(markdownRow([]string{s.name, s.value}))
		}
		w.raw("\n")
	}

	// A node's own text, which is not any of its children. Emitted after the
	// table because it is the node's value and the table describes it.
	if n.Text != "" {
		w.raw(n.Text + "\n\n")
	}

	for _, s := range sections {
		if s.ListOf != "" {
			writeMarkdownList(w, s, depth+1)
			continue
		}
		if s.CDATA {
			// The text is already markdown — internal/adf converted it on the
			// way in — so it goes out verbatim. Escaping it here would show a
			// reader the backslashes instead of the formatting, which is the
			// whole reason this format exists.
			w.raw(strings.Repeat("#", depth+1) + " " + s.Name + "\n\n")
			w.raw(strings.TrimRight(s.Text, "\n") + "\n\n")
			continue
		}
		writeMarkdownRecord(w, s, depth+1)
	}
}

// writeMarkdownList renders a homogeneous list as a list.
//
// A list container has a count attribute and N identically named children, so
// the generic Field/Value table rendered it as `| count | 2 |` followed by two
// rows both called `label` — a two-column table with duplicate keys, which is
// not what a table means. An empty one was worse: no children and no text made
// it look like a scalar, and it came out as `| components |  |`, which reads as
// a field that exists and is blank rather than a list with nothing in it.
//
// Both were found by generating the goldens and reading them, which is the
// argument for goldening a format nobody promises to keep stable.
func writeMarkdownList(w *writer, n *Node, depth int) {
	w.raw(strings.Repeat("#", depth) + " " + n.Name + "\n\n")

	if len(n.Children) == 0 {
		w.raw("_None._\n\n")
		return
	}
	for _, item := range n.Children {
		if len(item.Children) > 0 {
			// An item with structure of its own is a record, not a line.
			writeMarkdownRecord(w, item, depth+1)
			continue
		}
		w.raw("- " + markdownListItem(item) + "\n")
	}
	w.raw("\n")
}

// markdownScalar reduces a leaf to the one value a reader wants from it.
//
// Text wins when there is text: `<status category="in-progress">In
// Progress</status>` reads as "In Progress", because the text is what a person
// is reading for and the attribute is the machine's half of the same fact.
// That is the one place this format is lossy against the other four, and the
// reason it is presentation rather than contract — every attribute is still in
// XML, JSON, and YAML, which is where anything parsing should be looking.
//
// A leaf with attributes and *no* text is the case that made this a function.
// `<author display="Ada Lovelace"/>` has nothing in Text, and rendering that
// gave `| author |  |` — a field that exists and is blank, when the name was
// sitting in an attribute. Third time this shape has bitten: the tag
// descriptions in `jr version` and the empty list container were the others.
func markdownScalar(n *Node) string {
	if n.Text != "" {
		return n.Text
	}
	return markdownListItem(n)
}

// markdownListItem renders one list entry that has no children of its own.
//
// An item carrying attributes used to be sent to writeMarkdownRecord, which
// gave `jr version --format markdown` eight `### tag tui` headings each
// containing a one-row table — and dropped every tag's description, because it
// is the node's text and that function rendered only attributes. Found by
// running the binary; the goldens had no node shaped like that.
func markdownListItem(n *Node) string {
	labels := make([]string, 0, len(n.Attrs))
	for _, a := range n.Attrs {
		if a.Value != "" {
			labels = append(labels, a.Value)
		}
	}
	label := strings.Join(labels, " — ")

	switch {
	case label == "":
		return n.Text
	case n.Text == "":
		return label
	default:
		return "**" + label + "** " + n.Text
	}
}

// markdownTitle names a node: its element, plus its first attribute value when
// it has one, which is nearly always the key or id a reader is looking for.
func markdownTitle(n *Node) string {
	if len(n.Attrs) > 0 && n.Attrs[0].Value != "" {
		return n.Name + " " + n.Attrs[0].Value
	}
	return n.Name
}

// writeMarkdownDiagnostic emits an error or a warning.
//
// A format that renders results and not errors is half a format: a caller who
// passed --format markdown and hit a 404 would get a diagnostic in some other
// shape, or none.
func writeMarkdownDiagnostic(w *writer, n *Node) {
	// Set here as well as in writeMarkdown, because a diagnostic does not go
	// through it. An error's detail carries the offending value, which is
	// exactly the string most likely to hold what somebody put in a field.
	w.normalize = lineEndings.Replace
	w.raw("# " + n.Name + "\n\n")
	for _, c := range n.Children {
		if c.Text == "" {
			continue
		}
		// Verbatim, deliberately. An earlier version escaped the characters
		// that start markup and turned NO_ISSUE into NO\_ISSUE — the code is
		// the one value here a caller greps for and pastes into a bug report,
		// and mangling it is worse than the accidental emphasis that escaping
		// prevents. There is no table to restructure outside a cell, so the
		// only cost is cosmetic and only in prose.
		w.raw("**" + c.Name + "** " + c.Text + "\n\n")
	}
}

// markdownRow renders one table row.
func markdownRow(cells []string) string {
	var b strings.Builder
	b.WriteString("|")
	for _, c := range cells {
		b.WriteString(" ")
		b.WriteString(escapeMarkdownCell(c))
		b.WriteString(" |")
	}
	b.WriteString("\n")
	return b.String()
}

// markdownRule is the header separator a table needs to be a table.
func markdownRule(n int) string {
	return "|" + strings.Repeat(" --- |", n) + "\n"
}

// escapeMarkdownCell makes a value safe inside a table cell.
//
// A pipe would end the cell and a newline would end the row, so a value
// containing either would silently restructure the table into one with
// different columns — the same class of defect as an unescaped separator in
// TSV, which is why escapeTSV exists.
//
// Deliberately not internal/adf's escapePipes, despite solving the same
// problem: that one escapes while *generating* markdown from an ADF document,
// this one escapes an arbitrary value on its way into a cell. Sharing would
// mean render imports adf, and both are foundation packages that stay leaves.
func escapeMarkdownCell(s string) string { return markdownCellEscaper.Replace(s) }

var markdownCellEscaper = strings.NewReplacer(
	`\`, `\\`,
	"|", `\|`,
	// A cell is one line. Control characters cannot reach here — renderable
	// refuses them before any writer runs — so only the two legal line breaks
	// need a spelling that keeps the row intact.
	"\n", "<br>",
	"\r", "",
)
