package site

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/transport"
)

// LabelSuggestPath is the endpoint that answers which labels a site knows,
// relative to the API base.
//
// It is not the obvious route. `/label` is the documented label catalogue and
// is the wrong one twice over, measured on 2026-08-13 against both
// deployments:
//
//   - It does not exist on Data Center. Jira Software 9.12.38 and 10.4.1
//     both answer `/rest/api/2/label` with a 404, the way they answer the
//     paged changelog route, so a check built on it would work on Cloud and
//     silently do nothing on the deployment where a large label set is most
//     likely.
//   - On Cloud it answers a different question. Its `query` parameter is
//     ignored, so verifying one label costs sweeping every label on the site;
//     and a label whose last issue was deleted stays in it, so it reports what
//     has ever been used rather than what a query would match.
//
// This route is the one Jira's own label picker uses. It exists on both
// deployments under each one's API base, it filters by value, and it is backed
// by the search index: a label vanishes from it the moment the last issue
// carrying it is deleted, which is the question `--label` is actually asking.
const LabelSuggestPath = "/jql/autocompletedata/suggestions"

// SuggestionCap is how many suggestions the server will return, measured as 15
// on Jira Software Data Center 9.12.38, on 10.4.1, and on Cloud.
//
// It is a ceiling on the answer and not a page size: there is no cursor and no
// total, so a caller cannot ask for the rest. That is survivable here only
// because a lookup asks for the whole label and not a prefix of one. The
// server filters before it truncates and orders what survives lexicographically
// ascending, and a string sorts before every string that extends it, so an
// exact match is the first result whenever it exists at all. A full result set
// with no exact match in it is still treated as no answer rather than as a
// missing label, because that reasoning rests on an ordering the server does
// not document.
const SuggestionCap = 15

// suggestResponse is the response shape, which both deployments share.
type suggestResponse struct {
	Results []struct {
		// Value is the label rendered as JQL, so `a,b` arrives as the five
		// characters "a,b" and a backslash arrives doubled. DisplayName is the
		// same label with HTML markup around the matched prefix, which is why
		// nothing here reads it.
		Value string `json:"value"`
	} `json:"results"`
}

// SuggestLabels returns the labels this site knows whose name begins with
// prefix, up to SuggestionCap of them. An empty prefix asks for any.
//
// The comparison is the server's: it matches case-insensitively and anchors at
// the start, so "pag" finds "pagination" and a substring from the middle of the
// same word finds nothing.
func SuggestLabels(
	ctx context.Context, client Doer, info Info, prefix string,
) ([]string, error) {
	path := info.APIBase() + LabelSuggestPath

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   path,
		// predicateName is deliberately not sent. Jira 9.12.38 and 10.4.1 both
		// answer a request carrying it with a 500.
		Query: url.Values{
			"fieldName":  {"labels"},
			"fieldValue": {prefix},
		},
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw suggestResponse
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, errs.Remote("MALFORMED_LABEL_SUGGESTIONS",
			"%s did not return usable label suggestions", path).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}

	labels := make([]string, 0, len(raw.Results))
	for _, r := range raw.Results {
		// A value this cannot read is refused rather than compared as it
		// stands. Comparing the rendered form against what the caller typed
		// would report every label needing quotes as unknown, which is the
		// defect this file exists to fix wearing the other hat.
		label, err := jql.Unquote(r.Value)
		if err != nil {
			return nil, errs.Remote("MALFORMED_LABEL_SUGGESTIONS",
				"%s returned a label this cannot read", path).
				WithDetail("%q", r.Value).
				WithRequestID(resp.RequestID).
				Wrap(err)
		}
		labels = append(labels, label)
	}
	return labels, nil
}

// UnknownLabels returns the members of want that no issue on this site carries.
//
// It costs one request per distinct label, and one more only when it is about
// to report something. Labels are deliberately not cached: the field catalogue
// can be a day stale because a custom field id does not change, and a label
// exists exactly as long as an issue carries it, so a cached answer would be
// wrong in the direction that matters, reporting a label as unknown because
// nothing carried it this morning.
//
// An empty result means nothing was found to report, which is not the same as
// every label existing. Three cases return no unknowns rather than a verdict,
// and each one is a place this would otherwise invent a warning:
//
//   - the server filled its answer to SuggestionCap, so the exact match may be
//     past the end of it;
//   - the site reports no labels at all, which a site with no labels and a
//     route that answers nothing produce identically;
//   - anything failed, which the caller's own query is about to report
//     properly if it matters.
func UnknownLabels(
	ctx context.Context, client Doer, info Info, want []string,
) ([]string, error) {
	var unknown []string
	seen := make(map[string]bool, len(want))

	for _, w := range want {
		label := strings.TrimSpace(w)
		if label == "" {
			continue
		}
		// Folded, because the server matches that way: asking twice about two
		// spellings of one label would spend a request to learn the same
		// thing and report it twice.
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true

		found, err := SuggestLabels(ctx, client, info, label)
		if err != nil {
			return nil, err
		}
		if hasFold(found, label) || len(found) >= SuggestionCap {
			continue
		}
		unknown = append(unknown, label)
	}

	if len(unknown) == 0 {
		return nil, nil
	}

	// The route answers 200 with an empty list for a field name it does not
	// know, so "no labels match" and "this endpoint tells me nothing" are the
	// same bytes. One request separates them, and it is spent only on the path
	// that was going to warn.
	any, err := SuggestLabels(ctx, client, info, "")
	if err != nil {
		return nil, err
	}
	if len(any) == 0 {
		return nil, nil
	}
	return unknown, nil
}

// UnknownLabels resolves against the site this metadata belongs to.
func (m *Metadata) UnknownLabels(ctx context.Context, want []string) ([]string, error) {
	return UnknownLabels(ctx, m.Client, m.Info, want)
}
