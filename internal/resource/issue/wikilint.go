package issue

import (
	"fmt"
	"strings"
)

// Wiki markup is what Data Center stores, and jr sends it through untouched.
// Nothing in this tree parses it: internal/adf is markdown and ADF, which is
// the Cloud direction. This is not a parser either, and the distinction is the
// whole design.
//
// A parser would have to decide what a construct means. These checks only
// report where a construct has no single meaning, which is a countable fact
// about the characters and needs no model of any renderer. That matters because
// a warning that fires on valid markup is worse than no warning: it teaches the
// reader to skip the channel, and the warnings jr emits today are worth
// reading.
//
// The audience is the reason this exists at all. A person posts, looks at the
// result, and edits it if it came out wrong. An agent has no such loop, and a
// mangled heading is cosmetic enough that nobody may ever mention it, so the
// agent never learns it got it wrong.

// fenceMacros are the macros whose contents are not wiki markup.
//
// Text inside one is literal, so a brace run in a code sample is not ambiguous
// and must not be reported. Getting this wrong would fire the warning on every
// issue that pastes a shell snippet, which is most of them.
var fenceMacros = []string{"code", "noformat", "quote", "panel", "color"}

// wikiFindings reports what is ambiguous about a wiki markup body.
//
// Empty when there is nothing to say, which is the common case and the one that
// has to stay quiet.
func wikiFindings(text string) []string {
	if text == "" {
		return nil
	}
	fences, unclosed := scanFences(text)

	var out []string
	for _, macro := range unclosed {
		out = append(out, fmt.Sprintf(
			"{%s} is opened and never closed; everything after it renders as %s",
			macro, macro))
	}
	out = append(out, braceFindings(text, fences)...)
	return out
}

// span is a half-open region of the text.
type span struct{ from, to int }

// scanFences finds the regions covered by a fence macro, and names the macros
// left open.
//
// A macro token both opens and closes: `{code}` starts a block and `{code}`
// ends it, and `{code:go}` starts one that a plain `{code}` ends. So the
// occurrences of one family alternate, and an odd count means the last one
// never closed.
func scanFences(text string) ([]span, []string) {
	type opening struct {
		macro string
		at    int
	}
	var (
		spans []span
		open  []opening
		odd   []string
	)
	seen := map[string]int{}

	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		macro, end, ok := fenceTokenAt(text, i)
		if !ok {
			continue
		}
		seen[macro]++
		// Close the most recent unclosed opening of this macro, if any.
		closed := false
		for k := len(open) - 1; k >= 0; k-- {
			if open[k].macro != macro {
				continue
			}
			spans = append(spans, span{from: open[k].at, to: end})
			open = append(open[:k], open[k+1:]...)
			closed = true
			break
		}
		if !closed {
			open = append(open, opening{macro: macro, at: i})
		}
		i = end - 1
	}

	for _, o := range open {
		odd = append(odd, o.macro)
		// An unclosed fence swallows the rest of the document, and that is
		// exactly what the reader will see, so the region says so too.
		spans = append(spans, span{from: o.at, to: len(text)})
	}
	return spans, odd
}

// fenceTokenAt reads a fence macro token starting at i, returning its family
// name and the index just past the closing brace.
func fenceTokenAt(text string, i int) (string, int, bool) {
	rest := text[i:]
	if !strings.HasPrefix(rest, "{") {
		return "", 0, false
	}
	end := strings.IndexByte(rest, '}')
	if end < 0 {
		return "", 0, false
	}
	// `{code:go}` and `{code}` are the same family. A colon or a pipe starts
	// the parameters, which this does not care about.
	inner := rest[1:end]
	if cut := strings.IndexAny(inner, ":|"); cut >= 0 {
		inner = inner[:cut]
	}
	for _, macro := range fenceMacros {
		if inner == macro {
			return macro, i + end + 1, true
		}
	}
	return "", 0, false
}

// braceFindings reports ambiguous brace runs and unclosed monospace spans,
// ignoring anything inside a fence.
func braceFindings(text string, fences []span) []string {
	var out []string
	var opens, closes int
	reported := false

	for i := 0; i < len(text); i++ {
		if text[i] != '{' && text[i] != '}' {
			continue
		}
		if inAny(fences, i) {
			continue
		}
		c := text[i]
		run := 1
		for i+run < len(text) && text[i+run] == c {
			run++
		}
		switch {
		case run >= 3 && !reported:
			// Three in a row cannot be told apart from a span delimiter beside
			// a literal brace, in either order. This is the construct that
			// produced the report: `{{/subjects/{subject\}}}` ends in `}}}`
			// and a balanced-count check passes it, because the counts are
			// balanced. They are balanced under both readings.
			out = append(out, fmt.Sprintf(
				"%q is three or more braces in a row, which is a monospace "+
					"delimiter beside a literal brace under one reading and the "+
					"reverse under another; jr cannot predict which your Jira "+
					"picks", strings.Repeat(string(c), run)))
			reported = true
		case run == 2 && c == '{':
			opens++
		case run == 2 && c == '}':
			closes++
		}
		i += run - 1
	}

	if opens != closes {
		out = append(out, fmt.Sprintf(
			"%d {{ and %d }} outside a code block, so a monospace span is "+
				"opened and never closed", opens, closes))
	}
	return out
}

// inAny reports whether i falls inside one of the spans.
func inAny(spans []span, i int) bool {
	for _, s := range spans {
		if i >= s.from && i < s.to {
			return true
		}
	}
	return false
}
