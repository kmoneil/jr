package adf

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// FromMarkdown converts markdown to an ADF document.
//
// It covers a documented subset and refuses the rest by name. That is the
// whole design: this is where the incumbent's known bugs live, and every one
// of them is the same bug — markdown the converter did not understand reaching
// Jira as something else. A construct outside the subset is an error naming it
// and the line it is on, never a guess.
//
// The subset is CommonMark's block and inline structure minus the parts ADF
// has no node for, plus GFM tables, task lists, and strikethrough, plus the
// link schemes ToMarkdown writes — so a body read out of Jira as markdown goes
// back in as the document it came from. See docs/output-contract.md.
//
// A single newline inside a paragraph is a soft break, which is markdown's own
// meaning for it: the lines join with a space. A line ending in a backslash is
// a hard break. This is deliberately not what FromText does with the same
// input, because FromText contains text and this parses it.
func FromMarkdown(text string) (Node, error) {
	if !utf8.ValidString(text) {
		return Node{}, errs.Usage("INVALID_ENCODING", "the text is not valid UTF-8").
			WithRemedy("check the file or pipe it came from")
	}

	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	content, err := parseBlocks(strings.Split(normalized, "\n"), 1)
	if err != nil {
		return Node{}, err
	}
	if len(content) == 0 {
		// An empty document still has to be a document: Jira rejects a doc
		// with no content, and it rejects it with a message about the schema
		// rather than about the empty body somebody actually sent.
		content = []Node{{Type: "paragraph"}}
	}

	doc := Node{Type: "doc", Version: Version, Content: content}
	numberTasks(doc.Content, new(int))
	return doc, nil
}

// numberTasks gives every task list and task a local id.
//
// Jira requires one and rejects the whole document without it, which is how
// this was found: a comment whose only fault was a checkbox came back as
// "INVALID_INPUT; comment", naming neither the node nor the attribute.
//
// The ids are sequential rather than random because they only have to be
// unique inside one document, and a document that is a function of its input
// is one a test can pin. Nothing outside the document refers to them: ADF's
// local id is editor state, and ToMarkdown drops it for that reason.
func numberTasks(nodes []Node, n *int) {
	for i := range nodes {
		switch nodes[i].Type {
		case "taskList", "taskItem":
			*n++
			if nodes[i].Attrs == nil {
				nodes[i].Attrs = map[string]any{}
			}
			nodes[i].Attrs["localId"] = strconv.Itoa(*n)
		}
		numberTasks(nodes[i].Content, n)
	}
}

// unsupported is the one refusal in this file. Every construct outside the
// subset reports the same way, and every one of them names its line.
func unsupported(line int, what string, args ...any) *errs.Error {
	return errs.Usage("MARKDOWN_UNSUPPORTED",
		"this markdown holds %s, which Jira has no way to store",
		fmt.Sprintf(what, args...)).
		WithDetail("line %d", line).
		WithRemedy("remove it, or send the body with --body-format text to " +
			"store it as the characters you typed")
}

// parseBlocks converts a run of lines. first is the 1-based number of lines[0]
// in the caller's input, so a refusal from inside a nested list still names the
// line somebody has to go and edit.
func parseBlocks(lines []string, first int) ([]Node, error) {
	var out []Node
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}
		at := first + i

		if isIndentedCode(line) {
			// CommonMark's four-space code block. It is refused rather than
			// supported because at this point in a document it is far more
			// often a list item that lost its marker than deliberate code.
			return nil, unsupported(at, "an indented code block").
				WithRemedy("fence it with ``` so it cannot be confused with " +
					"an over-indented list item")
		}

		var node Node
		var used int
		var err error
		switch {
		case fenceOf(line) != "":
			node, used, err = parseFence(lines[i:], at)
		case headingLevel(line) > 0:
			node, used, err = parseHeading(lines[i:], at)
		case isThematicBreak(line):
			node, used = Node{Type: "rule"}, 1
		case strings.HasPrefix(strings.TrimLeft(line, " "), ">"):
			node, used, err = parseQuote(lines[i:], at)
		case isTableStart(lines[i:]):
			node, used, err = parseTable(lines[i:], at)
		case listMarker(line) != nil:
			node, used, err = parseList(lines[i:], at)
		default:
			node, used, err = parseParagraph(lines[i:], at)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, node)
		i += used
	}
	return out, nil
}

// isIndentedCode reports a line indented far enough to be CommonMark's code
// block. Only called where a block can begin, so a continuation line inside a
// list item never reaches it.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

// fenceOf returns the fence that opens a code block, or "".
func fenceOf(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	for _, char := range []string{"`", "~"} {
		run := 0
		for run < len(trimmed) && string(trimmed[run]) == char {
			run++
		}
		if run >= 3 {
			return trimmed[:run]
		}
	}
	return ""
}

func parseFence(lines []string, at int) (Node, int, error) {
	fence := fenceOf(lines[0])
	info := strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(lines[0], " "), fence))
	if strings.HasPrefix(fence, "`") && strings.Contains(info, "`") {
		return Node{}, 0, unsupported(at, "a backtick in a code fence's language")
	}

	for end := 1; end < len(lines); end++ {
		closer := strings.TrimSpace(lines[end])
		if strings.HasPrefix(closer, fence[:1]) && len(closer) >= len(fence) &&
			strings.Trim(closer, fence[:1]) == "" {
			node := Node{Type: "codeBlock"}
			if info != "" {
				node.Attrs = map[string]any{"language": info}
			}
			if code := strings.Join(lines[1:end], "\n"); code != "" {
				node.Content = []Node{{Type: "text", Text: code}}
			}
			return node, end + 1, nil
		}
	}
	return Node{}, 0, unsupported(at, "a code fence that is never closed")
}

// headingLevel returns the level of an ATX heading, or 0.
func headingLevel(line string) int {
	trimmed := strings.TrimLeft(line, " ")
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0
	}
	if level == len(trimmed) || trimmed[level] == ' ' {
		return level
	}
	return 0
}

func parseHeading(lines []string, at int) (Node, int, error) {
	trimmed := strings.TrimLeft(lines[0], " ")
	level := headingLevel(lines[0])
	text := strings.TrimSpace(trimmed[level:])
	// A closing run of hashes is decoration, not content.
	text = strings.TrimRight(text, "#")

	content, err := parseInline(strings.TrimSpace(text), at)
	if err != nil {
		return Node{}, 0, err
	}
	return Node{
		Type:    "heading",
		Attrs:   map[string]any{"level": float64(level)},
		Content: content,
	}, 1, nil
}

// isThematicBreak reports a rule: three or more of one character, spaces
// permitted between them and nothing else on the line.
func isThematicBreak(line string) bool {
	trimmed := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
	if len(trimmed) < 3 {
		return false
	}
	for _, char := range "-*_" {
		if strings.Trim(trimmed, string(char)) == "" {
			return true
		}
	}
	return false
}

// isSetextUnderline reports a line that would turn the paragraph above it into
// a heading. It is refused rather than honoured, because `---` under a
// paragraph and `---` after a blank line are a heading and a rule, and one
// invisible blank line between them is the whole difference.
func isSetextUnderline(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return strings.Trim(trimmed, "=") == "" || strings.Trim(trimmed, "-") == ""
}

func parseQuote(lines []string, at int) (Node, int, error) {
	var inner []string
	used := 0
	for used < len(lines) {
		line := strings.TrimLeft(lines[used], " ")
		if !strings.HasPrefix(line, ">") {
			if strings.TrimSpace(line) == "" {
				break
			}
			// CommonMark would read this as a lazy continuation of the quote.
			// Whether a line belongs to the quotation or to the text after it
			// should not depend on knowing that rule.
			return Node{}, 0, unsupported(at+used,
				"a quoted line with no > in front of it").
				WithRemedy("put > on every line of the quote")
		}
		inner = append(inner, strings.TrimPrefix(strings.TrimPrefix(line, ">"), " "))
		used++
	}

	// `> [!WARNING]` on the first line is a panel rather than a quotation.
	kind := ""
	if len(inner) > 0 {
		if name, ok := alertName(inner[0]); ok {
			kind, inner = name, inner[1:]
		}
	}

	content, err := parseBlocks(inner, at)
	if err != nil {
		return Node{}, 0, err
	}
	if kind == "" {
		return Node{Type: "blockquote", Content: content}, used, nil
	}
	if len(content) == 0 {
		// A panel with no content is not a shape Jira stores.
		content = []Node{{Type: "paragraph"}}
	}
	return Node{
		Type:    "panel",
		Attrs:   map[string]any{"panelType": kind},
		Content: content,
	}, used, nil
}

// alertName reads a GitHub alert marker and maps it back to ADF's panel type.
func alertName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[!") || !strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	want := strings.TrimSuffix(strings.TrimPrefix(trimmed, "[!"), "]")
	for kind, alert := range panelTypes {
		if alert == want {
			return kind, true
		}
	}
	return "", false
}

// marker is one list item's opening.
type marker struct {
	// width is how far the item's content is indented from the line start.
	width int
	// bullet is the character for an unordered list, or 0.
	bullet byte
	// delim is the `.` or `)` of an ordered list, or 0.
	delim byte
	// number is an ordered list's first number.
	number int
	// task is "TODO" or "DONE" for a checkbox item, or "".
	task string
	// indent is the leading whitespace before the marker.
	indent int
}

// sameListAs reports whether two markers belong to one list. CommonMark starts
// a new list when the marker character changes, and so does this.
func (m marker) sameListAs(other marker) bool {
	return m.bullet == other.bullet && m.delim == other.delim &&
		(m.task == "") == (other.task == "")
}

// listMarker reads the marker opening a list item, or nil.
func listMarker(line string) *marker {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	rest := line[indent:]
	if rest == "" || isThematicBreak(line) {
		return nil
	}

	m := marker{indent: indent}
	switch rest[0] {
	case '-', '*', '+':
		m.bullet = rest[0]
		rest = rest[1:]
	default:
		digits := 0
		for digits < len(rest) && digits < 9 && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 || digits >= len(rest) || (rest[digits] != '.' && rest[digits] != ')') {
			return nil
		}
		m.number, _ = strconv.Atoi(rest[:digits])
		m.delim = rest[digits]
		rest = rest[digits+1:]
	}

	spaces := len(rest) - len(strings.TrimLeft(rest, " "))
	if spaces == 0 && rest != "" {
		// `-word` is text, not a list.
		return nil
	}
	if spaces == 0 {
		// A marker alone on its line: an empty item.
		m.width = len(line) - indent + indent
		return &m
	}
	rest = rest[spaces:]
	m.width = len(line) - len(rest)

	// A checkbox turns the item into a task, which ADF stores as its own kind
	// of list rather than as a list item that happens to start with a box.
	if m.bullet != 0 && len(rest) >= 3 && rest[0] == '[' && rest[2] == ']' &&
		(len(rest) == 3 || rest[3] == ' ') {
		switch rest[1] {
		case ' ':
			m.task = "TODO"
		case 'x', 'X':
			m.task = "DONE"
		}
		if m.task != "" {
			m.width += 3
			if len(rest) > 3 {
				m.width++
			}
		}
	}
	return &m
}

// listItem is one item's lines, plus where they came from.
type listItem struct {
	lines []string
	at    int
	state string
}

func parseList(lines []string, at int) (Node, int, error) {
	first := listMarker(lines[0])

	// Gather each item's lines: the marker line's remainder, plus every
	// following line indented at least to the item's content.
	var items []listItem
	used := 0
	for used < len(lines) {
		m := listMarker(lines[used])
		if m == nil || m.indent != first.indent || !m.sameListAs(*first) {
			break
		}
		body := []string{lines[used][min(m.width, len(lines[used])):]}
		start := used
		used++

		for used < len(lines) {
			line := lines[used]
			if strings.TrimSpace(line) == "" {
				// A blank line ends the item unless the list continues under
				// it, which is what makes a list loose rather than what ends
				// it.
				if used+1 >= len(lines) || !continuesItem(lines[used+1], *first) {
					break
				}
				body = append(body, "")
				used++
				continue
			}
			if !continuesItem(line, *first) {
				break
			}
			body = append(body, dedent(line, m.width))
			used++
		}
		items = append(items, listItem{lines: body, at: at + start, state: m.task})
	}

	if first.task != "" {
		node, err := taskListNode(items)
		return node, used, err
	}

	nodes := make([]Node, 0, len(items))
	for _, it := range items {
		content, err := parseBlocks(it.lines, it.at)
		if err != nil {
			return Node{}, 0, err
		}
		if len(content) == 0 {
			content = []Node{{Type: "paragraph"}}
		}
		nodes = append(nodes, Node{Type: "listItem", Content: content})
	}

	out := Node{Type: "bulletList", Content: nodes}
	if first.delim != 0 {
		out.Type = "orderedList"
		if first.number != 1 {
			out.Attrs = map[string]any{"order": float64(first.number)}
		}
	}
	return out, used, nil
}

// continuesItem reports whether a line belongs to the item above it: indented
// past the marker, or a nested marker of its own.
func continuesItem(line string, first marker) bool {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	return indent > first.indent
}

// dedent removes up to width leading spaces.
func dedent(line string, width int) string {
	for range width {
		if !strings.HasPrefix(line, " ") {
			break
		}
		line = line[1:]
	}
	return line
}

// taskListNode builds a taskList.
//
// Its items hold inline content and a nested task list, because that is what
// ADF's taskItem holds. A checkbox with a paragraph and a table under it is a
// shape Jira will not store, so it is refused here rather than sent.
func taskListNode(items []listItem) (Node, error) {
	tasks := make([]Node, 0, len(items))
	for _, it := range items {
		var own []string
		var nested []listItem
		for i, line := range it.lines {
			if m := listMarker(line); m != nil && m.task != "" && i > 0 {
				// Everything from here down is the sub-list.
				sub, err := nestedTasks(it.lines[i:], it.at+i)
				if err != nil {
					return Node{}, err
				}
				nested = sub
				break
			}
			own = append(own, line)
		}

		text := strings.TrimSpace(strings.Join(own, "\n"))
		if strings.Contains(text, "\n\n") {
			return Node{}, unsupported(it.at, "a task holding more than one paragraph")
		}
		content, err := parseInline(text, it.at)
		if err != nil {
			return Node{}, err
		}

		state := it.state
		if state == "" {
			state = "TODO"
		}
		task := Node{
			Type:    "taskItem",
			Attrs:   map[string]any{"state": state},
			Content: content,
		}
		tasks = append(tasks, task)

		if len(nested) > 0 {
			sub, err := taskListNode(nested)
			if err != nil {
				return Node{}, err
			}
			tasks = append(tasks, sub)
		}
	}
	return Node{Type: "taskList", Content: tasks}, nil
}

// nestedTasks splits an indented run of checkbox lines into items.
func nestedTasks(lines []string, at int) ([]listItem, error) {
	first := listMarker(lines[0])
	if first == nil {
		return nil, nil
	}

	var items []listItem
	for i := 0; i < len(lines); {
		m := listMarker(lines[i])
		if m == nil || m.indent != first.indent {
			return nil, unsupported(at+i, "a task list broken by other content")
		}
		body := []string{lines[i][min(m.width, len(lines[i])):]}
		start := i
		i++
		for i < len(lines) && listMarker(lines[i]) == nil {
			body = append(body, dedent(lines[i], m.width))
			i++
		}
		items = append(items, listItem{lines: body, at: at + start, state: m.task})
	}
	return items, nil
}

// isTableStart reports a GFM table: a row of cells, then a delimiter row.
func isTableStart(lines []string) bool {
	if len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "|") {
		return false
	}
	delim := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(delim, "|") {
		return false
	}
	for _, cell := range splitRow(delim) {
		if strings.Trim(strings.TrimSpace(cell), "-:") != "" || cell == "" {
			return false
		}
	}
	return true
}

func parseTable(lines []string, at int) (Node, int, error) {
	for _, cell := range splitRow(lines[1]) {
		if strings.Contains(cell, ":") {
			// ADF has no column alignment. Storing the table without it would
			// drop something the author wrote on purpose.
			return Node{}, 0, unsupported(at+1, "a table with column alignment")
		}
	}

	header, err := tableRowNode(splitRow(lines[0]), "tableHeader", at)
	if err != nil {
		return Node{}, 0, err
	}
	rows := []Node{header}

	used := 2
	for used < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[used]), "|") {
		row, err := tableRowNode(splitRow(lines[used]), "tableCell", at+used)
		if err != nil {
			return Node{}, 0, err
		}
		rows = append(rows, row)
		used++
	}
	return Node{Type: "table", Content: rows}, used, nil
}

// splitRow splits a table row on unescaped pipes.
func splitRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|")

	var cells []string
	var cur strings.Builder
	for i := 0; i < len(trimmed); i++ {
		switch {
		case trimmed[i] == '\\' && i+1 < len(trimmed) && trimmed[i+1] == '|':
			// The escape is consumed here: a cell's content is the text
			// without the escaping the row structure needed.
			cur.WriteByte('|')
			i++
		case trimmed[i] == '|':
			cells = append(cells, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(trimmed[i])
		}
	}
	cells = append(cells, cur.String())
	return cells
}

func tableRowNode(cells []string, element string, at int) (Node, error) {
	out := make([]Node, 0, len(cells))
	for _, cell := range cells {
		content, err := parseInline(strings.TrimSpace(cell), at)
		if err != nil {
			return Node{}, err
		}
		// An empty cell is still a cell, and ADF spells it as one holding an
		// empty paragraph rather than as one holding nothing.
		out = append(out, Node{
			Type:    element,
			Content: []Node{{Type: "paragraph", Content: content}},
		})
	}
	return Node{Type: "tableRow", Content: out}, nil
}

func parseParagraph(lines []string, at int) (Node, int, error) {
	used := 0
	for used < len(lines) {
		line := lines[used]
		if strings.TrimSpace(line) == "" {
			break
		}
		if used > 0 {
			if isSetextUnderline(line) {
				return Node{}, 0, unsupported(at+used, "a setext heading").
					WithRemedy("write the heading with # instead")
			}
			if startsBlock(lines[used:]) {
				break
			}
		}
		used++
	}

	content, err := parseInline(strings.Join(lines[:used], "\n"), at)
	if err != nil {
		return Node{}, 0, err
	}

	// A paragraph holding nothing but images is how ADF spells an attachment,
	// which is a block of its own rather than a paragraph.
	if media, ok := mediaBlock(content); ok {
		return media, used, nil
	}
	for _, n := range content {
		if n.Type == "media" {
			return Node{}, 0, unsupported(at,
				"an image beside other text in one paragraph").
				WithRemedy("put the image on a line of its own")
		}
	}
	return Node{Type: "paragraph", Content: content}, used, nil
}

// startsBlock reports whether a line interrupts the paragraph above it.
func startsBlock(lines []string) bool {
	line := lines[0]
	return headingLevel(line) > 0 || fenceOf(line) != "" || isThematicBreak(line) ||
		strings.HasPrefix(strings.TrimLeft(line, " "), ">") ||
		listMarker(line) != nil || isTableStart(lines)
}

// mediaBlock wraps a run of images as the block ADF stores them in.
func mediaBlock(content []Node) (Node, bool) {
	if len(content) == 0 {
		return Node{}, false
	}
	for _, n := range content {
		if n.Type != "media" {
			return Node{}, false
		}
	}
	if len(content) == 1 {
		return Node{
			Type:    "mediaSingle",
			Attrs:   map[string]any{"layout": "center"},
			Content: content,
		}, true
	}
	return Node{Type: "mediaGroup", Content: content}, true
}
