package adf

import (
	"strconv"
	"strings"
)

// parseInline converts one run of inline markdown.
//
// It is a scanner rather than a full CommonMark implementation, and where the
// two would differ it refuses instead of choosing. The rule throughout: if the
// text could mean two things, it is not converted. Emphasis is the exception,
// and it is CommonMark's own algorithm rather than an approximation of it —
// see emphasis.go for what an approximation cost.
func parseInline(s string, at int) ([]Node, error) {
	nodes, err := scanInline(s, at)
	if err != nil {
		return nil, err
	}
	return coalesce(nodes), nil
}

// inlineOut accumulates the pieces scanInline emits and the literal text
// between them.
//
// It exists so the construct arms of that switch can share one shape. Each of
// them asks a scanner for a construct at the cursor and gets a width back, and
// every one then answers the same two questions the same way. Six constructs
// wrote out the same six lines, at three levels of nesting — for, switch, case,
// if — which is the most expensive place in the function to repeat anything and
// is where most of its score came from.
//
// What is left in the switch is the part that is genuinely CommonMark's: one
// arm per inline construct markdown has, each naming the bytes that open it.
type inlineOut struct {
	pieces []piece
	lit    strings.Builder
}

// literal adds one byte to the run of plain text.
func (o *inlineOut) literal(c byte) { o.lit.WriteByte(c) }

// flush ends the current run of literal text, if there is one.
func (o *inlineOut) flush() {
	if o.lit.Len() > 0 {
		o.pieces = append(o.pieces, piece{
			nodes: []Node{{Type: "text", Text: o.lit.String()}},
		})
		o.lit.Reset()
	}
}

// take accepts what a construct scanner produced and reports how far the cursor
// advances.
//
// A width of zero is the scanner saying "not that construct here" — a backtick
// that never closes, a bracket with no link after it. That is a literal byte
// rather than an error, which is the rule this whole scanner runs on: where the
// text could mean two things it is refused, but where it plainly means one
// thing it is that thing, and an unmatched delimiter plainly means itself.
func (o *inlineOut) take(c byte, nodes []Node, width int) int {
	if width == 0 {
		o.literal(c)
		return 1
	}
	o.flush()
	o.pieces = append(o.pieces, piece{nodes: nodes})
	return width
}

// takeOne is take for the scanners that produce a single node.
func (o *inlineOut) takeOne(c byte, node Node, width int) int {
	return o.take(c, []Node{node}, width)
}

// delimiter records a run of emphasis characters, whose meaning is decided
// once the whole line is in hand.
func (o *inlineOut) delimiter(d piece) int {
	if !d.canOpen && !d.canClose {
		// A run that can do neither is text, and writing it as text now keeps
		// the literal run it sits in contiguous. That is worth doing rather
		// than leaving to the merge at the end: the intraword underscore is
		// the common case, a description full of custom field ids has
		// thousands of them, and each one would otherwise split a paragraph
		// into three nodes for the merge to put back together.
		for range d.n {
			o.literal(d.char)
		}
		return d.n
	}
	o.flush()
	o.pieces = append(o.pieces, d)
	return d.n
}

// scanInline walks the bytes of one inline run, emitting a node per construct
// and gathering everything else as literal text.
//
// # Why this scores 20 and stops there
//
// It was 40 cognitive and 30 cyclomatic, the highest in the module. Six of the
// nine arms had written out the same six lines — ask a scanner, treat a zero
// width as a literal byte, otherwise flush and emit — at three levels of
// nesting. That was structure rather than construct count, and it moved to
// inlineOut.take.
//
// The 20 that remains was measured rather than estimated, by removing each
// component and reading the difference:
//
//	12  four `if err != nil` at nesting two, three points each
//	 5  five multi-byte guards: \ before a newline, \ before punctuation,
//	    ! before [, ~ before ~, and * or _
//	 2  the switch, nested inside the loop
//	 1  the byte loop
//
// Neither of the two that matter can go. The twelve is Go's error propagation
// written where the errors happen — four constructs can refuse, and folding
// their checks together would hide which one did. The five is the definition of
// which bytes open which construct, which is CommonMark's list and not ours; a
// dispatch table would move the arms somewhere else without removing one of
// them, and a flat dispatch over N constructs is not improved by hiding N.
//
// So this stays over the hot-path bar of 15 on purpose. It is no longer the
// highest score in the module, and what is left is the shape of the problem.
func scanInline(s string, at int) ([]Node, error) {
	var out inlineOut

	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			// A backslash at the end of a line is markdown's hard break, and
			// the one this package writes. Width two, so it goes through the
			// same path as every other construct rather than beside it.
			i += out.takeOne(c, Node{Type: "hardBreak"}, 2)

		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			out.literal(s[i+1])
			i += 2

		case c == '\n':
			// A soft break. Markdown joins the lines, and a caller who wanted
			// them apart writes a backslash or a blank line.
			out.literal(' ')
			i++

		case c == '`':
			node, width := codeSpanAt(s[i:])
			i += out.takeOne(c, node, width)

		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			node, width, err := imageAt(s[i:], at)
			if err != nil {
				return nil, err
			}
			i += out.takeOne(c, node, width)

		case c == '[':
			nodes, width, err := linkAt(s[i:], at)
			if err != nil {
				return nil, err
			}
			i += out.take(c, nodes, width)

		case c == '<':
			node, width := autolinkAt(s[i:])
			i += out.takeOne(c, node, width)

		case c == '~' && strings.HasPrefix(s[i:], "~~"):
			nodes, width, err := delimitedAt(s[i:], "~~", Mark{Type: "strike"}, at)
			if err != nil {
				return nil, err
			}
			i += out.take(c, nodes, width)

		case c == '*' || c == '_':
			// Recorded rather than resolved. What a run of asterisks means
			// depends on the runs after it as well as the text around it, so
			// the decision waits until the line has been read.
			i += out.delimiter(delimiterAt(s, i))

		default:
			out.literal(c)
			i++
		}
	}
	out.flush()

	matchEmphasis(out.pieces)
	return emphasisNodes(out.pieces, at)
}

// isASCIIPunct reports the characters a backslash may escape in CommonMark.
func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// codeSpanAt reads a code span at the start of s, returning its width in bytes
// or zero if the span never closes.
func codeSpanAt(s string) (Node, int) {
	open := 0
	for open < len(s) && s[open] == '`' {
		open++
	}
	for i := open; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(s) && s[i+run] == '`' {
			run++
		}
		if run != open {
			i += run
			continue
		}
		text := s[open:i]
		// CommonMark strips one space from each end where both are present,
		// which is what lets a span hold a leading or trailing backtick. It is
		// also exactly what codeSpan adds.
		if len(text) >= 2 && text[0] == ' ' && text[len(text)-1] == ' ' &&
			strings.TrimSpace(text) != "" {
			text = text[1 : len(text)-1]
		}
		// A newline inside a span is a soft break like anywhere else.
		text = strings.ReplaceAll(text, "\n", " ")
		return Node{
			Type: "text", Text: text,
			Marks: []Mark{{Type: "code"}},
		}, i + run
	}
	return Node{}, 0
}

// delimitedAt reads text wrapped in a fixed delimiter and applies one mark.
func delimitedAt(s, delim string, mark Mark, at int) ([]Node, int, error) {
	end := findCloser(s, len(delim), delim)
	if end <= len(delim) {
		// Not closed, or closed immediately: an empty span is not a span.
		return nil, 0, nil
	}
	inner, err := scanInline(s[len(delim):end], at)
	if err != nil {
		return nil, 0, err
	}
	if err := addMark(inner, mark, at); err != nil {
		return nil, 0, err
	}
	return inner, end + len(delim), nil
}

// findCloser returns the index of the next closing delimiter at or after from,
// skipping escapes and code spans so a delimiter inside one does not close the
// span outside it. It returns -1 where there is none.
//
// This serves strikethrough, which GFM spells with a fixed pair and not with
// the delimiter runs emphasis uses. Emphasis went through here once, matching
// a closer of exactly the length it opened with, which is the bug emphasis.go
// exists to have fixed.
func findCloser(s string, from int, delim string) int {
	for i := from; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i += 2
		case s[i] == '`':
			if _, width := codeSpanAt(s[i:]); width > 0 {
				i += width
				continue
			}
			i++
		case s[i] == delim[0]:
			// A run longer than the delimiter is not this delimiter, so `~~~`
			// does not close a strike opened with `~~`.
			run := 0
			for i+run < len(s) && s[i+run] == delim[0] {
				run++
			}
			if run == len(delim) {
				return i
			}
			i += run
		default:
			i++
		}
	}
	return -1
}

// addMark applies a mark to every node a span wrapped.
//
// ADF puts marks on text, and on nothing else this package writes. Emphasising
// an image or a mention is a document Jira will not store, so it is refused
// here rather than sent and rejected with a message about the schema.
func addMark(nodes []Node, mark Mark, at int) error {
	for i := range nodes {
		switch nodes[i].Type {
		case "text":
			// Jira stores the code mark only on its own, and refuses a text
			// node carrying it alongside any other with a 400 that names
			// neither. Emphasised code is refused here instead.
			if hasMark(nodes[i].Marks, "code") || mark.Type == "code" {
				if len(nodes[i].Marks) > 0 && !hasMark(nodes[i].Marks, mark.Type) {
					return unsupported(at, "emphasis on inline code")
				}
			}
			if !hasMark(nodes[i].Marks, mark.Type) {
				nodes[i].Marks = append(nodes[i].Marks, mark)
			}
		case "hardBreak":
			// A break carries no text to mark, and marking it is not an error.
		default:
			return unsupported(at, "emphasis around %s", article(nodes[i].Type))
		}
	}
	return nil
}

func hasMark(marks []Mark, name string) bool {
	for _, m := range marks {
		if m.Type == name {
			return true
		}
	}
	return false
}

// article names a node type for a message, so the refusal reads as a sentence.
func article(nodeType string) string {
	switch nodeType {
	case "media":
		return "an image"
	case "mention":
		return "a mention"
	case "status":
		return "a status lozenge"
	case "date":
		return "a date"
	case "inlineCard":
		return "a link card"
	}
	return "a " + nodeType
}

// autolinkAt reads `<url>`, which is how ToMarkdown writes a link card and how
// Jira itself renders a pasted URL.
func autolinkAt(s string) (Node, int) {
	end := strings.IndexByte(s, '>')
	if end < 1 {
		return Node{}, 0
	}
	url := s[1:end]
	if strings.ContainsAny(url, " \t\n<") || !hasScheme(url) {
		return Node{}, 0
	}
	return Node{Type: "inlineCard", Attrs: map[string]any{"url": url}}, end + 1
}

// hasScheme reports a URL with a scheme, which is what separates an autolink
// from an angle bracket somebody typed.
func hasScheme(s string) bool {
	colon := strings.IndexByte(s, ':')
	if colon < 1 || colon == len(s)-1 {
		return false
	}
	for i := range colon {
		if !isSchemeByte(s[i]) {
			return false
		}
	}
	return true
}

func isSchemeByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.'
}

// linkAt reads `[text](target)`.
//
// A target in one of the documented jira- schemes is the node it stands for
// rather than a link, which is what makes a body read out of Jira go back in
// as the document it came from.
func linkAt(s string, at int) ([]Node, int, error) {
	text, target, title, width, ok := bracketPair(s, 1)
	if !ok {
		return nil, 0, nil
	}

	nodes, handled, err := jiraSchemeLink(text, target, at)
	if err != nil {
		return nil, 0, err
	}
	if handled {
		return nodes, width, nil
	}
	return ordinaryLink(text, target, title, width, at)
}

// jiraSchemeLink reads the jira- targets this converter writes for the ADF
// nodes markdown has no spelling of its own for. A false second return means
// the target names no such scheme, which makes it an ordinary link.
func jiraSchemeLink(text, target string, at int) ([]Node, bool, error) {
	scheme, value, found := strings.Cut(target, ":")
	if !found {
		return nil, false, nil
	}
	switch scheme {
	case "jira-user", "jira-status":
		// These carry a plain string, not marked-up content, so the label is
		// read as text rather than left with its escapes in it. Storing the raw
		// slice put a backslash in the name and grew another on every
		// conversion, which the round-trip fuzzer found.
		label, err := literalText(text, at)
		if err != nil {
			return nil, false, err
		}
		chip, err := chipNode(scheme, label, value, at)
		return chip, true, err

	case "jira-date":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return nil, false, unsupported(at, "a jira-date link stamped %q", value)
		}
		return []Node{{Type: "date", Attrs: map[string]any{
			"timestamp": value,
		}}}, true, nil

	case "jira-media":
		return nil, false, unsupported(at, "an attachment written as a link").
			WithRemedy("an attachment is an image: write it as ![alt](jira-media:...)")
	}
	return nil, false, nil
}

// chipNode builds the mention or the status lozenge a jira- scheme names.
func chipNode(scheme, label, value string, at int) ([]Node, error) {
	if scheme == "jira-user" {
		if value == "" {
			return nil, unsupported(at, "a mention of nobody")
		}
		return []Node{{Type: "mention", Attrs: map[string]any{
			"id": value, "text": label,
		}}}, nil
	}
	if label == "" || value == "" {
		return nil, unsupported(at, "a status lozenge with no text or colour")
	}
	return []Node{{Type: "status", Attrs: map[string]any{
		"text": label, "color": value,
	}}}, nil
}

// ordinaryLink builds a link mark over the bracketed text.
func ordinaryLink(text, target, title string, width, at int) ([]Node, int, error) {
	inner, err := scanInline(text, at)
	if err != nil {
		return nil, 0, err
	}
	if len(inner) == 0 {
		// ADF has no way to store a link with no text.
		return nil, 0, unsupported(at, "a link with no text")
	}
	if target == "" {
		// `[text]()` is a link to nowhere. ADF's link mark requires an address,
		// and building one without would be a document ToMarkdown then refuses:
		// the two halves have to agree on the same subset.
		return nil, 0, unsupported(at, "a link with no address")
	}
	mark := Mark{Type: "link", Attrs: map[string]any{"href": target}}
	if title != "" {
		mark.Attrs["title"] = title
	}
	if err := addMark(inner, mark, at); err != nil {
		return nil, 0, err
	}
	return inner, width, nil
}

// imageAt reads `![alt](target)`.
func imageAt(s string, at int) (Node, int, error) {
	label, target, _, width, ok := bracketPair(s, 2)
	if !ok {
		return Node{}, 0, nil
	}
	// Alt text is a plain string in ADF, for the same reason a mention's name
	// is: it is read rather than rendered.
	alt, err := literalText(label, at)
	if err != nil {
		return Node{}, 0, err
	}

	if target == "" {
		return Node{}, 0, unsupported(at, "an image with no address")
	}
	attrs := map[string]any{"type": "external", "url": target}
	if id, found := strings.CutPrefix(target, "jira-media:"); found {
		attrs = map[string]any{"type": "file"}
		if collection, rest, split := strings.Cut(id, "/"); split {
			attrs["collection"], id = collection, rest
		}
		if id == "" {
			return Node{}, 0, unsupported(at, "an attachment with no id")
		}
		attrs["id"] = id
	}
	if alt != "" {
		attrs["alt"] = alt
	}
	return Node{Type: "media", Attrs: attrs}, width, nil
}

// literalText reads inline markdown that has to come out as a plain string.
//
// A mention's name, a status lozenge's text, and an image's alt text are all
// strings in ADF rather than content, so there is nowhere to put a mark on
// one — and silently dropping the mark would be the alteration this package
// refuses.
func literalText(s string, at int) (string, error) {
	nodes, err := scanInline(s, at)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, n := range nodes {
		if n.Type != "text" || len(n.Marks) > 0 {
			return "", unsupported(at, "markup where Jira stores a plain label")
		}
		b.WriteString(n.Text)
	}
	return b.String(), nil
}

// bracketPair reads `[text](target)` starting at open, where open is the index
// just past the opening bracket — 1 for a link, 2 for an image.
//
// It returns the text, the destination, an optional title, and the byte width
// of the whole construct.
func bracketPair(s string, open int) (text, target, title string, width int, ok bool) {
	depth, i := 1, open
	for i < len(s) {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i += 2
			continue
		case s[i] == '`':
			if _, w := codeSpanAt(s[i:]); w > 0 {
				i += w
				continue
			}
		case s[i] == '[':
			depth++
		case s[i] == ']':
			depth--
			if depth == 0 {
				text = s[open:i]
				target, title, width, ok = destination(s, i+1)
				return text, target, title, width, ok
			}
		}
		i++
	}
	return "", "", "", 0, false
}

// destination reads `(target)` or `(<target>)`, with an optional quoted title,
// starting at the opening parenthesis.
//
// The three scans below each end where the next one begins, so each returns the
// index its caller resumes at. That is what keeps them separable: the shape
// this replaced ran all three over one shared index and jumped out of a switch
// inside a loop to reach the title, which is a control flow you have to
// simulate to read.
func destination(s string, i int) (target, title string, width int, ok bool) {
	if i >= len(s) || s[i] != '(' {
		return "", "", 0, false
	}
	i++

	if i < len(s) && s[i] == '<' {
		target, i, ok = scanAngleTarget(s, i)
	} else {
		target, i, ok = scanBareTarget(s, i)
	}
	if !ok {
		return "", "", 0, false
	}

	title, i, ok = scanTitle(s, i)
	if !ok {
		return "", "", 0, false
	}
	if i >= len(s) || s[i] != ')' {
		return "", "", 0, false
	}
	return target, title, i + 1, true
}

// scanAngleTarget reads the `<target>` form starting at the opening angle
// bracket, and returns the index just past the closing one. This is what
// ToMarkdown writes for any address holding a space, a tab, a parenthesis, an
// angle bracket, or a backslash — see linkTarget.
func scanAngleTarget(s string, i int) (target string, next int, ok bool) {
	i++
	var b strings.Builder
	for i < len(s) && s[i] != '>' {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] == '\n' {
			// A line ending cannot appear in either destination form, and an
			// unclosed `<` that ran to the end of the line is not one.
			return "", 0, false
		}
		b.WriteByte(s[i])
		i++
	}
	if i >= len(s) {
		return "", 0, false
	}
	return b.String(), i + 1, true
}

// scanBareTarget reads the unbracketed form, which ends at a space, a newline,
// or a closing parenthesis that no `(` inside the address opened. It returns
// the index of that terminator rather than the index past it, because a `)`
// terminator is also the one closing the whole construct.
//
// Tab is deliberately not a terminator, which is a divergence from CommonMark
// and is safe in one direction only: linkTarget writes any address holding a
// tab in the angle form, so nothing this package emits reaches here with one
// in it. A tab arriving in hand-written markdown is read as part of the
// address, and converts back out as `<a\tb>` — lossless, which is the property
// that matters, rather than identical to a reference implementation.
func scanBareTarget(s string, i int) (target string, next int, ok bool) {
	var b strings.Builder
	depth := 0
	for i < len(s) {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i += 2
			continue
		case s[i] == '(':
			depth++
		case s[i] == ')':
			if depth == 0 {
				return b.String(), i, true
			}
			depth--
		case s[i] == ' ' || s[i] == '\n':
			return b.String(), i, true
		}
		b.WriteByte(s[i])
		i++
	}
	return "", 0, false
}

// scanTitle reads the optional `"title"` or `'title'` that may follow a
// destination, along with the whitespace around it, and returns the index of
// the closing parenthesis the caller still has to check for.
//
// A destination with no title is not a failure, so ok reports only that a title
// which started was also finished.
func scanTitle(s string, i int) (title string, next int, ok bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\n') {
		i++
	}
	if i >= len(s) || (s[i] != '"' && s[i] != '\'') {
		return "", i, true
	}

	quote := s[i]
	i++
	var b strings.Builder
	for i < len(s) && s[i] != quote {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	if i >= len(s) {
		return "", 0, false
	}
	i++
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return b.String(), i, true
}
