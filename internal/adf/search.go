package adf

// This file is the writer's answer to a choice that was wrong later.
//
// renderInline walks a run left to right and commits: renderChoices enumerates
// the ways to open the span at one position and takes the first that can be
// written, and nothing ever goes back. That is correct whenever a locally
// writable choice leaves the rest of the run writable, which is almost always,
// and it is wrong exactly when it does not.
//
// Three things are chosen while writing a run, and any of them can be the
// reason something later has no spelling:
//
//   - which mark opens the span at a position, where a node carries several;
//   - how far that span reaches, where the mark reaches further than the span
//     can be written;
//   - which of the two characters an emphasis span is written with.
//
// The greedy walk fixes all three at the moment it reaches them. The third is
// the one that hides: `*_00_0 __0__*` is em over two nodes with strong on the
// second, and writing that strong span `**0**` leaves the em with no spelling,
// because the em's content then ends in a live asterisk. Writing it `__0__`
// instead leaves the em its asterisk and the document comes out exactly as its
// own reader built it. Nothing about that is visible from inside the strong
// span, which is written correctly either way.
//
// So the enumeration was not too clever, it was too small, and it was too small
// in all three directions. searchInline is the same enumeration over the whole
// run, and it is written in continuation-passing style for the third one:
// `enumerate` yields every way of writing a run rather than the first, so a
// failure after a span can send the search back into the span's own content.
//
// It runs only when the greedy walk has already refused. That is what makes it
// free: measured over the 247 real documents and the 1911 accepted fuzz inputs,
// the greedy walk makes 1.00 attempts per span position at the median and 1.55
// at the worst, so the first choice is almost always the answer and this file is
// almost never reached. Nothing that writes today writes differently, because
// the search yields spellings in the order the greedy walk tried them and the
// greedy walk's answer is therefore the first one it finds.

// searchBudget is how many spans one run may attempt before the search gives up
// and the document is refused.
//
// A backtracking walk is exponential in the worst case and this package holds
// itself to linear allocation in the span count, so the budget is the whole of
// what keeps that promise. It is generous against every measurement here and
// still a constant multiple of the run: the busiest document in either corpus
// attempts 1.06 spans per position, and a document that needs eight times that
// is not one this converter is going to find an answer for by trying longer.
//
// The budget is shared across the whole search, nested spans included, so a
// document cannot buy itself more of it by being deeper.
func searchBudget(n int) int { return 8*n + 64 }

// searchInline writes a run by searching the choices at every position, and
// returns the first complete assignment.
//
// It searches twice, and the order is the point. The first pass allows only the
// first workable character for every emphasis span, which is what the greedy
// walk emits, so any document the walk could nearly write comes out spelled the
// way it has always been spelled. Only when that finds nothing at all does the
// second pass let a span take its other character.
//
// Doing it the other way round, letting one span reach for its second character
// before another position has tried its first, is a search over the same set
// that returns a different member of it. That is not free: `*0***0*****0** ...`
// came out `_**0**_**0**0` in 0.9.1 and would come out `__*0*__**0**0`, which
// is the same document spelled differently, for a body somebody may already
// have recorded. The stability policy calls moving text that was stable
// breaking, and nothing here is worth that.
func searchInline(nodes []Node, applied []Mark, written, where string, crowded bool) (string, error) {
	var refused error
	// Emphasis has two spellings, so one skip exhausts the alternatives.
	for maxSkip := range 2 {
		s := &inlineSearch{
			where:   where,
			crowded: crowded,
			budget:  searchBudget(len(nodes)),
			maxSkip: maxSkip,
		}
		var out string
		err := s.enumerate(nodes, applied, written, func(text string) error {
			out = text
			return nil
		})
		switch {
		case err == nil:
			return out, nil
		case !isNoSpelling(err):
			return "", err
		case refused == nil:
			refused = err
		}
	}
	return "", refused
}

// inlineSearch is one search: what is left of the budget, and the first refusal
// it saw.
//
// refused keeps the first noSpelling rather than the last, because that one
// names the position a reader should look at rather than whichever branch the
// search happened to abandon last.
type inlineSearch struct {
	where   string
	crowded bool
	budget  int
	// maxSkip is how far past the first workable spelling any one span may
	// reach on this pass. See searchInline for why it is a pass rather than an
	// inner loop.
	maxSkip int
	refused error
}

// enumerate calls yield with each way of writing nodes under applied, in the
// order the greedy walk would have tried them.
//
// yield returns nil to accept a spelling, which ends the enumeration and is
// reported all the way up, or a noSpelling error to ask for another. Any other
// error is the document's and stops everything.
func (s *inlineSearch) enumerate(
	nodes []Node, applied []Mark, written string, yield func(string) error,
) error {
	var step func(i int, text string) error

	step = func(i int, text string) error {
		if i == len(nodes) {
			return yield(text)
		}
		choices := spanChoices(nodes, i, applied)
		if len(choices) == 0 {
			piece, err := inline(nodes[i], atLineStart(written+text),
				s.where+" > "+nodes[i].Type)
			if err != nil {
				return err
			}
			return step(i+1, text+piece)
		}
		return s.spans(choices, nodes, applied, written, text, i, step)
	}
	return step(0, "")
}

// spans tries every way of opening the span at i, and every way of continuing
// after each one.
func (s *inlineSearch) spans(
	choices []spanChoice, nodes []Node, applied []Mark, written, text string,
	i int, step func(int, string) error,
) error {
	for _, c := range choices {
		for j := c.j; j > i; j-- {
			// The same two filters renderChoices applies, for the same
			// reasons: a cut that strands a mark on whitespace says something
			// the document does not, and a cut strike writes four tildes.
			if j < c.j && (strands(nodes, j) || cutRunsTogether(c.mark)) {
				continue
			}
			err := s.span(nodes, applied, written, text, i, j, c.mark, step)
			if err == nil {
				return nil
			}
			if !isNoSpelling(err) {
				return err
			}
			s.note(err)
		}
	}
	return s.exhausted()
}

// span enumerates the ways to write one span at nodes[i:j] and, for each,
// everything after it.
//
// The content is enumerated rather than written once, because which character
// a nested emphasis span picks decides what this one's delimiters sit against.
func (s *inlineSearch) span(
	nodes []Node, applied []Mark, written, text string, i, j int, mark Mark,
	step func(int, string) error,
) error {
	if s.budget <= 0 {
		// Out of budget is refused, not written wrongly. The caller reports
		// the document, which is the honest answer once the converter has
		// stopped looking.
		return s.exhausted()
	}
	s.budget--

	// A fresh backing array, because two branches of the search hold their own
	// mark stacks and appending onto a shared one has them overwrite each other.
	inside := append(applied[:len(applied):len(applied)], mark)

	return s.enumerate(nodes[i:j], inside, written+text, func(inner string) error {
		return s.wrap(nodes, applied, text, j, mark, inner, step)
	})
}

// wrap puts delimiters around one written content, every spelling in turn, and
// continues past the span for each.
func (s *inlineSearch) wrap(
	nodes []Node, applied []Mark, text string, j int, mark Mark, inner string,
	step func(int, string) error,
) error {
	for skip := 0; skip <= s.maxSkip; skip++ {
		span, err := wrapSpan(nodes, j, mark, applied, inner, text, s.where, s.crowded, skip)
		if err != nil {
			if isNoSpelling(err) {
				// No further spelling of this content. The caller's next move
				// is a different content, or a different span.
				return s.exhausted()
			}
			return err
		}
		err = step(j, text+span)
		if err == nil {
			return nil
		}
		if !isNoSpelling(err) {
			return err
		}
		s.note(err)
	}
	return s.exhausted()
}

// note keeps the first refusal the search saw.
func (s *inlineSearch) note(err error) {
	if s.refused == nil && isNoSpelling(err) {
		s.refused = err
	}
}

// exhausted is the answer when nothing is left to try.
func (s *inlineSearch) exhausted() error {
	if s.refused == nil {
		s.refused = &noSpelling{where: s.where}
	}
	return s.refused
}
