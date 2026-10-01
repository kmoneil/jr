package site

import (
	"context"
	"encoding/json"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// Counting what a query matches lives beside CheckJQL for the same reason: the
// answer comes from a different endpoint on each deployment, and which one is
// available is a fact about the site.

// IssueCount is how many issues a query matched, and how the server knew.
type IssueCount struct {
	Issues int
	// Approximate is Cloud, whose only count is documented as an estimate that
	// can trail a very recent update. Data Center's total is exact.
	Approximate bool
}

// CountIssues asks Jira how many issues a query matches, fetching none of them.
//
// It is one request on both deployments. Data Center counts with the zero-row
// search its validity check already sends. Cloud's search reports no total, so
// the count comes from its approximate-count endpoint, which answers a query it
// cannot parse with a count of zero at HTTP 200: it is no validity check, and a
// caller has to have checked the query first.
func CountIssues(ctx context.Context, client Doer, info Info, query string) (IssueCount, error) {
	if info.Kind == Cloud {
		return countApproximately(ctx, client, info, query)
	}
	return countBySearch(ctx, client, info, query)
}

// CountIssues counts against the site this metadata belongs to.
func (m *Metadata) CountIssues(ctx context.Context, query string) (IssueCount, error) {
	return CountIssues(ctx, m.Client, m.Info, query)
}

func countBySearch(ctx context.Context, client Doer, info Info, query string) (IssueCount, error) {
	resp, err := searchNoRows(ctx, client, info, query)
	if err != nil {
		return IssueCount{}, err
	}
	if err := transport.Err(resp); err != nil {
		return IssueCount{}, err
	}
	var parsed struct {
		Total *int `json:"total"`
	}
	if err := json.Unmarshal(resp.Body, &parsed); err != nil || parsed.Total == nil {
		return IssueCount{}, malformedCount(info.APIBase()+"/search", resp).Wrap(err)
	}
	return IssueCount{Issues: *parsed.Total}, nil
}

func countApproximately(ctx context.Context, client Doer, info Info, query string) (IssueCount, error) {
	path := info.APIBase() + "/search/approximate-count"
	body, err := json.Marshal(map[string]any{"jql": query})
	if err != nil {
		return IssueCount{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the query").Wrap(err)
	}
	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodPost,
		Path:   path,
		// A count: sending it twice reads twice and changes nothing.
		Idempotent: true,
		Header:     map[string][]string{"Content-Type": {"application/json"}},
		Body:       body,
	})
	if err != nil {
		return IssueCount{}, err
	}
	if err := transport.Err(resp); err != nil {
		return IssueCount{}, err
	}
	var parsed struct {
		Count *int `json:"count"`
	}
	if err := json.Unmarshal(resp.Body, &parsed); err != nil || parsed.Count == nil {
		return IssueCount{}, malformedCount(path, resp).Wrap(err)
	}
	return IssueCount{Issues: *parsed.Count, Approximate: true}, nil
}

// malformedCount is a count response with no count in it. An absent count is
// not zero: reading it as zero would size every sweep as free.
func malformedCount(path string, resp *transport.Response) *errs.Error {
	return errs.Remote("MALFORMED_COUNT", "%s did not return a usable count", path).
		WithRequestID(resp.RequestID)
}
