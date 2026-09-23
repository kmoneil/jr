package jql

import (
	"strconv"

	"github.com/kmoneil/jr/internal/render"
)

// The jql.explain kind lives here rather than in the jql resource because
// every command that composes a query emits one under --explain, and
// resources may not import each other. A kind owned outside resource/* has
// precedent: internal/registry owns schema.commands and contract.
const (
	KindExplain = "jql.explain"
	// VersionExplain is 2: v2 added the optional `unresolved` list naming
	// the filters whose values are resolved just before sending.
	VersionExplain = 2
)

func init() {
	render.RegisterSchema(KindExplain, ExplainSchema())
}

// ExplainSchema is the shape of an explanation.
func ExplainSchema() *render.Schema {
	return &render.Schema{
		Element: "explanation",
		Attrs: []render.Field{
			{Name: "parenthesized", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("fragment", render.TypeString)},
			{Schema: render.Leaf("query", render.TypeString)},
			{Schema: render.Leaf("project", render.TypeString), Optional: true},
			{Schema: render.ListSchema("fields", "field",
				render.Leaf("field", render.TypeString))},
			{Schema: render.ListSchema("unresolved", "filter", &render.Schema{
				Element: "filter",
				Attrs: []render.Field{
					{Name: "flag", Type: render.TypeString},
				},
				Text: &render.Field{Type: render.TypeString},
			}), Optional: true},
		},
	}
}

// Explanation is what `jql explain` and the global --explain report.
type Explanation struct {
	// Fragment is the caller's raw --jql, as they wrote it, or empty for an
	// invocation that composed its whole query from flags.
	Fragment string
	// Query is what would be sent.
	Query string
	// Project is the scope the fragment was combined with, or empty.
	Project string
	// Fields are the fields the fragment references, tokenized.
	Fields []string
	// Parenthesized reports whether the fragment was wrapped. It always is.
	// That is stated rather than assumed, because the whole reason the command
	// exists is that the consequence of not wrapping is invisible.
	Parenthesized bool
	// Unresolved names the filters whose values the command resolves just
	// before sending, so Query carries what the caller typed where the sent
	// query will carry what it resolved to. Resolution is a request, and an
	// explanation makes none.
	Unresolved []UnresolvedFilter
}

// UnresolvedFilter is one value the query carries as typed.
type UnresolvedFilter struct {
	// Flag is the flag's declared name, without dashes.
	Flag string
	// Value is what the caller typed.
	Value string
}

// Node renders an explanation.
func (e Explanation) Node() *render.Node {
	n := render.El("explanation").
		Attr("parenthesized", strconv.FormatBool(e.Parenthesized)).
		Leaf("fragment", e.Fragment).
		Leaf("query", e.Query).
		LeafIf("project", e.Project)

	fields := make([]*render.Node, 0, len(e.Fields))
	for _, f := range e.Fields {
		fields = append(fields, render.El("field").SetText(f))
	}
	n = n.Child(render.ListEl("fields", "field", fields...))

	if len(e.Unresolved) > 0 {
		filters := make([]*render.Node, 0, len(e.Unresolved))
		for _, u := range e.Unresolved {
			filters = append(filters,
				render.El("filter").Attr("flag", u.Flag).SetText(u.Value))
		}
		n = n.Child(render.ListEl("unresolved", "filter", filters...))
	}
	return n
}

// Explain builds the query without sending it.
//
// It goes through the same builder and the same ordering policy the issue
// commands use, so what it reports is what would be sent rather than a second
// account of it.
func Explain(fragment, project, sort, order string) (Explanation, error) {
	// Fields tokenizes, so it runs first and owns the lexing refusal;
	// ValidateFragment then adds what tokens alone cannot show, the balance
	// and a smuggled ORDER BY. In the other order the Fields error would be
	// a branch nothing can take, because a fragment that fails to tokenize
	// would already have been refused.
	fields, err := Fields(fragment)
	if err != nil {
		return Explanation{}, err
	}
	if err := ValidateFragment(fragment); err != nil {
		return Explanation{}, err
	}

	b := New()
	if project != "" {
		b.Project(project)
	}
	b.Raw(fragment)
	if err := AppendOrder(b, sort, order); err != nil {
		return Explanation{}, err
	}

	query, err := b.Render()
	if err != nil {
		return Explanation{}, err
	}

	return Explanation{
		Fragment: fragment, Query: query, Project: project,
		Fields: fields, Parenthesized: true,
	}, nil
}
