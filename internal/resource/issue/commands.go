package issue

import (
	"context"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/jql"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

func init() {
	registry.Register(listCommand())
}

func listCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "list"},
		Summary: "List issues matching a query",
		Description: strings.TrimSpace(`
Builds a JQL query from the flags, or takes one whole with --jql, and returns
the matching issues.

--limit says how many results you want and is not capped: the client pages
until it has them. --page-size tunes the transport and is rarely worth setting.
There is deliberately no offset flag — Cloud pages by cursor, so an offset
could not be honored, and --page-token is opaque precisely so the same flag
works against both deployments.

A result cut short by --limit or by --max-requests is never reported as
complete. It exits 3, says so on stderr, and carries a token to resume from.

Raw JQL from --jql is always parenthesized before being combined with the other
filters, so an OR inside it cannot escape the project scope.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue list --project ENG --status 'In Progress'",
			buildinfo.App + " issue list --jql 'labels IN (retry, transport)' --limit all",
			buildinfo.App + " issue list --assignee currentUser --sort updated --order desc",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "jql", Type: registry.TypeString,
				Usage: "raw JQL, combined with the other filters and always parenthesized",
			},
			{
				Name: "status", Type: registry.TypeString, Repeatable: true,
				Usage: "status to match; repeat for several",
			},
			{
				Name: "label", Type: registry.TypeString, Repeatable: true,
				Usage: "label to match; repeat for several",
			},
			{
				Name: "not-label", Type: registry.TypeString, Repeatable: true,
				Usage: "label to exclude; repeat for several",
			},
			{
				Name: "type", Short: "t", Type: registry.TypeString, Repeatable: true,
				Usage: "issue type to match; repeat for several",
			},
			{
				Name: "assignee", Short: "a", Type: registry.TypeString,
				Usage: "assignee; the word currentUser resolves to the caller",
			},
			{
				Name: "reporter", Type: registry.TypeString,
				Usage: "reporter; the word currentUser resolves to the caller",
			},
			{
				Name: "created-after", Type: registry.TypeString,
				Usage: "only issues created on or after this date or offset, e.g. -7d",
			},
			{
				Name: "created-before", Type: registry.TypeString,
				Usage: "only issues created on or before this date or offset",
			},
			{
				Name: "updated-after", Type: registry.TypeString,
				Usage: "only issues updated on or after this date or offset",
			},
			{
				Name: "sort", Short: "s", Type: registry.TypeString,
				Usage: "field to sort by, e.g. updated",
			},
			{
				Name: "order", Short: "o", Type: registry.TypeEnum,
				Enum: []string{"asc", "desc"}, Default: "asc",
				Usage: "sort direction",
			},
			{
				Name: "field", Type: registry.TypeString, Repeatable: true,
				Usage: "field to request from Jira; repeat for several",
			},
			{
				Name: "page-size", Type: registry.TypeInt,
				Usage: "results per HTTP request, 1 to 100; transport tuning only",
			},
			{
				Name: "page-token", Type: registry.TypeString,
				Usage: "resume from a next-page-token returned by a previous run",
			},
		},
		Paginated: true,
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Run: runList,
	}
}

// runList is wired by the CLI layer, which owns the transport and the resolved
// context. The registry holds the declaration; the invocation carries the rest.
func runList(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "issue list has no connection to Jira")
	}

	query, err := BuildQuery(QueryOptions{
		// Project comes from the resolved context rather than a local flag:
		// --project is global, and it is a default, never a requirement.
		Project:       inv.Jira.Project(),
		JQL:           inv.Flags.String("jql"),
		Statuses:      inv.Flags.StringSlice("status"),
		Labels:        inv.Flags.StringSlice("label"),
		NotLabels:     inv.Flags.StringSlice("not-label"),
		Types:         inv.Flags.StringSlice("type"),
		Assignee:      inv.Flags.String("assignee"),
		Reporter:      inv.Flags.String("reporter"),
		CreatedAfter:  inv.Flags.String("created-after"),
		CreatedBefore: inv.Flags.String("created-before"),
		UpdatedAfter:  inv.Flags.String("updated-after"),
		Sort:          inv.Flags.String("sort"),
		Order:         inv.Flags.String("order"),
	})
	if err != nil {
		return nil, err
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{Transport: conn, Site: info}

	result, err := client.List(ctx, ListOptions{
		JQL:       query,
		Limit:     inv.Limit,
		PageSize:  inv.Flags.Int("page-size"),
		PageToken: inv.Flags.String("page-token"),
		Fields:    requestedFields(inv.Flags.StringSlice("field")),
	})
	if err != nil {
		return nil, err
	}
	return ListDoc(result), nil
}

// requestedFields returns what to ask Jira for. An empty list means the default
// set, which is everything the output contract's columns need.
func requestedFields(requested []string) []string {
	if len(requested) > 0 {
		return requested
	}
	return DefaultFields()
}

// DefaultFields is what `issue list` asks Jira for.
//
// It is exactly what the default columns and the XML payload need. Asking for
// "*all" instead would be simpler and would make every list an order of
// magnitude larger for fields nobody rendered.
func DefaultFields() []string {
	return []string{
		"summary", "status", "assignee", "reporter",
		"priority", "issuetype", "project", "created", "updated", "labels",
	}
}

// ListDoc renders a result. The complete attribute comes straight from the
// client, which is the only thing that knows whether the result set ran out.
func ListDoc(result *ListResult) *render.Doc {
	items := make([]*render.Node, 0, len(result.Issues))
	for _, i := range result.Issues {
		items = append(items, i.Node())
	}
	return render.List(KindList, VersionList, &render.Collection{
		Name:          "issues",
		Items:         items,
		Complete:      result.Complete,
		NextPageToken: result.NextPageToken,
		Columns:       ListColumns(),
	})
}

// QueryOptions are the filter flags, before they become JQL.
type QueryOptions struct {
	Project       string
	JQL           string
	Statuses      []string
	Labels        []string
	NotLabels     []string
	Types         []string
	Assignee      string
	Reporter      string
	CreatedAfter  string
	CreatedBefore string
	UpdatedAfter  string
	Sort          string
	Order         string
}

// BuildQuery turns filter flags into JQL.
//
// Every value goes in as data through the builder. Nothing here concatenates a
// string, and the raw fragment is parenthesized by the renderer rather than by
// this function remembering to.
func BuildQuery(opt QueryOptions) (string, error) {
	b := jql.New()

	if opt.Project != "" {
		b.Project(opt.Project)
	}
	b.In("status", opt.Statuses...)
	b.In("labels", opt.Labels...)
	b.NotIn("labels", opt.NotLabels...)
	b.In("issuetype", opt.Types...)

	addUser(b, "assignee", opt.Assignee)
	addUser(b, "reporter", opt.Reporter)

	for _, d := range []struct {
		field, value string
		since        bool
	}{
		{"created", opt.CreatedAfter, true},
		{"created", opt.CreatedBefore, false},
		{"updated", opt.UpdatedAfter, true},
	} {
		if d.value == "" {
			continue
		}
		build := jql.Until
		if d.since {
			build = jql.Since
		}
		expr, err := build(d.field, d.value)
		if err != nil {
			return "", err
		}
		b.Where(expr)
	}

	if opt.JQL != "" {
		b.Raw(opt.JQL)
	}

	if opt.Sort != "" {
		dir, ok := jql.ParseDirection(orElse(opt.Order, "asc"))
		if !ok {
			return "", errs.Usage("INVALID_ORDER",
				"--order does not accept %q", opt.Order).
				WithDetail("valid values: asc, desc").
				WithRemedy("sorting is --sort <field> plus --order asc|desc")
		}
		b.OrderBy(opt.Sort, dir)
	}

	return b.Render()
}

// addUser handles the one value with a special meaning. currentUser is a JQL
// function, and quoting it as a string would search for a user literally named
// "currentUser" and return nothing.
func addUser(b *jql.Builder, field, value string) {
	switch {
	case value == "":
	case strings.EqualFold(value, "currentUser"), strings.EqualFold(value, "currentUser()"):
		b.Clause(field, jql.OpEq, &jql.Func{Name: "currentUser"})
	case strings.EqualFold(value, "unassigned"), strings.EqualFold(value, "empty"):
		b.IsEmpty(field)
	default:
		b.Eq(field, value)
	}
}

func orElse(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
