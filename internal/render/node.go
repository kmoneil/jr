package render

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kmoneil/jr/internal/errs"
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
		if values, isList := listValues(cur); isList {
			return JoinList(values), true
		}
		return cur.Text, true
	}
	return "", false
}

// listValues flattens a list container into the values a single cell carries.
//
// The output contract has always said "a column over a list flattens: values
// are joined with `,`", and until `--field labels` needed one, no column in the
// tree had ever addressed a list — so the rule was documented, JoinList existed
// for it, and the only caller was a resource pre-joining an attribute by hand.
// A path naming a list container resolved to the container's own text, which is
// empty, and the cell came out blank.
//
// A node is a list when ListEl built it, which is what ListOf records — not
// when its children happen to look like one. The first version of this asked
// whether every child was a bare text leaf, and an `<issue>` carrying a single
// `<summary>` satisfied that, so a column path naming a whole record resolved
// to the summary. Asking the constructor is exact; inferring from the contents
// is a guess that is right until a record has one child.
func listValues(n *Node) ([]string, bool) {
	if n.ListOf == "" {
		return nil, false
	}
	out := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		out = append(out, c.Text)
	}
	return out, true
}

// validate checks the invariants every writer depends on: a non-empty element
// name, unique attribute names, no collision between an attribute name and a
// child element name, and values every format can actually carry.
func (n *Node) validate(where string) error {
	if n.Name == "" {
		return errs.Runtime("INVALID_NODE", "node at %s has no element name", where)
	}
	here := where + "/" + n.Name

	// Checked here because here is the one place both paths meet: Write
	// validates a buffered document, Stream.Write validates each row before it
	// reaches stdout, and every writer is downstream of both. Four writers each
	// deciding what they can carry is what produced two that could and two that
	// could not.
	if err := renderable(n.Text, here, "text"); err != nil {
		return err
	}

	if err := n.validateNames(here); err != nil {
		return err
	}
	if err := n.validateCount(here); err != nil {
		return err
	}

	for _, c := range n.Children {
		if err := c.validate(here); err != nil {
			return err
		}
	}
	return nil
}

// validateNames checks that attributes are named, unique, carriable, and do not
// collide with a child element.
func (n *Node) validateNames(here string) error {
	seen := make(map[string]struct{}, len(n.Attrs)+len(n.Children))
	for _, a := range n.Attrs {
		if a.Name == "" {
			return errs.Runtime("INVALID_NODE", "node %s has an unnamed attribute", here)
		}
		if _, dup := seen[a.Name]; dup {
			return errs.Runtime("INVALID_NODE", "node %s repeats attribute %q", here, a.Name)
		}
		if err := renderable(a.Value, here, "attribute "+strconv.Quote(a.Name)); err != nil {
			return err
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
	return nil
}

// validateCount holds a list container's count attribute to the children it
// actually has. A count that disagrees is exactly the kind of quiet lie this
// format exists to prevent.
func (n *Node) validateCount(here string) error {
	if n.ListOf == "" {
		return nil
	}
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
		return errs.Runtime("INVALID_NODE",
			"list container %s claims count=%s but holds %d %q elements", here, want, got, n.ListOf)
	}
	return nil
}

// renderable refuses a value no output format can carry.
//
// The narrow definition is XML's, because XML is the strictest of the four and
// a value has to mean the same thing in all of them: --format chooses an
// encoding, not what this tool is willing to say. JSON and YAML can encode a
// control character and were doing so happily while XML emitted the raw byte
// and TSV passed it through, so the same issue rendered four ways and parsed
// two — which is the output contract splitting along an axis nobody declared.
//
// Refused rather than escaped, because escaping is not available: XML 1.0
// forbids most of C0 outright, and `&#1;` is no more legal than the byte. And
// refused rather than dropped, because dropping is this tool quietly changing
// what Jira holds — the rule the five input-side checks already follow.
func renderable(s, where, what string) error {
	for i, r := range s {
		// A range over a string yields U+FFFD for an invalid byte, and U+FFFD
		// is itself a legal character — so the two are told apart by how much
		// was consumed rather than by the rune alone.
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				return errs.Runtime("UNRENDERABLE_VALUE",
					"the %s of %s is not valid UTF-8", what, where).
					WithDetail("at byte %d", i).
					WithRemedy("this is what Jira returned; the field has to be " +
						"corrected there")
			}
			continue
		}
		if !xmlChar(r) {
			return errs.Runtime("UNRENDERABLE_VALUE",
				"the %s of %s holds a character no output format can carry",
				what, where).
				WithDetail("U+%04X at byte %d", r, i).
				WithRemedy("this is what Jira returned; the field has to be " +
					"corrected there")
		}
	}
	return nil
}

// xmlChar reports whether r is in XML 1.0's Char production.
//
// It is narrower than "printable" in one direction and wider in another: tab,
// newline, and carriage return are in it while the rest of C0 is not, and DEL
// and the C1 block are perfectly legal despite looking like control characters.
// Written as the production rather than as a list of things seen in the wild,
// so it does not drift toward whatever a fixture happened to contain.
func xmlChar(r rune) bool {
	switch {
	case r == '\t' || r == '\n' || r == '\r':
		return true
	case r < 0x20:
		return false
	case r <= 0xD7FF:
		return true
	case r < 0xE000:
		// Surrogate halves. Unreachable from a valid UTF-8 string, and listed
		// so the production is complete rather than complete-by-accident.
		return false
	case r <= 0xFFFD:
		return true
	case r < 0x10000:
		// U+FFFE and U+FFFF are not characters.
		return false
	default:
		return r <= 0x10FFFF
	}
}
