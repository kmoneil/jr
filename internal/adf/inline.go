package adf

import (
	"strconv"
	"strings"
)

// parseInline converts one run of inline markdown.
//
// It is a scanner rather than a full CommonMark implementation, and where the
// two would differ it refuses instead of choosing. The rule throughout: if the
// text could mean two things, it is not converted.
func parseInline(s string, at int) ([]Node, error) {
	nodes, err := scanInline(s, at)
	if err != nil {
		return nil, err
	}
	return coalesce(nodes), nil
}

func scanInline(s string, at int) ([]Node, error) {
	var out []Node
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, Node{Type: "text", Text: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			// A backslash at the end of a line is markdown's hard break, and
			// the one this package writes.
			flush()
			out = append(out, Node{Type: "hardBreak"})
			i += 2

		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			lit.WriteByte(s[i+1])
			i += 2

		case c == '\n':
			// A soft break. Markdown joins the lines, and a caller who wanted
			// them apart writes a backslash or a blank line.
			lit.WriteByte(' ')
			i++

		case c == '`':
			node, width := codeSpanAt(s[i:])
			if width == 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flush()
			out = append(out, node)
			i += width

		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			node, width, err := imageAt(s[i:], at)
			if err != nil {
				return nil, err
			}
			if width == 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flush()
			out = append(out, node)
			i += width

		case c == '[':
			nodes, width, err := linkAt(s[i:], at)
			if err != nil {
				return nil, err
			}
			if width == 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flush()
			out = append(out, nodes...)
			i += width

		case c == '<':
			node, width := autolinkAt(s[i:])
			if width == 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flush()
			out = append(out, node)
			i += width

		case c == '~' && strings.HasPrefix(s[i:], "~~"):
			nodes, width, err := delimitedAt(s[i:], "~~", Mark{Type: "strike"}, at)
			if err != nil {
				return nil, err
			}
			if width == 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flush()
			out = append(out, nodes...)
			i += width

		case c == '*' || c == '_':
			nodes, width, err := emphasisAt(s, i, at)
			if err != nil {
				return nil, err
			}
			if width == 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flush()
			out = append(out, nodes...)
			i += width

		default:
			lit.WriteByte(c)
			i++
		}
	}
	flush()
	return out, nil
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
			// A run longer than the delimiter is not this delimiter: `*` does
			// not close inside `**`. Matching one was how `*0**0**0*` — a bold
			// word inside an italic sentence, which CommonMark reads correctly
			// — came apart into three pieces.
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

// emphasisAt reads a run of `*` or `_` and the text it wraps.
func emphasisAt(s string, start, at int) ([]Node, int, error) {
	char := s[start]
	run := 0
	for start+run < len(s) && s[start+run] == char {
		run++
	}
	run = min(run, 3)

	// An underscore between two word characters is inert in CommonMark, which
	// is what keeps `customfield_10042` a field id rather than emphasis.
	if char == '_' && start > 0 && isWordByte(s[start-1]) {
		return nil, 0, nil
	}
	// Nothing opens a span with whitespace after it.
	if start+run >= len(s) || isSpaceByte(s[start+run]) {
		return nil, 0, nil
	}

	delim := strings.Repeat(string(char), run)
	rest := s[start:]

	// A run with a space before it does not close, so keep looking. `a * b * c`
	// is three words and two asterisks, not emphasis around " b ".
	end := -1
	for from := run; ; {
		found := findCloser(rest, from, delim)
		if found < 0 {
			return nil, 0, nil
		}
		closes := found > run && !isSpaceByte(rest[found-1])
		if closes && char == '_' && found+run < len(rest) && isWordByte(rest[found+run]) {
			// An underscore between two word characters is inert, so it is not
			// this span's closer — the next one might be. Giving up here left
			// `_0_0_` unparsed, and its writer had no other spelling to use.
			closes = false
		}
		if closes {
			end = found
			break
		}
		from = found + run
	}
	inner, err := scanInline(rest[run:end], at)
	if err != nil {
		return nil, 0, err
	}
	// One delimiter is emphasis, two are strong, three are both.
	if run != 2 {
		if err := addMark(inner, Mark{Type: "em"}, at); err != nil {
			return nil, 0, err
		}
	}
	if run >= 2 {
		if err := addMark(inner, Mark{Type: "strong"}, at); err != nil {
			return nil, 0, err
		}
	}
	return inner, end + run, nil
}

// isSpaceByte is CommonMark's whitespace, which a delimiter may not sit
// against.
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 0x80 ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
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

	if scheme, value, found := strings.Cut(target, ":"); found {
		switch scheme {
		case "jira-user", "jira-status":
			// These carry a plain string, not marked-up content, so the label
			// is read as text rather than left with its escapes in it. Storing
			// the raw slice put a backslash in the name and grew another on
			// every conversion, which the round-trip fuzzer found.
			label, err := literalText(text, at)
			if err != nil {
				return nil, 0, err
			}
			if scheme == "jira-user" {
				if value == "" {
					return nil, 0, unsupported(at, "a mention of nobody")
				}
				return []Node{{Type: "mention", Attrs: map[string]any{
					"id": value, "text": label,
				}}}, width, nil
			}
			if label == "" || value == "" {
				return nil, 0, unsupported(at, "a status lozenge with no text or colour")
			}
			return []Node{{Type: "status", Attrs: map[string]any{
				"text": label, "color": value,
			}}}, width, nil
		case "jira-date":
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return nil, 0, unsupported(at, "a jira-date link stamped %q", value)
			}
			return []Node{{Type: "date", Attrs: map[string]any{
				"timestamp": value,
			}}}, width, nil
		case "jira-media":
			return nil, 0, unsupported(at, "an attachment written as a link").
				WithRemedy("an attachment is an image: write it as ![alt](jira-media:...)")
		}
	}

	inner, err := scanInline(text, at)
	if err != nil {
		return nil, 0, err
	}
	if len(inner) == 0 {
		// ADF has no way to store a link with no text.
		return nil, 0, unsupported(at, "a link with no text")
	}
	if target == "" {
		// `[text]()` is a link to nowhere. ADF's link mark requires an
		// address, and building one without would be a document ToMarkdown
		// then refuses — the two halves have to agree on the same subset.
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
func destination(s string, i int) (target, title string, width int, ok bool) {
	if i >= len(s) || s[i] != '(' {
		return "", "", 0, false
	}
	i++

	var b strings.Builder
	if i < len(s) && s[i] == '<' {
		// The angle-bracket form, which is what ToMarkdown writes for a URL
		// holding a bracket or a space.
		i++
		for i < len(s) && s[i] != '>' {
			if s[i] == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			if s[i] == '\n' {
				return "", "", 0, false
			}
			b.WriteByte(s[i])
			i++
		}
		if i >= len(s) {
			return "", "", 0, false
		}
		i++
	} else {
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
					goto done
				}
				depth--
			case s[i] == ' ' || s[i] == '\n':
				goto done
			}
			b.WriteByte(s[i])
			i++
		}
		return "", "", 0, false
	}

done:
	for i < len(s) && (s[i] == ' ' || s[i] == '\n') {
		i++
	}
	if i < len(s) && (s[i] == '"' || s[i] == '\'') {
		quote := s[i]
		i++
		var t strings.Builder
		for i < len(s) && s[i] != quote {
			if s[i] == '\\' && i+1 < len(s) {
				t.WriteByte(s[i+1])
				i += 2
				continue
			}
			t.WriteByte(s[i])
			i++
		}
		if i >= len(s) {
			return "", "", 0, false
		}
		title = t.String()
		i++
		for i < len(s) && s[i] == ' ' {
			i++
		}
	}
	if i >= len(s) || s[i] != ')' {
		return "", "", 0, false
	}
	return b.String(), title, i + 1, true
}
