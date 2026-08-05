package render

import (
	"github.com/kmoneil/jira-cli/internal/errs"
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

// IsComplete reports whether the result set is exhaustive. A record is always
// complete; only a collection can be truncated.
func (d *Doc) IsComplete() bool {
	if d.Collection != nil {
		return d.Collection.Complete
	}
	return true
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
		return d.Record.validate(d.Kind)
	}

	c := d.Collection
	if c.Name == "" {
		return errs.Runtime("INVALID_DOC", "result kind %q has no container name", d.Kind)
	}
	if len(c.Columns) == 0 {
		return errs.Runtime("INVALID_DOC",
			"result kind %q declares no columns; TSV would have nothing to emit", d.Kind)
	}
	for _, col := range c.Columns {
		if col.Header == "" {
			return errs.Runtime("INVALID_DOC",
				"result kind %q has a column with no header", d.Kind)
		}
		if err := ValidatePath(col.Path); err != nil {
			return err
		}
	}
	if c.Complete && c.NextPageToken != "" {
		return errs.Runtime("INVALID_DOC",
			"result kind %q is complete but carries a next-page token", d.Kind)
	}

	var itemName string
	for i, it := range c.Items {
		if it == nil {
			return errs.Runtime("INVALID_DOC", "result kind %q has a nil item at %d", d.Kind, i)
		}
		if itemName == "" {
			itemName = it.Name
		} else if it.Name != itemName {
			return errs.Runtime("INVALID_DOC",
				"result kind %q mixes item elements %q and %q", d.Kind, itemName, it.Name)
		}
		if err := it.validate(d.Kind + "/" + c.Name); err != nil {
			return err
		}
	}
	return nil
}

// ItemName returns the element name shared by every item in a collection.
func (c *Collection) ItemName() string {
	if len(c.Items) == 0 {
		return "item"
	}
	return c.Items[0].Name
}
