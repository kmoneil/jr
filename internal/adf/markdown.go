package adf

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
)

// ToMarkdown converts an ADF document to markdown.
//
// The conversion is lossless or it is refused. Every node this function emits
// carries everything the ADF node held; a node that markdown cannot hold in
// full — a coloured span, a two-column layout, a table cell spanning three
// columns — is an error naming the construct, never a best effort that reads
// like the real thing. The caller keeps `--raw-body`, which emits the document
// exactly as Jira sent it, so refusing costs a flag and never the content.
//
// Some Jira constructs have no CommonMark or GFM spelling. Rather than drop
// them, each becomes a link with a documented scheme — `jira-user:`,
// `jira-media:`, `jira-status:`, `jira-date:` — and a panel becomes the alert
// syntax GitHub introduced. One rule with four cases is easier to rely on than
// three plus an exception, which is why a date is a link and not bare text.
// They are part of the output contract — see docs/output-contract.md.
//
// Presentation is not content. A panel's colour, an image's layout and width,
// and a status lozenge's local id say where something sits on a page, not what
// it says; markdown has no page, and those are dropped deliberately rather
// than refused. Everything that carries meaning is kept.
//
// The text it returns is a fixed point: reading it back and writing it again
// gives the same characters. See settle, which is where that is arranged and
// why it is not free.
func ToMarkdown(doc Node) (string, error) {
	md, err := write(doc)
	if err != nil {
		return "", err
	}
	return settle(doc, md), nil
}

// write converts the document once, which is what this function did under the
// name ToMarkdown until settling arrived above it.
//
// It is the unchecked half of the pair, and it has exactly two callers:
// ToMarkdown, which settles what it returns, and FromMarkdown, which asks it
// whether the document it just built can be carried back. Neither of those
// calls the other's public entry point, so there is no recursion here rather
// than a bounded one.
func write(doc Node) (string, error) {
	blocks, err := blockList(doc.Content, "doc")
	if err != nil {
		return "", err
	}
	return strings.Join(blocks, "\n\n"), nil
}

// settleRounds bounds the search below. The worst input anybody has measured
// needs three, and every one of the 1911 in the verdict corpus needs at most
// one, so eight is more than double the headroom over the only case that has
// ever exceeded the corpus.
const settleRounds = 8

// settle converts the text until it stops changing, and returns the first
// version if it never does or if settling would cost the document anything.
//
// One conversion of this package is not a fixed point. A mark on whitespace is
// dropped when that whitespace lands at the edge of a span, which is deliberate
// and documented, but which span an edge belongs to is decided while writing:
// two mark runs that overlap without nesting force a cut, the cut can leave a
// marked space at the head of what is left, and only one such space lands there
// per conversion. So a body somebody read out of `issue get` could differ from
// the body they got by piping it back in, with nothing to say which of the two
// was the answer.
//
// The nightly sweep of 2026-08-19 reported it as text still changing after two
// conversions, on a document with two marked spaces that needed three. That is
// the shape the fuzz target's allowance was built for and one conversion wider
// than it permits, and widening the allowance is not the answer: n marked
// spaces need n conversions, so no fixed number is the right one to allow.
//
// The check against contentKey is the whole of why this is safe, and it was
// not in the first version of this function.
//
// Settling looked free: over every accepted input in the verdict corpus six
// texts move, none of them loses a mark, and none changes the projection. That
// corpus is markdown-shaped, so it does not hold the documents markdown cannot
// spell, and one of those was three feet away in this package's own unit
// tests. An ADF text node holding a newline is written with the newline and the
// next line's `#` escaped; reading that back joins the lines with a space,
// because a soft break is a space in CommonMark, and **settling adopted the
// join.** `a\n# second line` came back as `a # second line` and the newline was
// gone. That is a real loss the writer has always had latently, and settling
// without this check would have made it the answer this function returns.
//
// So a conversion is only settled through when the document it reads back is
// the document it was given. What is shed on the way to a fixed point is then
// only ever a mark on a space, which `docs/output-contract.md` says moves
// outside its span and which contentKey counts as no content for that reason.
// Anything else and the first version stands, unchanged from what shipped
// before this existed.
//
// Returning the first version when it does not settle keeps today's behaviour
// for a document this cannot help, rather than picking an arbitrary point in a
// sequence that is still moving. The round-trip fuzzer still reports it, which
// is the signal that matters.
func settle(doc Node, md string) string {
	first := md
	var want string
	var known bool
	for range settleRounds {
		again, err := read(md)
		if err != nil {
			// The writer produced text its own reader cannot take. That is a
			// defect, and it is the round-trip property's to report: refusing
			// here would turn it into a failed `issue get` on a body the caller
			// can do nothing about.
			return first
		}
		next, err := write(again)
		if err != nil {
			return first
		}
		if next == md {
			return md
		}
		// Only here is a different text about to be adopted, and only here is
		// the anchor worth computing. Almost every document is already a fixed
		// point and leaves through the line above, so projecting it twice to
		// find that out was most of what this function cost. Timed over the
		// 166 real documents that convert, 200 reps each: 9.0x one conversion
		// at the median and 39us on the worst of them, against 4.3x and 20us
		// once the projection moved down here.
		if !known {
			want, known = contentKey(doc), true
		}
		if contentKey(again) != want {
			return first
		}
		md = next
	}
	return first
}

// unrepresentable is the one refusal in this file, so every construct markdown
// cannot hold reports the same way and points at the same escape hatch.
func unrepresentable(where, what string, args ...any) *errs.Error {
	return errs.Usage("ADF_UNREPRESENTABLE",
		"the document contains %s, which markdown cannot represent",
		fmt.Sprintf(what, args...)).
		WithDetail("at %s", where).
		WithRemedy("--raw-body emits the document exactly as Jira sent it")
}

// blockList converts a run of block nodes. An empty block is dropped rather
// than emitted as a blank one, because a paragraph with no content and no
// paragraph at all read identically in markdown.
func blockList(nodes []Node, where string) ([]string, error) {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		s, err := block(n, where+" > "+n.Type)
		if err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func block(n Node, where string) (string, error) {
	switch n.Type {
	case "paragraph":
		return inlineList(n.Content, where)

	case "heading":
		level, ok := attrInt(n.Attrs, "level")
		if !ok || level < 1 || level > 6 {
			return "", unrepresentable(where, "a heading at level %v", n.Attrs["level"])
		}
		text, err := inlineList(n.Content, where)
		if err != nil {
			return "", err
		}
		if text == "" {
			// `##` with nothing after it is a heading in markdown too, but an
			// empty one reads as stray punctuation. It is still the document.
			return strings.Repeat("#", int(level)), nil
		}
		return strings.Repeat("#", int(level)) + " " + escapeHeadingTail(text), nil

	case "blockquote":
		inner, err := blockList(n.Content, where)
		if err != nil {
			return "", err
		}
		return quote(strings.Join(inner, "\n\n")), nil

	case "panel":
		return panelBlock(n, where)

	case "rule":
		return "---", nil

	case "codeBlock":
		return codeBlock(n, where)

	case "bulletList", "orderedList":
		return list(n, where)

	case "taskList":
		return taskList(n, where)

	case "table":
		return table(n, where)

	case "mediaSingle", "mediaGroup":
		// Layout and width are dropped: they place an image on a page, and
		// markdown has no page. The image itself is carried in full.
		return inlineList(n.Content, where)

	case "blockCard", "embedCard":
		url, err := cardURL(n, where)
		if err != nil {
			return "", err
		}
		return autolink(url, where)

	case "expand", "nestedExpand":
		return "", unrepresentable(where, "a collapsible section")
	case "layoutSection", "layoutColumn":
		return "", unrepresentable(where, "a multi-column layout")
	case "decisionList", "decisionItem":
		return "", unrepresentable(where, "a decision list")
	case "extension", "bodiedExtension", "multiBodiedExtension":
		return "", unrepresentable(where, "a macro")
	}
	return "", unrepresentable(where, "a %q node", n.Type)
}

// panelTypes maps ADF's panel types onto the alert syntax GitHub introduced and
// several other renderers have since adopted.
//
// The type name is carried through rather than mapped onto GitHub's five, so
// the panel that comes back is the panel that went in. A renderer that does not
// know `[!SUCCESS]` shows a blockquote, which is what a panel is.
var panelTypes = map[string]string{
	"info":    "INFO",
	"note":    "NOTE",
	"warning": "WARNING",
	"success": "SUCCESS",
	"error":   "ERROR",
	"tip":     "TIP",
}

func panelBlock(n Node, where string) (string, error) {
	kind, _ := attrString(n.Attrs, "panelType")
	alert, ok := panelTypes[kind]
	if !ok {
		// A custom panel carries its own colour and icon, which is content a
		// reader acts on — "the red box" — and markdown has nowhere to put it.
		return "", unrepresentable(where, "a %q panel", kind)
	}
	inner, err := blockList(n.Content, where)
	if err != nil {
		return "", err
	}
	body := strings.Join(inner, "\n\n")
	if body == "" {
		return quote("[!" + alert + "]"), nil
	}
	return quote("[!" + alert + "]\n" + body), nil
}

// escapeHeadingTail escapes a heading's closing run of hashes.
//
// `# a #` is a heading whose text is "a": the trailing run is decoration and
// markdown strips it. A heading whose text really does end in a hash therefore
// has to escape it, or it comes back one character shorter every time.
func escapeHeadingTail(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == '#' {
		end--
	}
	if end == len(s) || (end > 0 && s[end-1] != ' ') {
		return s
	}
	return s[:end] + strings.Repeat(`\#`, len(s)-end)
}

// quote prefixes every line for a blockquote. A blank line keeps the bare `>`,
// because dropping it would end the quote and split one block into two.
func quote(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ">"
			continue
		}
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// codeBlock fences the code with enough of a delimiter to contain whatever run
// of it the code itself holds.
//
// The delimiter is a backtick unless the language name contains one, because a
// backtick fence's info string may not — a language of "a`b" written after
// ``` produces a fence this package cannot read back. A tilde fence takes
// anything but a line ending.
func codeBlock(n Node, where string) (string, error) {
	var body strings.Builder
	for _, c := range n.Content {
		// A codeBlock holds text nodes and nothing else. Marks inside one are
		// not rendered by Jira either, so the text is the content.
		body.WriteString(c.Text)
	}
	code := body.String()

	lang, _ := attrString(n.Attrs, "language")
	if strings.ContainsAny(lang, "\n\r") {
		// There is no fence at all for this: the info string is the rest of
		// one line by definition.
		return "", unrepresentable(where, "a code block whose language holds a line break")
	}

	char := byte('`')
	if strings.Contains(lang, "`") {
		char = '~'
	}
	fence := strings.Repeat(string(char), max(3, longestRun(code, char)+1))
	return fence + lang + "\n" + code + "\n" + fence, nil
}

func longestBacktickRun(s string) int { return longestRun(s, '`') }

func longestRun(s string, char byte) int {
	longest, run := 0, 0
	for i := range len(s) {
		if s[i] == char {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	return longest
}

func list(n Node, where string) (string, error) {
	ordered := n.Type == "orderedList"
	number := int64(1)
	if start, ok := attrInt(n.Attrs, "order"); ok && ordered {
		number = start
	}

	items := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		if item.Type != "listItem" {
			return "", unrepresentable(where+" > "+item.Type,
				"a %q where a list item belongs", item.Type)
		}
		inner, err := blockList(item.Content, where+" > listItem")
		if err != nil {
			return "", err
		}

		marker := "- "
		if ordered {
			marker = strconv.FormatInt(number, 10) + ". "
			number++
		}
		items = append(items, indent(strings.Join(inner, "\n\n"), marker,
			strings.Repeat(" ", len(marker))))
	}
	// A single newline between items: a blank line would make the list loose,
	// which renders with paragraph spacing the document did not ask for.
	out := strings.Join(items, "\n")

	// Three levels of empty single-item list render as `- - -`, which is a
	// thematic break and not a list at all. There is no other spelling, so it
	// is refused rather than written down as something else.
	for _, line := range strings.Split(out, "\n") {
		if isThematicBreak(line) {
			return "", unrepresentable(where, "a list markdown cannot tell from a rule")
		}
	}
	return out, nil
}

func taskList(n Node, where string) (string, error) {
	items := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		switch item.Type {
		case "taskItem":
		case "taskList":
			// A task list nested directly under another is how ADF spells an
			// indented subtask.
			nested, err := taskList(item, where+" > taskList")
			if err != nil {
				return "", err
			}
			items = append(items, indent(nested, "  ", "  "))
			continue
		default:
			return "", unrepresentable(where+" > "+item.Type,
				"a %q where a task belongs", item.Type)
		}

		text, err := inlineList(item.Content, where+" > taskItem")
		if err != nil {
			return "", err
		}
		box := "- [ ] "
		if state, _ := attrString(item.Attrs, "state"); state == "DONE" {
			box = "- [x] "
		}
		items = append(items, indent(text, box, strings.Repeat(" ", len(box))))
	}
	return strings.Join(items, "\n"), nil
}

// indent prefixes the first line with first and every later line with rest.
func indent(s, first, rest string) string {
	if s == "" {
		// An empty item is its marker and nothing else. Keeping the marker's
		// space would leave trailing whitespace on the line, which markdown
		// strips and this package refuses to write.
		return strings.TrimRight(first, " ")
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		switch {
		case i == 0:
			lines[i] = first + line
		case line == "":
			// No trailing whitespace on a blank line; it is invisible and some
			// tools strip it, so writing it makes two runs differ for nothing.
		default:
			lines[i] = rest + line
		}
	}
	return strings.Join(lines, "\n")
}

func table(n Node, where string) (string, error) {
	if len(n.Content) == 0 {
		return "", nil
	}

	rows := make([][]string, 0, len(n.Content))
	for _, row := range n.Content {
		if row.Type != "tableRow" {
			return "", unrepresentable(where+" > "+row.Type,
				"a %q where a table row belongs", row.Type)
		}
		cells, err := tableRow(row, where+" > tableRow")
		if err != nil {
			return "", err
		}
		rows = append(rows, cells)
	}

	// GFM's first row is its header; ADF's first row is a header only if it is
	// made of header cells. Promoting a body row would state that the document
	// has a header it does not, so the table is refused instead.
	for _, cell := range n.Content[0].Content {
		if cell.Type != "tableHeader" {
			return "", unrepresentable(where, "a table with no header row")
		}
	}

	width := len(rows[0])

	// A row wider than the header has nowhere to put the cells past that width.
	// The grid is implied by the pipes, so tableLine writes the header's number
	// of them and everything after it falls off the end of the same loop. It
	// did exactly that until 2026-08-18, with err nil and exit 0: three cells
	// of content under a one-cell header came out as one.
	//
	// GFM ignores the excess by definition, so there is no spelling to fall
	// back to, and widening the table would invent the header cells nobody
	// wrote. That is why this is refused and the short row is not: padding a
	// short row adds empty cells and invents nothing a reader sees, and
	// dropping a cell loses what somebody typed.
	//
	// This is also what refuses the markdown, and not through a second check.
	// FromMarkdown builds each row from its own pipe count with no reference to
	// the header, so a one-cell header over a two-cell body row is a document
	// it will happily build; its closing self-check calls this function and
	// gets this refusal. The two defects were hiding each other, which is why
	// neither showed: the parser kept a cell GFM discards, the writer dropped a
	// cell that existed, and the round trip came out looking clean.
	for i, row := range rows[1:] {
		if len(row) > width {
			return "", unrepresentable(
				fmt.Sprintf("%s > tableRow %d", where, i+2),
				"a table row of %d cells under a header row of %d",
				len(row), width)
		}
	}

	var b strings.Builder
	b.WriteString(tableLine(rows[0], width))
	b.WriteString("\n|")
	for range width {
		b.WriteString(" --- |")
	}
	for _, row := range rows[1:] {
		b.WriteString("\n")
		b.WriteString(tableLine(row, width))
	}
	return b.String(), nil
}

func tableRow(row Node, where string) ([]string, error) {
	cells := make([]string, 0, len(row.Content))
	for _, cell := range row.Content {
		if cell.Type != "tableHeader" && cell.Type != "tableCell" {
			return nil, unrepresentable(where+" > "+cell.Type,
				"a %q where a table cell belongs", cell.Type)
		}
		// A merged cell has no GFM spelling at all: the grid is implied by the
		// pipes, so a cell covering two columns cannot be written without
		// moving every cell after it into the wrong column.
		for _, span := range []string{"colspan", "rowspan"} {
			if n, ok := attrInt(cell.Attrs, span); ok && n != 1 {
				return nil, unrepresentable(where+" > "+cell.Type,
					"a table cell spanning %d by %s", n, strings.TrimSuffix(span, "span"))
			}
		}

		text, err := tableCell(cell, where+" > "+cell.Type)
		if err != nil {
			return nil, err
		}
		cells = append(cells, text)
	}
	return cells, nil
}

// tableCell converts one cell, which GFM can hold only if it is inline.
func tableCell(cell Node, where string) (string, error) {
	switch len(cell.Content) {
	case 0:
		return "", nil
	case 1:
		if cell.Content[0].Type == "paragraph" {
			text, err := inlineList(cell.Content[0].Content, where)
			if err != nil {
				return "", err
			}
			// A newline ends the row, so a hard break inside a cell becomes a
			// space — with its backslash, which is the break, removed rather
			// than left as a stray character.
			text = strings.ReplaceAll(text, "\\\n", " ")
			return strings.ReplaceAll(text, "\n", " "), nil
		}
	}
	return "", unrepresentable(where,
		"a table cell holding more than a single paragraph")
}

// escapePipes escapes the pipes a cell's own content holds, leaving alone any
// the text escaping already dealt with. A code span is verbatim and can hold a
// bare one, which would close the cell early.
func escapePipes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == '|' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// tableLine writes one row, padded to width so a short row does not silently
// become a different shape than the header it sits under.
func tableLine(cells []string, width int) string {
	var b strings.Builder
	b.WriteString("|")
	for i := range width {
		b.WriteString(" ")
		if i < len(cells) {
			b.WriteString(escapePipes(cells[i]))
		}
		b.WriteString(" |")
	}
	return b.String()
}

func inlineList(nodes []Node, where string) (string, error) {
	nodes = trimEdgeBreaks(coalesce(nodes))

	out, err := renderInline(nodes, nil, "", where, false)
	if isNoSpelling(err) {
		// Nothing in this run could be spelled while every span was also
		// keeping the asterisk clear for the span after it. That preference is
		// a guess about a decision nobody has made yet, so the run is written
		// again without it. See delimiterAfter, which is where it is given up.
		out, err = renderInline(nodes, nil, "", where, true)
	}
	if isNoSpelling(err) {
		// Both walks commit to the first spelling that works at each position
		// and never reconsider, and a span that is locally writable can leave
		// the rest of the run with nothing. So the run is searched rather than
		// walked, in the same order, which returns what the walk would have
		// returned if the walk could have gone back. See search.go: reaching
		// here at all is rare enough that it costs nothing measurable, because
		// the walk makes 1.00 attempts per span position at the median of both
		// corpora and 1.55 at the worst.
		for _, crowded := range []bool{false, true} {
			out, err = searchInline(nodes, nil, "", where, crowded)
			if !isNoSpelling(err) {
				break
			}
		}
	}
	if e, ok := errors.AsType[*noSpelling](err); ok {
		// Every spelling of some span in here was refused, so this one is about
		// the document after all. It becomes the ordinary refusal here, at the
		// only place that knows nothing else was left to try.
		return "", unrepresentable(e.where,
			"emphasis that markdown cannot spell unambiguously here")
	}
	if err != nil {
		return "", err
	}
	if err := checkEdgeSpace(out, where); err != nil {
		return "", err
	}
	return out, nil
}

// trimEdgeBreaks drops a line break at the very start or end of a block.
//
// Markdown has no way to write one — a trailing backslash there is a backslash,
// and a leading break is indentation — and neither renders as anything in Jira
// either, which is the same reason edge whitespace moves outside a span. It is
// what pressing shift-enter at the end of a paragraph leaves behind.
func trimEdgeBreaks(nodes []Node) []Node {
	for len(nodes) > 0 && nodes[0].Type == "hardBreak" {
		nodes = nodes[1:]
	}
	for len(nodes) > 0 && nodes[len(nodes)-1].Type == "hardBreak" {
		nodes = nodes[:len(nodes)-1]
	}
	return nodes
}

// spanMarks are the marks that become a span, outermost first.
//
// The order is fixed so two text nodes carrying the same marks in a different
// order produce the same markdown - Jira's order is an artefact of how the
// text was typed. code is not here: it is innermost and verbatim, so it is
// applied at the leaf.
var spanMarks = []string{"link", "strong", "em", "strike"}

// renderInline writes a run of inline nodes, grouping neighbours that share a
// mark into one span.
//
// ADF puts a mark on each text node; markdown wraps a span around several. A
// renderer that emitted each node independently produced
// **0 *****0*****0** for one bold sentence with a bold-italic word in it -
// five delimiter runs that no parser reads back as what they came from. Found
// by the round-trip fuzzer, which is the only thing that would have.
// written is everything emitted before this run, which is what says whether
// the next character starts a line. Escaping decided per node instead put a
// backslash in front of a hash that was mid-line and left one alone that was
// not — the two passes disagreed, which is how the fuzzer saw it.
func renderInline(nodes []Node, applied []Mark, written, where string,
	crowded bool,
) (string, error) {
	var b strings.Builder
	for i := 0; i < len(nodes); {
		choices := spanChoices(nodes, i, applied)
		if len(choices) == 0 {
			s, err := inline(nodes[i], atLineStart(written+b.String()), where+" > "+nodes[i].Type)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
			i++
			continue
		}
		span, j, err := renderChoices(choices, nodes, i, applied, written, &b, where, crowded)
		if err != nil {
			return "", err
		}
		b.WriteString(span)
		i = j
	}
	return b.String(), nil
}

// renderChoices writes the span at i the first way that reads back, and returns
// the index after it.
//
// Every choice describes the same document, so the first one that can be
// written down is the answer and the order is what keeps the output stable. The
// order is the best spelling first: the mark reaching furthest, at its full
// extent. What follows it is narrower, and narrower is a real loss where the
// span has edge whitespace, so it is only ever reached when the span before it
// has no spelling at all.
//
// `*0*~~*0*~~0` is the shape that needs the second attempt. It is em over two
// nodes with strike on the second, and the em cannot close after the strike's
// `~` and in front of a digit. Cutting the em back to the first node writes
// each of them as its own span, and the strike goes outside the second, which
// is what the input said in the first place.
func renderChoices(choices []spanChoice, nodes []Node, i int, applied []Mark,
	written string, b *strings.Builder, where string, crowded bool,
) (string, int, error) {
	var refused error
	for _, c := range choices {
		for j := c.j; j > i; j-- {
			// The full extent is never skipped, because a cut is what strands
			// a mark and there is no cut where the mark itself ends. So every
			// choice reaches renderSpan at least once, and the caller always
			// has either a span or a reason.
			if j < c.j && (strands(nodes, j) || cutRunsTogether(c.mark)) {
				continue
			}
			span, err := renderSpan(nodes, i, j, c.mark, applied, written, b, where, crowded)
			switch {
			case err == nil:
				return span, j, nil
			case !isNoSpelling(err):
				return "", 0, err
			case refused == nil:
				refused = err
			}
		}
	}
	return "", 0, refused
}

// strands reports whether cutting a span before node j would move its mark off
// the whitespace at the cut.
//
// Whitespace at the edge of a span moves outside it, which is a normalisation
// where it is the edge of what the mark covers and a loss anywhere else. Cut
// `**a *b***c` after the first node and it writes `**a** ***b***c`, which comes
// back with the space unmarked: a narrower span is only worth reaching for when
// it says the same thing, and where no cut does the document has no spelling
// and is refused.
func strands(nodes []Node, j int) bool {
	return strings.TrimRight(nodes[j-1].Text, edgeSpace) != nodes[j-1].Text ||
		strings.TrimLeft(nodes[j].Text, edgeSpace) != nodes[j].Text
}

// cutRunsTogether reports a mark whose delimiter cannot be written twice in a
// row, so a span of it is written at its full extent or not at all.
//
// A strike is `~~` whatever sits beside it: GFM gives it no flanking rules, so
// nothing outside it can make it inert the way an underscore goes inert inside
// a word. What can is another `~~` flush against it, because a reader takes
// four tildes for text. A cut leaves the rest of the mark to open its own span
// at the cut with nothing in between, so `~~a~~~~b~~` is what a cut strike
// writes, and the one thing that would hold the two runs apart is whitespace
// at the cut, which strands has already refused for its own reason.
//
// Refusing the cut is not refusing the document. renderChoices answers it the
// way it answers any refused spelling, by opening the span with the next mark
// instead, and where the node carries one that reaches as far the document
// comes out written the other way round: a strike over emphasised `a.` and a
// word is `*~~a.~~*~~b~~`, both marks where the document put them. Only a span
// whose first node carries nothing else has no spelling left, and that one is
// refused, which is the answer every unwritable document gets.
//
// The fuzzer reported this shape three times in two days, twice as a span
// inside the strike refused over a collision that was not there and once with
// nothing wrong upstream of it at all. Both of those causes are fixed. This is
// the ending they shared, and it is the one that made them wrong documents
// rather than refusals.
func cutRunsTogether(m Mark) bool {
	return m.Type == "strike"
}

// isNoSpelling reports a span refused for the way it was about to be written
// rather than for what it says.
func isNoSpelling(err error) bool {
	_, ok := errors.AsType[*noSpelling](err)
	return ok
}

// renderSpan writes one run of nodes carrying the same mark, delimiters and all.
func renderSpan(nodes []Node, i, j int, mark Mark, applied []Mark,
	written string, b *strings.Builder, where string, crowded bool,
) (string, error) {
	inner, err := renderInline(nodes[i:j], append(applied, mark),
		written+b.String(), where, crowded)
	if err != nil {
		return "", err
	}
	return wrapSpan(nodes, j, mark, applied, inner, b.String(), where, crowded, 0)
}

// wrapSpan puts the delimiters around a span whose content is already written.
//
// It is the half of renderSpan that does not depend on how the content was
// produced, which is what the search needs: searchInline enumerates the ways to
// write the content and calls this for each of them, where the greedy walk
// writes the content once and calls it once.
//
// skip is how many otherwise-workable spellings to pass over before taking one.
// The greedy walk always passes zero, which is the first spelling that works and
// is what it has always emitted. The search counts up from zero, because the
// character a span is written with can be the reason the span *after* it has no
// spelling, and that is not something the span can see from where it is.
func wrapSpan(nodes []Node, j int, mark Mark, applied []Mark,
	inner, sofar, where string, crowded bool, skip int,
) (string, error) {
	// Whitespace at the edge of a span moves outside it. Markdown cannot
	// emphasise a leading or trailing space: `* x*` is an asterisk and a word,
	// not a span, and Jira's editor produces one constantly, from bolding a
	// word and then typing the space after it. The characters are unchanged and
	// so is what a reader sees; only the extent of the mark moves, by exactly
	// the whitespace nobody can see it on.
	lead, core, trail := splitEdgeSpace(inner)
	if mark.Type == "link" {
		// A link's brackets are not flanking-sensitive, and its text may be
		// whitespace and still be a link. Moving it out would drop the link
		// entirely.
		lead, core, trail = "", inner, ""
	}
	if core == "" {
		if skip > 0 {
			// A span with no core has one spelling, which is its content.
			return "", &noSpelling{where: where}
		}
		return inner, nil
	}

	// The trailing whitespace this span moves outside itself sits between its
	// closing delimiter and whatever comes next, so that is what the delimiter
	// is up against.
	next, guessed := after(nodes[j:], applied)
	if trail != "" {
		next, guessed = rune(trail[0]), false
	}
	open, closing, err := delimiters(mark, core,
		beforeOf(sofar, lead), endsWithLiveOf(sofar, lead), next,
		delimiterAfter(next, guessed, crowded), skip, where)
	if err != nil {
		return "", err
	}
	return lead + open + core + closing + trail, nil
}

// spanChoice is one way to open a span at i: which mark, and one past the last
// node it covers.
type spanChoice struct {
	mark Mark
	j    int
}

// spanChoices lists the ways the span at i can be opened, best first: the mark
// reaching furthest along the run, with spanMarks order breaking a tie.
//
// Reaching furthest is what markdown can express. A span nests inside another
// or it does not overlap it at all, so opening the shorter mark first cuts the
// longer one into pieces, and each piece is a fresh span whose edge whitespace
// then moves outside it — which is where the mark on that whitespace is lost.
// The round-trip fuzzer found it as text that took one pass per space to
// settle: `*__0__ __0__ __0__*` is em over the whole run with strong on each
// word, and opening strong first (it is outermost in spanMarks) emitted
// `***0*** _**0** **0**_`, dropping the em from one space per conversion until
// there were none left.
//
// The tie is what spanMarks was for and still decides: two marks over the same
// extent nest in a fixed order, so the same text under the same marks is
// written the same way whatever order Jira stored them in.
//
// It is a list rather than the one best answer because the best answer is
// sometimes not writable. See renderChoices, which walks the rest of it.
func spanChoices(nodes []Node, i int, applied []Mark) []spanChoice {
	n := nodes[i]
	if n.Type != "text" {
		return nil
	}
	var out []spanChoice
	for _, name := range spanMarks {
		for _, m := range n.Marks {
			if m.Type != name || carriesAll(applied, m) {
				continue
			}
			j := i + 1
			for j < len(nodes) && carries(nodes[j], m) {
				j++
			}
			out = append(out, spanChoice{mark: m, j: j})
		}
	}
	// Stable, so spanMarks order survives as the tiebreak it is.
	slices.SortStableFunc(out, func(a, b spanChoice) int { return b.j - a.j })
	return out
}

func carriesAll(applied []Mark, m Mark) bool {
	for _, a := range applied {
		if a.Type == m.Type && reflect.DeepEqual(a.Attrs, m.Attrs) {
			return true
		}
	}
	return false
}

// carries reports whether n holds exactly this mark, attributes included. Two
// links to different addresses are two spans, not one.
func carries(n Node, m Mark) bool {
	if n.Type != "text" {
		return false
	}
	return carriesAll(n.Marks, m)
}

// delimiters returns what opens and closes a span.
//
// Only emphasis has a delimiter to choose. A strike is `~~` whatever sits
// beside it (this package's reader spells it with a fixed pair and no flanking
// rules, as GFM does), and a link is brackets. Both used to go through the
// emphasis choice first and inherit its refusal, so a strike next to an
// asterisk was reported as emphasis with no unambiguous spelling: an error
// about a mark the document did not have, over a character the span does not
// use. The round-trip fuzzer found it on the second pass over `*0*~~*0*~~0`.
func delimiters(
	m Mark, inner string, prev rune, prevLive byte, next rune, nextLive byte,
	skip int, where string,
) (open, closing string, err error) {
	if skip > 0 && m.Type != "strong" && m.Type != "em" {
		// A strike is `~~` and a link is brackets. Neither has a second
		// spelling to ask for.
		return "", "", &noSpelling{where: where}
	}
	switch m.Type {
	case "strong", "em":
		return emphasisDelimiters(m.Type, inner, prev, prevLive, next, nextLive, skip, where)
	case "strike":
		return "~~", "~~", nil
	case "link":
		return linkDelimiters(m, where)
	}
	return "", "", unrepresentable(where, "a %q mark", m.Type)
}

// emphasisDelimiters picks the character a span of emphasis is written with.
//
// The asterisk form unless one of its delimiters would merge into a run with
// what sits next to it: a nested span flush against this one on a single side,
// or a neighbouring span's own delimiter. `*0*` beside `**0**` is `*0***0**`,
// and the run of three in the middle is read as something neither span says.
// Both sides flush is the ordinary `***x***` spelling, where the runs merge
// symmetrically and read back correctly.
//
// A candidate that cannot flank is refused the same way, and it is the older
// bug of the two: merging is about a delimiter being read as part of something
// bigger, flanking is about it not being read as a delimiter at all. A run
// against punctuation cannot close in front of a word character, so the em over
// `0~~0~~` in `*0*~~*0*~~0` came back out as `*0~~0~~*0`, where the closing
// asterisk sits between the strike's `~` and a digit, opens a second span, and
// closes nothing. Both asterisks then read as text and the emphasis was gone,
// exit 0, from output this package produced itself.
func emphasisDelimiters(
	kind, inner string, prev rune, prevLive byte, next rune, nextLive byte,
	skip int, where string,
) (open, closing string, err error) {
	for _, char := range []byte{'*', '_'} {
		if merges(char, inner, prev, prevLive, next, nextLive) ||
			!flanks(char, inner, prev, next) {
			continue
		}
		if skip > 0 {
			// Workable, but the caller has already tried this one and come
			// back. See wrapSpan.
			skip--
			continue
		}
		run := string(char)
		if kind == "strong" {
			run += run
		}
		return run, run, nil
	}
	// Markdown has two spellings for emphasis and neither of them would be read
	// as what this document says. There is no third, so this way of writing the
	// span is refused, and renderInline answers that by trying another. Only a
	// document with no working spelling at all reaches the caller.
	return "", "", &noSpelling{where: where}
}

// linkDelimiters returns the brackets and the address around a link's text.
func linkDelimiters(m Mark, where string) (open, closing string, err error) {
	href, ok := attrString(m.Attrs, "href")
	if !ok || href == "" {
		return "", "", unrepresentable(where, "a link with no address")
	}
	target, err := linkTarget(href, where)
	if err != nil {
		return "", "", err
	}
	if title, ok := attrString(m.Attrs, "title"); ok && title != "" {
		if blankLineInside(title) {
			// Escaping fixes a line that *starts* something. It cannot fix a
			// line that is nothing: a blank line ends the paragraph, there is
			// no character to put a backslash in front of, and CommonMark says
			// in as many words that a title may not contain one. So this is
			// refused where the address holding a line break is refused, and
			// for the same reason.
			return "", "", unrepresentable(where, "a link title holding a blank line")
		}
		// Markdown's own title syntax. Dropping it would be a silent loss: it
		// is what a reader sees on hover and it is not the address.
		target += ` "` + escapeTitle(title) + `"`
	}
	return "[", "](" + target + ")", nil
}

// blankLineInside reports a line of s that is empty or all whitespace, ignoring
// the first and the last.
//
// Those two are not lines of their own in the output. The first carries the
// bracket, the address and the opening quote in front of it, and the last
// carries the closing quote and parenthesis after it, so neither can be blank
// however little the title puts on it. Every line between them stands alone,
// and one of those being blank is what ends the paragraph.
func blankLineInside(s string) bool {
	lines := strings.Split(s, "\n")
	for _, line := range lines[1:max(len(lines)-1, 1)] {
		if strings.TrimSpace(line) == "" {
			return true
		}
	}
	return false
}

// escapeTitle escapes a link title: text like any other text this writer
// emits, plus the quote that would close it.
//
// It used to escape the backslash and the quote and nothing else, which is
// every character that can end a title early and none of the ones that can end
// the *paragraph* early. A title may span lines, and a line inside one still
// begins a line as far as the block parser is concerned, so
// `[x](u "a` + newline + `> b")` puts a block quote under a paragraph: the
// block layer takes the paragraph apart and the inline parser never sees a
// link. Jira Cloud stores a newline in a title, verified against the sandbox,
// so this is a document the server hands back rather than only one a caller
// can construct.
//
// escapeText is the rest of it, and reusing it is the point: the set of things
// that begin a block is one set, and this file has been bitten twice by
// keeping a second copy of a rule the reader already owns. It escapes more than
// a title strictly needs, because emphasis and brackets are inert inside one
// anyway, and that costs nothing: scanTitle drops the backslash from anything
// it escaped.
//
// The order matters. escapeText doubles a backslash and leaves the quote alone,
// so the quotes go on after it, and a title holding a backslash in front of a
// quote survives both passes.
func escapeTitle(s string) string {
	return strings.ReplaceAll(escapeText(s, false), `"`, `\"`)
}

// noSpelling is one way of writing a span refused, not the document refused.
//
// It is a type rather than the usual message because renderInline answers it by
// writing the same document a different way: a narrower span, or a different
// mark on the outside. Only a refusal that survives every spelling is the
// caller's problem, and inlineList is where it becomes one.
type noSpelling struct{ where string }

func (e *noSpelling) Error() string {
	return "no unambiguous spelling for emphasis at " + e.where
}

// flanks reports whether a run of char written around core can be read back as
// the span it was meant to be: opening in front of core, closing behind it.
//
// The characters that decide it are the ones the delimiters will actually sit
// between, which is why this asks about the rendered core rather than about the
// nodes. A nested strike leaves a `~` there, a nested link a `)`, and an
// escaped character leaves whatever it escaped, all of them punctuation, which
// is the class that cannot close in front of a word.
//
// A nested span flush against this one on both sides is the exception, and it
// is the `***bold italic***` spelling: the two delimiters are one run to a
// reader, so what flanks it is what is outside the pair and what is just inside
// the nested one. Reading the nested delimiter as this run's neighbour refuses
// `foo***bar***baz`, which is em and strong over one word and reads back
// correctly. merges has already rejected flush on one side only, which does
// not.
//
// core is never empty: renderSpan writes the whitespace and returns before
// reaching a delimiter at all where a span has nothing else in it.
func flanks(char byte, core string, prev, next rune) bool {
	run := core
	if core[0] == char && endsWithLive(core) == char {
		if run = strings.Trim(core, string(char)); run == "" {
			return false
		}
	}
	first, _ := utf8.DecodeRuneInString(run)
	last, _ := utf8.DecodeLastRuneInString(run)
	canOpen, _ := flanking(char, sideOf(prev), classified(first))
	_, canClose := flanking(char, classified(last), sideOf(next))
	return canOpen && canClose
}

// classified is sideOf for a character that is definitely there.
//
// The two are separate because zero means two things and only one of them is a
// character. sideOf reads it as nothing there, which is right for what sits
// outside a span and wrong for what sits inside one: core is never empty, so a
// zero from it is a literal NUL, and CommonMark's classification of a NUL is
// punctuation. Sharing sideOf refused 25 inputs from the fuzz corpus that this
// converter had written correctly the day before, every one of them a NUL
// against a delimiter, because whitespace is the one class that can neither
// open nor close. The seed `\x00**0***` is the same collision from the other
// side, found the same way, in the other half of the package.
func classified(r rune) side {
	space, punct := classify(r)
	return side{space: space, punct: punct}
}

// sideOf classifies a character the writer is about to sit a delimiter against.
//
// Zero is nothing there, the start of the text or the end of it, and it counts
// as whitespace, the same as the edge does on the way in. A literal NUL
// arrives as zero too and is punctuation to the reader, and both reach the same
// verdict here: whitespace and punctuation are read as alternatives in every
// clause of the flanking rules that mentions either, so the one place they
// differ is a side this writer never asks about.
func sideOf(r rune) side {
	if r == 0 {
		return side{space: true}
	}
	space, punct := classify(r)
	return side{space: space, punct: punct}
}

// edgeSpace is every character markdown counts as whitespace, not just the two
// that come up in practice: a delimiter may not sit against any of them.
const edgeSpace = " \t\n\r\v\f"

// splitEdgeSpace separates a span's leading and trailing whitespace from what
// the delimiters actually go around.
func splitEdgeSpace(s string) (lead, core, trail string) {
	core = strings.TrimLeft(s, edgeSpace)
	lead = s[:len(s)-len(core)]
	trimmed := strings.TrimRight(core, edgeSpace)
	trail = core[len(trimmed):]
	return lead, trimmed, trail
}

// merges reports whether a delimiter written with char would run together
// with something beside it and be read as a different span.
//
// A run of delimiters is a single token to markdown: `*0*` written next to
// `**0**` is `*0***0**`, and the three in the middle belong to neither. The
// same is true of a nested span flush against one side of this one — flush
// against both is the ordinary `***x***` spelling, where the runs merge
// symmetrically and read back correctly.
//
// The underscore's extra rule (inert against a word character, which is what
// keeps `customfield_10042` a field id) used to be stated here a second time
// and in a second vocabulary, where an underscore was itself a word character
// and every byte over 0x7f was one. flanks asks the reader's own rule instead,
// so the two halves of the package cannot drift on it.
func merges(char byte, inner string, prev rune, prevLive byte, next rune, nextLive byte) bool {
	// A live delimiter strictly inside would close this span early, wherever
	// it came from — a nested span that picked the same character, or an
	// underscore inside a word, which looks like one and is inert.
	if insideLive(inner, char) {
		return true
	}
	// Flush on one side only.
	if (len(inner) > 0 && inner[0] == char) != (endsWithLive(inner) == char) {
		return true
	}
	// Both of those are about the span's own content and are decided before
	// what surrounds it, because the check below used to sit in front of them
	// and hide both. A span with a space either side reached it, returned "does
	// not merge", and was written with asterisks however many live delimiters
	// its content held: `00 ***0*0*0*0*0*** 0` is what that produced, and no
	// reader takes those runs apart the way they were meant.
	//
	// What surrounding whitespace does rule out is the merge below it, because
	// nothing opens or closes against a space, so a neighbour's delimiter
	// cannot run together with this one's.
	if prev != 0 && next != 0 && unicode.IsSpace(prev) && unicode.IsSpace(next) {
		return false
	}
	return prevLive == char || nextLive == char
}

// insideLive reports an unescaped delimiter somewhere other than the two ends.
//
// The two ends are excluded because a delimiter flush against either of them is
// the flush check's question, not this one. Which characters are escaped is a
// separate matter, and it is read from the start of the string: the scan used
// to begin at the first interior byte, so a backslash sitting at index 0 was
// never consumed and the character it escaped was counted as live.
//
// That is one byte of state and it cost a span. `\*0` is an escaped asterisk
// and a digit, and it is what an emphasised text node holding `*0` renders to.
// Reading its `*` as live said the asterisk spelling would close the span
// early, so the span was refused, and the only other spelling is `_`, which
// cannot close in front of the digit that followed. With no spelling for the
// emphasis, the strike around it was cut back to the first of its two nodes,
// and a cut strike writes its closing `~~` against the next one's opening
// `~~`. A reader takes `~~~~` for four literal tildes. Same ending as the
// `after` bug of the day before, which is further down this file, reached from
// the other side of one question: what is actually written where the delimiter
// goes.
func insideLive(s string, char byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if i > 0 && i < len(s)-1 && s[i] == char {
			return true
		}
	}
	return false
}

// endsWithLive reports the delimiter character a string ends with, or zero.
// An escaped one is text and merges with nothing.
func endsWithLive(s string) byte {
	var live byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			live = 0
			continue
		}
		live = 0
		if s[i] == '*' || s[i] == '_' {
			live = s[i]
		}
	}
	return live
}

// atLineStart reports whether the next character written begins a line.
func atLineStart(written string) bool {
	return written == "" || written[len(written)-1] == '\n'
}

// before is the character a span will sit against on its left. Whether that
// character is a live delimiter is a separate question — an escaped asterisk
// is punctuation and merges with nothing.
//
// A rune and not a byte, because the flanking rules sort it into three classes
// and two of them hold characters that do not fit in one. Reading the last byte
// and calling everything over 0x7f a word character refuses `“**‘x’**”`, which
// CommonMark reads as bold and this package can write.
func before(written string) rune {
	r, size := utf8.DecodeLastRuneInString(written)
	if size == 0 {
		return 0
	}
	return r
}

// beforeOf and endsWithLiveOf answer before and endsWithLive over two pieces
// without joining them.
//
// The pieces are everything written so far and the whitespace this span moved
// outside itself, and joining them is what made this converter quadratic. Both
// questions are about the very end of that text — the last byte, and whether a
// backslash escapes it — and the caller was building the whole of it, once per
// span, to look at the tail. A paragraph of 1600 inline nodes spent 982MB of
// 1.01GB on that one concatenation and took 393 times as long as one of 50,
// for output that is byte-identical either way.
//
// Worth saying which line it was, because two others in this function have the
// same shape and are harmless: `written+b.String()` at the plain-node and
// recursive calls is a copy too, and the profile puts both at 2MB. The cost is
// not the concatenation, it is doing it once per span against a buffer that
// grows with the output. Measure before assuming the tidy explanation.
//
// They are separate from before and endsWithLive rather than replacing them
// because the originals are the specification: FuzzSplitHelpersMatchJoined
// fuzzes the pair against the joined form, which is what makes this a
// behaviour-preserving change by proof rather than by argument.
//
// The last rune of the pair is not the last rune of the suffix: the suffix is
// whitespace this span moved outside itself and is usually empty, and a rune
// spans up to four bytes, so the two pieces have to be read as one string
// without becoming one. Four bytes off the end of the pair hold the whole of
// the final rune whatever it is, and DecodeLastRune scans no further back than
// that either.
func beforeOf(prefix, suffix string) rune {
	var tail [utf8.UTFMax]byte
	n := 0
	for i := len(suffix) - 1; i >= 0 && n < len(tail); i-- {
		n++
		tail[len(tail)-n] = suffix[i]
	}
	for i := len(prefix) - 1; i >= 0 && n < len(tail); i-- {
		n++
		tail[len(tail)-n] = prefix[i]
	}
	if n == 0 {
		return 0
	}
	r, _ := utf8.DecodeLastRune(tail[len(tail)-n:])
	return r
}

// endsWithLiveOf is endsWithLive over the same two pieces.
//
// Only the final byte can be the answer, and whether it is live is the parity
// of the backslash run immediately behind it: an even run is escaped
// backslashes and leaves the delimiter live, an odd one escapes it. That is
// exactly what endsWithLive's forward scan computes, and it is the half of this
// pair worth fuzzing hardest — the parity is easy to state and easy to get one
// off.
func endsWithLiveOf(prefix, suffix string) byte {
	last := func(i int) byte {
		if i < len(suffix) {
			return suffix[len(suffix)-1-i]
		}
		return prefix[len(prefix)-1-(i-len(suffix))]
	}
	n := len(prefix) + len(suffix)
	if n == 0 {
		return 0
	}
	c := last(0)
	if c != '*' && c != '_' {
		return 0
	}
	slashes := 0
	for i := 1; i < n && last(i) == '\\'; i++ {
		slashes++
	}
	if slashes%2 == 1 {
		return 0
	}
	return c
}

// after is the character a span will sit against on its right.
//
// A neighbouring node carrying a mark renders starting with that mark's
// delimiter, which is the collision this exists to see; anything else is that
// node's first character **as it will be written**, which is not the same as
// the character the node holds.
//
// Which mark's delimiter lands there is spanMarks' answer, because spanMarks is
// the order the writer opens them in and the outermost one is written first.
// Asking it rather than naming two of the four is the third version of this
// function and the second bug in it: it knew about emphasis and not about a
// link, which is written starting with a bracket. A bracket is punctuation and
// the link's text usually is not, and emphasis ending in punctuation can close
// in front of the first and not the second, so `*a.*` in front of a link was
// refused. The sweep of 2026-08-18 reported it as this package refusing to
// write a document it had produced itself, one conversion earlier, from an
// inlineCard.
//
// It used to be the raw one, over a comment claiming that reading the
// unescaped text was "conservative in the direction that matters". It is
// conservative in the direction that produces more refusals, and a refusal here
// is not free: renderChoices answers it by writing a narrower span, and a
// narrower span puts this span's closing delimiter against the next one's
// opening delimiter. `~~00*a*\*~~` is the document that showed it. The `*` is a
// literal asterisk, so it is written `\*` and merges with nothing, but the raw
// rune said `*` and the emphasis inside refused to close against it. The strike
// was cut in two and written `~~00*a*~~~~\*~~`, and **a reader takes `~~~~` for
// four literal tildes**, so the text no longer said what the document said. The
// fuzzer saw it as drift on the pass after that, when those tildes came back
// escaped.
//
// Asking escapeText rather than keeping a second list of what it escapes: the
// answer has to be the escaper's, and a list beside it is the drift this
// package has already paid for twice.
// The second return says the character is predicted rather than settled, which
// is true of exactly one of the answers below: the asterisk `opensWith` names
// for a neighbouring emphasis span, whose delimiter is chosen later. A tilde and
// a bracket are what a strike and a link are always written with, and every
// other answer is a character already in the document. delimiterAfter is what
// the distinction is for.
func after(rest []Node, applied []Mark) (rune, bool) {
	if len(rest) == 0 || rest[0].Type != "text" {
		return 0, false
	}
	first, size := utf8.DecodeRuneInString(rest[0].Text)
	// Whitespace comes before the next span's delimiter, because that span
	// moves its own edge whitespace outside itself. Two delimiters with a
	// space between them do not merge.
	if size > 0 && unicode.IsSpace(first) {
		return first, false
	}
	if d := opensWith(rest[0], applied); d != 0 {
		return d, d == '*'
	}
	if size == 0 {
		return 0, false
	}
	if written := escapeText(string(first), false); written[0] == '\\' {
		// It goes out escaped, so the character against the delimiter is the
		// backslash. Mid-line by construction: a span's delimiter precedes it,
		// which is why lineStart is false and why the line-start-only escapes
		// are correctly absent from the answer.
		return '\\', false
	}
	return first, false
}

// delimiterAfter is the delimiter this span's closing run is certainly written
// against, which is a narrower question than the one `after` answers.
//
// The flanking rules need the character beside the run only to classify it, and
// a predicted asterisk classifies the same as the underscore that may turn up
// instead. Merging needs its identity, and there the prediction is the whole
// difference: a neighbouring emphasis span has not picked its character yet, and
// `opensWith` names the asterisk on its behalf so this span avoids it and the
// neighbour, seeing an underscore beside it, keeps the asterisk.
//
// That yielding is worth doing, because an underscore is the harder of the two
// to place: it goes inert between word characters, so a span that has to close
// in front of one has only the asterisk. Handing it along is why `_a_**b**c`
// is written rather than refused.
//
// It is also unsatisfiable as soon as three emphasis spans sit against each
// other. The first yields the asterisk and takes the underscore; the second has
// an underscore on its left and a predicted asterisk on its right and may take
// neither; and the run has no spelling at all. `_a_**b**_c_` is a document Jira
// stores, CommonMark reads it back as exactly the three spans it came from, and
// this writer refused it — along with everything else of that shape, which is
// what the nightly sweep of 2026-08-19 reported as this package being unable to
// read `0 ~~*0*~~__\!__*\!*`, which it had just written itself.
//
// So the preference is given up rather than abandoned. `crowded` is the second
// attempt at one inline run, made by inlineList only after the first attempt
// refused every way of spelling some span in it, and it says to take the
// asterisk here and let the neighbour see what is actually beside it. The first
// attempt is unchanged, so no document that is written today is written
// differently; a document that is refused today may now be written.
//
// Guarding on `guessed` rather than on the character alone is belt and braces:
// a literal asterisk in text is escaped and reaches `after` as a backslash, and
// an underscore at the start of a text node is escaped for the same reason, so
// today a settled `*` or `_` beside a span cannot happen. If that ever changes,
// this drops a real collision rather than a prediction, and the round trip is
// where it would be found.
func delimiterAfter(next rune, guessed, crowded bool) byte {
	if next != '*' && next != '_' {
		return 0
	}
	if guessed && crowded {
		return 0
	}
	return byte(next)
}

// opensWith is the delimiter a node's own marks put in front of its text, or
// zero where its text is written first.
//
// spanMarks is the order the writer opens marks in, so the outermost mark the
// node does not already sit inside is the one whose delimiter lands against
// whatever is on its left. A mark already open around both nodes is not written
// between them, which is what applied says.
func opensWith(n Node, applied []Mark) rune {
	for _, name := range spanMarks {
		for _, m := range n.Marks {
			if m.Type != name || carriesAll(applied, m) {
				continue
			}
			switch name {
			case "link":
				return '['
			case "strong", "em":
				// Which delimiter that span will pick is not known yet, so the
				// asterisk is assumed: it is the one this span then avoids, and
				// the next span sees an underscore beside it and keeps the
				// asterisk.
				return '*'
			case "strike":
				return '~'
			}
		}
	}
	return 0
}

// leaf writes one text node, with every span mark already open around it.
//
// It is where a mark this converter cannot write is refused, so the refusal
// happens once no matter which span the text sits in.
func leaf(n Node, lineStart bool, where string) (string, error) {
	code := false
	for _, m := range n.Marks {
		switch m.Type {
		case "code":
			code = true
		case "strike", "em", "strong", "link":
			// Written as a span by renderInline.
		case "underline":
			return "", unrepresentable(where, "underlined text")
		case "subsup":
			return "", unrepresentable(where, "superscript or subscript text")
		case "textColor", "backgroundColor":
			return "", unrepresentable(where, "coloured text")
		case "alignment", "indentation":
			return "", unrepresentable(where, "aligned or indented text")
		case "annotation":
			return "", unrepresentable(where, "an inline comment")
		case "border":
			return "", unrepresentable(where, "a bordered span")
		default:
			return "", unrepresentable(where, "a %q mark", m.Type)
		}
	}
	if code {
		// Verbatim: escaping inside a code span would put backslashes in the
		// text, and a span needs no escaping to be exact.
		return codeSpan(n.Text), nil
	}
	return escapeText(n.Text, lineStart), nil
}

// checkEdgeSpace refuses text whose line begins or ends with a space or a tab.
//
// Markdown strips both: leading whitespace is indentation, four spaces of it
// is a code block, and trailing whitespace is either a hard break or nothing.
// There is no spelling that carries it, so a paragraph that starts with four
// spaces cannot be written down - and writing it anyway produces markdown that
// reads back as code, which is the silent alteration this package refuses.
func checkEdgeSpace(s, where string) error {
	for _, line := range strings.Split(s, "\n") {
		// The hard break this package writes is a backslash, so a line ending
		// in one is not trailing whitespace.
		trimmed := strings.TrimSuffix(line, "\\")
		if trimmed == "" {
			continue
		}
		switch {
		case trimmed[0] == ' ' || trimmed[0] == '\t':
			return unrepresentable(where, "text beginning with a space or a tab")
		case trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t':
			return unrepresentable(where, "text ending with a space or a tab")
		}
	}
	return nil
}

// coalesce joins neighbouring text nodes that carry the same marks.
//
// Where a run of bold text is one node or three is an artefact of how it was
// typed and edited, and Jira stores whichever it ended up with. Rendering them
// separately closes and reopens the emphasis between two words — `**a****b**` —
// which is not what the document says and, run together, is not reliably what
// a CommonMark parser reads either.
// The run is gathered and joined once rather than appended to as it grows. A
// string is immutable, so `prev.Text += n.Text` copies everything already
// merged on every node, which is quadratic in the bytes of the run — half a
// second on a paragraph holding ten thousand delimiters, which the emphasis
// scanner produces one node each for.
func coalesce(nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	var run []string

	// closeRun writes the gathered text back to the node it belongs to. While
	// a run is open that node holds only its first piece, and nothing reads it
	// until here.
	closeRun := func() {
		if len(run) > 0 {
			out[len(out)-1].Text = strings.Join(run, "")
			run = run[:0]
		}
	}

	for _, n := range nodes {
		if len(out) > 0 && n.Type == "text" {
			if prev := &out[len(out)-1]; prev.Type == "text" && sameMarks(prev.Marks, n.Marks) {
				if len(run) == 0 {
					run = append(run, prev.Text)
				}
				run = append(run, n.Text)
				continue
			}
		}
		closeRun()
		out = append(out, n)
	}
	closeRun()
	return out
}

// sameMarks compares two mark lists as sets, because the order Jira stores them
// in is not meaningful and this converter emits its own order anyway.
func sameMarks(a, b []Mark) bool {
	if len(a) != len(b) {
		return false
	}
	for _, want := range a {
		found := false
		for _, got := range b {
			if want.Type == got.Type && reflect.DeepEqual(want.Attrs, got.Attrs) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func inline(n Node, lineStart bool, where string) (string, error) {
	switch n.Type {
	case "text":
		return leaf(n, lineStart, where)

	case "hardBreak":
		// A backslash, not two trailing spaces. Both are CommonMark hard
		// breaks; only one survives a tool that trims trailing whitespace, and
		// an invisible one cannot be reviewed in a diff.
		return "\\\n", nil

	case "media":
		return media(n, where)

	case "mention":
		return mention(n, where)

	case "emoji":
		return emoji(n, where)

	case "status":
		return status(n, where)

	case "date":
		return date(n, where)

	case "inlineCard":
		url, err := cardURL(n, where)
		if err != nil {
			return "", err
		}
		return autolink(url, where)

	case "inlineExtension":
		return "", unrepresentable(where, "an inline macro")
	case "placeholder":
		return "", unrepresentable(where, "an editor placeholder")
	}
	return "", unrepresentable(where, "a %q node", n.Type)
}

// mention renders a person as a link to their account id, so the identity
// survives a round trip that the display name alone would not.
func mention(n Node, where string) (string, error) {
	id, _ := attrText(n.Attrs, "id")
	text, _ := attrString(n.Attrs, "text")
	if text == "" {
		text = "@" + id
	}
	if id == "" {
		return "", unrepresentable(where, "a mention of nobody")
	}
	target, err := linkTarget("jira-user:"+id, where)
	if err != nil {
		return "", err
	}
	return "[" + escapeText(text, false) + "](" + target + ")", nil
}

// emoji renders the character itself where there is one. The text attribute is
// the emoji; the short name is what Jira shows when it has no character to
// show. Either is the whole content.
func emoji(n Node, where string) (string, error) {
	if text, ok := attrString(n.Attrs, "text"); ok && text != "" {
		return escapeText(text, false), nil
	}
	if short, ok := attrString(n.Attrs, "shortName"); ok && short != "" {
		return escapeText(short, false), nil
	}
	return "", unrepresentable(where, "an emoji with no character")
}

// status renders a lozenge as its text, linked to the colour it carries.
func status(n Node, where string) (string, error) {
	text, _ := attrString(n.Attrs, "text")
	if text == "" {
		return "", unrepresentable(where, "a status lozenge with no text")
	}
	colour, ok := attrString(n.Attrs, "color")
	if !ok || colour == "" {
		colour = "neutral"
	}
	target, err := linkTarget("jira-status:"+colour, where)
	if err != nil {
		return "", err
	}
	return "[" + escapeText(text, false) + "](" + target + ")", nil
}

// date renders a date chip as its day, linked to the instant it holds.
//
// Writing the date alone would read better and would lose the millisecond
// stamp Jira stores, so the day is the link text and the stamp is the target.
// It is the same rule a mention and a status lozenge follow, and one rule with
// four cases is easier to rely on than three plus an exception.
func date(n Node, where string) (string, error) {
	stamp, ok := attrText(n.Attrs, "timestamp")
	if !ok {
		return "", unrepresentable(where, "a date with no timestamp")
	}
	ms, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return "", unrepresentable(where, "a date stamped %q", stamp)
	}
	t := time.UnixMilli(ms).UTC()
	// Jira's date chip is a day, and a day is what it stores. Anything with a
	// time in it shows the time rather than being rounded down to the date.
	shown := t.Format("2006-01-02")
	if !t.Equal(t.Truncate(24 * time.Hour)) {
		shown = t.Format(time.RFC3339)
	}
	target, err := linkTarget("jira-date:"+stamp, where)
	if err != nil {
		return "", err
	}
	return "[" + shown + "](" + target + ")", nil
}

func media(n Node, where string) (string, error) {
	alt, _ := attrString(n.Attrs, "alt")

	// An external or linked media carries the URL it points at, which is a
	// markdown image with nothing invented.
	if url, ok := attrString(n.Attrs, "url"); ok && url != "" {
		target, err := linkTarget(url, where)
		if err != nil {
			return "", err
		}
		return "![" + escapeText(alt, false) + "](" + target + ")", nil
	}

	id, _ := attrText(n.Attrs, "id")
	if id == "" {
		return "", unrepresentable(where, "an attachment with no id")
	}
	collection, _ := attrString(n.Attrs, "collection")
	if strings.Contains(id, "/") || strings.Contains(collection, "/") {
		// The slash is what separates the two in `jira-media:<collection>/<id>`
		// and there is nowhere to escape one, so an id holding a slash cannot
		// be written down and read back as itself.
		return "", unrepresentable(where, "an attachment whose id holds a slash")
	}
	target := "jira-media:" + id
	if collection != "" {
		target = "jira-media:" + collection + "/" + id
	}
	written, err := linkTarget(target, where)
	if err != nil {
		return "", err
	}
	return "![" + escapeText(alt, false) + "](" + written + ")", nil
}

// cardURL reads the link a card points at.
//
// A card with embedded JSON-LD instead of a URL, or one backed by a datasource
// query, is a rendered object rather than a link; there is no address to write
// down and the object itself is not markdown.
func cardURL(n Node, where string) (string, error) {
	url, ok := attrString(n.Attrs, "url")
	if !ok || url == "" {
		return "", unrepresentable(where, "a card with no link")
	}
	return url, nil
}

// codeSpan wraps text in enough backticks to hold whatever run of them it
// contains, padding with spaces where CommonMark would otherwise eat one.
func codeSpan(s string) string {
	ticks := strings.Repeat("`", longestBacktickRun(s)+1)
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") ||
		(strings.HasPrefix(s, " ") && strings.HasSuffix(s, " ") && strings.TrimSpace(s) != "") {
		pad = " "
	}
	return ticks + pad + s + pad + ticks
}

// escapeText escapes the characters that would otherwise start a markdown
// construct, so text that came out of Jira reads back as the same text.
//
// It escapes what can change meaning and nothing else. An underscore between
// two letters cannot open emphasis in CommonMark, so `customfield_10042` is
// written as it is rather than as `customfield\_10042` — the escaping is there
// to be correct, not to be visible.
func escapeText(s string, lineStart bool) string {
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	atLineStart := lineStart
	for i, r := range runes {
		switch r {
		// A pipe is not escaped here. It only closes anything inside a table,
		// where the cell escapes it — and escaping it everywhere would put a
		// backslash into every JQL string a description quotes.
		case '\\', '`', '*', '[', ']', '<', '>', '~', '!', '&':
			b.WriteByte('\\')
		case '_':
			if !intraword(runes, i) {
				b.WriteByte('\\')
			}
		case '#', '+', '-', '=', '|':
			// Only meaningful where a block can begin. Mid-line they are text —
			// except the pipe, which starts a table row given a delimiter under
			// it, and a hard break can put one under it.
			if atLineStart {
				b.WriteByte('\\')
			}
		case '.', ')':
			// An ordered-list marker: a run of digits at the start of a line
			// followed by one of these.
			if startsOrderedList(runes, i, lineStart) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
		// A line start survives the whitespace in front of it. The read side
		// trims a line before it decides what block the line begins, and it
		// trims more than the indentation markdown itself strips: a vertical
		// tab, a form feed, and a non-breaking space all reach the setext check
		// as nothing, so `\v=` under a paragraph line arrives there as `=` and
		// is read as a heading underline rather than as the two characters that
		// were written. Unicode's whitespace, because that is the set the
		// strings.TrimSpace over there works from.
		atLineStart = r == '\n' || (atLineStart && unicode.IsSpace(r))
	}
	return b.String()
}

// intraword reports whether the rune at i sits between two characters that are
// neither whitespace nor punctuation, which is where CommonMark's flanking
// rules make an underscore inert and where it is safe to leave unescaped.
//
// Running out of runes is not intraword. The next node's first character is not
// visible from here, so the two ends of a text node are escaped whether they
// need it or not.
func intraword(runes []rune, i int) bool {
	return i > 0 && i+1 < len(runes) &&
		isWordRune(runes[i-1]) && isWordRune(runes[i+1])
}

// isWordRune is the third class of the flanking rules, the one with no name in
// the specification: not whitespace and not punctuation.
//
// It used to count an underscore itself as a word character, which made the
// second underscore of a pair look intraword and go out unescaped. That agreed
// with the scanner this package had, which asked the same question of the
// single character in front of the delimiter. It does not agree with the
// flanking rules, where an underscore is punctuation: `\__0` is an escaped
// underscore and then a live one that opens a span, so the writer emitted two
// characters of text and the reader read one of them as markup. Found by the
// round-trip fuzzer half a second into the sweep that was verifying the parser
// this rule now has to match.
func isWordRune(r rune) bool {
	space, punct := classify(r)
	return !space && !punct
}

// startsOrderedList reports whether the rune at i closes an ordered-list marker
// — that is, whether everything before it back to the line start is digits, and
// there is at least one.
//
// Running out of runes means the start of this text, which is a line start only
// if the text itself begins one. Assuming it did escaped the parenthesis in
// `*0)*`, which is a digit and a bracket in the middle of a sentence.
func startsOrderedList(runes []rune, i int, lineStart bool) bool {
	j := i - 1
	for j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
		j--
	}
	if j == i-1 {
		return false
	}
	if j < 0 {
		return lineStart
	}
	return runes[j] == '\n'
}

// linkTarget writes a link destination.
//
// A URL Jira stored can hold anything, including the parenthesis that would
// close the link. Rather than percent-encode it — which cannot be undone,
// because a `%28` that was already in the URL and one this function wrote are
// the same three characters — the destination goes inside angle brackets,
// which is CommonMark's own answer and reverses exactly.
func linkTarget(s, where string) (string, error) {
	if strings.ContainsAny(s, "\n\r") {
		// A line ending cannot appear in a link destination in either form,
		// and percent-encoding it is one-way: the encoding and a `%0A` that
		// was already in the address are the same three characters coming
		// back. It is not a URL either.
		return "", unrepresentable(where, "a link whose address holds a line break")
	}
	if s == "" {
		return "<>", nil
	}
	if !strings.ContainsAny(s, " \t()<>\\") {
		return s, nil
	}
	r := strings.NewReplacer(
		`\`, `\\`,
		"<", `\<`,
		">", `\>`,
	)
	return "<" + r.Replace(s) + ">", nil
}

// autolink writes a bare URL. CommonMark's `<url>` form holds no spaces and no
// angle brackets, so a URL carrying either becomes an ordinary link whose text
// is the URL — which reads the same and reverses the same.
func autolink(s, where string) (string, error) {
	if s != "" && !strings.ContainsAny(s, " \t\n\r<>\\") {
		return "<" + s + ">", nil
	}
	target, err := linkTarget(s, where)
	if err != nil {
		return "", err
	}
	return "[" + escapeText(s, false) + "](" + target + ")", nil
}
