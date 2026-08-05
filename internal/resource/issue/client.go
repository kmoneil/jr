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
}

// ListOptions is one `issue list` request.
type ListOptions struct {
	// JQL is the fully rendered query. It is a string here because
	// internal/jql owns building it, and this package must never concatenate
	// one.
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
func (c *Client) List(ctx context.Context, opt ListOptions) (*ListResult, error) {
	pageSize, err := resolvePageSize(opt.PageSize)
	if err != nil {
		return nil, err
	}
	token, err := DecodePageToken(opt.PageToken, c.Site.Kind)
	if err != nil {
		return nil, err
	}

	out := &ListResult{}
	for {
		want := pageSize
		if !opt.Limit.All {
			remaining := opt.Limit.N - len(out.Issues)
			if remaining <= 0 {
				break
			}
			// Never ask for more than is wanted: over-fetching costs the
			// caller's rate limit for rows that are thrown away.
			want = min(want, remaining)
		}

		page, err := c.fetch(ctx, opt, token, want)
		if err != nil {
			// A spent request budget is not a failure: it means there is more,
			// and the caller gets what was fetched plus a way to resume.
			if transport.IsBudgetExceeded(err) && len(out.Issues) > 0 {
				out.NextPageToken = EncodePageToken(token)
				out.Complete = false
				return out, nil
			}
			return nil, err
		}
		out.Requests++

		issues, err := decodeIssues(page.Issues)
		if err != nil {
			return nil, err
		}
		out.Issues = append(out.Issues, issues...)

		next, last := c.advance(token, page, len(issues))
		if last {
			out.Complete = true
			return out, nil
		}
		token = next

		if !opt.Limit.All && len(out.Issues) >= opt.Limit.N {
			break
		}
		if len(issues) == 0 {
			// A page with no rows that also does not claim to be last would
			// loop forever. Report what there is and say it is not exhaustive.
			out.Complete = false
			out.NextPageToken = EncodePageToken(token)
			return out, nil
		}
	}

	// The loop ended because the limit was reached, not because the result set
	// ran out, so this is explicitly not complete.
	out.Complete = false
	out.NextPageToken = EncodePageToken(token)
	return out, nil
}

// advance computes the token for the next page and reports whether this was the
// last one.
func (c *Client) advance(current PageToken, page *searchResponse, got int) (PageToken, bool) {
	if c.Site.CursorPaginated() {
		// Cloud is authoritative about the end: isLast, or no further token.
		if (page.IsLast != nil && *page.IsLast) || page.NextPageToken == "" {
			return PageToken{}, true
		}
		return PageToken{Deployment: c.Site.Kind, Cursor: page.NextPageToken}, false
	}

	// Data Center pages by offset and reports a total.
	next := current.Offset + got
	if page.StartAt != nil {
		next = *page.StartAt + got
	}
	if page.Total != nil && next >= *page.Total {
		return PageToken{}, true
	}
	if got == 0 {
		return PageToken{}, true
	}
	return PageToken{Deployment: c.Site.Kind, Offset: next}, false
}

func (c *Client) fetch(
	ctx context.Context, opt ListOptions, token PageToken, want int,
) (*searchResponse, error) {
	query := url.Values{}
	query.Set("jql", opt.JQL)
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
