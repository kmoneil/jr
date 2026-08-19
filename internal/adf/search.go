package adf

import "strings"

// This file is the writer's answer to a choice that was wrong later.
//
// renderInline walks a run left to right and commits: renderChoices enumerates
// the ways to open the span at one position and takes the first that can be
// written, and nothing ever goes back. That is correct whenever a locally
// writable choice leaves the rest of the run writable, which is almost always,
// and it is wrong exactly when it does not.
//
// The nightly's third find of 2026-08-19 is where it is wrong. The last three
// nodes of `*0***0*****0** **0*****0**0` are strong over two of them and strong
// over the last, and the wider span has no spelling, so it is cut to one node
// and written `***0***`. That is a correct span. It leaves an asterisk against
// the node after it, where `**` merges and `__` cannot close between two word
// characters, and the run is refused. Opening the `em` on that node instead
// writes `_**0**_`, the next span takes `**`, and the whole run comes out as
// the text the reader was handed in the first place.
//
// So the enumeration was not too clever, it was too small. searchInline is the
// same enumeration over the whole run rather than over one position.
//
// It runs only when the greedy walk has already refused. That is what makes it
// free: measured over the 247 real documents and the 1911 accepted fuzz inputs,
// the greedy walk makes 1.00 attempts per span position at the median and 1.55
// at the worst, so the first choice is almost always the answer and this file
// is almost never reached. Nothing that writes today writes differently,
// because a search that returns the first complete assignment in the same order
// the greedy walk tried returns exactly what the greedy walk returned whenever
// the greedy walk returned anything.

// searchBudget is how many spans one run may attempt before the search gives up
// and the document is refused.
//
// A backtracking walk is exponential in the worst case and this package holds
// itself to linear allocation in the span count, so the budget is the whole of
// what keeps that promise. It is generous against every measurement here and
// still a constant multiple of the run: the busiest document in either corpus
// attempts 1.06 spans per position, and a document that needs eight times that
// is not one this converter is going to find an answer for by trying longer.
func searchBudget(n int) int { return 8*n + 64 }

// searchInline writes a run by depth-first search over the choices at every
// position, returning the first complete assignment.
//
// The order is renderChoices' order at each position, so the first assignment
// it finds is the one the greedy walk would have found if the greedy walk had
// been able to reconsider.
func searchInline(nodes []Node, applied []Mark, written, where string, crowded bool) (string, error) {
	s := &inlineSearch{
		nodes:   nodes,
		applied: applied,
		written: written,
		where:   where,
		crowded: crowded,
		budget:  searchBudget(len(nodes)),
	}
	return s.from(0, "")
}

// inlineSearch is one run's search: what is being written, and what is left of
// the budget.
//
// refused keeps the first noSpelling seen anywhere in the search rather than
// the last, because that one names the position a reader should look at rather
// than whichever branch the search happened to give up on last.
type inlineSearch struct {
	nodes   []Node
	applied []Mark
	written string
	where   string
	crowded bool
	budget  int
	refused error
}

// from writes nodes[i:] onto prefix and returns the whole run's text.
func (s *inlineSearch) from(i int, prefix string) (string, error) {
	if i == len(s.nodes) {
		return prefix, nil
	}
	choices := spanChoices(s.nodes, i, s.applied)
	if len(choices) == 0 {
		return s.plain(i, prefix)
	}
	return s.spans(choices, i, prefix)
}

// plain writes a node carrying nothing this file chooses between.
func (s *inlineSearch) plain(i int, prefix string) (string, error) {
	text, err := inline(s.nodes[i], atLineStart(s.written+prefix),
		s.where+" > "+s.nodes[i].Type)
	if err != nil {
		return "", err
	}
	return s.from(i+1, prefix+text)
}

// spans tries every way of opening the span at i, and every way of continuing
// after each one.
func (s *inlineSearch) spans(choices []spanChoice, i int, prefix string) (string, error) {
	for _, c := range choices {
		for j := c.j; j > i; j-- {
			// The same two filters renderChoices applies, for the same
			// reasons: a cut that strands a mark on whitespace says something
			// the document does not, and a cut strike writes four tildes.
			if j < c.j && (strands(s.nodes, j) || cutRunsTogether(c.mark)) {
				continue
			}
			out, err := s.attempt(i, j, c.mark, prefix)
			if err == nil {
				return out, nil
			}
			if !isNoSpelling(err) {
				return "", err
			}
		}
	}
	return "", s.exhausted()
}

// attempt writes one span and everything after it, and is where the budget is
// spent.
func (s *inlineSearch) attempt(i, j int, mark Mark, prefix string) (string, error) {
	if s.budget <= 0 {
		// Out of budget is refused, not written wrongly. The caller reports
		// the document, which is the honest answer once the converter has
		// stopped looking.
		return "", s.exhausted()
	}
	s.budget--

	var b strings.Builder
	b.WriteString(prefix)
	span, err := renderSpan(s.nodes, i, j, mark, s.applied, s.written, &b, s.where, s.crowded)
	if err != nil {
		s.note(err)
		return "", err
	}

	// The span is written. Everything after it still has to be, and if it
	// cannot be then this span was the wrong choice.
	out, err := s.from(j, prefix+span)
	if err != nil {
		s.note(err)
	}
	return out, err
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
