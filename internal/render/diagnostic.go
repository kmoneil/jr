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
func truncationNode(d *Doc) *Node {
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
	if c.NextPageToken != "" {
		n.Leaf("remedy", "resume with --page-token, or raise --limit")
	} else {
		n.Leaf("remedy", "raise --limit, or use --limit all")
	}
	return n
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
