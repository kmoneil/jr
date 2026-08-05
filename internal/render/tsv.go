package render

import (
	"fmt"
	"strings"
)

// tsvEscaper keeps every record on exactly one line and every field in exactly
// one column, so a consumer can split on \t and \n with no defensive code.
// A literal backslash is doubled first, by virtue of being the first pair.
var tsvEscaper = strings.NewReplacer(
	`\`, `\\`,
	"\t", `\t`,
	"\n", `\n`,
	"\r", `\r`,
)

func escapeTSV(s string) string { return tsvEscaper.Replace(s) }

// writeTSV emits a header row and nothing else: no envelope, no counts.
// Truncation is signalled on stderr as a structured warning plus exit 3, so a
// script that checks $? cannot miss it.
func writeTSV(w *writer, d *Doc) {
	if d.Record != nil {
		writeTSVRows(w, []string{"field", "value"}, nodeRows(d.Record))
		return
	}

	c := d.Collection
	headers := make([]string, 0, len(c.Columns))
	for _, col := range c.Columns {
		headers = append(headers, col.Header)
	}

	rows := make([][]string, 0, len(c.Items))
	for _, it := range c.Items {
		row := make([]string, 0, len(c.Columns))
		for _, col := range c.Columns {
			// A path that does not resolve yields an empty cell. Whether the
			// path is even resolvable against this kind is asserted by the
			// contract tests, not guessed at here.
			v, _ := it.Lookup(col.Path)
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	writeTSVRows(w, headers, rows)
}

func writeTSVRows(w *writer, headers []string, rows [][]string) {
	w.raw(joinTSV(headers))
	for _, r := range rows {
		w.raw(joinTSV(r))
	}
}

func joinTSV(fields []string) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(escapeTSV(f))
	}
	b.WriteByte('\n')
	return b.String()
}

// nodeRows flattens a node into field/value pairs in document order, naming
// each field with the same path syntax the Column type uses. Repeated sibling
// elements get an index suffix so no two rows share a field name.
func nodeRows(n *Node) [][]string {
	var rows [][]string
	walkRows(n, "", &rows)
	return rows
}

func walkRows(n *Node, prefix string, rows *[][]string) {
	for _, a := range n.Attrs {
		*rows = append(*rows, []string{prefix + "@" + a.Name, a.Value})
	}
	if n.Text != "" {
		name := strings.TrimSuffix(prefix, "/")
		if name == "" {
			name = "."
		}
		*rows = append(*rows, []string{name, n.Text})
	}

	counts := make(map[string]int, len(n.Children))
	for _, c := range n.Children {
		counts[c.Name]++
	}
	seen := make(map[string]int, len(counts))
	for _, c := range n.Children {
		name := c.Name
		if counts[name] > 1 {
			name = fmt.Sprintf("%s[%d]", name, seen[c.Name])
			seen[c.Name]++
		}
		walkRows(c, prefix+name+"/", rows)
	}
}
