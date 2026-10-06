package issue

import (
	"strconv"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// bodyCharsFlag bounds each body in the feed, between the whole text and
// --no-body. Issue 219: fifty events were mostly three pasted log excerpts,
// and --no-body would also have dropped the two-line comments that answered
// the question.
const bodyCharsFlag = "body-chars"

// bodyLengthColumn is where TSV and markdown say a body was cut, since neither
// has attributes. Empty is a whole body, the way an empty cell in this row is
// "no such part" everywhere else.
var bodyLengthColumn = render.Column{Header: "body-length", Path: "body@length"}

// eventBodySchema is a body that can say it was cut.
//
// Its own schema rather than two more attributes on bodySchema, which every
// kind carrying a body shares: comment list, issue get, and worklog list would
// each move a version for a flag none of them has.
func eventBodySchema() *render.Schema {
	s := bodySchema("body")
	s.Attrs = append(s.Attrs,
		render.Field{Name: "truncated", Type: render.TypeBool, Optional: true},
		render.Field{Name: "length", Type: render.TypeInt, Optional: true},
	)
	return s
}

// bodyNode renders an event's body, marked when it was cut.
func (e Event) bodyNode() *render.Node {
	n := render.El("body").Attr("format", e.BodyFormat)
	if e.BodyLength > 0 {
		n.Attr("truncated", "true").Attr("length", strconv.Itoa(e.BodyLength))
	}
	n.SetCDATA(e.Body)
	n.Bounded = e.BodyBounded
	return n
}

// boundBody cuts the body to its first n characters.
//
// Characters are code points, not bytes: a byte bound would cut inside a
// multi-byte character and emit a body that is not UTF-8. The cut is exact and
// at the bound, with nothing trimmed or added, so the body is a prefix of the
// one Jira holds and length says how much of it there was.
func (e Event) boundBody(n int) Event {
	e.BodyBounded = true
	length := utf8.RuneCountInString(e.Body)
	if length <= n {
		return e
	}
	cut, i := 0, 0
	for i = range e.Body {
		if cut == n {
			break
		}
		cut++
	}
	e.Body, e.BodyLength = e.Body[:i], length
	return e
}

// validateBodyChars refuses the three --body-chars invocations that could only
// be answered by choosing for the caller.
//
// --no-body beside it is two answers to one question. --raw-body is Cloud's
// ADF document, which cut is not a document, while its format attribute still
// says adf; refused on Data Center too, where the flag changes nothing, so the
// answer does not depend on which deployment a context names. And a bound
// below one is --no-body spelled another way.
func validateBodyChars(inv *registry.Invocation) error {
	if !inv.Flags.WasSet(bodyCharsFlag) {
		return nil
	}
	if n := inv.Flags.Int(bodyCharsFlag); n < 1 {
		return errs.Usage("INVALID_BODY_CHARS", "--body-chars must be at least 1").
			WithDetail("got %d", n).
			WithRemedy("pass --no-body to drop bodies altogether")
	}
	if inv.Flags.Bool(noBodyFlag) {
		return errs.Usage("BODY_CHARS_AND_NO_BODY",
			"--body-chars and --no-body are two answers to how much body to keep").
			WithRemedy("pass one of them")
	}
	if inv.Flags.Bool("raw-body") {
		return errs.Usage("BODY_CHARS_AND_RAW_BODY",
			"--body-chars would cut an ADF document, and a cut one is not a document").
			WithRemedy("drop --raw-body to bound the markdown it converts to")
	}
	return nil
}
