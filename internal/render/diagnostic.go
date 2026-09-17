package render

import (
	"strconv"

	"github.com/kmoneil/jr/internal/errs"
)

// diagnosticVersion is the schema version of the <error> and <warning>
// envelopes. It moves independently of any result kind.
const diagnosticVersion = 1

// TruncatedCode is the warning code emitted when a result set was cut short.
// It always accompanies exit 3.
const TruncatedCode = "RESULT_TRUNCATED"

// Stop says which bound ended a collection early.
//
// The remedy depends on it and nothing downstream can work it out: a walk that
// spent its request budget and a walk that reached `--limit` both arrive here
// as rows plus a token, and only the thing doing the paging knows which. It was
// inferred from whether a token existed, which told a caller who had already
// passed `--limit all` to raise `--limit`, on a command with no `--page-token`
// to resume from either.
type Stop string

const (
	// StopUnstated is a caller that did not say. The remedy then describes the
	// limit, which is what every truncation meant before budgets could report
	// themselves, and it is right for every bound a command applies to its own
	// rows.
	StopUnstated Stop = ""
	// StopLimit is the caller's --limit.
	StopLimit Stop = "limit"
	// StopBudget is --max-requests, which is not a bound on the answer at all
	// but on what may be spent finding it.
	StopBudget Stop = "budget"
)

// truncationRemedy is what to do about a result that stopped early.
//
// Two questions, and both have to be asked: what stopped it, and whether this
// command can resume. `issue activity` has no resume token, so for a budget cut
// there the only true remedy is the budget and the query.
func truncationRemedy(stop Stop, token string) string {
	if stop == StopBudget {
		if token != "" {
			return "raise --max-requests, or resume with --page-token"
		}
		return "raise --max-requests, or narrow the query"
	}
	if token != "" {
		return "resume with --page-token, or raise --limit"
	}
	return "raise --limit, or use --limit all"
}

// errorNode builds the <error> envelope. Retryable is always present, never
// omitted when false, so an agent can read it unconditionally instead of
// inferring a default.
func errorNode(e *errs.Error) *Node {
	n := El("error").Attr("v", strconv.Itoa(diagnosticVersion)).
		Leaf("code", e.Code).
		Leaf("message", e.Message)
	n.LeafIf("detail", e.Detail)
	n.LeafIf("remedy", e.Remedy)
	n.Leaf("retryable", strconv.FormatBool(e.Retryable))
	n.Leaf("exit", strconv.Itoa(e.Exit.Int()))
	n.Leaf("exit-name", e.Exit.Name())
	n.LeafIf("request-id", e.RequestID)
	return n
}

// truncationNode builds the <warning> envelope that accompanies exit 3. It is
// emitted for every format, not only TSV: the XML and JSON envelopes also carry
// complete="false", but the exit code and the stderr warning are what a script
// checks.
func truncationNode(d *Doc, partialElement string, stop Stop) *Node {
	if d.Collection == nil {
		return recordTruncationNode(d)
	}
	c := d.Collection
	n := El("warning").Attr("v", strconv.Itoa(diagnosticVersion)).
		Leaf("code", TruncatedCode).
		Leaf("message", "result set was truncated before it was exhausted").
		Leaf("kind", d.Kind).
		Leaf("count", strconv.Itoa(len(c.Items)))
	n.LeafIf("next-page-token", c.NextPageToken)

	// A collection can be short for two different reasons and they have
	// different fixes. Usually the rows ran out against a bound, and raising it
	// gets the rest. But every row can be present and a *container inside* one
	// be clipped — `issue list --with-comments` against Cloud, which inlines
	// twenty comments of a longer thread — and there `--limit all` changes
	// nothing at all. Offering it would be the dead end with a helpful tone
	// that the sweep guard's own rule warns about, so the element is named
	// instead, exactly as a partial record's warning names it.
	//
	// A buffered document is asked; a streamed one is told, because its rows
	// were bytes on stdout before this ran.
	if partial := incompleteItem(c.Items); partial != nil {
		n.Leaf("element", partial.Name)
		if count, ok := partial.AttrValue("count"); ok {
			n.Leaf("count", count)
		}
		if total, ok := partial.AttrValue("total"); ok {
			n.Leaf("total", total)
		}
		n.Leaf("remedy", partialSubresourceRemedy)
		return n
	}
	if partialElement != "" {
		n.Leaf("element", partialElement)
		n.Leaf("remedy", partialSubresourceRemedy)
		return n
	}

	n.Leaf("remedy", truncationRemedy(stop, c.NextPageToken))
	return n
}

// partialSubresourceRemedy is what to do when the rows are all present and
// something inside one of them is not. Deliberately generic: naming the exact
// command would mean the renderer knowing which resource it is rendering, and
// the element name is enough to find the verb that pages it.
const partialSubresourceRemedy = "every row is here and one of them holds part " +
	"of a paged subresource; read that subresource with the command that pages it"

// incompleteItem finds the first container inside a collection's rows that says
// it is partial. Nil when the rows themselves are what ran short.
//
// The nil check is not defensive: a streamed collection's items are nil
// placeholders that exist to be counted, because the real rows were written and
// released one page at a time.
func incompleteItem(items []*Node) *Node {
	for _, item := range items {
		if item == nil {
			continue
		}
		if found := item.incompleteContainer(); found != nil {
			return found
		}
	}
	return nil
}

// recordTruncationNode is the same warning for a record whose contents are
// bounded — an issue carrying part of its comment thread, today.
//
// It names the container rather than the kind alone, because a record can hold
// more than one and "this issue is partial" does not say which part. There is
// no page token: a nested container is not resumable in place, and the remedy
// is the command that pages that subresource properly.
func recordTruncationNode(d *Doc) *Node {
	partial := d.Record.incompleteContainer()
	n := El("warning").Attr("v", strconv.Itoa(diagnosticVersion)).
		Leaf("code", TruncatedCode).
		Leaf("message", "part of this record was truncated before it was exhausted").
		Leaf("kind", d.Kind)
	if partial == nil {
		return n
	}
	n.Leaf("element", partial.Name)
	if count, ok := partial.AttrValue("count"); ok {
		n.Leaf("count", count)
	}
	// Generic on purpose. The resource could supply an exact command, but only
	// by putting it in the document as an attribute, and a remedy is a
	// diagnostic rather than data — `issue.get` should not carry advice about
	// how to read it. The element name is enough to find the command that pages
	// it, and that command is where the paging contract already lives.
	n.Leaf("remedy", "raise the cap, or use the command that lists "+
		partial.Name+" to page through all of them")
	return n
}

// warningNode builds a general <warning> envelope.
//
// It is the same shape as the truncation warning, because a consumer that
// learned to read one should not have to learn a second — the code is what
// distinguishes them.
func warningNode(code, message string) *Node {
	return El("warning").Attr("v", strconv.Itoa(diagnosticVersion)).
		Leaf("code", code).
		Leaf("message", message)
}
