package adf

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

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
func ToMarkdown(doc Node) (string, error) {
	blocks, err := blockList(doc.Content, "doc")
	if err != nil {
		return "", err
	}
	return strings.Join(blocks, "\n\n"), nil
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
	out, err := renderInline(trimEdgeBreaks(coalesce(nodes)), nil, "", where)
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
func renderInline(nodes []Node, applied []Mark, written, where string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(nodes); {
		mark, ok := nextSpanMark(nodes, i, applied)
		if !ok {
			s, err := inline(nodes[i], atLineStart(written+b.String()), where+" > "+nodes[i].Type)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
			i++
			continue
		}

		// Extend the span over every neighbour carrying the same mark.
		j := i + 1
		for j < len(nodes) && carries(nodes[j], mark) {
			j++
		}
		span, err := renderSpan(nodes, i, j, mark, applied, written, &b, where)
		if err != nil {
			return "", err
		}
		b.WriteString(span)
		i = j
	}
	return b.String(), nil
}

// renderSpan writes one run of nodes carrying the same mark, delimiters and all.
func renderSpan(nodes []Node, i, j int, mark Mark, applied []Mark,
	written string, b *strings.Builder, where string,
) (string, error) {
	inner, err := renderInline(nodes[i:j], append(applied, mark),
		written+b.String(), where)
	if err != nil {
		return "", err
	}

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
		return inner, nil
	}

	// The trailing whitespace this span moves outside itself sits between its
	// closing delimiter and whatever comes next, so that is what the delimiter
	// is up against.
	next := after(nodes[j:], applied)
	if trail != "" {
		next = trail[0]
	}
	open, closing, err := delimiters(mark, core,
		beforeOf(b.String(), lead), endsWithLiveOf(b.String(), lead), next, where)
	if err != nil {
		return "", err
	}
	return lead + open + core + closing + trail, nil
}

// nextSpanMark returns the mark to open at i: the one reaching furthest along
// the run, with spanMarks order breaking a tie.
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
func nextSpanMark(nodes []Node, i int, applied []Mark) (Mark, bool) {
	n := nodes[i]
	if n.Type != "text" {
		return Mark{}, false
	}
	var best Mark
	reach := 0
	for _, name := range spanMarks {
		for _, m := range n.Marks {
			if m.Type != name || carriesAll(applied, m) {
				continue
			}
			run := 1
			for j := i + 1; j < len(nodes) && carries(nodes[j], m); j++ {
				run++
			}
			if run > reach {
				best, reach = m, run
			}
		}
	}
	return best, reach > 0
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
// Emphasis switches to the underscore form when its content begins or ends
// with an asterisk, which happens whenever a nested span sits flush against
// this one's delimiter. `*0**0***` is what the asterisk form produces there,
// and CommonMark reads it as two emphasised words rather than as one holding a
// bold one — the round-trip fuzzer found it, and nothing else would have.
func delimiters(
	m Mark, inner string, prev, prevLive, next byte, where string,
) (open, closing string, err error) {
	// The asterisk form unless one of its delimiters would merge into a run
	// with what sits next to it — a nested span flush against this one on a
	// single side, or a neighbouring span's own delimiter. `*0*` beside
	// `**0**` is `*0***0**`, and the run of three in the middle is read as
	// something neither span says. Both sides flush is the ordinary `***x***`
	// spelling, where the runs merge symmetrically and read back correctly.
	char := ""
	for _, candidate := range []byte{'*', '_'} {
		if !merges(candidate, inner, prev, prevLive, next) {
			char = string(candidate)
			break
		}
	}
	if char == "" {
		// Markdown has two spellings for emphasis and both of them would be
		// read as something this document does not say. There is no third, so
		// this is refused rather than written down and hoped over.
		return "", "", unrepresentable(where,
			"emphasis that markdown cannot spell unambiguously here")
	}

	switch m.Type {
	case "strong":
		return char + char, char + char, nil
	case "em":
		return char, char, nil
	case "strike":
		return "~~", "~~", nil
	case "link":
		href, ok := attrString(m.Attrs, "href")
		if !ok || href == "" {
			return "", "", unrepresentable(where, "a link with no address")
		}
		target, err := linkTarget(href, where)
		if err != nil {
			return "", "", err
		}
		if title, ok := attrString(m.Attrs, "title"); ok && title != "" {
			// Markdown's own title syntax. Dropping it would be a silent loss:
			// it is what a reader sees on hover and it is not the address.
			target += ` "` + strings.NewReplacer(`\`, `\`+`\`, `"`, `\`+`"`).Replace(title) + `"`
		}
		return "[", "](" + target + ")", nil
	}
	return "", "", unrepresentable(where, "a %q mark", m.Type)
}

// splitEdgeSpace separates a span's leading and trailing whitespace from what
// the delimiters actually go around.
func splitEdgeSpace(s string) (lead, core, trail string) {
	// Every character markdown counts as whitespace, not just the two that
	// come up in practice: a delimiter may not sit against any of them.
	const space = " \t\n\r\v\f"
	core = strings.TrimLeft(s, space)
	lead = s[:len(s)-len(core)]
	trimmed := strings.TrimRight(core, space)
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
// The underscore has one more rule: it is inert against a word character,
// which is what keeps `customfield_10042` a field id.
func merges(char byte, inner string, prev, prevLive, next byte) bool {
	if char == '_' && (isWordByte(prev) || isWordByte(next)) {
		return true
	}
	// Nothing opens or closes against whitespace, so a span written there
	// would not be a span at all.
	if isSpaceByte(prev) && isSpaceByte(next) && prev != 0 && next != 0 {
		return false
	}
	if prevLive == char || next == char {
		return true
	}
	// A live delimiter strictly inside would close this span early, wherever
	// it came from — a nested span that picked the same character, or an
	// underscore inside a word, which looks like one and is inert.
	if insideLive(inner, char) {
		return true
	}
	// Flush on one side only.
	return (len(inner) > 0 && inner[0] == char) != (endsWithLive(inner) == char)
}

// insideLive reports an unescaped delimiter somewhere other than the two ends.
func insideLive(s string, char byte) bool {
	for i := 1; i < len(s)-1; i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == char {
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
func before(written string) byte {
	if written == "" {
		return 0
	}
	return written[len(written)-1]
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
func beforeOf(prefix, suffix string) byte {
	if suffix != "" {
		return suffix[len(suffix)-1]
	}
	return before(prefix)
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
// A neighbouring node carrying emphasis renders starting with its own
// delimiter, which is the collision this exists to see; anything else is that
// node's first character. An escaped one begins with a backslash, so reading
// the unescaped text is conservative in the direction that matters.
func after(rest []Node, applied []Mark) byte {
	if len(rest) == 0 || rest[0].Type != "text" {
		return 0
	}
	// Whitespace comes before the next span's delimiter, because that span
	// moves its own edge whitespace outside itself. Two delimiters with a
	// space between them do not merge.
	if rest[0].Text != "" && isSpaceByte(rest[0].Text[0]) {
		return rest[0].Text[0]
	}
	for _, m := range rest[0].Marks {
		// A mark already open around both nodes is not written between them.
		// Which delimiter the next span picks is not known yet, so the
		// asterisk is assumed: it is the one this span then avoids, and the
		// next span sees an underscore beside it and keeps the asterisk.
		if (m.Type == "strong" || m.Type == "em") && !carriesAll(applied, m) {
			return '*'
		}
	}
	if rest[0].Text == "" {
		return 0
	}
	return rest[0].Text[0]
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
func coalesce(nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if len(out) > 0 && n.Type == "text" {
			if prev := &out[len(out)-1]; prev.Type == "text" && sameMarks(prev.Marks, n.Marks) {
				prev.Text += n.Text
				continue
			}
		}
		out = append(out, n)
	}
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

// intraword reports whether the rune at i sits between two letters or digits,
// where CommonMark's flanking rules make an underscore inert.
func intraword(runes []rune, i int) bool {
	return i > 0 && i+1 < len(runes) &&
		isWordRune(runes[i-1]) && isWordRune(runes[i+1])
}

func isWordRune(r rune) bool {
	return r == '_' ||
		(r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		r > 127
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
