package issue

import (
	"context"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
)

// costList answers --explain-cost for issue list: what the run would spend,
// split into what it spends preparing, what the walk spends, and what it
// spends afterwards because of what the walk found.
//
// The preparation is measured rather than predicted. Validate has already
// run, the way it does before a real walk, so the session's count holds the
// deployment probe and every lookup validation made, and a run made now makes
// the same ones, less any this answer has just cached. Predicting them instead
// would be a second copy of Validate that nothing keeps in step with the first.
//
// The walk is computed from one count, the same count issue activity's sweep
// guard spends, through the same arithmetic the walk uses: freshPerPage reads
// the rows each page after the first brings off overlapFetch, and the floor is
// sweepFloor's. On Data Center the count is exact, so the pages are too, for a
// result set that does not change. Cloud's only count is an estimate, and the
// answer says so rather than quoting a number as exact.
func costList(ctx context.Context, inv *registry.Invocation) (registry.Cost, error) {
	if inv.Jira == nil {
		return registry.Cost{},
			errs.Runtime("NO_SESSION", "issue list has no connection to Jira")
	}
	// A resumed walk's size is what lies past its token, and a Cloud cursor
	// is opaque, so there is nothing to count it with.
	if inv.Flags.String("page-token") != "" {
		return registry.Cost{}, errs.Usage("INVALID_USAGE",
			"--explain-cost prices a walk from its start, and --page-token resumes one partway").
			WithRemedy("drop --page-token to price the whole walk, which costs at " +
				"least what the rest of it does")
	}
	pageSize, err := resolvePageSize(inv.Flags.Int("page-size"))
	if err != nil {
		return registry.Cost{}, err
	}

	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return registry.Cost{}, err
	}
	prepared := conn.Requests()

	query := listQuery(inv)
	text, err := BuildQuery(query)
	if err != nil {
		return registry.Cost{}, err
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return registry.Cost{}, err
	}
	count, err := meta.CountIssues(ctx, text)
	if err != nil {
		return registry.Cost{}, err
	}

	walk := &Client{Site: info}
	opt := ListOptions{Query: query, Limit: inv.Limit, PageSize: pageSize}
	rows := count.Issues
	search := registry.CostStep{
		Name:     registry.CostSearch,
		Requests: walk.pages(opt, pageSize, rows),
		Rows:     &rows,
		PageSize: pageSize,
		Paging:   walk.paging(opt),
	}
	steps := []registry.CostStep{
		{Name: registry.CostPreparation, Requests: prepared},
		search,
	}
	if checksAnEmptyScope(info, query, rows) {
		steps = append(steps, registry.CostStep{Name: registry.CostCheck, Requests: 1})
	}
	return registry.Cost{Approximate: count.Approximate, Steps: steps}, nil
}

// pages is how many search requests the walk ListStream makes for opt takes
// over a query matching rows issues: the rows it will read, which a --limit
// can cut short, at a full first page and freshPerPage on every page after.
// A query matching nothing is still one request, the one that finds it empty.
func (c *Client) pages(opt ListOptions, pageSize, rows int) int {
	if !opt.Limit.All {
		rows = min(rows, opt.Limit.N)
	}
	return sweepFloor(rows, pageSize, c.freshPerPage(opt, pageSize))
}

// paging names the way the walk for opt will page, the same three branches
// freshPerPage and overlapFetch take.
func (c *Client) paging(opt ListOptions) string {
	switch {
	case c.Site.CursorPaginated():
		return registry.PagingCursor
	case c.canKeyset(opt):
		return registry.PagingKeyset
	default:
		return registry.PagingOffset
	}
}

// checksAnEmptyScope reports whether the run will ask about its project after
// the walk, which refuseNoRowsOverUnknownProject does on Cloud when a search
// scoped to one project comes back with no rows.
func checksAnEmptyScope(info site.Info, query QueryOptions, rows int) bool {
	return rows == 0 && info.Kind == site.Cloud && len(query.scope()) == 1
}
