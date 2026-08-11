package adf

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"

	"github.com/kmoneil/jr/internal/errs"
)

// Mark is one inline mark on a text node: strong, em, link, and the rest.
//
// Attrs is a map for the same reason Node.Attrs is — see there.
type Mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Node is one node of an Atlassian Document Format tree.
//
// The named fields are the whole node-level vocabulary of ADF, and Parse
// refuses a document carrying any other, so a field Atlassian adds is a
// refusal rather than a value silently dropped on the way to markdown.
//
// Attrs has to be a map because ADF's attributes are per node type — a heading
// carries a level, a codeBlock a language, a media an id and a collection —
// and no single struct holds them without inventing fields for the nodes that
// do not have them. It is not an invitation to put anything in one: nothing on
// the write side populates Attrs from caller input, and the reader validates
// the attributes of every node type it converts.
type Node struct {
	Type    string         `json:"type"`
	Version int            `json:"version,omitempty"`
	Text    string         `json:"text,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Marks   []Mark         `json:"marks,omitempty"`
	Content []Node         `json:"content,omitempty"`
}

// Parse reads an ADF document from the JSON Jira returned.
//
// Unknown node-level fields are refused rather than ignored. Ignoring one
// would mean converting a document to markdown while dropping part of it and
// reporting the result as the description — the failure this package exists to
// avoid. A caller that wants the document whatever it holds asks for it
// unconverted.
func Parse(raw []byte) (Node, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var n Node
	if err := dec.Decode(&n); err != nil {
		return Node{}, errs.Remote("MALFORMED_ADF",
			"Jira returned a document this tool cannot read").
			WithDetail("%s", err.Error()).
			WithRemedy("--raw-body emits the document exactly as Jira sent it").
			Wrap(err)
	}
	// A second value after the document means this is not one document, and
	// converting the first while ignoring the rest is the same silent loss.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Node{}, errs.Remote("MALFORMED_ADF",
			"Jira returned more than one document where one belongs").
			WithRemedy("--raw-body emits the document exactly as Jira sent it")
	}
	if n.Type != "doc" {
		return Node{}, errs.Remote("MALFORMED_ADF",
			"the document's root node is not a doc").
			WithDetail("root node type is %q", n.Type).
			WithRemedy("--raw-body emits the document exactly as Jira sent it")
	}
	return n, nil
}

// attrString reads a string attribute. The second result reports whether it was
// present and a string, so a caller can tell an absent attribute from an empty
// one rather than treating both as "".
func attrString(attrs map[string]any, key string) (string, bool) {
	v, ok := attrs[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// attrInt reads an integer attribute.
//
// JSON numbers decode as float64, so a value that is not a whole number is
// reported absent rather than truncated: a heading at level 2.5 is a document
// this tool does not understand, not a level 2 heading.
func attrInt(attrs map[string]any, key string) (int64, bool) {
	v, ok := attrs[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// attrText renders an attribute as the text it holds, for the attributes that
// are carried into a markdown URI. A number keeps its integer spelling rather
// than picking up the exponent form float64 formatting would give it.
func attrText(attrs map[string]any, key string) (string, bool) {
	if s, ok := attrString(attrs, key); ok {
		return s, true
	}
	if i, ok := attrInt(attrs, key); ok {
		return strconv.FormatInt(i, 10), true
	}
	return "", false
}
