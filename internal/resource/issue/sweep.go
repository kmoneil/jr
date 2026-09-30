package issue

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
)

// sweepCeiling is how many search pages issue activity spends without being
// told it may: a thousand candidate issues at the default page size.
//
// Decided 2026-09-27 on issue 149, and not measured, because nothing measured
// picks the number. It admits the reporter's single-project week, which was one
// page, and refuses their instance-wide one, which was about sixty pages of
// changelog and was killed after five minutes with nothing on stdout.
const sweepCeiling = 10

// refuseSweepTooLarge sizes the candidate sweep before it starts, and refuses
// one the caller has not agreed to pay for.
//
// The sweep downloads every candidate's changelog, comments and worklogs, and
// --user, --kind and the field filters are applied afterwards, in this process,
// so its cost grows with everybody's updates in the window rather than with the
// caller's. --limit does not bound it: every candidate is read before the
// newest events are chosen. One count request says how big it is.
//
// --max-requests is how a caller accepts the cost, because a cap is a stated
// cost. A floor above what it leaves is refused too, and that one is not a
// judgement: activity has no resume token, so a sweep the budget cuts short is
// spent work with no way to continue it, knowable here for one request instead
// of discovered after the budget is gone.
func refuseSweepTooLarge(ctx context.Context, inv *registry.Invocation) error {
	if inv.Jira == nil {
		return nil
	}
	pageSize, err := resolvePageSize(inv.Flags.Int("page-size"))
	if err != nil {
		return err
	}
	query, err := BuildQuery(activityQuery(inv))
	if err != nil {
		return err
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	count, err := meta.CountIssues(ctx, query)
	if err != nil {
		return err
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return err
	}

	walk := &Client{Site: info}
	floor := sweepFloor(count.Issues, pageSize, walk.freshPerPage(ListOptions{
		Query: activityQuery(inv), Limit: registry.Limit{All: true}, PageSize: pageSize,
	}, pageSize))
	remaining := conn.Remaining()
	switch {
	case remaining < 0 && floor <= sweepCeiling:
		return nil
	case remaining >= 0 && floor <= remaining:
		return nil
	}

	// What --max-requests has to say for this sweep to run: what validation
	// has spent already, which the next run spends again, plus the floor.
	enough := conn.Requests() + floor
	sized := fmt.Sprintf("issue activity would read %s candidate issues, "+
		"at least %d search requests", countWords(count), floor)
	detail := fmt.Sprintf("every candidate is read before --user, --kind and "+
		"the field filters apply; the candidate query: %s", query)
	if remaining < 0 {
		return errs.Usage("SWEEP_TOO_LARGE",
			"%s, and it spends at most %d without --max-requests", sized, sweepCeiling).
			WithDetail("%s", detail).
			WithRemedy("narrow the candidates with --project, --jql or a shorter "+
				"--since, or pass --max-requests %d or more to accept the cost", enough)
	}
	return errs.Usage("SWEEP_TOO_LARGE",
		"%s, and --max-requests leaves %d", sized, remaining).
		WithDetail("%s", detail).
		WithRemedy("narrow the candidates with --project, --jql or a shorter "+
			"--since, or raise --max-requests to %d or more; a sweep the budget "+
			"cuts short cannot be resumed", enough)
}

// sweepFloor is the fewest search requests a sweep over n candidates takes,
// when the first page brings pageSize of them and every later page brings
// fresh. It is a floor and not an estimate: an issue holding more than twenty
// worklogs costs one request more, and that cannot be known before its page
// arrives. An empty candidate set is still one request, the one that finds it
// empty.
func sweepFloor(n, pageSize, fresh int) int {
	if n <= pageSize {
		return 1
	}
	return 1 + (n-pageSize+fresh-1)/fresh
}

// freshPerPage is how many rows the walk ListStream makes for opt gains from
// each page after the first. A cursor or keyset page gains all of them. An
// offset page asks again for the row the last one ended on, to prove the set
// did not shift under it, and never asks for more than the search accepts, so
// at the largest page size it gains one fewer. Read off overlapFetch rather
// than restated, so the count cannot drift from the walk.
func (c *Client) freshPerPage(opt ListOptions, pageSize int) int {
	if c.Site.CursorPaginated() || c.canKeyset(opt) {
		return pageSize
	}
	_, ask := c.overlapFetch(false, PageToken{Anchor: "the last row", Offset: pageSize}, pageSize)
	return ask - overlapRow
}

// countWords says the count the way the server meant it. Cloud's is an
// estimate, and a refusal quoting it as exact would claim more than was known.
func countWords(c site.IssueCount) string {
	if c.Approximate {
		return "about " + strconv.Itoa(c.Issues)
	}
	return strconv.Itoa(c.Issues)
}
