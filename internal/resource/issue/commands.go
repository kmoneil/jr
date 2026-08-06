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
	"github.com/kmoneil/jira-cli/internal/site"
)

func init() {
	registry.Register(listCommand())
	registry.Register(getCommand())

	render.RegisterSchema(KindList, Schema())
	render.RegisterSchema(KindGet, Schema())
}

// Schema is the shape of an issue, as `jr contract` reports it and as
// render.Doc.Validate holds every emitted issue to.
func Schema() *render.Schema {
	return &render.Schema{
		Element: "issue",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "id", Type: render.TypeString},
			{Name: "type", Type: render.TypeString, Optional: true},
			{Name: "priority", Type: render.TypeString, Optional: true},
			{Name: "project", Type: render.TypeString, Optional: true},
			{Name: "resolution", Type: render.TypeString, Optional: true},
			{Name: "parent", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("summary", render.TypeString)},
			{Schema: &render.Schema{
				Element: "status",
				Attrs: []render.Field{{
					// A project can rename a status to anything; the category
					// stays one of four values, which is what anything
					// automated should branch on.
					Name: "category", Type: render.TypeString,
					Enum: []string{
						site.CategoryToDo, site.CategoryInProgress,
						site.CategoryDone, site.CategoryUnknown,
					},
				}},
				Text: &render.Field{Type: render.TypeString},
			}},
			{Schema: &render.Schema{
				// Always present, and empty when nobody is assigned: absent and
				// unassigned are different facts and this kind reports both.
				Element: "assignee",
				Attrs: []render.Field{
					{Name: "id", Type: render.TypeString},
					{Name: "display", Type: render.TypeString},
				},
			}},
			{Schema: render.Leaf("created", render.TypeTimestamp), Optional: true},
			{Schema: render.Leaf("updated", render.TypeTimestamp), Optional: true},
			{Schema: &render.Schema{
				// Mixed content in CDATA, with the markup named rather than
				// guessed at: wiki on Data Center, adf on Cloud.
				Element: "description",
				Attrs: []render.Field{{
					Name: "format", Type: render.TypeString,
					Enum: []string{BodyWiki, BodyADF, BodyMarkdown},
				}},
				Text: &render.Field{Type: render.TypeString},
			}, Optional: true},
			{Schema: render.ListSchema("labels", "label",
				render.Leaf("label", render.TypeString))},
			{Schema: render.ListSchema("components", "component",
				render.Leaf("component", render.TypeString)), Optional: true},
			{Schema: render.ListSchema("fix-versions", "fix-version",
				render.Leaf("fix-version", render.TypeString)), Optional: true},
		},
		Extra: &render.Extra{
			Named: "the id of a field requested with --field, e.g. customfield_10042",
			Type:  render.TypeString,
		},
	}
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
				Usage: "extra field to include, by id or name, e.g. " +
					"customfield_10042 or 'Story Points'; " +
					"added to the default set, repeat for several",
			},
			{
				Name: "all-projects", Type: registry.TypeBool,
				Usage: "search every project the credential can see, ignoring " +
					"the context's; required to exhaust an unfiltered query",
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

	query := listQuery(inv)

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
		Fields:    requestedFields(resolvedFields(inv)),
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
func requestedFields(resolved []string) []string {
	return append(DefaultFields(), ExtraFieldNames(resolved)...)
}

// resolvedFieldsKey is where the ids resolved during validation are left for
// the rest of the invocation.
const resolvedFieldsKey = "issue.fields"

// validateFields resolves --field against the site's catalogue and leaves the
// ids on the invocation.
//
// It happens here rather than in the command body because the columns are
// computed from those ids before the body runs — a streaming command's header
// goes out first — and because a name that resolves to nothing has to be
// refused before any bytes reach stdout.
// validateList refuses the one invocation that is a sweep rather than a query,
// then resolves the field names.
//
// Both have to happen here rather than in the body: this is a streaming
// command, so its header — and its columns — go out before the body runs, and a
// rejection from inside it would arrive after bytes were already on stdout.
func validateList(ctx context.Context, inv *registry.Invocation) error {
	if err := refuseUnconstrainedSweep(inv); err != nil {
		return err
	}
	return validateFields(ctx, inv)
}

// refuseUnconstrainedSweep stops `issue list --limit all` with nothing set.
//
// The default bound is what makes the unfiltered query harmless: it costs one
// request and fifty rows, and refusing it would be a judgement about what
// somebody meant. --limit all is different. It pages until the instance is
// exhausted, and on a production Data Center a personal access token inherits
// every project its owner was ever added to — so the result is every issue in
// every project they can see, which is rarely what was meant and is not
// something to find out afterwards.
//
// --all-projects is how to mean it. The refusal is not that the request cannot
// be honored; it is that a request this wide should be stated rather than
// arrived at.
func refuseUnconstrainedSweep(inv *registry.Invocation) error {
	if !inv.Limit.All || inv.Flags.Bool("all-projects") {
		return nil
	}
	if listQuery(inv).Constrained() {
		return nil
	}
	return errs.Usage("UNCONSTRAINED_QUERY",
		"issue list --limit all with no filter would return every issue in "+
			"every project this credential can see").
		WithDetail("any of these constrains it: %s", strings.Join(constrainingFlags, ", ")).
		WithRemedy("set --project, add a filter, or pass --all-projects to mean it")
}

// constrainingFlags is what the refusal offers instead, so the caller does not
// have to go and read --help to find out what would have been enough.
var constrainingFlags = []string{
	"--project", "--jql", "--status", "--label", "--not-label", "--type",
	"--assignee", "--reporter", "--created-after", "--created-before",
	"--updated-after",
}

func validateFields(ctx context.Context, inv *registry.Invocation) error {
	requested := inv.Flags.StringSlice("field")
	if len(requested) == 0 {
		// No --field means no catalogue, and no catalogue means no request. The
		// common invocation must not pay for a feature it did not ask for.
		inv.SetValue(resolvedFieldsKey, []string(nil))
		return nil
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION", "--field cannot be resolved without a connection to Jira")
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return err
	}

	resolved, err := ResolveFields(catalogue, requested)
	if err != nil {
		return err
	}
	inv.SetValue(resolvedFieldsKey, resolved)
	return nil
}

// resolvedFields returns the ids validation resolved.
func resolvedFields(inv *registry.Invocation) []string {
	ids, _ := inv.Value(resolvedFieldsKey).([]string)
	return ids
}

// listColumnsFor appends a column per requested field, so asking for one
// changes the default TSV output rather than only the structured formats.
func listColumnsFor(inv *registry.Invocation) []render.Column {
	return append(ListColumns(), ExtraColumns(resolvedFields(inv))...)
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

// ListQueryFor exposes listQuery for a test that needs to see what the flags
// resolved to without sending anything.
func ListQueryFor(inv *registry.Invocation) QueryOptions { return listQuery(inv) }

// listQuery reads the filters off one invocation.
//
// It is one function because two callers need the same answer: the guard that
// decides whether the query constrains anything, and the body that sends it. A
// second copy would let them disagree about what "unfiltered" means, which is
// the one thing the guard exists to be right about.
func listQuery(inv *registry.Invocation) QueryOptions {
	opt := QueryOptions{
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
	// Project comes from the resolved context rather than a local flag:
	// --project is global, and it is a default, never a requirement.
	// --all-projects lifts it, which is the only way past a context that sets
	// one — an empty --project falls back to the context rather than clearing
	// it.
	if inv.Jira != nil && !inv.Flags.Bool("all-projects") {
		opt.Project = inv.Jira.Project()
	}
	return opt
}

// Constrained reports whether these options narrow the result set at all.
//
// The sort does not count. Ordering an unbounded query does not make it
// smaller, and treating it as a filter is how a guard against sweeping the
// instance gets talked out of firing by --sort updated.
func (o QueryOptions) Constrained() bool {
	for _, v := range []string{
		o.Project, o.JQL, o.Assignee, o.Reporter,
		o.CreatedAfter, o.CreatedBefore, o.UpdatedAfter,
	} {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return len(o.Statuses) > 0 || len(o.Labels) > 0 ||
		len(o.NotLabels) > 0 || len(o.Types) > 0
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

	if err := jql.AppendOrder(b, opt.Sort, opt.Order); err != nil {
		return "", err
	}
	return b.Render()
}

// SortKey is the field results are ordered by when the caller names none.
//
// The policy lives in internal/jql, because `jql explain` has to produce the
// same string this does — a second copy of the ordering rules would make the
// explanation a second implementation, and the two would drift on the first
// change to either.
const SortKey = jql.DefaultSortField

// SortsByKey reports whether these options leave the default key ordering in
// place, which is what keyset pagination requires.
func (o QueryOptions) SortsByKey() bool { return jql.SortsByKey(o.Sort, o.Order) }

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

func getCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "get"},
		Summary: "Fetch one issue in full",
		Description: strings.TrimSpace(`
Returns a single issue with its description, resolution, components, and fix
versions — the fields worth fetching one issue at a time.

Data Center serves wiki markup, which is carried through unchanged. Cloud
serves an Atlassian Document Format object, which is converted to markdown —
losslessly, or not at all: a description holding something markdown cannot
represent is an error naming it rather than an approximation. --raw-body emits
the document itself. The format attribute says which you have.

The issue shape here is the same one issue list emits for a row, so a caller
parses both identically. It simply has more of it filled in.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue get ENG-101",
			buildinfo.App + " issue get ENG-101 --format json",
			buildinfo.App + " issue get ENG-101 --field customfield_10042",
			buildinfo.App + " issue get ENG-101 --raw-body",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{{
			Name: "field", Type: registry.TypeString, Repeatable: true,
			Usage: "extra field to include, by id or name, e.g. " +
				"customfield_10042 or 'Story Points'; " +
				"added to the default set, repeat for several",
		}, rawBodyFlag()},
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: KindGet, Version: VersionGet}},
		ExitCodes: []exitcode.Code{
			exitcode.Usage, exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateFields,
		Run:      runGet,
	}
}

func runGet(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "issue get has no connection to Jira")
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{Transport: conn, Site: info, Body: bodyMode(inv)}

	fields := append(DetailFields(), ExtraFieldNames(resolvedFields(inv))...)
	issue, err := client.Get(ctx, inv.Args[0], fields)
	if err != nil {
		return nil, err
	}
	return GetDoc(issue), nil
}

// GetDoc renders one issue as a record, so it defaults to XML: a description
// full of newlines and code fences is mixed content, and XML carries it without
// an escaping tax.
func GetDoc(i Issue) *render.Doc {
	return render.Record(KindGet, VersionGet, i.Node())
}
