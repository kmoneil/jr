package adf

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file is CommonMark's emphasis algorithm: delimiter runs, the flanking
// rules that decide which of them can open and close, and the pairing pass the
// specification calls "process emphasis".
//
// It replaced a scanner that looked for a closing run of exactly the length it
// had opened with, and the difference is that a run is a quantity rather than a
// token. `**bold *and italic***` ends in three asterisks: the em needs one of
// them and the strong needs the other two. Searching for a run of exactly two
// skipped it, no closer was found, and *both* spans dissolved into text with
// the asterisks kept as characters. Silently, exit 0, on markdown anybody would
// write.
//
// The round-trip fuzzer reported that as the writer refusing its own output,
// because the writer emits exactly that shape for the document it describes and
// the mangled re-read then needs emphasis with no unambiguous spelling. The
// defect was here, one layer down, which is the usual shape of a round-trip
// failure: the property trips the half that is correct.

// piece is one span of an inline run as the scanner found it: either content
// that is already a node, or a run of emphasis characters whose meaning is not
// decided until the rest of the line has been read.
//
// The two share a type because their order is what matters. A delimiter's
// meaning depends on what sits either side of it, and a mark covers everything
// between two of them, so both questions are asked over one flat list rather
// than over a tree the scanner would have to commit to before it had the
// evidence.
type piece struct {
	// nodes is the content, for a piece that is not a delimiter.
	nodes []Node

	// char is the emphasis character, or zero for content. n counts the
	// characters still unspent, orig how many there were: the rule of three is
	// stated in terms of the original lengths, so spending one may not change
	// what a run is allowed to pair with.
	char     byte
	n, orig  int
	canOpen  bool
	canClose bool

	// em and strong are the marks the pairing decided this piece sits inside.
	// ADF marks are a set, so a piece inside two strong spans is strong once.
	em, strong bool
}

// isDelim reports whether this piece is a run of emphasis characters. A zero
// byte is not one of them, so the character doubles as the discriminator.
func (p piece) isDelim() bool { return p.char != 0 }

// delimiterAt reads the run of emphasis characters at i and classifies it.
func delimiterAt(s string, i int) piece {
	char := s[i]
	n := 0
	for i+n < len(s) && s[i+n] == char {
		n++
	}

	d := piece{char: char, n: n, orig: n}
	d.canOpen, d.canClose = flanking(char,
		neighbour(s[:i], true), neighbour(s[i+n:], false))
	return d
}

// side is how the flanking rules see the character on one side of a delimiter
// run: whitespace, punctuation, or neither.
type side struct{ space, punct bool }

// flanking reports what a delimiter run of char may do where it sits.
//
// Flanking is about what is either side of the whole run rather than about the
// run itself: a run that can open has something other than whitespace after it,
// and one that can close has something other than whitespace before it, with
// punctuation making both conditional on the other side. The underscore carries
// one extra rule, which is the whole reason `customfield_10042` is a field id
// and not emphasis around "10042".
//
// Both halves of the package ask it. The reader asks about a run it found; the
// writer asks about a run it is *about to write*, because a delimiter that can
// neither open nor close is not ambiguous, it is inert. The reader keeps it as
// text and the span disappears, silently, on markdown this package wrote
// itself, which is the failure the round-trip fuzzer exists to catch.
func flanking(char byte, prev, next side) (canOpen, canClose bool) {
	left := !next.space && (!next.punct || prev.space || prev.punct)
	right := !prev.space && (!prev.punct || next.space || next.punct)
	if char == '*' {
		return left, right
	}
	return left && (!right || prev.punct), right && (!left || next.punct)
}

// neighbour classifies the character on one side of a delimiter run.
//
// The edge of the text counts as whitespace, which is what lets a run at the
// start of a paragraph open a span and stops it closing one. Punctuation is
// Unicode's, not ASCII's: an em dash or a curly quote beside a delimiter has
// the same effect on flanking as a comma, and reading only the ASCII set would
// make emphasis behave differently in a description written in French.
func neighbour(s string, before bool) side {
	var r rune
	var size int
	if before {
		r, size = utf8.DecodeLastRuneInString(s)
	} else {
		r, size = utf8.DecodeRuneInString(s)
	}
	if size == 0 {
		return side{space: true}
	}
	space, punct := classify(r)
	return side{space: space, punct: punct}
}

// classify sorts a rune the way CommonMark's flanking rules do: whitespace,
// punctuation, or neither. The third class has no name in the specification and
// is what the escaping on the other side calls a word character, which is why
// this is one function and not two.
//
// A control character counts as punctuation, which is the one place this
// departs from the letter of the specification, and it is there so the two
// halves of this package agree. Leaving them in the third class made
// `\x00__0__` an inert intraword underscore on the way in and a strong span on
// the way out, which the round-trip fuzzer found in two seconds. The reference
// implementation reaches the same verdict by a different road: it substitutes
// U+FFFD for a NUL, and that is a symbol, which is punctuation under the same
// rule.
func classify(r rune) (space, punct bool) {
	space = unicode.IsSpace(r)
	punct = unicode.IsPunct(r) || unicode.IsSymbol(r) ||
		(!space && (r < 0x20 || r == 0x7f))
	return space, punct
}

// matchEmphasis pairs the delimiter runs in place: it spends characters off the
// inner end of each partner and records the mark on everything between them.
//
// The walk is CommonMark's. Take the closers left to right; for each, look back
// for the nearest run of the same character that can open and has characters
// left. A pair spends two characters each where both have two, which is strong,
// and one otherwise, which is em. Whatever is left of either run stays in play,
// so one run of three closes an em and a strong in that order.
func matchEmphasis(pieces []piece) {
	// bottoms is the reference implementation's openers_bottom, and it is here
	// for the same reason: without it the backwards search rescans the whole
	// prefix for every closer that finds nothing, which is quadratic in a line
	// of unmatched delimiters and those are cheap to write by accident.
	//
	// A failed search is a fact about the closer's character, its length modulo
	// three, and whether it could also open, because those are the only things
	// the search asked about below it. Record the floor per combination and no
	// later closer with the same three answers looks below it again.
	var bottoms [2][3][2]int

	for ci := range pieces {
		closer := &pieces[ci]
		if !closer.isDelim() || !closer.canClose {
			continue
		}
		for closer.n > 0 {
			floor := &bottoms[charIndex(closer.char)][closer.orig%3][boolIndex(closer.canOpen)]
			oi := findOpener(pieces, ci, *floor)
			if oi < 0 {
				*floor = ci
				break
			}
			pairAt(pieces, oi, ci)
		}
	}
}

// findOpener returns the index of the run that closes at ci, or -1.
func findOpener(pieces []piece, ci, floor int) int {
	closer := pieces[ci]
	for oi := ci - 1; oi >= floor; oi-- {
		opener := pieces[oi]
		if !opener.isDelim() || opener.char != closer.char ||
			!opener.canOpen || opener.n == 0 {
			continue
		}
		// The rule of three. Where either run could serve as both an opener and
		// a closer, a pair whose original lengths sum to a multiple of three is
		// refused unless both lengths are themselves multiples of three. It
		// reads like an arbitrary rule and it is the one that makes
		// `*foo**bar*` one emphasised phrase holding two asterisks rather than
		// a nest no writer could have meant.
		if (closer.canOpen || opener.canClose) &&
			(opener.orig+closer.orig)%3 == 0 &&
			(opener.orig%3 != 0 || closer.orig%3 != 0) {
			continue
		}
		return oi
	}
	return -1
}

// pairAt spends the characters of one pairing and marks what it wrapped.
func pairAt(pieces []piece, oi, ci int) {
	use := 1
	if pieces[oi].n >= 2 && pieces[ci].n >= 2 {
		use = 2
	}
	for i := oi + 1; i < ci; i++ {
		if use == 2 {
			pieces[i].strong = true
		} else {
			pieces[i].em = true
		}
		// A delimiter left inside a matched pair can never pair with anything
		// itself: its partner would have to sit outside this span, and a span
		// that overlaps another has no spelling in markdown. It stays as the
		// characters it is made of.
		pieces[i].canOpen, pieces[i].canClose = false, false
	}
	pieces[oi].n -= use
	pieces[ci].n -= use
}

func charIndex(c byte) int {
	if c == '_' {
		return 1
	}
	return 0
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// emphasisNodes flattens the pieces into the nodes the pairing decided on.
func emphasisNodes(pieces []piece, at int) ([]Node, error) {
	out := make([]Node, 0, len(pieces))
	for _, p := range pieces {
		nodes := pieceNodes(p)
		if len(nodes) == 0 {
			continue
		}
		if err := markPiece(p, nodes, at); err != nil {
			return nil, err
		}
		out = append(out, nodes...)
	}
	return out, nil
}

// pieceNodes is what a piece contributes: its content, or the characters of a
// delimiter run that paired with nothing.
//
// An unmatched run is text, which is CommonMark's rule and this package's rule
// as well: where the markup could mean two things it is refused, and where it
// plainly means one thing it is that thing. A lone asterisk plainly means an
// asterisk. A run that spent every character contributes nothing.
func pieceNodes(p piece) []Node {
	if !p.isDelim() {
		return p.nodes
	}
	if p.n == 0 {
		return nil
	}
	return []Node{{Type: "text", Text: strings.Repeat(string(p.char), p.n)}}
}

// markPiece applies the marks the pairing decided this piece sits inside.
//
// addMark is what refuses emphasis around an image or a mention, so a span
// wrapping one is reported here rather than sent to Jira and rejected with a
// message that names neither.
func markPiece(p piece, nodes []Node, at int) error {
	if p.em {
		if err := addMark(nodes, Mark{Type: "em"}, at); err != nil {
			return err
		}
	}
	if p.strong {
		return addMark(nodes, Mark{Type: "strong"}, at)
	}
	return nil
}
