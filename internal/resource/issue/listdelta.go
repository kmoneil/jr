package issue

import "strings"

// Two fields reach the changelog as a whole list on each side, where every
// other multi-valued field arrives one element per item. Measured on Cloud and
// on Data Center 10.4.0 on 2026-09-30, one field per save, adding a value,
// adding a second, and removing the first:
//
//	Component    to "10000" "probe, comma"; then from "10000" "probe, comma"
//	labels       "probe-a" -> "probe-a probe-b" -> "probe-b", ids null
//	Sprint       "1" "S 1" -> "1, 2" "S 1, S 2" -> "1, 3" "S 1, S 3"
//
// Component and Fix Version are already the delta, one item per element with
// its own id, so a comma in a name is never split because nothing is. Watchers
// are not in the changelog at all. So labels and Sprint are the two this
// splits, into the shape the others arrive in: one change per element, `from`
// alone for a removal and `to` alone for an addition. A row then says what
// moved instead of repeating both lists (issue 120), and every multi-valued
// field reads one way.
//
// A split that cannot be exact is not made. The row keeps both lists whole,
// which is what it said before, rather than a delta that might be wrong.

// listChange is one element added or removed.
type listChange struct {
	element
	removed bool
}

// element is one member of a list-valued side. id is empty where the field
// has no ids, as labels do.
type element struct{ name, id string }

// key is what two elements are compared by: the id where there is one, so a
// sprint renamed between two saves is not a removal and an addition.
func (e element) key() string {
	if e.id != "" {
		return e.id
	}
	return e.name
}

// applyTo fills one side of c, leaving the other unsent, as Jira sends an
// addition or a removal of a component.
func (d listChange) applyTo(c Change) Change {
	if d.removed {
		c.From, c.FromID, c.HasFrom = d.name, d.id, true
		return c
	}
	c.To, c.ToID, c.HasTo = d.name, d.id, true
	return c
}

// splitList reports the elements an item removed and added, removals first,
// or false when the item is not a list-valued field or cannot be split exactly.
//
// A save that a split sees no difference in is also false. Jira recorded that
// something changed, and zero rows would drop that from the feed.
func splitList(item rawItem) ([]listChange, bool) {
	var from, to []element
	switch {
	case item.Field == "labels" && item.FieldType == "jira":
		// A label cannot hold whitespace, so the split cannot be ambiguous.
		from, to = labelsOf(item.FromString), labelsOf(item.ToString)
	case item.Field == "Sprint" && item.FieldType == "custom":
		var okFrom, okTo bool
		from, okFrom = sprintsOf(item.From, item.FromString)
		to, okTo = sprintsOf(item.To, item.ToString)
		if !okFrom || !okTo {
			return nil, false
		}
	default:
		return nil, false
	}

	var out []listChange
	for _, e := range difference(from, to) {
		out = append(out, listChange{element: e, removed: true})
	}
	for _, e := range difference(to, from) {
		out = append(out, listChange{element: e})
	}
	return out, len(out) > 0
}

// labelsOf splits a labels side, which Jira joins with a single space.
func labelsOf(text *string) []element {
	if text == nil {
		return nil
	}
	var out []element
	for label := range strings.FieldsSeq(*text) {
		out = append(out, element{name: label})
	}
	return out
}

// sprintSeparator joins both the ids and the names of a Sprint side.
const sprintSeparator = ", "

// sprintsOf pairs a Sprint side's ids with its names, or reports false.
//
// The ids are numeric, so their split is exact. The names are not: a sprint
// name can hold the separator. So names pair with ids by position only when
// the two splits agree on the count, and a disagreement is a name that held
// it, which leaves the row whole. A side with names and no ids is not the
// agile field, whatever it is called.
func sprintsOf(ids, names *string) ([]element, bool) {
	idText, nameText := deref(ids), deref(names)
	if idText == "" && nameText == "" {
		return nil, true
	}
	idParts := strings.Split(idText, sprintSeparator)
	nameParts := strings.Split(nameText, sprintSeparator)
	if len(idParts) != len(nameParts) {
		return nil, false
	}
	out := make([]element, 0, len(idParts))
	for i, id := range idParts {
		if !digits(id) {
			return nil, false
		}
		out = append(out, element{name: nameParts[i], id: id})
	}
	return out, true
}

// difference is what a holds and b does not, in a's order, counting
// duplicates, so an element present twice and removed once leaves one.
func difference(a, b []element) []element {
	left := map[string]int{}
	for _, e := range b {
		left[e.key()]++
	}
	var out []element
	for _, e := range a {
		if left[e.key()] > 0 {
			left[e.key()]--
			continue
		}
		out = append(out, e)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
