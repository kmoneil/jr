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
				Usage: "extra field id to include, e.g. customfield_10042; " +
					"added to the default set, repeat for several",
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
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "issues",
		Columns:        ListColumns(),
		ColumnsFor:     listColumnsFor,
		Validate:       validateList,
		Outputs:        []registry.Output{{Kind: KindList, Version: VersionList}},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Stream: runList,
	}
}

// runList streams matching issues.
//
// Rows go out as each page arrives rather than after the last request, so a
// long run produces output immediately, a downstream `head` can stop it early,
// and an interrupt leaves the caller with what was fetched.
func runList(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "issue list has no connection to Jira")
	}

	query := QueryOptions{
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
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	client := &Client{Transport: conn, Site: info}

	result, err := client.ListStream(ctx, ListOptions{
		Query:     query,
		Limit:     inv.Limit,
		PageSize:  inv.Flags.Int("page-size"),
		PageToken: inv.Flags.String("page-token"),
		Fields:    requestedFields(inv.Flags.StringSlice("field")),
	}, func(page []Issue, total int) error {
		nodes := make([]*render.Node, 0, len(page))
		for _, i := range page {
			nodes = append(nodes, i.Node())
		}
		if err := out.Write(nodes...); err != nil {
			return err
		}
		// The server discloses the total on the first page, so a long run can
		// say how long it will be from its first second rather than never.
		inv.Progress.Update(out.Count(), total)
		return nil
	})
	if err != nil {
		return registry.StreamResult{}, err
	}

	return registry.StreamResult{
		Complete:      result.Complete,
		NextPageToken: result.NextPageToken,
	}, nil
}

// requestedFields returns what to ask Jira for.
//
// --field is additive. Replacing the default set instead would mean
// `--field customfield_10042` silently blanked the status and assignee columns,
// which looks like every issue is unassigned rather than like a flag that
// narrowed the request.
func requestedFields(requested []string) []string {
	return append(DefaultFields(), ExtraFieldNames(requested)...)
}

// validateList rejects flags the generic machinery cannot check, before the
// stream opens and its header is written.
func validateList(inv *registry.Invocation) error {
	return ValidateFieldNames(inv.Flags.StringSlice("field"))
}

// listColumnsFor appends a column per requested field, so asking for one
// changes the default TSV output rather than only the structured formats.
func listColumnsFor(inv *registry.Invocation) []render.Column {
	return append(ListColumns(), ExtraColumns(inv.Flags.StringSlice("field"))...)
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
	// BeforeKey bounds the query below an issue key, which is how keyset
	// pagination resumes. It is set by the client, never by a flag.
	BeforeKey string
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

	// The keyset bound goes in as a clause like any other filter, so it is
	// quoted and combined by the builder rather than spliced into a string.
	if opt.BeforeKey != "" {
		b.Clause(SortKey, jql.OpLt, jql.Text(opt.BeforeKey))
	}

	if err := addOrder(b, opt); err != nil {
		return "", err
	}
	return b.Render()
}

// SortKey is the field results are ordered by when the caller names none.
//
// It is the issue key because pagination has to be stable, and the key is the
// only field that is both unique and immutable. Ordering by a mutable field
// such as updated means an issue edited mid-run moves between pages, and an
// offset-paginated deployment then skips or repeats it — a result that is
// quietly missing rows while reporting itself complete.
const SortKey = "issuekey"

// addOrder appends the ORDER BY clause.
//
// There is always one. Without it the order is whatever the server happens to
// do, which is undocumented, free to change, and not guaranteed to be the same
// between two requests — so a paged result could interleave two different
// orderings and nobody would see it happen.
func addOrder(b *jql.Builder, opt QueryOptions) error {
	if opt.Sort == "" {
		b.OrderBy(SortKey, jql.Desc)
		return nil
	}

	dir, ok := jql.ParseDirection(orElse(opt.Order, "asc"))
	if !ok {
		return errs.Usage("INVALID_ORDER",
			"--order does not accept %q", opt.Order).
			WithDetail("valid values: asc, desc").
			WithRemedy("sorting is --sort <field> plus --order asc|desc")
	}
	b.OrderBy(opt.Sort, dir)

	// A caller's sort field is rarely unique — every issue updated in the same
	// bulk edit shares a timestamp — so ties would break arbitrarily and
	// differently each run. The key is the tiebreaker because it is the one
	// field guaranteed to make the order total.
	if !strings.EqualFold(opt.Sort, SortKey) {
		b.OrderBy(SortKey, jql.Desc)
	}
	return nil
}

// SortsByKey reports whether these options leave the default key ordering in
// place, which is what keyset pagination requires.
func (o QueryOptions) SortsByKey() bool {
	return o.Sort == "" || (strings.EqualFold(o.Sort, SortKey) &&
		!strings.EqualFold(o.Order, "asc"))
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
