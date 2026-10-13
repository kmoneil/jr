package render

import (
	"slices"
	"time"
)

// A timestamp is an instant, and the zone it is spelled in is presentation.
//
// Everything a resource hands this package is RFC 3339 in UTC, and checkValue
// holds it to that. What a caller reads may be spelled in another zone, with
// the offset in the value, so `2026-05-12T09:10:00Z` and
// `2026-05-12T11:10:00+02:00` are one answer written twice. The respelling
// happens here, at the last step before encoding, for three reasons.
//
// It is one place. A schema already says which values are timestamps, so no
// resource has to remember to convert and none can forget.
//
// It leaves the document alone. Cursors, preconditions, `--age` and the feed's
// window all compare the UTC text a resource built, and a plan written to a
// file is read back by `apply`. Each of those is given no zone and is
// byte-identical to what it was.
//
// And it cannot change an instant. The only operation is time.In, so a value
// that parses comes out naming the same moment, and one that does not parse
// never reached here: Validate refused the document first.

// HasTimestamp reports whether this shape declares a timestamp anywhere a
// document can hold one.
//
// It is how a caller knows, before a command runs, whether the zone will be
// needed at all. Resolving the account's zone costs a request and can fail,
// and a command that prints no timestamp should pay for neither.
//
// An open shape is not searched: see inZone.
func (s *Schema) HasTimestamp() bool {
	if s == nil {
		return false
	}
	if s.Text != nil && s.Text.Type == TypeTimestamp {
		return true
	}
	if slices.ContainsFunc(s.Attrs, func(f Field) bool { return f.Type == TypeTimestamp }) {
		return true
	}
	return slices.ContainsFunc(s.Children, func(c Child) bool {
		return c.Schema.HasTimestamp()
	})
}

// KindHasTimestamp reports whether a document of this kind can hold a
// timestamp. A kind with no registered schema holds none that anything here
// could find.
func KindHasTimestamp(kind string) bool {
	s, ok := SchemaFor(kind)
	return ok && s.HasTimestamp()
}

// inZone returns a copy of the node with every value this schema declares a
// timestamp spelled in loc. The node it was given is not changed.
//
// Only declared children are descended into. An element the schema admits
// through Extra or Recursive is returned as it is, and that is safe because no
// kind declares a timestamp inside an open shape:
// TestNoOpenShapeDeclaresATimestamp fails when one does, which is the moment
// to teach this function about it and not before.
func (s *Schema) inZone(n *Node, loc *time.Location) *Node {
	// A struct copy, so a field added to Node later is carried without this
	// function knowing its name. The two slices are the only shared state.
	out := *n
	out.Attrs = slices.Clone(n.Attrs)
	for i, a := range out.Attrs {
		if f, ok := s.attr(a.Name); ok && f.Type == TypeTimestamp {
			out.Attrs[i].Value = spellIn(a.Value, loc)
		}
	}
	if s.Text != nil && s.Text.Type == TypeTimestamp {
		out.Text = spellIn(n.Text, loc)
	}
	out.Children = slices.Clone(n.Children)
	for i, c := range out.Children {
		if child, ok := s.child(c.Name); ok {
			out.Children[i] = child.Schema.inZone(c, loc)
		}
	}
	return &out
}

// spellIn writes one timestamp in loc, with its offset.
//
// A value that is empty or does not parse is returned untouched. Empty is a
// declared timestamp nobody set, and checkValue lets it through for the same
// reason. Unparseable cannot happen to a validated document, and handing the
// text back is the answer that invents nothing if it ever does.
func spellIn(value string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	// Nano, which writes a fraction only where there is one, so a timestamp
	// carrying more than seconds keeps it. Nothing emits one today.
	return t.In(loc).Format(time.RFC3339Nano)
}

// inZone returns the document as it should be written: itself when no zone was
// asked for, and otherwise a copy whose timestamps are spelled in d.Zone.
//
// It runs after Validate, which is what holds every timestamp to UTC, so the
// check and the respelling cannot disagree about what a resource may build.
func (d *Doc) inZone() *Doc {
	s, ok := SchemaFor(d.Kind)
	if d.Zone == nil || !ok {
		return d
	}
	out := *d
	if d.Record != nil {
		out.Record = s.inZone(d.Record, d.Zone)
		return &out
	}
	c := *d.Collection
	c.Items = slices.Clone(d.Collection.Items)
	for i, item := range c.Items {
		c.Items[i] = s.inZone(item, d.Zone)
	}
	out.Collection = &c
	return &out
}
