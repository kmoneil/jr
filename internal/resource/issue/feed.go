package issue

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// Kinds the change feed emits.
const (
	KindChanges    = "issue.changes"
	VersionChanges = 1
)

func init() {
	registry.Register(changesCommand())

	render.RegisterSchema(KindChanges, FeedChangeSchema())
}

// FeedChangeSchema is a change plus the issue it happened to.
//
// A separate kind from `issue.history` rather than an optional attribute on it,
// because the two answer different questions and a consumer branching on `kind`
// should not have to test whether the key is there. Every row here names an
// issue; no row there does, since they all came from the one in the argument.
func FeedChangeSchema() *render.Schema {
	s := ChangeSchema()
	s.Attrs = append([]render.Field{
		{Name: "issue", Type: render.TypeString},
	}, s.Attrs...)
	return s
}

// FeedChange is one recorded change and the issue that recorded it.
type FeedChange struct {
	Change
	// Issue is the key the change belongs to.
	Issue string
}

// Node renders one row. It is the same builder `issue history` uses, given a key
// to name, so the two kinds cannot drift in what a change looks like.
func (f FeedChange) Node() *render.Node { return f.node(f.Issue) }

// ChangeFeedColumns is the default TSV column set for `issue changes`.
//
// `created` leads because a feed is read in time order, and the issue key comes
// second because it is what a consumer dispatches on. The two unbounded columns
// are last, as everywhere else.
func ChangeFeedColumns() []render.Column {
	return []render.Column{
		{Header: "created", Path: "created"},
		{Header: "issue", Path: "@issue"},
		{Header: "author", Path: "author@display"},
		{Header: "field", Path: "@field"},
		{Header: "from", Path: "from"},
		{Header: "to", Path: "to"},
	}
}

// sortFeed orders a batch oldest first, tie-broken by issue and then by the id
// of the save, so two runs over the same data produce the same rows in the same
// order.
//
// Oldest first is the opposite of `issue activity`, and deliberately: this is a
// feed something consumes in order, where the next row is the next thing that
// happened. The server's own order is not a contract and is not inherited — see
// sortEvents, where three orderings were measured across two deployments for one
// feature.
func sortFeed(rows []FeedChange) {
	sort.SliceStable(rows, func(a, b int) bool {
		x, y := rows[a], rows[b]
		if x.Created != y.Created {
			return x.Created < y.Created
		}
		if x.Issue != y.Issue {
			return x.Issue < y.Issue
		}
		return x.ID < y.ID
	})
}

func changesCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "changes"},
		Summary: "Report what changed since the last poll, and where to resume",
		Description: strings.TrimSpace(`
An incremental feed of recorded changes: every field that moved on every issue in
scope, oldest first, with a cursor to poll again from.

This is the question a diff of two listings cannot answer. A listing says what an
issue is now, so polling one and comparing shows that something moved without
saying what, misses a change that was reverted between polls, is blind to every
field the columns do not project, and re-fetches five thousand issues to discover
that three moved. All four are properties of the method rather than of how often
it runs.

--since takes the ` + "`next-since-token`" + ` from the previous answer, or a date or
offset for a first poll. **The token is the only correct way to poll.** It names
a window rather than a row, and the reason is measured: JQL cannot express a
bound finer than a minute and neither of its comparison operators can bisect one,
so ` + "`updated >= <the last timestamp>`" + ` either re-reports a minute's worth of
changes on every poll or skips whatever landed inside the minute it rounded past.
There is no third query. A poller built the obvious way passes every test anybody
writes and drops a transition once a week under load.

Each answer reports the changes created after the previous poll's bound and at or
before this poll's start, which is read from the site's own clock rather than
this machine's. Two consecutive polls therefore cover every instant exactly once,
whatever the server does with ties: a bulk edit that stamps four hundred changes
with one timestamp falls entirely inside one answer or entirely inside the next.

**The cursor is only issued when the answer was whole.** A run cut short by
--limit, by the request budget, or by a changelog the server would not send in
full exits 3 and carries no ` + "`next-since-token`" + `, because advancing past a window
that was not fully reported is how a feed loses a change and says nothing. Poll
again with the same --since.

The cursor is in the envelope, so a poll wants a structured format: TSV has no
envelope and carries no token, exactly as it carries no site and no completeness.

One row is one field, as in ` + "`issue history`" + `, and rows from one save share its id
and timestamp. Comments are not here: Jira writes a field transition to the
changelog and a comment is not a field transition.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue changes --since -1h --format json",
			buildinfo.App + " issue changes --since eyJkIjoiY2xvdWQi… --format json",
			buildinfo.App + ` issue changes --since -1d --jql "project = ENG" --format json`,
		}, "\n"),
		Flags: []registry.Flag{{
			Name: sinceFlag, Type: registry.TypeString,
			Usage: "the next-since-token from a previous answer, or a date or " +
				"offset like -1h for a first poll; required, and a date " +
				"function like startOfWeek() is refused because this command " +
				"has to resolve the bound itself",
			Required: true,
		}, {
			Name: "jql", Type: registry.TypeString,
			Usage: "raw JQL narrowing the issues watched, combined with the " +
				"window bound and always parenthesized",
		}, {
			Name: "page-size", Type: registry.TypeInt,
			Usage: "issues per HTTP request, 1 to 100; transport tuning only",
		}},
		Paginated:      true,
		NeedsJira:      true,
		CollectionName: "changes",
		Columns:        ChangeFeedColumns(),
		Outputs: []registry.Output{
			{Kind: KindChanges, Version: VersionChanges},
		},
		ExitCodes: []exitcode.Code{
			exitcode.Partial, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
			exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: validateChanges,
		Stream:   runChanges,
	}
}

// validateChanges refuses what cannot be honored before a byte reaches stdout,
// which for a streaming command is every check it has.
//
// The cursor half is deliberately not checked here. Its deployment cannot be
// compared without knowing which site is on the other end, and asking costs a
// round trip; ParseChangeCursor settles everything about a token that can be
// settled from the string, and DecodeChangeCursor does the rest once the
// session exists. It is the same split PageToken has, for the same reason.
func validateChanges(ctx context.Context, inv *registry.Invocation) error {
	if raw := inv.Flags.String("jql"); raw != "" {
		if err := jql.ValidateFragment(raw); err != nil {
			return err
		}
	}
	since := strings.TrimSpace(inv.Flags.String(sinceFlag))
	if LooksLikeChangeCursor(since) {
		_, err := ParseChangeCursor(since)
		return err
	}

	// Not a cursor, so it has to be a date this process can turn into an
	// instant. A function is refused for the reason `issue activity` refuses
	// one: computing startOfWeek() here would substitute this client's notion
	// of a boundary for the server's, and the window is compared in this
	// process.
	if _, err := jql.ParseDate(since); err != nil {
		return err
	}
	if jql.ClassifyDate(since) == jql.DateFunction {
		return errs.Usage("UNBOUNDABLE_DATE",
			"--%s cannot take a date function on this command", sinceFlag).
			WithDetail("%s compares timestamps in this process, and computing %s "+
				"here would substitute this client's boundary for Jira's",
				strings.Join([]string{buildinfo.App, "issue", "changes"}, " "),
				since).
			WithRemedy("use an offset like -1h, an absolute date, or the " +
				"next-since-token from a previous answer")
	}
	// The same server-side verdict `issue list` and `issue activity` get. A
	// query Jira does not understand comes back as a confident empty result on
	// Cloud, and a feed that polled one would report "nothing changed" forever.
	if err := refuseQueryJiraDoesNotUnderstand(ctx, inv); err != nil {
		return err
	}
	return requirePageSize(inv)
}

func runChanges(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
) (registry.StreamResult, error) {
	if inv.Jira == nil {
		return registry.StreamResult{},
			errs.Runtime("NO_SESSION", "issue changes has no connection to Jira")
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return registry.StreamResult{}, err
	}
	client := &Client{Transport: conn, Site: info}

	window, err := feedWindow(ctx, inv, conn, info)
	if err != nil {
		return registry.StreamResult{}, err
	}
	opt, err := feedRequest(ctx, inv, window)
	if err != nil {
		return registry.StreamResult{}, err
	}

	whole, err := streamFeed(ctx, inv, out, client, opt, window)
	if err != nil {
		return registry.StreamResult{}, err
	}

	// The cursor is issued only for an answer that covered its whole window.
	// Advancing past a window that was cut short is how a feed loses a change
	// while reporting itself fine, which is the failure this command exists to
	// make impossible rather than unlikely.
	switch whole {
	case feedWhole:
		out.SetNextSinceToken(EncodeChangeCursor(window.Cursor(info.Kind)))
		return registry.StreamResult{Complete: true}, nil
	case feedClipped:
		return registry.StreamResult{Complete: false, PartialElement: "change"}, nil
	default:
		// Cut short by the caller or by the budget. There is no page token
		// either: the rows are merged from the changelogs of many issues, so an
		// offset into them names no place a request can start from.
		return registry.StreamResult{Complete: false}, nil
	}
}

// How a poll ended, which is what decides whether a cursor is issued.
type feedOutcome int

const (
	// feedWhole means every change in the window was reported.
	feedWhole feedOutcome = iota
	// feedClipped means the server would not send a changelog in full, so the
	// window holds changes this answer could not see.
	feedClipped
	// feedShort means the caller's limit or the request budget stopped the walk.
	feedShort
)

// feedRequest builds the one search this poll pages through.
//
// The query bound is absolute and minted once, so every page of the walk is
// filtered by the same instant. A relative offset would be re-evaluated against
// a later clock on each page, which moves the bound forward mid-walk and drops
// whatever sat in the band it moved past.
func feedRequest(
	ctx context.Context, inv *registry.Invocation, window ChangeWindow,
) (ListOptions, error) {
	pageSize, err := resolvePageSize(inv.Flags.Int("page-size"))
	if err != nil {
		return ListOptions{}, err
	}
	loc, err := accountLocation(ctx, inv)
	if err != nil {
		return ListOptions{}, err
	}
	floor, err := window.Floor(loc)
	if err != nil {
		return ListOptions{}, err
	}
	return ListOptions{
		Query: QueryOptions{
			Project:      inv.Jira.Project(),
			JQL:          inv.Flags.String("jql"),
			UpdatedAfter: floor,
		},
		Limit:    registry.Limit{All: true},
		PageSize: pageSize,
		// The changelog is the whole point of the request and the fields are
		// not. Asking for one small field rather than none keeps the response to
		// the histories plus a key: `fields=` empty is read as "every field" by
		// both deployments, which would fetch a description to throw it away.
		Fields:        []string{"summary"},
		WithChangelog: true,
	}, nil
}

// streamFeed walks the candidate issues and writes the rows inside the window.
func streamFeed(
	ctx context.Context, inv *registry.Invocation, out *render.Stream,
	client *Client, opt ListOptions, window ChangeWindow,
) (feedOutcome, error) {
	var clipped, atLimit bool
	result, err := client.ListStream(ctx, opt, func(page []Issue, total int) error {
		rows, short := feedRows(page, window)
		if short {
			clipped = true
		}
		sortFeed(rows)
		bounded, err := writeRows(inv, out, rows,
			func(f FeedChange) *render.Node { return f.Node() })
		if err != nil {
			return err
		}
		if bounded {
			atLimit = true
			return errStopPaging
		}
		inv.Progress.Update(out.Count(), total)
		return nil
	})
	switch {
	case err != nil && !errors.Is(err, errStopPaging):
		return feedShort, err
	case atLimit, result == nil || !result.Complete:
		return feedShort, nil
	case clipped:
		return feedClipped, nil
	}
	return feedWhole, nil
}

// feedWindow resolves --since into the interval this poll reports.
func feedWindow(
	ctx context.Context, inv *registry.Invocation, conn Doer, info site.Info,
) (ChangeWindow, error) {
	since := strings.TrimSpace(inv.Flags.String(sinceFlag))

	// The site's clock first, because it anchors both ends of the window. A
	// relative offset resolved against this machine's clock would be wrong by
	// the skew between the two, and on a client running ahead of the site
	// `--since -1h` names an instant the site has not reached — a refusal for a
	// first poll that is nobody's mistake. Everything in a feed is measured on
	// the server's clock because everything it compares was written there.
	now, err := ServerNow(ctx, conn, info)
	if err != nil {
		return ChangeWindow{}, err
	}

	var after time.Time
	if LooksLikeChangeCursor(since) {
		cursor, err := DecodeChangeCursor(since, info.Kind)
		if err != nil {
			return ChangeWindow{}, err
		}
		instant, ok := cursor.Instant()
		if !ok {
			// ParseChangeCursor refused this shape already, so reaching here
			// means the two disagree about what a cursor is.
			return ChangeWindow{}, errs.Runtime("INVALID_SINCE_TOKEN",
				"a cursor that parsed carries no instant")
		}
		after = instant
	} else {
		// A first poll. An absolute literal is a wall clock and Jira reads it in
		// the account's zone, so only that form costs the /myself request; an
		// offset names an instant and costs nothing.
		var loc *time.Location
		if jql.ClassifyDate(since) == jql.DateAbsolute {
			resolved, err := accountLocation(ctx, inv)
			if err != nil {
				return ChangeWindow{}, err
			}
			loc = resolved
		}
		resolved, ok := jql.ResolveDate(since, loc, now)
		if !ok {
			return ChangeWindow{}, errs.Usage("UNBOUNDABLE_DATE",
				"--%s cannot be resolved to an instant", sinceFlag).
				WithDetail("input: %s", since).
				WithRemedy("use an offset like -1h, an absolute date, or the " +
					"next-since-token from a previous answer")
		}
		after = resolved
	}

	return NewChangeWindow(after, now)
}

// feedRows turns one page of issues into the rows inside the window, and reports
// whether any issue's changelog was short.
//
// A clipped changelog is the reason this command could not be built before
// 2026-08-17: Cloud bounds the projection at forty saves, so an issue edited more
// than that in the window holds changes the response does not carry, and a feed
// that emitted what it got and advanced its cursor would skip them silently.
func feedRows(page []Issue, window ChangeWindow) ([]FeedChange, bool) {
	var short bool
	var rows []FeedChange
	for _, i := range page {
		if i.HasChanges && !i.ChangesComplete() {
			short = true
		}
		for _, c := range i.Changes {
			at, err := time.Parse(time.RFC3339, c.Created)
			if err != nil {
				// normalizeTime already refused anything unparseable on the way
				// in, so this is a change with no timestamp at all rather than
				// one this cannot read. It cannot be placed in a window, and a
				// row placed in the wrong window is worse than one reported
				// missing, so the page is short by it.
				short = true
				continue
			}
			if !window.Holds(at) {
				continue
			}
			rows = append(rows, FeedChange{Change: c, Issue: i.Key})
		}
	}
	return rows, short
}
