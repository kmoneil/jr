package render

import (
	"github.com/kmoneil/jr/internal/errs"
)

// Doc is a complete command result: a stable kind, a schema version for that
// kind, and exactly one payload — a Collection or a Record.
//
// The kind/version pair is the contract an agent dispatches on. Adding an
// optional element or attribute is a minor change; adding a field to a default
// column set, or changing a kind, is major. See docs/output-contract.md.
type Doc struct {
	Kind    string
	Version int

	// Site is the Jira this answer came from: the base URL the request was
	// actually sent to, including any context path, and not the one configured.
	// Empty for a command that never reached a site, which is every local one —
	// `context list` has no Jira to name and says nothing rather than guessing.
	//
	// It is provenance and therefore data, not a diagnostic. An answer stored
	// in a file outlives the shell that produced it, and two answers from two
	// Jiras are otherwise indistinguishable once they are both on disk. The
	// case that raised it was a command silently running against the wrong
	// instance and returning a well-formed, complete, exit-0 document about it.
	//
	// **TSV carries no envelope and so carries no site.** That is the same
	// limitation truncation already has there, and the reason `complete="false"`
	// becomes a stderr warning plus exit 3 in that format.
	Site string

	// Exactly one of Collection and Record is set.
	Collection *Collection
	Record     *Node
}

// Collection is a rectangular result: a homogeneous list of items plus the
// column projection the TSV writer emits.
type Collection struct {
	// Name is the container element, e.g. "issues", and the JSON/YAML array
	// key. Items are its children, e.g. "issue".
	Name  string
	Items []*Node

	// Columns is the default field set for TSV. Adding one is a major change.
	Columns []Column

	// Complete is true only if the result set is exhaustive. A truncated
	// result is never reported as complete: that guarantee is the single most
	// load-bearing part of this format.
	Complete bool

	// NextPageToken resumes a truncated result set. It is set if and only if
	// Complete is false and the server offered a cursor.
	NextPageToken string
}

// Column is one TSV column: a header and the path that extracts it from an item.
type Column struct {
	Header string
	Path   string
}

// Record is a single non-rectangular result — a document, or one entity with
// nested structure.
func Record(kind string, version int, n *Node) *Doc {
	return &Doc{Kind: kind, Version: version, Record: n}
}

// List is a complete collection result.
func List(kind string, version int, c *Collection) *Doc {
	return &Doc{Kind: kind, Version: version, Collection: c}
}

// Count returns the number of items in a collection, or 1 for a record.
func (d *Doc) Count() int {
	if d.Collection != nil {
		return len(d.Collection.Items)
	}
	return 1
}

// CompleteAttr is the attribute a container carries when it holds a bounded
// slice of something larger.
//
// A collection says this in its envelope. A record could not say it at all
// until an issue learned to carry its comment thread: the thread is paged, so a
// bounded one is the normal case, and "`complete="false"` or exit 3" is
// unqualified. So a container *inside* a record may carry it, and everything
// downstream — the stderr warning, exit 3 — keys off IsComplete rather than off
// the envelope's shape.
const CompleteAttr = "complete"

// IsComplete reports whether the result set is exhaustive.
//
// A collection answers from its envelope. A record answers from its contents:
// any container within it carrying complete="false" makes the whole document
// partial, because a caller who asked for an issue and got most of its
// conversation has been given less than they asked for, and nothing else in the
// document would say so.
func (d *Doc) IsComplete() bool {
	if d.Collection != nil {
		return d.Collection.Complete
	}
	return d.Record == nil || d.Record.complete()
}

// complete reports whether this node and everything under it is exhaustive.
func (n *Node) complete() bool {
	if v, ok := n.AttrValue(CompleteAttr); ok && v == "false" {
		return false
	}
	for _, c := range n.Children {
		if !c.complete() {
			return false
		}
	}
	return true
}

// incompleteContainer finds the first container that says it is partial, so a
// warning can name it. Nil when everything is exhaustive.
func (n *Node) incompleteContainer() *Node {
	if v, ok := n.AttrValue(CompleteAttr); ok && v == "false" {
		return n
	}
	for _, c := range n.Children {
		if found := c.incompleteContainer(); found != nil {
			return found
		}
	}
	return nil
}

// Validate enforces every invariant the writers rely on. Write calls it before
// emitting a byte, so a malformed payload fails loudly instead of producing
// output that looks fine and parses wrong.
func (d *Doc) Validate() error {
	if d == nil {
		return errs.Runtime("INVALID_DOC", "nil result document")
	}
	if d.Kind == "" {
		return errs.Runtime("INVALID_DOC", "result document has no kind")
	}
	if d.Version < 1 {
		return errs.Runtime("INVALID_DOC", "result kind %q has no schema version", d.Kind)
	}
	switch {
	case d.Collection != nil && d.Record != nil:
		return errs.Runtime("INVALID_DOC",
			"result kind %q sets both a collection and a record", d.Kind)
	case d.Collection == nil && d.Record == nil:
		return errs.Runtime("INVALID_DOC", "result kind %q has no payload", d.Kind)
	case d.Record != nil:
		if err := d.Record.validate(d.Kind); err != nil {
			return err
		}
		return conformTo(d.Kind, d.Record)
	}

	if err := validateCollectionShape(d.Kind, d.Collection); err != nil {
		return err
	}
	return validateCollectionItems(d.Kind, d.Collection)
}

// validateCollectionShape checks everything about a collection that does not
// depend on its rows, which is what the header is written from.
func validateCollectionShape(kind string, c *Collection) error {
	if c.Name == "" {
		return errs.Runtime("INVALID_DOC", "result kind %q has no container name", kind)
	}
	if len(c.Columns) == 0 {
		return errs.Runtime("INVALID_DOC",
			"result kind %q declares no columns; TSV would have nothing to emit", kind)
	}
	for _, col := range c.Columns {
		if col.Header == "" {
			return errs.Runtime("INVALID_DOC",
				"result kind %q has a column with no header", kind)
		}
		if err := ValidatePath(col.Path); err != nil {
			return err
		}
	}
	if c.Complete && c.NextPageToken != "" {
		return errs.Runtime("INVALID_DOC",
			"result kind %q is complete but carries a next-page token", kind)
	}
	return nil
}

// validateCollectionItems checks the rows, including that they are all the same
// element: a collection whose items disagree renders as TSV columns that mean
// different things from one line to the next.
func validateCollectionItems(kind string, c *Collection) error {
	var itemName string
	for i, it := range c.Items {
		if it == nil {
			return errs.Runtime("INVALID_DOC", "result kind %q has a nil item at %d", kind, i)
		}
		if itemName == "" {
			itemName = it.Name
		} else if it.Name != itemName {
			return errs.Runtime("INVALID_DOC",
				"result kind %q mixes item elements %q and %q", kind, itemName, it.Name)
		}
		// Annotated with the row's own identity: the path both checks report is
		// built from element names, so it is the same string for every row.
		if err := it.validate(kind + "/" + c.Name); err != nil {
			return identify(it, err)
		}
		if err := conformTo(kind, it); err != nil {
			return identify(it, err)
		}
	}
	return nil
}

// conformTo holds a payload node to its kind's declared schema.
//
// A kind with no registered schema passes. That is not a hole left open on
// purpose — internal/cli/contract_test.go fails if any kind lacks one — it is
// so that a test building an ad-hoc document does not have to invent a schema
// to render it.
func conformTo(kind string, n *Node) error {
	s, ok := SchemaFor(kind)
	if !ok {
		return nil
	}
	return s.Conform(n, kind)
}

// ItemName returns the element name shared by every item in a collection.
func (c *Collection) ItemName() string {
	if len(c.Items) == 0 {
		return "item"
	}
	return c.Items[0].Name
}
