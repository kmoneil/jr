package render

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Attr is a single name/value pair on a Node. Attributes keep their declared
// order so output is byte-for-byte deterministic.
type Attr struct {
	Name  string
	Value string
}

// Node is one element of a result payload, in the format-independent
// intermediate representation every writer consumes.
//
// A node is either a leaf carrying Text or a branch carrying Children; a node
// with both is legal but only XML preserves the mixed form. Attribute names and
// child element names must not collide within a node, because the JSON and YAML
// writers flatten both into one object.
type Node struct {
	Name     string
	Attrs    []Attr
	Children []*Node
	Text     string
	// CDATA marks Text as mixed content: newlines, quotes, and fenced code
	// blocks are emitted verbatim inside a CDATA section rather than escaped.
	CDATA bool
	// ListOf names the child element this node is a homogeneous list of. It
	// makes JSON and YAML emit an array unconditionally — an empty list is
	// [], never an absent field — so a consumer never has to distinguish
	// "none" from "not applicable".
	ListOf string
}

// El builds a node with the given element name.
func El(name string) *Node { return &Node{Name: name} }

// ListEl builds a container for a homogeneous list of items. It records the
// count itself, so a count attribute can never disagree with the number of
// children beneath it.
func ListEl(name, itemName string, items ...*Node) *Node {
	n := &Node{Name: name, ListOf: itemName}
	n.Attr("count", strconv.Itoa(len(items)))
	for _, it := range items {
		n.Child(it)
	}
	return n
}

// Attr appends an attribute and returns the receiver. An empty value is still
// emitted, because "present but empty" and "absent" are different facts.
func (n *Node) Attr(name, value string) *Node {
	n.Attrs = append(n.Attrs, Attr{Name: name, Value: value})
	return n
}

// AttrIf appends an attribute only when value is non-empty.
func (n *Node) AttrIf(name, value string) *Node {
	if value == "" {
		return n
	}
	return n.Attr(name, value)
}

// Text sets the node's leaf text and returns the receiver.
func (n *Node) SetText(s string) *Node {
	n.Text = s
	return n
}

// SetCDATA sets the node's leaf text as mixed content and returns the receiver.
func (n *Node) SetCDATA(s string) *Node {
	n.Text = s
	n.CDATA = true
	return n
}

// Child appends a child node and returns the receiver. A nil child is ignored,
// so callers can build conditionally without branching.
func (n *Node) Child(c *Node) *Node {
	if c != nil {
		n.Children = append(n.Children, c)
	}
	return n
}

// Leaf appends a text-only child element and returns the receiver.
func (n *Node) Leaf(name, text string) *Node {
	return n.Child(El(name).SetText(text))
}

// LeafIf appends a text-only child element only when text is non-empty.
func (n *Node) LeafIf(name, text string) *Node {
	if text == "" {
		return n
	}
	return n.Leaf(name, text)
}

// AttrValue returns the value of the named attribute.
func (n *Node) AttrValue(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// ChildNamed returns the first child with the given element name.
func (n *Node) ChildNamed(name string) (*Node, bool) {
	for _, c := range n.Children {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

// pathSegment matches one segment of a column path: a bare element name, an
// element name with an attribute suffix, or a leading attribute reference.
var pathSegment = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9._-]*)?(?:@[A-Za-z][A-Za-z0-9._-]*)?$`)

// ValidatePath reports whether p is a well-formed column path.
//
// A path is slash-separated. Every segment but the last must be a plain element
// name. The last segment may be an element name, an element name followed by
// "@attr", or a bare "@attr" referring to an attribute of the node itself.
func ValidatePath(p string) error {
	if p == "" {
		return errs.Usage("INVALID_COLUMN_PATH", "column path is empty")
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" || !pathSegment.MatchString(s) {
			return errs.Usage("INVALID_COLUMN_PATH", "malformed column path %q", p).
				WithDetail("segment %d: %q", i, s)
		}
		if i < len(segs)-1 && strings.Contains(s, "@") {
			return errs.Usage("INVALID_COLUMN_PATH",
				"only the final segment of a column path may reference an attribute").
				WithDetail("path %q, segment %d: %q", p, i, s)
		}
	}
	return nil
}

// Lookup resolves a column path against the node and returns the scalar it
// names. The second result is false when the path does not resolve, which the
// TSV writer renders as an empty cell.
func (n *Node) Lookup(path string) (string, bool) {
	cur := n
	segs := strings.Split(path, "/")
	for i, s := range segs {
		last := i == len(segs)-1
		name, attr, hasAttr := strings.Cut(s, "@")
		if name != "" {
			c, ok := cur.ChildNamed(name)
			if !ok {
				return "", false
			}
			cur = c
		}
		if !last {
			continue
		}
		if hasAttr {
			return cur.AttrValue(attr)
		}
		return cur.Text, true
	}
	return "", false
}

// validate checks the invariants every writer depends on: a non-empty element
// name, unique attribute names, and no collision between an attribute name and
// a child element name.
func (n *Node) validate(where string) error {
	if n.Name == "" {
		return errs.Runtime("INVALID_NODE", "node at %s has no element name", where)
	}
	here := where + "/" + n.Name

	seen := make(map[string]struct{}, len(n.Attrs)+len(n.Children))
	for _, a := range n.Attrs {
		if a.Name == "" {
			return errs.Runtime("INVALID_NODE", "node %s has an unnamed attribute", here)
		}
		if _, dup := seen[a.Name]; dup {
			return errs.Runtime("INVALID_NODE", "node %s repeats attribute %q", here, a.Name)
		}
		seen[a.Name] = struct{}{}
	}
	for _, c := range n.Children {
		if _, clash := seen[c.Name]; clash {
			// JSON and YAML flatten attributes and children into one object,
			// so a collision would silently drop one of them.
			return errs.Runtime("INVALID_NODE",
				"node %s uses %q as both an attribute and a child element", here, c.Name)
		}
	}
	if n.ListOf != "" {
		got := 0
		for _, c := range n.Children {
			if c.Name == n.ListOf {
				got++
			}
		}
		want, ok := n.AttrValue("count")
		if !ok {
			return errs.Runtime("INVALID_NODE", "list container %s has no count attribute", here)
		}
		if want != strconv.Itoa(got) {
			// A count that disagrees with the children is exactly the kind of
			// quiet lie this format exists to prevent.
			return errs.Runtime("INVALID_NODE",
				"list container %s claims count=%s but holds %d %q elements", here, want, got, n.ListOf)
		}
	}

	for _, c := range n.Children {
		if err := c.validate(here); err != nil {
			return err
		}
	}
	return nil
}
