package issue

import (
	"context"
	"fmt"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
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
	sized := fmt.Sprintf("issue activity would read %s, at least %s",
		countWords(count), plural(floor, "search request"))
	detail := fmt.Sprintf("every candidate is read before --user, --kind and "+
		"the field filters apply; the candidate query: %s", query)
	narrow := narrowing(inv)
	if remaining < 0 {
		return errs.Usage("SWEEP_TOO_LARGE",
			"%s, and it spends at most %d without --max-requests", sized, sweepCeiling).
			WithDetail("%s", detail).
			WithRemedy("%spass --max-requests %d or more to accept the cost", narrow, enough)
	}
	return errs.Usage("SWEEP_TOO_LARGE",
		"%s, and --max-requests leaves %d", sized, remaining).
		WithDetail("%s", detail).
		WithRemedy("%sraise --max-requests to %d or more; a sweep the budget "+
			"cuts short cannot be resumed", narrow, enough)
}

// narrowing is the first half of the refusal's remedy, up to the word that
// introduces accepting the cost instead.
//
// With --user and no --jql it names the clause, spelled for that person, rather
// than only the flag: issue 302, where the remedy said "--jql" and the clause it
// meant was in three documents and nowhere in the refusal. A caller who passed
// --jql has narrowed the candidates already, and is not told their query was
// the wrong one.
func narrowing(inv *registry.Invocation) string {
	clause := involvementClause(inv)
	if clause == "" || strings.TrimSpace(inv.Flags.String("jql")) != "" {
		return "narrow the candidates with --project, --jql or a shorter --since, or "
	}
	return fmt.Sprintf("narrow the candidates with --project, a shorter --since, "+
		"or the person's involvement, --jql %s, which misses a comment, or a "+
		"change to a field other than %s, on an issue they are not otherwise "+
		"tied to; or ", shellQuote(clause), DefaultChangedField)
}

// involvementClause is the JQL for the issues the --user person is tied to:
// every field --involving covers, and the status changes they made. It is the
// clause the docs give for narrowing a sweep, built from the same list
// --involving is, so the two cannot disagree about which fields count.
//
// currentUser() where that is what was typed, so the query says "whoever runs
// this" the way issue list's does. Anyone else is the id validation resolved,
// which is what this deployment puts in JQL: an accountId on Cloud and a
// username on Data Center. Built as values rather than through userExpr, so a
// username that happens to be one of the sentinel words is still a username.
// Empty when there is no person to spell it for.
func involvementClause(inv *registry.Invocation) string {
	typed := strings.TrimSpace(inv.Flags.String(userFlag))
	if typed == "" {
		return ""
	}
	var who jql.Value = &jql.Func{Name: "currentUser"}
	if !isCurrentUser(typed) {
		user, _ := inv.Value(activityUserKey).(site.User)
		if user.ID == "" {
			return ""
		}
		who = jql.Text(user.ID)
	}
	exprs := make([]jql.Expr, 0, len(InvolvingFields)+1)
	for _, field := range InvolvingFields {
		exprs = append(exprs, &jql.Clause{Field: field, Op: jql.OpEq, Value: who})
	}
	exprs = append(exprs, &jql.Clause{
		Field: DefaultChangedField, Op: jql.OpChanged,
		Predicates: []jql.Predicate{jql.By(who)},
	})
	clause, err := jql.Render(&jql.Query{Where: jql.AnyOf(exprs...)})
	if err != nil {
		return ""
	}
	return clause
}

// shellQuote makes s one single-quoted shell word, so a clause holding a
// quote, as a Data Center username can, still pastes as one argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
		return "about " + plural(c.Issues, "candidate issue")
	}
	return plural(c.Issues, "candidate issue")
}
