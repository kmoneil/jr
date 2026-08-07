package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Page size bounds. These are transport-level tuning, not a user intent:
// --limit says how many results the caller wants, and the client pages until it
// has them.
const (
	// MinPageSize is the smallest page worth requesting.
	MinPageSize = 1
	// MaxPageSize is what Jira accepts for a search. Asking for more does not
	// fail; it silently returns fewer, which is exactly the kind of quiet
	// disagreement this tool refuses to build on.
	MaxPageSize = 100
	// DefaultPageSize balances round trips against wasted rows when --limit is
	// small.
	DefaultPageSize = 50
)

// Doer is the part of the transport this resource needs.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Client queries issues.
type Client struct {
	Transport Doer
	Site      site.Info
	// Body says how a Cloud description, comment, or worklog note is
	// reported. The zero value converts it to markdown; --raw-body asks for
	// the document instead. It lives here rather than on each options struct
	// because every read that can carry a body answers the same question.
	Body BodyMode
	// BodyFormat says how a body a caller sent should be read: --body-format,
	// empty for the default. Here for the same reason Body is — every write
	// that takes a body answers the same question.
	BodyFormat string
}

// ListOptions is one `issue list` request.
type ListOptions struct {
	// Query is the structured filter set. The client renders it through the
	// JQL builder each page, rather than being handed a finished string,
	// because keyset pagination narrows the query itself — and narrowing a
	// rendered string would mean concatenating JQL.
	Query QueryOptions
	// JQL overrides Query with an already-rendered query. It exists for tests
	// and for a caller that has one in hand; a request using it cannot page by
	// keyset, because there is no builder to narrow.
	JQL string
	// Limit is the caller's intent, decoupled from the page size.
	Limit registry.Limit
	// PageSize tunes the transport. Zero means DefaultPageSize.
	PageSize int
	// PageToken resumes a previous result set.
	PageToken string
	// Fields narrows what Jira returns.
	Fields []string
}

// ListResult is a page-spanning result plus everything needed to say honestly
// whether it is complete.
type ListResult struct {
	Issues []Issue
	// Keyset reports whether paging was stable. It is false when the request
	// fell back to offsets, where an issue created mid-run shifts every later
	// page.
	Keyset bool
	// Fetched counts every issue seen, whether it was accumulated or streamed
	// straight out.
	Fetched int
	// Total is what the server said the whole result set holds, when it says.
	// Cloud's cursor API does not.
	Total int
	// Complete is true only if the result set is exhaustive. It is false when
	// a limit or a budget cut it short, and then NextPageToken resumes.
	Complete      bool
	NextPageToken string
	// Requests is how many HTTP calls this cost, for --debug and for tests
	// that assert the client did not over-fetch.
	Requests int
}

// searchResponse covers both deployments. Cloud sends nextPageToken and isLast;
// Data Center sends startAt, maxResults, and total.
type searchResponse struct {
	Issues        []json.RawMessage `json:"issues"`
	NextPageToken string            `json:"nextPageToken"`
	IsLast        *bool             `json:"isLast"`
	StartAt       *int              `json:"startAt"`
	MaxResults    *int              `json:"maxResults"`
	Total         *int              `json:"total"`
}

// searchPath is the endpoint for each deployment. Cloud's offset-based
// /rest/api/3/search was removed in 2025; /search/jql is the cursor one.
func (c *Client) searchPath() string {
	if c.Site.CursorPaginated() {
		return c.Site.APIBase() + "/search/jql"
	}
	return c.Site.APIBase() + "/search"
}

// List returns issues matching a query, paging until the limit is satisfied.
//
// --limit is a user intent and the page size is transport tuning. The client
// loops until it has what was asked for, so a caller never has to page by hand
// and never has to know which pagination model the server uses.
//
// If the result set runs out first, the result is complete. If the limit or the
// request budget stops it first, the result is not complete and carries a token
// to resume from. There is no third state.
// List collects every page before returning. It is the buffered form, kept for
// callers that genuinely need the whole set in hand.
func (c *Client) List(ctx context.Context, opt ListOptions) (*ListResult, error) {
	return c.ListStream(ctx, opt, nil)
}

// ListStream calls onPage as each page arrives, so a caller can emit rows
// without waiting for the last request.
//
// That matters more than it sounds: a `--limit all` over a large project is a
// hundred requests, and buffering means no output for the whole run, nothing
// to show for an interrupt, and no way for a downstream `head` to stop early.
//
// onPage receives each page and the total the server reported, which is zero
// when it reported none — Cloud's cursor API does not. It may be nil, in which
// case every issue is accumulated into the result instead.
func (c *Client) ListStream(
	ctx context.Context, opt ListOptions, onPage func(page []Issue, total int) error,
) (*ListResult, error) {
	pageSize, err := resolvePageSize(opt.PageSize)
	if err != nil {
		return nil, err
	}
	token, err := DecodePageToken(opt.PageToken, c.Site.Kind)
	if err != nil {
		return nil, err
	}

	out := &ListResult{Keyset: c.canKeyset(opt)}
	for {
		want := pageSize
		if !opt.Limit.All {
			remaining := opt.Limit.N - out.Fetched
			if remaining <= 0 {
				return truncated(out, token)
			}
			// Never ask for more than is wanted: over-fetching costs the
			// caller's rate limit for rows that are thrown away.
			want = min(want, remaining)
		}

		page, err := c.fetch(ctx, opt, token, want)
		if err != nil {
			// A spent request budget is not a failure: it means there is more,
			// and the caller gets what was fetched plus a way to resume.
			if transport.IsBudgetExceeded(err) && out.Fetched > 0 {
				return truncated(out, token)
			}
			return nil, err
		}
		out.Requests++

		issues, err := decodeIssues(page.Issues, ExtraFieldNames(opt.Fields), c.Body)
		if err != nil {
			return nil, err
		}
		out.Fetched += len(issues)
		if page.Total != nil {
			out.Total = *page.Total
		}
		if onPage == nil {
			out.Issues = append(out.Issues, issues...)
		} else if err := onPage(issues, out.Total); err != nil {
			return nil, err
		}

		if out.Keyset {
			// Verify the server ordered the way this client assumes. If JQL's
			// key comparison differs from ours, a keyset cursor would silently
			// skip or repeat rows; failing here turns that into a loud error
			// rather than a result that is quietly missing issues.
			if err := verifyDescendingBelow(issues, token.AfterKey); err != nil {
				return nil, err
			}
		}

		next, last := c.advance(out.Keyset, token, page, issues, want)
		if last {
			out.Complete = true
			return out, nil
		}
		token = next

		if !opt.Limit.All && out.Fetched >= opt.Limit.N {
			return truncated(out, token)
		}
		if len(issues) == 0 {
			// A page with no rows that also does not claim to be last would
			// loop forever. Report what there is and say it is not exhaustive.
			return truncated(out, token)
		}
	}
}

// truncated is the one place a result becomes incomplete.
//
// Four paths through ListStream stop early: the request budget ran out
// mid-run, the limit was already satisfied before a page was asked for, the
// limit was reached by the page just fetched, or the server sent an empty page
// without claiming it was the last. All four mean one thing to a caller, and
// all four have to say it the same way — not complete, plus the cursor that
// would have fetched the next page.
//
// They were four spellings of that answer: two computing it inline and two
// falling through to a shared postlude, which also meant the postlude could not
// say which case had reached it. `complete="false"` or exit 3 is the single
// invariant this project exists to hold, and this is where it is decided for
// the most-used command in the tool. Four sites computing one answer is four
// places for them to drift, and the drift would be silent in the one way that
// matters: a result reported complete when it was cut short.
//
// The error return is always nil. It is there so every early exit is one line
// that reads as a return of the same answer, rather than two assignments a
// future edit can separate.
func truncated(out *ListResult, token PageToken) (*ListResult, error) {
	out.Complete = false
	out.NextPageToken = EncodePageToken(token)
	return out, nil
}

// canKeyset reports whether this request can page by key rather than by offset.
//
// Three things have to hold. The deployment must be offset-paginated, since
// Cloud's cursors are already stable. The query must be built here, so the
// keyset bound can go through the builder. And the ordering must be the key
// ordering, because a cursor is only meaningful in the order it was taken in.
func (c *Client) canKeyset(opt ListOptions) bool {
	return !c.Site.CursorPaginated() && opt.JQL == "" && opt.Query.SortsByKey()
}

// advance computes the token for the next page and reports whether this was the
// last one.
func (c *Client) advance(
	keyset bool, current PageToken, page *searchResponse, issues []Issue, want int,
) (PageToken, bool) {
	got := len(issues)

	if c.Site.CursorPaginated() {
		// Cloud is authoritative about the end: isLast, or no further token.
		if (page.IsLast != nil && *page.IsLast) || page.NextPageToken == "" {
			return PageToken{}, true
		}
		return PageToken{Deployment: c.Site.Kind, Cursor: page.NextPageToken}, false
	}
	if got == 0 {
		return PageToken{}, true
	}
	if keyset {
		return c.advanceKeyset(page, issues, want)
	}

	// Offset paging. The total is the cheapest way to know the end, and also
	// the reason this mode is unstable: the count it is compared against
	// changes as issues are created.
	next := current.Offset + got
	if page.StartAt != nil {
		next = *page.StartAt + got
	}
	if page.Total != nil && next >= *page.Total {
		return PageToken{}, true
	}
	return PageToken{Deployment: c.Site.Kind, Offset: next}, false
}

// advanceKeyset computes the next keyset cursor from the last row of a page.
func (c *Client) advanceKeyset(page *searchResponse, issues []Issue, want int) (PageToken, bool) {
	got := len(issues)
	if got == 0 {
		return PageToken{}, true
	}
	// A short page means the result set ran out: with a keyset bound there is
	// no offset arithmetic to second-guess it.
	if got < want {
		return PageToken{}, true
	}
	if page.Total != nil && got >= *page.Total {
		return PageToken{}, true
	}
	return PageToken{
		Deployment: c.Site.Kind,
		AfterKey:   issues[got-1].Key,
	}, false
}

// verifyDescendingBelow checks that a keyset page really is ordered the way this
// client assumed, and really does start below the cursor.
//
// It exists because the whole scheme rests on JQL comparing issue keys by
// project and number. If a server compared them as text instead, "ENG-1000"
// would sort below "ENG-999" and pages would silently skip issues. This turns
// that into an error naming the two keys.
func verifyDescendingBelow(issues []Issue, afterKey string) error {
	bound, hasBound := ParseKey(afterKey)

	var previous Key
	for i, issue := range issues {
		key, ok := ParseKey(issue.Key)
		if !ok {
			return errs.Remote("UNPARSEABLE_KEY",
				"Jira returned an issue key this client cannot order").
				WithDetail("%q", issue.Key).
				WithRemedy("re-run with --sort updated to page by offset instead")
		}
		if hasBound && !key.Before(bound) {
			return errs.Remote("PAGINATION_ORDER",
				"Jira returned an issue at or above the page cursor").
				WithDetail("cursor %s, got %s", bound, key).
				WithRemedy("this client's key ordering disagrees with the server's; " +
					"re-run with --sort updated to page by offset instead")
		}
		if i > 0 && !key.Before(previous) {
			return errs.Remote("PAGINATION_ORDER",
				"Jira returned issues out of descending key order").
				WithDetail("%s followed %s", key, previous).
				WithRemedy("this client's key ordering disagrees with the server's; " +
					"re-run with --sort updated to page by offset instead")
		}
		previous = key
	}
	return nil
}

// renderQuery builds the JQL for one page.
//
// A keyset bound is added through the builder rather than appended to a
// rendered string, because appending would be concatenating JQL — the one thing
// this project does not do.
func renderQuery(opt ListOptions, token PageToken) (string, error) {
	if opt.JQL != "" {
		return opt.JQL, nil
	}
	q := opt.Query
	q.BeforeKey = token.AfterKey
	return BuildQuery(q)
}

func (c *Client) fetch(
	ctx context.Context, opt ListOptions, token PageToken, want int,
) (*searchResponse, error) {
	jqlText, err := renderQuery(opt, token)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("jql", jqlText)
	query.Set("maxResults", strconv.Itoa(want))
	if len(opt.Fields) > 0 {
		query.Set("fields", strings.Join(opt.Fields, ","))
	}
	if param, value, ok := token.resumeValue(c.Site); ok {
		query.Set(param, value)
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.searchPath(),
		Query:  query,
	})
	if err != nil {
		return nil, err
	}

	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var page searchResponse
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return nil, errs.Remote("MALFORMED_SEARCH",
			"Jira returned a search result this tool cannot read").
			WithRequestID(resp.RequestID).Wrap(err)
	}
	return &page, nil
}

// resolvePageSize validates --page-size.
//
// A value above the server's cap is refused rather than clamped. Clamping would
// mean the tool silently did something other than what was asked, which is the
// habit this project exists to break.
func resolvePageSize(n int) (int, error) {
	if n == 0 {
		return DefaultPageSize, nil
	}
	if n < MinPageSize || n > MaxPageSize {
		return 0, errs.Usage("INVALID_PAGE_SIZE",
			"--page-size must be between %d and %d", MinPageSize, MaxPageSize).
			WithDetail("got %d", n).
			WithRemedy("--page-size tunes the transport; use --limit to say how many " +
				"results you want")
	}
	return n, nil
}

// Get fetches one issue.
//
// The key is validated locally first. Sending "foo" to Jira costs a round trip
// to be told what this package can see for itself, and a 404 for a malformed
// key reads like a missing issue rather than a typo.
func (c *Client) Get(ctx context.Context, key string, fields []string) (Issue, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return Issue{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}

	query := url.Values{}
	if len(fields) > 0 {
		query.Set("fields", strings.Join(fields, ","))
	}

	resp, err := c.Transport.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   c.Site.APIBase() + "/issue/" + parsed.String(),
		Query:  query,
	})
	if err != nil {
		return Issue{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Issue{}, err
	}

	issues, err := decodeIssues([]json.RawMessage{resp.Body}, ExtraFieldNames(fields), c.Body)
	if err != nil {
		return Issue{}, err
	}
	if len(issues) != 1 {
		return Issue{}, errs.Remote("MALFORMED_ISSUE",
			"Jira returned no usable issue for %s", parsed).
			WithRequestID(resp.RequestID)
	}
	return issues[0], nil
}
