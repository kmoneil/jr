// Package field is the field resource: what a site calls its fields, and what
// their ids are.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
//
// The catalogue itself lives in internal/site rather than here, because it is
// site metadata that other resources need: `issue list --field "Story Points"`
// has to resolve a name, and a resource may not import another resource. This
// package is the command surface over it.
package field

import (
	"context"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// Kinds and schema versions this resource emits. Bump a version in the same
// commit that changes the corresponding golden file.
const (
	KindList = "field.list"
	// v2 adds the optional custom-type leaf: Jira's own key for a custom
	// field's type, which the catalogue has always carried and this tool
	// parsed and threw away. It is what tells two "any" fields apart, and
	// without it the type column is the whole answer for five of the thirteen
	// custom fields a stock Data Center has.
	//
	// An element and not a column, so the default TSV column set does not move
	// and a script splitting on tabs keeps the four cells it had.
	VersionList = 2
)

func init() {
	registry.Register(listCommand())

	render.RegisterSchema(KindList, Schema())
}

// Schema is the shape of one field in the catalogue.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "field",
		Attrs: []render.Field{
			// The identity — the thing a caller passes back to --field.
			{Name: "id", Type: render.TypeString},
			{Name: "custom", Type: render.TypeBool},
			{Name: "searchable", Type: render.TypeBool},
			{Name: "orderable", Type: render.TypeBool},
			{Name: "navigable", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("type", render.TypeString), Optional: true},
			{Schema: render.Leaf("items", render.TypeString), Optional: true},
			// Jira's own key for the field's type. Present only on a custom
			// field, and the only thing that distinguishes two fields whose
			// schema type is both "any".
			{Schema: render.Leaf("custom-type", render.TypeString), Optional: true},
			// What a query may call this field. A custom field has at least
			// one, and often more than one.
			{Schema: render.ListSchema("clause-names", "clause-name",
				render.Leaf("clause-name", render.TypeString))},
		},
	}
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"field", "list"},
		Summary: "List every field this site has",
		Description: strings.TrimSpace(`
Returns the site's whole field catalogue: the id a request has to use, the name
a person calls it, and its type.

This is what makes --field "Story Points" work. The catalogue is cached under
$XDG_CACHE_HOME for a day, so resolving a name costs a round trip once rather
than on every invocation; --refresh fetches it again, and running this command
is what warms the cache.

Jira serves the catalogue whole rather than a page at a time, so --limit here
bounds what is printed rather than what is fetched. A bounded result is still
reported as incomplete and still exits 3, because a caller that asked for
everything and got some of it has to be told either way.`),
		Example: strings.Join([]string{
			buildinfo.App + " field list",
			buildinfo.App + " field list --format json",
			buildinfo.App + " field list --refresh",
		}, "\n"),
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "fields",
		Columns:        ListColumns(),
		Outputs:        []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Stream: runList,
	}
}

// ListColumns is the default TSV column set for `field list`.
//
// Adding a column here is a major version bump: agents diff output, and a new
// column shifts every field after it.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "name", Path: "name"},
		{Header: "type", Path: "type"},
		{Header: "custom", Path: "@custom"},
	}
}

// runList streams the catalogue.
//
// It arrives in one response — there is no cursor on this endpoint — so the
// rows go out in one write. The command still streams rather than returning a
// document, because that is what keeps a TSV run byte-identical to a buffered
// one and keeps the command from branching on format.
func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "field list has no connection to Jira")
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}

	fields := catalogue.Fields
	// A bound the caller set is honored exactly, and the result says it was
	// cut. Reporting a truncated catalogue as complete is how somebody
	// concludes their field does not exist.
	fields, complete := registry.Bound(inv.Limit, fields)

	nodes := make([]*render.Node, 0, len(fields))
	for _, f := range fields {
		nodes = append(nodes, Node(f))
	}
	if err := out.Write(nodes...); err != nil {
		return registry.StreamResult{}, err
	}
	inv.Progress.Update(out.Count(), len(catalogue.Fields))

	// There is no cursor to resume from: the next run fetches the same whole
	// catalogue. Handing back a token that meant nothing would be worse than
	// handing back none.
	return registry.StreamResult{Complete: complete}, nil
}

// Node renders one field.
//
// The id is an attribute because it is the identity — the thing a caller passes
// back to --field — and the name is an element because it is data. A consumer
// addresses them as @id and name in every format.
func Node(f site.Field) *render.Node {
	n := render.El("field").
		Attr("id", f.ID).
		Attr("custom", strconv.FormatBool(f.Custom))

	n.Leaf("name", f.Name)
	n.LeafIf("type", f.Type)
	n.LeafIf("items", f.Items)
	n.LeafIf("custom-type", f.CustomType)

	clauses := make([]*render.Node, 0, len(f.ClauseNames))
	for _, c := range f.ClauseNames {
		clauses = append(clauses, render.El("clause-name").SetText(c))
	}
	n.Child(render.ListEl("clause-names", "clause-name", clauses...))

	n.Attr("searchable", strconv.FormatBool(f.Searchable))
	n.Attr("orderable", strconv.FormatBool(f.Orderable))
	n.Attr("navigable", strconv.FormatBool(f.Navigable))
	return n
}

// ListDoc renders a catalogue as a document, for a caller that has one already
// rather than a stream to write into.
func ListDoc(fields []site.Field, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(fields))
	for _, f := range fields {
		items = append(items, Node(f))
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name:     "fields",
		Items:    items,
		Complete: complete,
		Columns:  ListColumns(),
	})
}
