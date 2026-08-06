package adf

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
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
		return strings.Repeat("#", int(level)) + " " + text, nil

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
		return codeBlock(n), nil

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
		return autolink(url), nil

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

// codeBlock fences the code with enough backticks to contain whatever run of
// them the code itself holds.
func codeBlock(n Node) string {
	var body strings.Builder
	for _, c := range n.Content {
		// A codeBlock holds text nodes and nothing else. Marks inside one are
		// not rendered by Jira either, so the text is the content.
		body.WriteString(c.Text)
	}
	code := body.String()

	fence := strings.Repeat("`", max(3, longestBacktickRun(code)+1))
	lang, _ := attrString(n.Attrs, "language")
	return fence + lang + "\n" + code + "\n" + fence
}

func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
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
	return strings.Join(items, "\n"), nil
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

// tableLine writes one row, padded to width so a short row does not silently
// become a different shape than the header it sits under.
func tableLine(cells []string, width int) string {
	var b strings.Builder
	b.WriteString("|")
	for i := range width {
		b.WriteString(" ")
		if i < len(cells) {
			b.WriteString(strings.ReplaceAll(cells[i], "|", "\\|"))
		}
		b.WriteString(" |")
	}
	return b.String()
}

func inlineList(nodes []Node, where string) (string, error) {
	var b strings.Builder
	for _, n := range coalesce(nodes) {
		s, err := inline(n, where+" > "+n.Type)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
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

func inline(n Node, where string) (string, error) {
	switch n.Type {
	case "text":
		return marked(n, where)

	case "hardBreak":
		// A backslash, not two trailing spaces. Both are CommonMark hard
		// breaks; only one survives a tool that trims trailing whitespace, and
		// an invisible one cannot be reviewed in a diff.
		return "\\\n", nil

	case "media":
		return media(n, where)

	case "mention":
		id, _ := attrText(n.Attrs, "id")
		text, _ := attrString(n.Attrs, "text")
		if text == "" {
			text = "@" + id
		}
		if id == "" {
			return "", unrepresentable(where, "a mention of nobody")
		}
		return "[" + escapeText(text) + "](" + linkTarget("jira-user:"+id) + ")", nil

	case "emoji":
		// The text attribute is the emoji itself; the short name is what Jira
		// shows when it has no character to show. Either is the whole content.
		if text, ok := attrString(n.Attrs, "text"); ok && text != "" {
			return escapeText(text), nil
		}
		if short, ok := attrString(n.Attrs, "shortName"); ok && short != "" {
			return escapeText(short), nil
		}
		return "", unrepresentable(where, "an emoji with no character")

	case "status":
		text, _ := attrString(n.Attrs, "text")
		if text == "" {
			return "", unrepresentable(where, "a status lozenge with no text")
		}
		colour, ok := attrString(n.Attrs, "color")
		if !ok || colour == "" {
			colour = "neutral"
		}
		return "[" + escapeText(text) + "](" + linkTarget("jira-status:"+colour) + ")", nil

	case "date":
		return date(n, where)

	case "inlineCard":
		url, err := cardURL(n, where)
		if err != nil {
			return "", err
		}
		return autolink(url), nil

	case "inlineExtension":
		return "", unrepresentable(where, "an inline macro")
	case "placeholder":
		return "", unrepresentable(where, "an editor placeholder")
	}
	return "", unrepresentable(where, "a %q node", n.Type)
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
	return "[" + shown + "](" + linkTarget("jira-date:"+stamp) + ")", nil
}

func media(n Node, where string) (string, error) {
	alt, _ := attrString(n.Attrs, "alt")

	// An external or linked media carries the URL it points at, which is a
	// markdown image with nothing invented.
	if url, ok := attrString(n.Attrs, "url"); ok && url != "" {
		return "![" + escapeText(alt) + "](" + linkTarget(url) + ")", nil
	}

	id, _ := attrText(n.Attrs, "id")
	if id == "" {
		return "", unrepresentable(where, "an attachment with no id")
	}
	target := "jira-media:" + id
	if collection, ok := attrString(n.Attrs, "collection"); ok && collection != "" {
		target = "jira-media:" + collection + "/" + id
	}
	return "![" + escapeText(alt) + "](" + linkTarget(target) + ")", nil
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

// marks this converter can write, innermost first. The order is fixed so that
// two text nodes with the same marks in a different order produce the same
// markdown — Jira's order is an artefact of how the text was typed.
var markOrder = []struct {
	name  string
	open  string
	close string
}{
	{"code", "`", "`"},
	{"strike", "~~", "~~"},
	{"em", "*", "*"},
	{"strong", "**", "**"},
}

func marked(n Node, where string) (string, error) {
	var link *Mark
	present := map[string]bool{}
	for i, m := range n.Marks {
		switch m.Type {
		case "code", "strike", "em", "strong":
			present[m.Type] = true
		case "link":
			link = &n.Marks[i]
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

	// Code is verbatim: escaping inside a code span would put backslashes in
	// the text, and a code span needs no escaping to be exact.
	out := escapeText(n.Text)
	if present["code"] {
		out = codeSpan(n.Text)
	}
	for _, m := range markOrder {
		if m.name == "code" || !present[m.name] {
			continue
		}
		out = m.open + out + m.close
	}

	if link != nil {
		href, ok := attrString(link.Attrs, "href")
		if !ok || href == "" {
			return "", unrepresentable(where, "a link with no address")
		}
		out = "[" + out + "](" + linkTarget(href) + ")"
	}
	return out, nil
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
func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	atLineStart := true
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
		case '#', '+', '-', '=':
			// Only meaningful where a block can begin. Mid-line they are text.
			if atLineStart {
				b.WriteByte('\\')
			}
		case '.', ')':
			// An ordered-list marker: a run of digits at the start of a line
			// followed by one of these.
			if startsOrderedList(runes, i) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
		atLineStart = r == '\n'
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
func startsOrderedList(runes []rune, i int) bool {
	j := i - 1
	for j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
		j--
	}
	return j < i-1 && (j < 0 || runes[j] == '\n')
}

// linkTarget writes a link destination.
//
// A URL Jira stored can hold anything, including the parenthesis that would
// close the link. Rather than percent-encode it — which cannot be undone,
// because a `%28` that was already in the URL and one this function wrote are
// the same three characters — the destination goes inside angle brackets,
// which is CommonMark's own answer and reverses exactly.
func linkTarget(s string) string {
	if s == "" {
		return "<>"
	}
	if !strings.ContainsAny(s, " \t\n\r()<>\\") {
		return s
	}
	r := strings.NewReplacer(
		`\`, `\\`,
		"<", `\<`,
		">", `\>`,
		// A line ending cannot appear in an angle-bracket destination at all,
		// so these two are the one place encoding is unavoidable.
		"\n", "%0A",
		"\r", "%0D",
	)
	return "<" + r.Replace(s) + ">"
}

// autolink writes a bare URL. CommonMark's `<url>` form holds no spaces and no
// angle brackets, so a URL carrying either becomes an ordinary link whose text
// is the URL — which reads the same and reverses the same.
func autolink(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r<>\\") {
		return "<" + s + ">"
	}
	return "[" + escapeText(s) + "](" + linkTarget(s) + ")"
}
