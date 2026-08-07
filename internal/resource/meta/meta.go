// Package meta answers "what can I actually do to this issue".
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
//
// The fetching and the name resolution live in internal/site rather than here,
// because `issue move` has to turn a transition name into an id and a resource
// may not import another resource. This package is the command surface.
package meta

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
)

// Kinds and schema versions this resource emits. Bump a version in the same
// commit that changes the corresponding golden file.
const (
	KindTransitions    = "meta.transitions"
	VersionTransitions = 1
	KindCreateMeta     = "meta.createmeta"
	VersionCreateMeta  = 1
)

func init() {
	registry.Register(transitionsCommand())
	registry.Register(createMetaCommand())

	render.RegisterSchema(KindTransitions, TransitionSchema())
	render.RegisterSchema(KindCreateMeta, MetaFieldSchema())
}

// TransitionSchema is the shape of one transition an issue can make right now.
func TransitionSchema() *render.Schema {
	return &render.Schema{
		Element: "transition",
		Attrs: []render.Field{
			// The id is what gets sent back, which is why it is the identity.
			{Name: "id", Type: render.TypeString},
			{Name: "has-screen", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: &render.Schema{
				// The category is on the destination rather than alongside it,
				// because it describes that status.
				Element: "to",
				Attrs: []render.Field{
					{Name: "id", Type: render.TypeString},
					{Name: "category", Type: render.TypeString, Enum: []string{
						site.CategoryToDo, site.CategoryInProgress,
						site.CategoryDone, site.CategoryUnknown,
					}},
				},
				Text: &render.Field{Type: render.TypeString},
			}},
			{Schema: render.ListSchema("fields", "field", MetaFieldSchema())},
		},
	}
}

// MetaFieldSchema is the shape of one field on a create or transition screen.
func MetaFieldSchema() *render.Schema {
	return &render.Schema{
		Element: "field",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			{Name: "required", Type: render.TypeBool},
			// Whether the screen supplies a value when none is given, which is
			// what makes a required field safe to omit.
			{Name: "has-default", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("type", render.TypeString), Optional: true},
			{Schema: render.Leaf("items", render.TypeString), Optional: true},
			{Schema: render.ListSchema("allowed-values", "allowed-value",
				render.Leaf("allowed-value", render.TypeString))},
		},
	}
}

func transitionsCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"meta", "transitions"},
		Summary: "List the transitions available on an issue right now",
		Description: strings.TrimSpace(`
Returns what this issue can do next, given its current status and this
credential's permissions: the transition id to send, the name a person uses,
and the status it lands in.

This is deliberately not cached. Transitions depend on where the issue is now,
so a stored copy answers the question as it stood when it was stored — and an
agent acting on a stale list sends an id the workflow no longer offers.

A transition missing from this list is usually blocked from the current status
rather than misspelled, which is why a name that does not resolve is refused
with the whole available set rather than with near matches.`),
		Example: strings.Join([]string{
			buildinfo.App + " meta transitions ENG-101",
			buildinfo.App + " meta transitions ENG-101 --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "transitions",
		Validate:       validateKey,
		Columns:        TransitionColumns(),
		Outputs: []registry.Output{
			{Kind: KindTransitions, Version: VersionTransitions},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Stream: runTransitions,
	}
}

// TransitionColumns is the default TSV column set for `meta transitions`.
//
// Adding a column here is a major version bump: agents diff output, and a new
// column shifts every field after it.
func TransitionColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "name", Path: "name"},
		{Header: "to", Path: "to"},
		{Header: "category", Path: "to@category"},
	}
}

// issueKey is the shape a key has. It is checked locally rather than sent,
// because a 404 for a malformed key reads like a missing issue rather than a
// typo — and because the value reaches a URL path.
//
// It is a local check rather than issue.ParseKey because a resource may not
// import another resource. Three lines of duplication is the cheaper side of
// that trade; ordering keys, which is where the subtle bug lives, still belongs
// to the issue package alone.
var issueKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// validateKey refuses a malformed key before the stream opens and its header is
// written.
func validateKey(_ context.Context, inv *registry.Invocation) error {
	if len(inv.Args) == 0 || !issueKey.MatchString(inv.Args[0]) {
		key := ""
		if len(inv.Args) > 0 {
			key = inv.Args[0]
		}
		return errs.Usage("INVALID_KEY", "%q is not an issue key", key).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}
	return nil
}

func runTransitions(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "meta transitions has no connection to Jira")
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	transitions, err := meta.Transitions(ctx, inv.Args[0])
	if err != nil {
		return registry.StreamResult{}, err
	}

	items := transitions.Items
	// A workflow rarely offers more moves than a limit allows, but a
	// bounded result is still reported as bounded. Reporting a cut list as
	// complete is how somebody concludes a transition does not exist.
	items, complete := registry.Bound(inv.Limit, items)

	nodes := make([]*render.Node, 0, len(items))
	for _, t := range items {
		nodes = append(nodes, TransitionNode(t))
	}
	if err := out.Write(nodes...); err != nil {
		return registry.StreamResult{}, err
	}
	inv.Progress.Update(out.Count(), len(transitions.Items))

	// The endpoint has no cursor, so there is nothing to resume from. A token
	// that meant nothing would be worse than none.
	return registry.StreamResult{Complete: complete}, nil
}

// TransitionNode renders one transition.
//
// The id is an attribute because it is what gets sent back; the name and the
// destination are elements because they are data. The status category is on the
// destination rather than alongside it, because it describes that status.
func TransitionNode(t site.Transition) *render.Node {
	n := render.El("transition").
		Attr("id", t.ID).
		Attr("has-screen", strconv.FormatBool(t.HasScreen))

	n.Leaf("name", t.Name)
	n.Child(render.El("to").
		Attr("id", t.To.ID).
		Attr("category", t.To.Category).
		SetText(t.To.Name))
	n.Child(fieldsNode(t.Fields))
	return n
}

// TransitionsDoc renders transitions as a document.
func TransitionsDoc(t *site.Transitions, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(t.Items))
	for _, item := range t.Items {
		items = append(items, TransitionNode(item))
	}
	return render.List(KindTransitions, VersionTransitions, &render.Collection{
		Name:     "transitions",
		Items:    items,
		Complete: complete,
		Columns:  TransitionColumns(),
	})
}

func createMetaCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"meta", "createmeta"},
		Summary: "List the fields a new issue of one type requires",
		Description: strings.TrimSpace(`
Returns every field a create screen offers for one project and issue type, with
the required ones first and the values Jira will accept where it constrains
them.

--project defaults to the context's project. --type is required, and is
resolved by name or by id: an unknown one is refused with the types the project
does offer, and an ambiguous one with the candidates.

The two deployments answer this very differently — Data Center serves it whole,
Cloud pages it and wants an issue type id rather than a name — so a Cloud run
costs one more request than a Data Center one. The result is the same shape
either way, and it is cached for a day because it changes when an administrator
edits a screen, not when an issue moves.`),
		Example: strings.Join([]string{
			buildinfo.App + " meta createmeta --type Bug",
			buildinfo.App + " meta createmeta --project ENG --type Story --format json",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "project", Type: registry.TypeString,
				Usage: "project key; defaults to the context's project",
			},
			{
				Name: "type", Short: "t", Type: registry.TypeString, Required: true,
				Usage: "issue type name or id, e.g. Bug",
			},
		},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "fields",
		Columns:        CreateMetaColumns(),
		Outputs: []registry.Output{
			{Kind: KindCreateMeta, Version: VersionCreateMeta},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Stream: runCreateMeta,
	}
}

// CreateMetaColumns is the default TSV column set for `meta createmeta`.
func CreateMetaColumns() []render.Column {
	return []render.Column{
		{Header: "id", Path: "@id"},
		{Header: "name", Path: "name"},
		{Header: "required", Path: "@required"},
		{Header: "type", Path: "type"},
	}
}

func runCreateMeta(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "meta createmeta has no connection to Jira")
	}

	project := inv.Flags.String("project")
	if project == "" {
		// The context's project is a default, and this is one of the few
		// commands that genuinely cannot proceed without one.
		resolved, err := inv.Jira.RequireProject()
		if err != nil {
			return registry.StreamResult{}, err
		}
		project = resolved
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	created, err := meta.CreateMeta(ctx, project, inv.Flags.String("type"))
	if err != nil {
		return registry.StreamResult{}, err
	}

	fields := created.Fields
	fields, complete := registry.Bound(inv.Limit, fields)

	nodes := make([]*render.Node, 0, len(fields))
	for _, f := range fields {
		nodes = append(nodes, MetaFieldNode(f))
	}
	if err := out.Write(nodes...); err != nil {
		return registry.StreamResult{}, err
	}
	inv.Progress.Update(out.Count(), len(created.Fields))

	return registry.StreamResult{Complete: complete}, nil
}

// MetaFieldNode renders one field of a create screen or a transition screen.
func MetaFieldNode(f site.MetaField) *render.Node {
	n := render.El("field").
		Attr("id", f.ID).
		Attr("required", strconv.FormatBool(f.Required)).
		Attr("has-default", strconv.FormatBool(f.HasDefault))

	n.Leaf("name", f.Name)
	n.LeafIf("type", f.Type)
	n.LeafIf("items", f.Items)

	values := make([]*render.Node, 0, len(f.AllowedValues))
	for _, v := range f.AllowedValues {
		values = append(values, render.El("allowed-value").SetText(v))
	}
	n.Child(render.ListEl("allowed-values", "allowed-value", values...))
	return n
}

// fieldsNode renders the fields a transition accepts.
func fieldsNode(fields []site.MetaField) *render.Node {
	items := make([]*render.Node, 0, len(fields))
	for _, f := range fields {
		items = append(items, MetaFieldNode(f))
	}
	return render.ListEl("fields", "field", items...)
}

// CreateMetaDoc renders create metadata as a document.
func CreateMetaDoc(m *site.CreateMeta, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(m.Fields))
	for _, f := range m.Fields {
		items = append(items, MetaFieldNode(f))
	}
	return render.List(KindCreateMeta, VersionCreateMeta, &render.Collection{
		Name:     "fields",
		Items:    items,
		Complete: complete,
		Columns:  CreateMetaColumns(),
	})
}
