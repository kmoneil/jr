package render

import (
	"strconv"

	"github.com/kmoneil/jira-cli/internal/errs"
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
