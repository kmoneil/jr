// Package project is the project resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
package project

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds and schema versions this resource emits.
const (
	KindList       = "project.list"
	VersionList    = 2
	KindGet        = "project.get"
	VersionGet     = 2
	KindComponents = "project.components"
	VersionComp    = 1
	KindVersions   = "project.versions"
	VersionVers    = 1
	KindStatuses   = "project.statuses"
	VersionStat    = 2
)

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())
	registry.Register(componentsCommand())
	registry.Register(versionsCommand())
	registry.Register(statusesCommand())

	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, Schema())
	render.RegisterSchema(KindComponents, ComponentSchema())
	render.RegisterSchema(KindVersions, VersionSchema())
	render.RegisterSchema(KindStatuses, StatusesSchema())
}

// Schema is the shape of a project.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "project",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "id", Type: render.TypeString},
			// Empty on Data Center, which has no such distinction. It is
			// reported rather than normalized away, because what a project
			// permits differs between them.
			{Name: "style", Type: render.TypeString, Optional: true},
			// Absent when the server did not say, which on Data Center is every
			// project: it sends no isPrivate, with or without expand. The
			// attribute was unconditional and therefore asserted private="false"
			// of a project nobody had asked the server about.
			{Name: "private", Type: render.TypeBool, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("type", render.TypeString), Optional: true},
			{Schema: render.Leaf("lead", render.TypeString), Optional: true},
		},
	}
}

// ComponentSchema is the shape of one component of a project.
func ComponentSchema() *render.Schema {
	return &render.Schema{
		Element: "component",
		Attrs:   []render.Field{{Name: "id", Type: render.TypeString}},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			// Absent when the server sent no lead. Data Center never sends one
			// on a component, so this is empty there whatever the truth is.
			{Schema: render.Leaf("lead", render.TypeString), Optional: true},
			// How Jira picks an assignee for issues in this component. It is
			// reported because it is the reason an issue can acquire an
			// assignee nobody chose.
			{Schema: render.Leaf("assignee-type", render.TypeString), Optional: true},
			{Schema: render.Leaf("description", render.TypeString), Optional: true},
		},
	}
}

// VersionSchema is the shape of one release version.
func VersionSchema() *render.Schema {
	return &render.Schema{
		Element: "version",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			// Released and archived are separate because they are: a version
			// can be either, both, or neither.
			{Name: "released", Type: render.TypeBool},
			{Name: "archived", Type: render.TypeBool},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			// A date, not a timestamp: Jira stores it without a time, and
			// inventing midnight in some timezone would be a value nobody set.
			{Schema: render.Leaf("release-date", render.TypeDate), Optional: true},
			{Schema: render.Leaf("start-date", render.TypeDate), Optional: true},
			{Schema: render.Leaf("description", render.TypeString), Optional: true},
		},
	}
}

// StatusesSchema is the shape of one issue type and the statuses it can be in.
func StatusesSchema() *render.Schema {
	return &render.Schema{
		Element: "issue-type",
		Attrs: []render.Field{
			{Name: "type", Type: render.TypeString},
			// The status names flattened into one value, so TSV has something
			// to put in a cell. Redundant with the <statuses> list below, and
			// deliberately so: the list is the truth, this is the projection a
			// format without lists can carry.
			{Name: "status-names", Type: render.TypeString},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("statuses", "status", &render.Schema{
				Element: "status",
				Attrs: []render.Field{
					{Name: "id", Type: render.TypeString},
					// The category matters more than the name for anything
					// automated: a project can rename a status to anything,
					// and the category stays one of four values.
					{Name: "category", Type: render.TypeString, Enum: []string{
						site.CategoryToDo, site.CategoryInProgress,
						site.CategoryDone, site.CategoryUnknown,
					}},
				},
				Text: &render.Field{Type: render.TypeString},
			})},
		},
	}
}

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client queries projects.
type Client struct {
	Transport Doer
	Site      site.Info
}

// Project is one project, in the shape this tool reports.
type Project struct {
	ID   string
	Key  string
	Name string
	// Type is the project's template family, e.g. software or service_desk.
	Type string
	// Style is "classic" or "next-gen" on Cloud, and empty on Data Center,
	// which has no such distinction. It is reported rather than normalized
	// away, because what a project permits differs between them.
	Style  string
	Lead   string
	Simple bool
	// Private is nil when the server did not say. Data Center never does, on
	// any version probed and with or without expand, so a plain bool reported
	// every project there as public on the strength of a missing field.
	Private *bool
}

type rawProject struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	ProjectType string `json:"projectTypeKey"`
	Style       string `json:"style"`
	Simplified  bool   `json:"simplified"`
	// A pointer so an absent isPrivate stays absent rather than decoding to
	// false, which is a claim Data Center never makes.
	IsPrivate *bool `json:"isPrivate"`
	Lead      *struct {
		DisplayName string `json:"displayName"`
		Name        string `json:"name"`
	} `json:"lead"`
}

func (r rawProject) convert() Project {
	out := Project{
		ID: r.ID, Key: r.Key, Name: r.Name, Type: r.ProjectType,
		Style: r.Style, Simple: r.Simplified, Private: r.IsPrivate,
	}
	if r.Lead != nil {
		out.Lead = r.Lead.DisplayName
		if out.Lead == "" {
			out.Lead = r.Lead.Name
		}
	}
	return out
}

// Node renders one project.
func (p Project) Node() *render.Node {
	n := render.El("project").
		Attr("key", p.Key).
		Attr("id", p.ID).
		AttrIf("style", p.Style)
	// Omitted rather than defaulted: Data Center sends no isPrivate, and
	// private="false" there is an assertion about visibility built out of a
	// field the server never wrote.
	if p.Private != nil {
		n.Attr("private", strconv.FormatBool(*p.Private))
	}
	return n.
		Leaf("name", p.Name).
		LeafIf("type", p.Type).
		LeafIf("lead", p.Lead)
}

// ListColumns is the default TSV column set for `project list`.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "key", Path: "@key"},
		{Header: "name", Path: "name"},
		{Header: "type", Path: "type"},
		{Header: "lead", Path: "lead"},
	}
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"project", "list"},
		Summary: "List the projects this credential can see",
		Description: strings.TrimSpace(`
Returns every project visible to the credential, ordered by key.

The two deployments answer this differently — Cloud pages a search endpoint,
Data Center returns the lot from one request — and the split is behind the
client, so the result is the same shape either way.

Ordered by key rather than by whatever the server felt like, so two runs of a
script agree.`),
		Example: strings.Join([]string{
			buildinfo.App + " project list",
			buildinfo.App + " project list --format json --limit all",
		}, "\n"),
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "projects",
		Columns:        ListColumns(),
		Outputs:        []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Stream: runList,
	}
}

// List reads every visible project.
//
// Cloud replaced the plain listing with a paged search and left the old one
// returning nothing useful; Data Center never had the search. The deployments
// are genuinely different endpoints, not one with different parameters, which
// is why the split is here rather than in a query builder.
func (c *Client) List(ctx context.Context) ([]Project, error) {
	if c.Site.Kind == site.Cloud {
		return c.listCloud(ctx)
	}
	return c.listDataCenter(ctx)
}

func (c *Client) listCloud(ctx context.Context) ([]Project, error) {
	path := c.Site.APIBase() + "/project/search"

	var out []Project
	startAt := 0
	for {
		resp, err := c.Transport.Do(ctx, transport.Request{
			Method: transport.MethodGet,
			Path:   path,
			Query: url.Values{
				"startAt":    {strconv.Itoa(startAt)},
				"maxResults": {"50"},
				"orderBy":    {"key"},
				// The lead is omitted unless expanded, on both deployments, and
				// the default column set has a lead in it. Data Center reported
				// forty projects with no lead, which was forty projects whose
				// lead nobody had asked for.
				"expand": {"lead"},
			},
		})
		if err != nil {
			return nil, err
		}
		if err := transport.Err(resp); err != nil {
			return nil, err
		}

		var page struct {
			IsLast bool         `json:"isLast"`
			Total  int          `json:"total"`
			Values []rawProject `json:"values"`
		}
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, errs.Remote("MALFORMED_PROJECTS",
				"%s did not return usable projects", path).
				WithRequestID(resp.RequestID).Wrap(err)
		}

		for _, raw := range page.Values {
			out = append(out, raw.convert())
		}
		// A page that added nothing ends the loop whatever the server claims,
		// so a server that never sets isLast cannot spin here.
		if len(page.Values) == 0 || page.IsLast || len(out) >= page.Total {
			break
		}
		startAt += len(page.Values)
	}
	return sorted(out), nil
}

func (c *Client) listDataCenter(ctx context.Context) ([]Project, error) {
	path := c.Site.APIBase() + "/project"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
		// The lead is omitted unless expanded, and the default column set has a
		// lead in it. Forty projects reporting no lead is not forty projects
		// without one.
		Query: url.Values{"expand": {"lead"}},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []rawProject
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_PROJECTS",
			"%s did not return usable projects", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	out := make([]Project, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.convert())
	}
	return sorted(out), nil
}

// sorted orders by key, so two runs against one site produce the same rows in
// the same order. Neither endpoint promises one.
func sorted(projects []Project) []Project {
	sort.Slice(projects, func(i, j int) bool { return projects[i].Key < projects[j].Key })
	return projects
}

func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, err := clientFor(ctx, inv, "project list")
	if err != nil {
		return registry.StreamResult{}, err
	}

	projects, err := client.List(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}

	projects, complete := registry.Bound(inv.Limit, projects)
	for _, p := range projects {
		if err := out.Write(p.Node()); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), out.Count())
	return registry.StreamResult{Complete: complete}, nil
}

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"project", "get"},
		Summary: "Fetch one project",
		Description: strings.TrimSpace(`
Returns a single project by key or id.

The key defaults to the context's project, so a configured caller can ask
without repeating themselves.`),
		Example: strings.Join([]string{
			buildinfo.App + " project get ENG",
			buildinfo.App + " project get --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "project key; defaults to the context's project",
		}},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Usage, exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateProject,
		Run:      runGet,
	}
}

// Get reads one project.
func (c *Client) Get(ctx context.Context, key string) (Project, error) {
	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.Site.APIBase() + "/project/" + url.PathEscape(key),
	})
	if err != nil {
		return Project{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Project{}, err
	}

	var raw rawProject
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.Key == "" {
		return Project{}, errs.Remote("MALFORMED_PROJECT",
			"the response for %s is not a usable project", key).
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return raw.convert(), nil
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "project get")
	if err != nil {
		return nil, err
	}
	key, err := projectArg(inv)
	if err != nil {
		return nil, err
	}

	got, err := client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return render.Record(KindGet, VersionGet, got.Node()), nil
}

// projectArg resolves the project to act on: the argument, or the context's.
//
// It is a default rather than a requirement everywhere else in this tool, and
// these commands are the few that genuinely cannot proceed without one.
func projectArg(inv *registry.Invocation) (string, error) {
	if len(inv.Args) > 0 && inv.Args[0] != "" {
		return inv.Args[0], nil
	}
	if inv.Jira == nil {
		return "", errs.Usage("NO_PROJECT", "a project key is required")
	}
	return inv.Jira.RequireProject()
}

// validateProject refuses a key that cannot name a project, before a session
// exists.
//
// All four commands here take one and none of them checked it, so
// `project get ../etc` spent the deployment probe and then a request to be told
// what the string itself already said. url.PathEscape made that a wasted round
// trip rather than an unsafe one — the invariant about a parser's output being
// safe in a path did not bind, because there was no parser.
//
// In Validate rather than beside the request for the reason every check here
// is: three of the four stream, so their column headers are on stdout before
// the body runs, and a refusal raised from inside would arrive after them.
//
// It resolves through projectArg, so a key from the context is held to the same
// grammar as one typed on the command line. That costs nothing — RequireProject
// reads the resolved context and asks no server — and the alternative is a
// check that covers the value a caller can see and misses the one they cannot.
func validateProject(_ context.Context, inv *registry.Invocation) error {
	key, err := projectArg(inv)
	if err != nil {
		return err
	}
	if site.ValidProjectKey(key) {
		return nil
	}
	return errs.Usage("INVALID_PROJECT_KEY", "%q is not a project key", key).
		WithDetail("a project key starts with a letter and continues with " +
			"letters, digits, or underscores").
		WithRemedy("see `" + buildinfo.App + " project list` for the keys this " +
			"credential can reach")
}

// clientFor is the opening every command here shares.
func clientFor(
	ctx context.Context, inv *registry.Invocation, command string,
) (*Client, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "%s has no connection to Jira", command)
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{Transport: conn, Site: info}, nil
}

// ListDoc renders projects as a document.
func ListDoc(projects []Project, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(projects))
	for _, p := range projects {
		items = append(items, p.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name: "projects", Items: items, Complete: complete, Columns: ListColumns(),
	})
}
