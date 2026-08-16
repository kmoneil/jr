package adf

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
)

// blockSpace is the whitespace markdown counts when it decides block
// structure: a space and a tab, and nothing else.
//
// strings.TrimSpace is Unicode's set, which adds the vertical tab, the form
// feed, NEL, and the non-breaking space. Deciding a block with it ate those
// characters silently at a line edge. A heading ending in a non-breaking space
// came back one character shorter, so did a table cell, and a line holding only
// one read as blank and split a paragraph in two. None of it was refused or
// warned about, which is the one thing this package is not allowed to do to a
// value.
//
// It matters because Jira keeps the character. A heading ending in U+00A0
// posted to the Cloud sandbox on 2026-08-16 came back with the U+00A0 on it, so
// the tool was dropping something the server stores, on the Jira to markdown to
// Jira path that a person actually performs. An NBSP at a line edge is not
// exotic; it is what a paste out of a word processor leaves behind.
//
// A line ending never reaches here. FromMarkdown turns CRLF and a lone CR into
// LF before anything is parsed and the text is split on LF, so a carriage
// return cannot survive to be trimmed or to make a line look non-blank. That
// was the risk worth checking before narrowing the set, and it was already
// closed.
const blockSpace = " \t"

// trimLine is TrimSpace over markdown's whitespace rather than Unicode's.
func trimLine(s string) string { return strings.Trim(s, blockSpace) }

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

	// Anything this builds has to be something ToMarkdown can carry back.
	//
	// Otherwise the two halves of this package disagree about the subset, and
	// a body means one thing going into Jira and another coming out — which is
	// the exact failure the whole package exists to prevent, arriving from the
	// inside. Checking it here costs one conversion of a small document and
	// makes the agreement structural rather than a property the tests happen
	// to hold: every case the round-trip fuzzer found was one of these.
	if _, err := ToMarkdown(doc); err != nil {
		return Node{}, errs.Usage("MARKDOWN_UNSUPPORTED",
			"this markdown builds something that cannot be written down again").
			WithDetail("%s", errs.Coerce(err).Message).
			WithRemedy("remove it, or send the body with --body-format text " +
				"to store it as the characters you typed")
	}
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

// blocksAllowedIn is what Jira Cloud accepts inside each container.
//
// It is not the ADF documentation's content model; it is the sandbox's answer
// to posting each combination. Jira refuses the rest with "INVALID_INPUT;
// comment: INVALID_INPUT", which names neither the node nor where it was — so
// a caller who nested a table inside a quote gets a message about their
// markdown here, and a 400 about nothing there.
var blocksAllowedIn = map[string]map[string]bool{
	"blockquote": {
		"paragraph": true, "codeBlock": true,
		"bulletList": true, "orderedList": true,
		"mediaSingle": true, "mediaGroup": true,
	},
	"panel": {
		"paragraph": true, "heading": true, "codeBlock": true,
		"bulletList": true, "orderedList": true, "taskList": true,
		"rule": true, "mediaSingle": true, "mediaGroup": true, "blockCard": true,
	},
	"listItem": {
		"paragraph": true, "codeBlock": true,
		"bulletList": true, "orderedList": true, "taskList": true,
		"mediaSingle": true, "mediaGroup": true,
	},
}

// blockNames read as a sentence, because the refusal is one.
var blockNames = map[string]string{
	"paragraph":   "a paragraph",
	"heading":     "a heading",
	"blockquote":  "a blockquote",
	"panel":       "a panel",
	"table":       "a table",
	"rule":        "a rule",
	"codeBlock":   "a code block",
	"bulletList":  "a list",
	"orderedList": "a list",
	"taskList":    "a task list",
	"listItem":    "a list item",
	"mediaSingle": "an image",
	"mediaGroup":  "images",
}

func blockName(nodeType string) string {
	if name, ok := blockNames[nodeType]; ok {
		return name
	}
	return "a " + nodeType
}

// checkContent refuses a block Jira will not accept where it was written.
func checkContent(container string, content []Node, at int) error {
	allowed := blocksAllowedIn[container]
	for _, n := range content {
		if allowed[n.Type] {
			continue
		}
		return unsupported(at, "%s inside %s", blockName(n.Type), blockName(container)).
			WithRemedy("Jira does not store one there; move it out")
	}
	return nil
}

// parseBlocks converts a run of lines. first is the 1-based number of lines[0]
// in the caller's input, so a refusal from inside a nested list still names the
// line somebody has to go and edit.
func parseBlocks(lines []string, first int) ([]Node, error) {
	var out []Node
	for i := 0; i < len(lines); {
		line := lines[i]
		if trimLine(line) == "" {
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
//
// A backtick fence whose info string holds a backtick is not a fence — that is
// CommonMark's rule, and it is what keeps a line beginning with a code span of
// three backticks a paragraph. Reading it as a fence was found by the
// round-trip fuzzer: a code span holding two backticks is written with three,
// and the line then opened a block that swallowed the rest of the document.
func fenceOf(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	for _, char := range []string{"`", "~"} {
		run := 0
		for run < len(trimmed) && string(trimmed[run]) == char {
			run++
		}
		if run < 3 {
			continue
		}
		if char == "`" && strings.Contains(trimmed[run:], "`") {
			return ""
		}
		return trimmed[:run]
	}
	return ""
}

func parseFence(lines []string, at int) (Node, int, error) {
	fence := fenceOf(lines[0])
	info := trimLine(strings.TrimPrefix(strings.TrimLeft(lines[0], " "), fence))

	for end := 1; end < len(lines); end++ {
		closer := trimLine(lines[end])
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
	text := trimLine(trimmed[level:])
	text = trimClosingHashes(text)

	content, err := parseInline(trimLine(text), at)
	if err != nil {
		return Node{}, 0, err
	}
	return Node{
		Type:    "heading",
		Attrs:   map[string]any{"level": float64(level)},
		Content: content,
	}, 1, nil
}

// trimClosingHashes removes a heading's decorative closing run.
//
// It is decoration only where a space precedes it, or where it is the whole
// heading — CommonMark's rule, and the one that keeps `# \#` a heading whose
// text is a hash. Trimming every trailing hash ate the escape and turned the
// heading into a stray backslash, which the round-trip fuzzer found by writing
// one and reading it back.
func trimClosingHashes(text string) string {
	end := len(text)
	for end > 0 && text[end-1] == '#' {
		end--
	}
	if end == len(text) {
		return text
	}
	if end == 0 || text[end-1] == ' ' {
		return strings.TrimRight(text[:end], " ")
	}
	return text
}

// isThematicBreak reports a rule: three or more of one character, spaces
// permitted between them and nothing else on the line.
func isThematicBreak(line string) bool {
	trimmed := strings.ReplaceAll(trimLine(line), " ", "")
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
	trimmed := trimLine(line)
	if trimmed == "" {
		return false
	}
	return strings.Trim(trimmed, "=") == "" || strings.Trim(trimmed, "-") == ""
}

// leadingQuoteType reports whether the first block of these lines is a
// blockquote or a panel, and which, or "" for anything else.
//
// It mirrors the arm of parseBlocks' dispatch that reaches parseQuote, and the
// two have to agree in one direction only. A miss is harmless — the existing
// checkContent after parseBlocks still refuses, just slowly, which is what
// every case did before this existed. A false positive is not: it would refuse
// markdown that parses. So every condition here is one that stops this
// returning a type, and the ordering matters for exactly one input: `    > x`
// is CommonMark's indented code block, and TrimLeft would otherwise read it as
// a quote.
func leadingQuoteType(lines []string) string {
	for _, line := range lines {
		if trimLine(line) == "" {
			continue
		}
		if isIndentedCode(line) || fenceOf(line) != "" ||
			headingLevel(line) > 0 || isThematicBreak(line) {
			return ""
		}
		trimmed := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmed, ">") {
			return ""
		}
		// `> [!WARNING]` opens a panel and everything else a quotation, which
		// is the same split parseQuote makes on its own first line. Neither is
		// storable inside anything, so this only decides which word the
		// refusal uses.
		if _, ok := alertName(strings.TrimPrefix(strings.TrimPrefix(trimmed, ">"), " ")); ok {
			return "panel"
		}
		return "blockquote"
	}
	return ""
}

// quoteBody strips one `>` marker from each line of the quotation, and reports
// how many lines it consumed.
//
// A line with no marker ends the quote if it is blank and refuses it otherwise.
// CommonMark would read the second as a lazy continuation, and whether a line
// belongs to the quotation or to the text after it should not depend on knowing
// that rule.
func quoteBody(lines []string, at int) ([]string, int, error) {
	var inner []string
	used := 0
	for used < len(lines) {
		line := strings.TrimLeft(lines[used], " ")
		if !strings.HasPrefix(line, ">") {
			if trimLine(line) == "" {
				break
			}
			return nil, 0, unsupported(at+used,
				"a quoted line with no > in front of it").
				WithRemedy("put > on every line of the quote")
		}
		inner = append(inner, strings.TrimPrefix(strings.TrimPrefix(line, ">"), " "))
		used++
	}
	return inner, used, nil
}

func parseQuote(lines []string, at int) (Node, int, error) {
	inner, used, err := quoteBody(lines, at)
	if err != nil {
		return Node{}, 0, err
	}

	// `> [!WARNING]` on the first line is a panel rather than a quotation.
	kind := ""
	if len(inner) > 0 {
		if name, ok := alertName(inner[0]); ok {
			kind, inner = name, inner[1:]
		}
	}

	container := "blockquote"
	if kind != "" {
		container = "panel"
	}
	// Refused before the recursion rather than after it. Nothing this parser
	// builds accepts a nested quote — no entry in blocksAllowedIn lists
	// blockquote or panel — so checkContent below was always going to refuse
	// this, having first parsed the whole nest to find out. `> > > …` strips one
	// marker per level and copies the rest, so the cost of reaching that verdict
	// was quadratic: 32KB of `> ` took 2.26s to say no, at a clean 4x per
	// doubling.
	//
	// checkContent produces the refusal rather than a message written here, so
	// the error is the same one by construction rather than by two strings
	// agreeing today. TestNestedQuotesAreRefusedInLinearTime pins the shape of
	// the curve; a message assertion would not have noticed the quadratic and
	// the ten-second ceiling in perf_test.go did not.
	if leading := leadingQuoteType(inner); leading != "" {
		if err := checkContent(container, []Node{{Type: leading}}, at); err != nil {
			return Node{}, 0, err
		}
	}

	content, err := parseBlocks(inner, at)
	if err != nil {
		return Node{}, 0, err
	}
	if kind == "" {
		if err := checkContent(container, content, at); err != nil {
			return Node{}, 0, err
		}
		return Node{Type: "blockquote", Content: content}, used, nil
	}
	if len(content) == 0 {
		// A panel with no content is not a shape Jira stores.
		content = []Node{{Type: "paragraph"}}
	}
	if err := checkContent(container, content, at); err != nil {
		return Node{}, 0, err
	}
	return Node{
		Type:    "panel",
		Attrs:   map[string]any{"panelType": kind},
		Content: content,
	}, used, nil
}

// alertName reads a GitHub alert marker and maps it back to ADF's panel type.
func alertName(line string) (string, bool) {
	trimmed := trimLine(line)
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
	rest, ok := m.readBullet(rest)
	if !ok {
		return nil
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
	m.readCheckbox(rest)
	return &m
}

// readBullet consumes the marker itself, either a bullet character or a number
// and its delimiter, and returns what follows it.
func (m *marker) readBullet(rest string) (string, bool) {
	switch rest[0] {
	case '-', '*', '+':
		m.bullet = rest[0]
		return rest[1:], true
	}

	digits := 0
	for digits < len(rest) && digits < 9 && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits >= len(rest) || (rest[digits] != '.' && rest[digits] != ')') {
		return "", false
	}
	m.number, _ = strconv.Atoi(rest[:digits])
	m.delim = rest[digits]
	return rest[digits+1:], true
}

// readCheckbox turns the item into a task, which ADF stores as its own kind of
// list rather than as a list item that happens to start with a box.
func (m *marker) readCheckbox(rest string) {
	if m.bullet == 0 || len(rest) < 3 || rest[0] != '[' || rest[2] != ']' ||
		(len(rest) != 3 && rest[3] != ' ') {
		return
	}
	switch rest[1] {
	case ' ':
		m.task = "TODO"
	case 'x', 'X':
		m.task = "DONE"
	default:
		return
	}
	m.width += 3
	if len(rest) > 3 {
		m.width++
	}
}

// listItem is one item's lines, plus where they came from.
type listItem struct {
	lines []string
	at    int
	state string
}

func parseList(lines []string, at int) (Node, int, error) {
	first := listMarker(lines[0])
	items, used := gatherItems(lines, at, *first)

	if first.task != "" {
		node, err := taskListNode(items)
		return node, used, err
	}

	nodes, err := listItemNodes(items)
	if err != nil {
		return Node{}, 0, err
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

// gatherItems collects each item's lines: the marker line's remainder, plus
// every following line indented at least to the item's content. It returns how
// many of the input lines the list consumed.
func gatherItems(lines []string, at int, first marker) ([]listItem, int) {
	var items []listItem
	used := 0
	for used < len(lines) {
		m := listMarker(lines[used])
		if m == nil || m.indent != first.indent || !m.sameListAs(first) {
			break
		}
		start := used
		body := []string{lines[used][min(m.width, len(lines[used])):]}
		used++

		rest, next := gatherItemBody(lines, used, first, m.width)
		body, used = append(body, rest...), next
		items = append(items, listItem{lines: body, at: at + start, state: m.task})
	}
	return items, used
}

// gatherItemBody reads the continuation lines of one item, and returns the
// index the next item starts at.
func gatherItemBody(lines []string, from int, first marker, width int) ([]string, int) {
	var body []string
	i := from
	for i < len(lines) {
		line := lines[i]
		if trimLine(line) == "" {
			// A blank line ends the item unless the list continues under it,
			// which is what makes a list loose rather than what ends it.
			if i+1 >= len(lines) || !continuesItem(lines[i+1], first) {
				break
			}
			body = append(body, "")
			i++
			continue
		}
		if !continuesItem(line, first) {
			break
		}
		body = append(body, dedent(line, width))
		i++
	}
	return body, i
}

// listItemNodes parses each gathered item into a listItem node. An item with no
// content still gets an empty paragraph, because ADF's listItem cannot be empty.
func listItemNodes(items []listItem) ([]Node, error) {
	nodes := make([]Node, 0, len(items))
	for _, it := range items {
		content, err := parseBlocks(it.lines, it.at)
		if err != nil {
			return nil, err
		}
		if len(content) == 0 {
			content = []Node{{Type: "paragraph"}}
		}
		if err := checkContent("listItem", content, it.at); err != nil {
			return nil, err
		}
		nodes = append(nodes, Node{Type: "listItem", Content: content})
	}
	return nodes, nil
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
		own, nested, err := splitNestedTasks(it)
		if err != nil {
			return Node{}, err
		}

		task, err := taskItemNode(it, own)
		if err != nil {
			return Node{}, err
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

// splitNestedTasks divides one item's lines into its own content and the
// sub-list underneath it. Everything from the first nested checkbox down is the
// sub-list.
func splitNestedTasks(it listItem) (own []string, nested []listItem, err error) {
	for i, line := range it.lines {
		if m := listMarker(line); m != nil && m.task != "" && i > 0 {
			sub, err := nestedTasks(it.lines[i:], it.at+i)
			if err != nil {
				return nil, nil, err
			}
			return own, sub, nil
		}
		own = append(own, line)
	}
	return own, nil, nil
}

// taskItemNode builds one taskItem from the lines that belong to it.
func taskItemNode(it listItem, own []string) (Node, error) {
	// The one trim in this file whose input is several lines rather than one,
	// so a line ending belongs in its set: a blank line above or below the
	// item's content is structure and goes, and trimLine alone would leave it
	// and hand the writer text starting with a newline.
	text := strings.Trim(strings.Join(own, "\n"), blockSpace+"\n")
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
	return Node{
		Type:    "taskItem",
		Attrs:   map[string]any{"state": state},
		Content: content,
	}, nil
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
	if len(lines) < 2 || !strings.HasPrefix(trimLine(lines[0]), "|") {
		return false
	}
	delim := trimLine(lines[1])
	if !strings.HasPrefix(delim, "|") {
		return false
	}
	for _, cell := range splitRow(delim) {
		if strings.Trim(trimLine(cell), "-:") != "" || cell == "" {
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
	for used < len(lines) && strings.HasPrefix(trimLine(lines[used]), "|") {
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
	trimmed := trimLine(line)
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
		content, err := parseInline(trimLine(cell), at)
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
	used, err := paragraphExtent(lines, at)
	if err != nil {
		return Node{}, 0, err
	}

	// Each line loses its leading and trailing whitespace, which is what
	// markdown does with it: indentation before a paragraph line is stripped,
	// and whitespace after one is either a break or nothing. Keeping it would
	// build a document ToMarkdown then refuses, because there is no spelling
	// that carries it — the two halves have to agree on the same subset.
	trimmed := make([]string, 0, used)
	for _, line := range lines[:used] {
		trimmed = append(trimmed, strings.Trim(line, " \t"))
	}

	content, err := parseInline(strings.Join(trimmed, "\n"), at)
	if err != nil {
		return Node{}, 0, err
	}
	content = trimAroundBreaks(content)

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

// paragraphExtent is how many lines the paragraph runs for. A setext underline
// is refused rather than read as a heading, because the writer has no spelling
// for one and the two halves have to agree on the same subset.
func paragraphExtent(lines []string, at int) (int, error) {
	used := 0
	for used < len(lines) {
		line := lines[used]
		if trimLine(line) == "" {
			return used, nil
		}
		if used > 0 {
			if isSetextUnderline(line) {
				return 0, unsupported(at+used, "a setext heading").
					WithRemedy("write the heading with # instead")
			}
			if startsBlock(lines[used:]) {
				return used, nil
			}
		}
		used++
	}
	return used, nil
}

// trimAroundBreaks drops the whitespace on either side of a line break.
//
// Markdown does the same: a space before the backslash is part of the break's
// spelling and indentation after it is indentation. Keeping either builds a
// paragraph whose line ends in a space, which ToMarkdown then refuses because
// there is no way to write it down — the round-trip fuzzer found it with
// `0 \` and a second line.
//
// Only unmarked text is trimmed. A space inside a code span is code.
func trimAroundBreaks(nodes []Node) []Node {
	for i := range nodes {
		if nodes[i].Type != "text" || len(nodes[i].Marks) > 0 {
			continue
		}
		if i+1 < len(nodes) && nodes[i+1].Type == "hardBreak" {
			nodes[i].Text = strings.TrimRight(nodes[i].Text, " \t")
		}
		if i > 0 && nodes[i-1].Type == "hardBreak" {
			nodes[i].Text = strings.TrimLeft(nodes[i].Text, " \t")
		}
	}

	// ADF has no text node holding an empty string, and one left behind by the
	// trimming above is a node Jira would reject.
	out := nodes[:0]
	for _, n := range nodes {
		if n.Type == "text" && n.Text == "" {
			continue
		}
		out = append(out, n)
	}
	return out
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
