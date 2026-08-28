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

// Component is one component of a project.
type Component struct {
	ID          string
	Name        string
	Description string
	// Lead is empty when the server sent no lead, which on Data Center is
	// every component: it sends assignee and realAssignee instead, and those
	// are who issues land on rather than who owns the component. Neither is
	// substituted for the other, so the column stays empty there.
	Lead string
	// AssigneeType is how Jira picks an assignee for issues in this component,
	// e.g. PROJECT_LEAD or COMPONENT_LEAD. It is reported because it is the
	// reason an issue can acquire an assignee nobody chose.
	AssigneeType string
}

// Version is one release version of a project.
type Version struct {
	ID          string
	Name        string
	Description string
	Released    bool
	Archived    bool
	// ReleaseDate is a date, not a timestamp: Jira stores it without a time,
	// and inventing midnight in some timezone would be a value nobody set.
	ReleaseDate string
	StartDate   string
}

// StatusesForType is the set of statuses one issue type can be in.
type StatusesForType struct {
	Type     string
	Statuses []NamedStatus
}

// NamedStatus is a status plus the category it belongs to. The category matters
// more than the name for anything automated: a project can rename a status to
// anything, but the category stays one of three values.
type NamedStatus struct {
	ID       string
	Name     string
	Category string
}

func componentsCommand() *registry.Command {
	return &registry.Command{
		Path:     []string{"project", "components"},
		ScopedBy: []string{registry.GlobalProject},
		Summary:  "List a project's components",
		Description: strings.TrimSpace(`
Returns the components of a project, ordered by name.

Each carries how Jira assigns issues filed against it. That is worth knowing
before filing one: a component can hand an issue to somebody nobody chose.`),
		Example: buildinfo.App + " project components ENG",
		Args: []registry.Arg{{
			Name: "key", Usage: "project key; defaults to the context's project",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "components",
		Columns: []render.Column{
			{Header: "id", Path: "@id"},
			{Header: "name", Path: "name"},
			{Header: "lead", Path: "lead"},
			{Header: "assignee-type", Path: "assignee-type"},
		},
		Outputs: []registry.Output{{Kind: KindComponents, Version: VersionComp}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateProject,
		Stream:   runComponents,
	}
}

// Components reads a project's components.
func (c *Client) Components(ctx context.Context, key string) ([]Component, error) {
	path := c.Site.APIBase() + "/project/" + url.PathEscape(key) + "/components"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		AssigneeType string `json:"assigneeType"`
		Lead         *struct {
			DisplayName string `json:"displayName"`
			Name        string `json:"name"`
		} `json:"lead"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_COMPONENTS",
			"%s did not return usable components", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	out := make([]Component, 0, len(raw))
	for _, r := range raw {
		component := Component{
			ID: r.ID, Name: r.Name, Description: r.Description,
			AssigneeType: r.AssigneeType,
		}
		if r.Lead != nil {
			component.Lead = r.Lead.DisplayName
			if component.Lead == "" {
				component.Lead = r.Lead.Name
			}
		}
		out = append(out, component)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func runComponents(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, key, err := clientAndProject(ctx, inv, "project components")
	if err != nil {
		return registry.StreamResult{}, err
	}

	components, err := client.Components(ctx, key)
	if err != nil {
		return registry.StreamResult{}, err
	}

	found := len(components)
	components, complete := registry.Bound(inv.Limit, components)
	for _, c := range components {
		node := render.El("component").
			Attr("id", c.ID).
			Leaf("name", c.Name).
			LeafIf("lead", c.Lead).
			LeafIf("assignee-type", c.AssigneeType)
		node.LeafIf("description", c.Description)
		if err := out.Write(node); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), found)
	return registry.StreamResult{Complete: complete}, nil
}

func versionsCommand() *registry.Command {
	return &registry.Command{
		Path:     []string{"project", "versions"},
		ScopedBy: []string{registry.GlobalProject},
		Summary:  "List a project's versions",
		Description: strings.TrimSpace(`
Returns the release versions of a project, newest first.

Released and archived are reported separately, because they are separate: an
archived version is hidden from pickers and a released one is not, and a
version can be either, both, or neither.

A release date is a date and stays one. Jira stores it without a time, and
turning it into a timestamp would put a midnight in some timezone that nobody
set.`),
		Example: buildinfo.App + " project versions ENG",
		Args: []registry.Arg{{
			Name: "key", Usage: "project key; defaults to the context's project",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "versions",
		Columns: []render.Column{
			{Header: "id", Path: "@id"},
			{Header: "name", Path: "name"},
			{Header: "released", Path: "@released"},
			{Header: "archived", Path: "@archived"},
			{Header: "release-date", Path: "release-date"},
		},
		Outputs: []registry.Output{{Kind: KindVersions, Version: VersionVers}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateProject,
		Stream:   runVersions,
	}
}

// Versions reads a project's versions.
func (c *Client) Versions(ctx context.Context, key string) ([]Version, error) {
	path := c.Site.APIBase() + "/project/" + url.PathEscape(key) + "/versions"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Released    bool   `json:"released"`
		Archived    bool   `json:"archived"`
		ReleaseDate string `json:"releaseDate"`
		StartDate   string `json:"startDate"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_VERSIONS",
			"%s did not return usable versions", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	out := make([]Version, 0, len(raw))
	for _, r := range raw {
		out = append(out, Version{
			ID: r.ID, Name: r.Name, Description: r.Description,
			Released: r.Released, Archived: r.Archived,
			ReleaseDate: r.ReleaseDate, StartDate: r.StartDate,
		})
	}
	// Newest first, which is what somebody asking about versions usually wants.
	// A version with no date sorts after the dated ones rather than before,
	// because an absent date is not the beginning of time.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].ReleaseDate == "") != (out[j].ReleaseDate == "") {
			return out[j].ReleaseDate == ""
		}
		return out[i].ReleaseDate > out[j].ReleaseDate
	})
	return out, nil
}

func runVersions(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, key, err := clientAndProject(ctx, inv, "project versions")
	if err != nil {
		return registry.StreamResult{}, err
	}

	versions, err := client.Versions(ctx, key)
	if err != nil {
		return registry.StreamResult{}, err
	}

	found := len(versions)
	versions, complete := registry.Bound(inv.Limit, versions)
	for _, v := range versions {
		node := render.El("version").
			Attr("id", v.ID).
			Attr("released", strconv.FormatBool(v.Released)).
			Attr("archived", strconv.FormatBool(v.Archived)).
			Leaf("name", v.Name)
		node.LeafIf("release-date", v.ReleaseDate)
		node.LeafIf("start-date", v.StartDate)
		node.LeafIf("description", v.Description)
		if err := out.Write(node); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), found)
	return registry.StreamResult{Complete: complete}, nil
}

func statusesCommand() *registry.Command {
	return &registry.Command{
		Path:     []string{"project", "statuses"},
		ScopedBy: []string{registry.GlobalProject},
		Summary:  "List the statuses each issue type can be in",
		Description: strings.TrimSpace(`
Returns, for every issue type in a project, the statuses its workflow allows.

Each status carries its category as well as its name. The category is what
anything automated should branch on: a project can rename "In Progress" to
whatever it likes, but the category stays one of three values.

This is what a workflow permits in principle. What one issue can do right now
is ` + "`" + buildinfo.App + ` meta transitions` + "`" + `, and it is a shorter list.`),
		Example: buildinfo.App + " project statuses ENG",
		Args: []registry.Arg{{
			Name: "key", Usage: "project key; defaults to the context's project",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "issue-types",
		Columns: []render.Column{
			{Header: "type", Path: "@type"},
			{Header: "statuses", Path: "@status-names"},
		},
		Outputs: []registry.Output{{Kind: KindStatuses, Version: VersionStat}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateProject,
		Stream:   runStatuses,
	}
}

// Statuses reads what each issue type's workflow allows.
func (c *Client) Statuses(ctx context.Context, key string) ([]StatusesForType, error) {
	path := c.Site.APIBase() + "/project/" + url.PathEscape(key) + "/statuses"

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet, Path: path,
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw []struct {
		Name     string `json:"name"`
		Statuses []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			StatusCategory *struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"statusCategory"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_STATUSES",
			"%s did not return usable statuses", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	out := make([]StatusesForType, 0, len(raw))
	for _, r := range raw {
		entry := StatusesForType{Type: r.Name}
		for _, s := range r.Statuses {
			named := NamedStatus{ID: s.ID, Name: s.Name}
			if s.StatusCategory != nil {
				named.Category = site.NormalizeCategory(
					s.StatusCategory.Key, s.StatusCategory.Name,
				)
			} else {
				named.Category = site.CategoryUnknown
			}
			entry.Statuses = append(entry.Statuses, named)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

func runStatuses(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	client, key, err := clientAndProject(ctx, inv, "project statuses")
	if err != nil {
		return registry.StreamResult{}, err
	}

	types, err := client.Statuses(ctx, key)
	if err != nil {
		return registry.StreamResult{}, err
	}

	found := len(types)
	types, complete := registry.Bound(inv.Limit, types)
	for _, t := range types {
		statuses := make([]*render.Node, 0, len(t.Statuses))
		names := make([]string, 0, len(t.Statuses))
		for _, s := range t.Statuses {
			statuses = append(statuses, render.El("status").
				Attr("id", s.ID).
				Attr("category", s.Category).
				SetText(s.Name))
			names = append(names, s.Name)
		}
		// The names again, flattened, because a TSV cell cannot hold the list
		// and a column over the container was blank on every row.
		node := render.El("issue-type").
			Attr("type", t.Type).
			Attr("status-names", render.JoinList(names))
		node.Child(render.ListEl("statuses", "status", statuses...))
		if err := out.Write(node); err != nil {
			return registry.StreamResult{}, err
		}
	}
	inv.Progress.Update(out.Count(), found)
	return registry.StreamResult{Complete: complete}, nil
}

// clientAndProject is the opening the three per-project listings share.
func clientAndProject(
	ctx context.Context, inv *registry.Invocation, command string,
) (*Client, string, error) {
	client, err := clientFor(ctx, inv, command)
	if err != nil {
		return nil, "", err
	}
	key, err := projectArg(inv)
	if err != nil {
		return nil, "", err
	}
	return client, key, nil
}
