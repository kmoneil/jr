package render

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Type is what a value in the output is, beyond being text.
//
// Every format this tool writes is textual, so a type here is a promise about
// the *shape* of the text rather than a JSON type. It is what lets a consumer
// know that `@count` will parse as an integer and `<created>` as an RFC 3339
// timestamp without discovering it from a sample.
type Type string

// The value shapes a schema can promise.
const (
	// TypeString promises nothing beyond text.
	TypeString Type = "string"
	// TypeInt is a decimal integer, possibly negative.
	TypeInt Type = "int"
	// TypeBool is exactly "true" or "false", never "yes" or "1".
	TypeBool Type = "bool"
	// TypeTimestamp is RFC 3339 in UTC. Jira's own formats are normalized to
	// this before they are emitted; see the sprint resource for why that is
	// not optional.
	TypeTimestamp Type = "timestamp"
	// TypeDate is a calendar date with no time, YYYY-MM-DD. Jira stores some
	// dates without a time, and inventing midnight in some timezone would be a
	// value nobody set.
	TypeDate Type = "date"
)

// Field is one attribute, or a node's text.
type Field struct {
	// Name is the attribute name. It is empty for a node's text.
	Name string
	Type Type
	// Optional means the field may be absent. It is not the same as empty: an
	// attribute present with an empty value is a fact, and this tool omits
	// rather than blanks whenever "not disclosed" and "empty" differ.
	Optional bool
	// Enum, when set, is the closed set of values. A value outside it is a
	// contract violation rather than a curiosity.
	Enum []string
}

// Child is one element that may appear inside another, with its cardinality.
type Child struct {
	Schema *Schema
	// Optional means zero occurrences are allowed.
	Optional bool
	// Repeated means more than one occurrence is allowed.
	Repeated bool
}

// Schema describes the shape of one element.
//
// It exists so `jr contract` can say what a kind looks like, not only what it
// is called and what version it is at. A consumer pins the version and then
// verifies the response against this, rather than writing a parser by hand
// against a sample and finding out later which fields were optional.
//
// It is checked on every document this tool writes, which is the only reason
// to trust it. A schema that were merely published alongside the code would
// describe the output as somebody once believed it to be.
type Schema struct {
	Element string
	Attrs   []Field
	// Children are the elements that may appear inside, matched by name and
	// not by order. Order is deterministic in the output, but requiring it here
	// would make adding a field a breaking change for no reader's benefit.
	Children []Child
	// Text describes the node's own text, or is nil when the node carries
	// none. Its Name is unused.
	Text *Field
	// ListOf names the child element this node is a homogeneous list of,
	// mirroring Node.ListOf. It is what makes JSON emit [] rather than
	// omitting an empty list, and a consumer needs to know which containers
	// behave that way.
	ListOf string
	// Extra permits child elements this schema does not name, for a kind whose
	// shape depends on what the caller asked for — `issue list --field
	// "Story Points"` adds a <customfield_10042>, and no fixed list can name
	// it. A schema without it is closed, and an undeclared element is a
	// violation.
	//
	// It is published rather than left implicit. A consumer that met an
	// unexpected element would otherwise have to guess whether it was a
	// contract change or a flag somebody passed.
	Extra *Extra
}

// Extra describes the elements a caller's own flags can add to a shape.
type Extra struct {
	// Named says where the element names come from, in a few words, e.g.
	// "the id of a field requested with --field".
	Named string
	Type  Type
}

// Leaf is the shape of an element that carries nothing but text, which is most
// of them. It exists because spelling it out every time buries the interesting
// declarations in boilerplate.
func Leaf(element string, t Type) *Schema {
	return &Schema{Element: element, Text: &Field{Type: t}}
}

// LeafEnum is Leaf for a closed set of values.
func LeafEnum(element string, values ...string) *Schema {
	return &Schema{Element: element, Text: &Field{Type: TypeString, Enum: values}}
}

// ListSchema is the shape of a homogeneous container built by ListEl: a count
// and zero or more of one child.
func ListSchema(element, itemName string, item *Schema) *Schema {
	return &Schema{
		Element: element,
		ListOf:  itemName,
		Attrs:   []Field{{Name: "count", Type: TypeInt}},
		Children: []Child{
			{Schema: item, Optional: true, Repeated: true},
		},
	}
}

// schemas is the per-kind registry. A resource registers its kinds from init,
// which is the only time it is written; the mutex is there because tests read
// it in parallel, not because registration races.
var (
	schemasMu sync.RWMutex
	schemas   = map[string]*Schema{}
)

// RegisterSchema records the shape of one output kind.
//
// A kind is registered once, by the resource that owns it, next to the code
// that builds the node — because the two have to agree, and the shortest
// distance between them is the same file.
func RegisterSchema(kind string, s *Schema) {
	if kind == "" || s == nil {
		panic("render: a schema needs a kind and a shape")
	}
	schemasMu.Lock()
	defer schemasMu.Unlock()
	if existing, ok := schemas[kind]; ok && existing != s {
		panic(fmt.Sprintf("render: kind %q already has a schema", kind))
	}
	schemas[kind] = s
}

// SchemaFor returns the registered shape of a kind.
func SchemaFor(kind string) (*Schema, bool) {
	schemasMu.RLock()
	defer schemasMu.RUnlock()
	s, ok := schemas[kind]
	return s, ok
}

// RegisteredKinds returns every kind with a schema, sorted.
func RegisteredKinds() []string {
	schemasMu.RLock()
	defer schemasMu.RUnlock()
	out := make([]string, 0, len(schemas))
	for k := range schemas {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Conform reports whether a node matches this schema.
//
// where names the position in the document, so a violation says which element
// under which kind was wrong rather than only that something was.
func (s *Schema) Conform(n *Node, where string) error {
	if s == nil {
		return nil
	}
	if n == nil {
		return violation(where, "expected <%s> and found nothing", s.Element)
	}
	at := where + "/" + n.Name
	if n.Name != s.Element {
		return violation(where, "expected <%s> and found <%s>", s.Element, n.Name)
	}
	if err := s.conformAttrs(n, at); err != nil {
		return err
	}
	if err := s.conformText(n, at); err != nil {
		return err
	}
	return s.conformChildren(n, at)
}

func (s *Schema) conformAttrs(n *Node, at string) error {
	seen := map[string]bool{}
	for _, a := range n.Attrs {
		field, ok := s.attr(a.Name)
		if !ok {
			return violation(at, "undeclared attribute %q", a.Name)
		}
		if seen[a.Name] {
			return violation(at, "attribute %q appears twice", a.Name)
		}
		seen[a.Name] = true
		if err := checkValue(at, "attribute "+a.Name, a.Value, field); err != nil {
			return err
		}
	}
	for _, f := range s.Attrs {
		if !f.Optional && !seen[f.Name] {
			return violation(at, "required attribute %q is missing", f.Name)
		}
	}
	return nil
}

func (s *Schema) conformText(n *Node, at string) error {
	if s.Text == nil {
		if n.Text != "" {
			return violation(at, "carries text, and none is declared")
		}
		return nil
	}
	// Declared text may still be empty: an element that exists with no content
	// is a different fact from an element that is absent, and both are legal.
	return checkValue(at, "text", n.Text, *s.Text)
}

func (s *Schema) conformChildren(n *Node, at string) error {
	counts := map[string]int{}
	for _, c := range n.Children {
		child, ok := s.child(c.Name)
		if !ok {
			if s.Extra != nil {
				if err := checkValue(at, "<"+c.Name+">", c.Text,
					Field{Type: s.Extra.Type}); err != nil {
					return err
				}
				continue
			}
			return violation(at, "undeclared element <%s>", c.Name)
		}
		counts[c.Name]++
		if counts[c.Name] > 1 && !child.Repeated {
			return violation(at, "<%s> appears more than once and is not repeated", c.Name)
		}
		if err := child.Schema.Conform(c, at); err != nil {
			return err
		}
	}
	for _, child := range s.Children {
		if !child.Optional && counts[child.Schema.Element] == 0 {
			return violation(at, "required element <%s> is missing", child.Schema.Element)
		}
	}
	return nil
}

func (s *Schema) attr(name string) (Field, bool) {
	for _, f := range s.Attrs {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func (s *Schema) child(element string) (Child, bool) {
	for _, c := range s.Children {
		if c.Schema != nil && c.Schema.Element == element {
			return c, true
		}
	}
	return Child{}, false
}

// checkValue holds a value to the shape its field promised.
//
// An empty value passes every type check. Emptiness is a fact this tool emits
// deliberately — "present but not disclosed" — and refusing it here would force
// every optional-looking field to be declared as a string.
func checkValue(at, what, value string, f Field) error {
	if value == "" {
		return nil
	}
	if len(f.Enum) > 0 && !slices.Contains(f.Enum, value) {
		return violation(at, "%s is %q, which is not one of %s",
			what, value, strings.Join(f.Enum, ", "))
	}

	switch f.Type {
	case TypeInt:
		if _, err := strconv.Atoi(value); err != nil {
			return violation(at, "%s is %q, which is declared as an integer", what, value)
		}
	case TypeBool:
		if value != "true" && value != "false" {
			return violation(at, "%s is %q, and a bool is exactly true or false", what, value)
		}
	case TypeTimestamp:
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return violation(at, "%s is %q, which is not RFC 3339", what, value)
		}
		if _, offset := t.Zone(); offset != 0 {
			return violation(at, "%s is %q, which is not UTC", what, value)
		}
	case TypeDate:
		if _, err := time.Parse(time.DateOnly, value); err != nil {
			return violation(at, "%s is %q, which is not a YYYY-MM-DD date", what, value)
		}
	case TypeString, "":
	}
	return nil
}

// violation is the one error every conformance failure produces.
//
// It is a runtime error rather than a usage one because nothing the caller
// typed caused it: the tool built a document that does not match the contract
// it publishes, which is a bug in this program.
func violation(at, format string, args ...any) error {
	return errs.Runtime("SCHEMA_VIOLATION",
		"%s does not match its declared schema", at).
		WithDetail(format, args...).
		WithRemedy("report this: the output contract and the code disagree")
}

// Node renders the schema itself, for `jr contract`.
func (s *Schema) Node() *Node {
	n := El("element").Attr("name", s.Element)
	if s.ListOf != "" {
		n.Attr("list-of", s.ListOf)
	}

	attrs := make([]*Node, 0, len(s.Attrs))
	for _, f := range s.Attrs {
		attrs = append(attrs, fieldNode("attribute", f))
	}
	n.Child(ListEl("attributes", "attribute", attrs...))

	if s.Text != nil {
		text := *s.Text
		text.Name = "text"
		n.Child(fieldNode("text", text))
	}

	children := make([]*Node, 0, len(s.Children))
	for _, c := range s.Children {
		child := c.Schema.Node().
			Attr("optional", strconv.FormatBool(c.Optional)).
			Attr("repeated", strconv.FormatBool(c.Repeated))
		children = append(children, child)
	}
	n.Child(ListEl("elements", "element", children...))

	if s.Extra != nil {
		n.Child(El("extra").
			Attr("type", string(s.Extra.Type)).
			SetText(s.Extra.Named))
	}
	return n
}

func fieldNode(element string, f Field) *Node {
	n := El(element).
		AttrIf("name", f.Name).
		Attr("type", string(f.Type)).
		Attr("optional", strconv.FormatBool(f.Optional))
	if len(f.Enum) > 0 {
		n.Attr("enum", strings.Join(f.Enum, ","))
	}
	return n
}

// ResolveColumn reports whether a column path names a value in this schema.
//
// A TSV cell is a scalar. A path that walks to a container — an element with
// children and no text of its own — has nowhere to get one, so the cell comes
// out empty on every row for every caller, forever. That is a column which
// cannot do what its header says, and this repository has a rule against
// shipping one.
//
// The TSV writer already claimed this was checked: "whether the path is even
// resolvable against this kind is asserted by the contract tests, not guessed
// at here." No such test existed. `project statuses` declared a `statuses`
// column over a list element and emitted an empty cell for every issue type on
// both deployments, and every test passed, because they asserted the header and
// the row count and never a cell.
func (s *Schema) ResolveColumn(path string) error {
	if s == nil {
		return errs.Runtime("UNKNOWN_KIND", "no schema to resolve %q against", path)
	}
	cur := s
	segs := strings.Split(path, "/")

	for i, seg := range segs {
		last := i == len(segs)-1
		name, attr, hasAttr := strings.Cut(seg, "@")

		if name != "" {
			next, ok := cur.childNamed(name)
			if !ok {
				if cur.Extra != nil {
					return nil // A kind whose shape depends on the request.
				}
				return errs.Runtime("UNKNOWN_COLUMN",
					"%q names element %q, which %q does not contain",
					path, name, cur.Element)
			}
			cur = next
		}
		if !last {
			continue
		}
		if hasAttr {
			if cur.hasAttr(attr) || cur.Extra != nil {
				return nil
			}
			return errs.Runtime("UNKNOWN_COLUMN",
				"%q names attribute %q, which %q does not have",
				path, attr, cur.Element)
		}
		if cur.Text == nil {
			return errs.Runtime("UNKNOWN_COLUMN",
				"%q names element %q, which carries no text of its own — "+
					"a column over a container is an empty cell on every row",
				path, cur.Element)
		}
	}
	return nil
}

func (s *Schema) childNamed(name string) (*Schema, bool) {
	for _, c := range s.Children {
		if c.Schema != nil && c.Schema.Element == name {
			return c.Schema, true
		}
	}
	return nil, false
}

func (s *Schema) hasAttr(name string) bool {
	for _, a := range s.Attrs {
		if a.Name == name {
			return true
		}
	}
	return false
}
