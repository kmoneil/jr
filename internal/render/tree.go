package render

import "strconv"

// nodeValue converts a node into the generic tree the JSON and YAML writers
// encode.
//
// A leaf with no attributes is its own text. Otherwise attributes and child
// elements flatten into one object — Node.validate guarantees their names do
// not collide. A child name that repeats becomes an array, so a consumer can
// tell "one label" from "several" by the presence of the array, never by
// counting.
func nodeValue(n *Node) any {
	if len(n.Attrs) == 0 && len(n.Children) == 0 && n.ListOf == "" {
		return n.Text
	}

	out := make(map[string]any, len(n.Attrs)+len(n.Children)+1)
	for _, a := range n.Attrs {
		out[a.Name] = a.Value
	}
	if n.Text != "" {
		out["text"] = n.Text
	}

	if n.ListOf != "" {
		addListItems(out, n)
	}
	addChildren(out, n)
	return out
}

// addListItems writes a list container's array, which is always present, and
// its count, which is a number to match the result envelope. Everywhere else an
// attribute stays a string, because an XML attribute has no type to preserve.
func addListItems(out map[string]any, n *Node) {
	items := make([]any, 0, len(n.Children))
	for _, c := range n.Children {
		if c.Name == n.ListOf {
			items = append(items, nodeValue(c))
		}
	}
	out[n.ListOf] = items
	out["count"] = len(items)
}

// addChildren writes every child that is not part of the list array, turning a
// repeated name into an array.
func addChildren(out map[string]any, n *Node) {
	counts := make(map[string]int, len(n.Children))
	for _, c := range n.Children {
		if c.Name != n.ListOf {
			counts[c.Name]++
		}
	}
	for _, c := range n.Children {
		if c.Name == n.ListOf {
			continue
		}
		v := nodeValue(c)
		if counts[c.Name] == 1 {
			out[c.Name] = v
			continue
		}
		list, _ := out[c.Name].([]any)
		out[c.Name] = append(list, v)
	}
}

// docValue converts a document into the generic tree, hoisting the envelope to
// the top level so JSON and YAML consumers see idiomatic shapes rather than a
// transliterated XML tree.
func docValue(d *Doc) map[string]any {
	out := map[string]any{
		"kind": d.Kind,
		"v":    d.Version,
	}
	if d.Site != "" {
		out["site"] = d.Site
	}
	if d.Record != nil {
		out[d.Record.Name] = nodeValue(d.Record)
		return out
	}

	c := d.Collection
	items := make([]any, 0, len(c.Items))
	for _, it := range c.Items {
		items = append(items, nodeValue(it))
	}
	out["count"] = len(c.Items)
	out["complete"] = c.Complete
	out[c.Name] = items
	if c.NextPageToken != "" {
		out["next-page-token"] = c.NextPageToken
	}
	return out
}

// diagnosticValue converts an <error> or <warning> node into the generic tree.
//
// Three fields are promoted out of their string form: the schema version, the
// exit status, and the retryable flag. They are the ones a caller branches on,
// and a JSON consumer comparing "5" to 5, or treating the string "false" as
// truthy, is a bug this format should not be able to cause.
func diagnosticValue(n *Node) map[string]any {
	v, _ := nodeValue(n).(map[string]any)
	if v == nil {
		v = map[string]any{}
	}
	v["v"] = diagnosticVersion
	if s, ok := v["exit"].(string); ok {
		if code, err := strconv.Atoi(s); err == nil {
			v["exit"] = code
		}
	}
	if s, ok := v["retryable"].(string); ok {
		if b, err := strconv.ParseBool(s); err == nil {
			v["retryable"] = b
		}
	}
	return map[string]any{n.Name: v}
}
