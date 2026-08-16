package site

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// Asking Jira whether a query means anything lives here rather than in the jql
// resource, because two resources need the same answer and a resource may not
// import another one. `jr jql validate` reports it; `issue list` refuses on it.
//
// It is site knowledge in its own right: the question is answered by a
// different endpoint on each deployment, and which one is available is a fact
// about the site. That is the same reason ResolveUser and the field catalogue
// are here.

// How a verdict was reached. They are not quite the same claim, so the answer
// says which.
const (
	// JQLByParse is Cloud's endpoint that exists for this and nothing else.
	JQLByParse = "parse"
	// JQLBySearch is Data Center, which has no parse endpoint. A search bounded
	// to zero rows parses and permission-checks the query and returns no
	// issues, which is the closest thing available.
	JQLBySearch = "search"
)

// JQLCheck is what Jira said about a query.
type JQLCheck struct {
	Query string
	Valid bool
	// Errors are Jira's own messages, unedited. They carry the position, which
	// is the part worth having and the part a rewrite would lose.
	Errors []string
	// Warnings are what Jira flags without refusing. On Cloud they are the only
	// report of a value that does not exist for a field, which is the class the
	// search endpoint answers with a confident empty result.
	Warnings []string
	// Method is JQLByParse or JQLBySearch.
	Method string
}

// CheckJQL asks Jira whether the query parses and means something.
//
// A query Jira rejects is a result rather than an error: the caller asked
// whether it is valid and found out. Only a failure to get an answer at all,
// no credential, no network, a 500, fails.
func CheckJQL(ctx context.Context, client Doer, info Info, query string) (JQLCheck, error) {
	if info.Kind == Cloud {
		return checkByParse(ctx, client, info, query)
	}
	return checkBySearch(ctx, client, info, query)
}

// CheckJQL resolves against the site this metadata belongs to.
func (m *Metadata) CheckJQL(ctx context.Context, query string) (JQLCheck, error) {
	return CheckJQL(ctx, m.Client, m.Info, query)
}

func checkByParse(ctx context.Context, client Doer, info Info, query string) (JQLCheck, error) {
	path := info.APIBase() + "/jql/parse"

	body, err := json.Marshal(map[string]any{"queries": []string{query}})
	if err != nil {
		return JQLCheck{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the query").Wrap(err)
	}

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodPost,
		Path:   path,
		// A query Jira would run with warnings is not one to call valid without
		// saying so. That was the argument for `strict`, and `strict` is the
		// mode that does not do it.
		//
		// Measured 2026-08-14 against a Cloud site, on `assignee = "nobody"`:
		// `strict` returns neither an errors key nor a warnings key, `warn`
		// returns the warning naming the value, and `none` returns both keys
		// empty. So the silence under `strict` is a dropped diagnostic rather
		// than a clean verdict, and every user-valued operand was invisible
		// here. Data Center never had the gap, because checkBySearch below reads
		// warningMessages off the search it already makes.
		//
		// `warn` was swept against `strict` over 25 queries and is a superset:
		// identical errors on every one, five warnings `strict` withheld, and
		// clean over twelve queries that legitimately match nothing. Nothing
		// here promotes a warning to invalid, so the only change is that the
		// warnings Jira sends reach the caller.
		Query:  url.Values{"validation": {"warn"}},
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   body,
	})
	if err != nil {
		return JQLCheck{}, err
	}
	if err := transport.Err(resp); err != nil {
		return JQLCheck{}, err
	}

	var parsed struct {
		Queries []struct {
			Query    string   `json:"query"`
			Errors   []string `json:"errors"`
			Warnings []string `json:"warnings"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(resp.Body, &parsed); err != nil || len(parsed.Queries) == 0 {
		return JQLCheck{}, errs.Remote("MALFORMED_PARSE_RESULT",
			"%s did not return a usable parse result", path).
			WithRequestID(resp.RequestID).Wrap(err)
	}

	first := parsed.Queries[0]
	return JQLCheck{
		Query: query, Valid: len(first.Errors) == 0,
		Errors: first.Errors, Warnings: first.Warnings,
		Method: JQLByParse,
	}, nil
}

// checkBySearch is Data Center, which has no parse endpoint.
//
// maxResults=0 is what makes this a check rather than a query: Jira parses the
// JQL, applies permissions, and returns the total with no issues attached. It
// is a heavier answer than a parse and it is the only one available, which is
// why the result says which was used.
func checkBySearch(ctx context.Context, client Doer, info Info, query string) (JQLCheck, error) {
	path := info.APIBase() + "/search"

	body, err := json.Marshal(map[string]any{
		// validateQuery is a boolean here. "strict" is Cloud's spelling on its
		// own parse endpoint, and sending it to Data Center is a deserialization
		// error that arrives as `valid="false"`, a working query reported
		// broken, which is the worst answer this can give.
		"jql": query, "maxResults": 0, "validateQuery": true,
	})
	if err != nil {
		return JQLCheck{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the query").Wrap(err)
	}

	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodPost,
		Path:   path,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   body,
	})
	if err != nil {
		return JQLCheck{}, err
	}

	// A 400 here is the answer, not a failure: it is how this deployment says
	// the query does not parse. Every other status is a real failure and goes
	// through the normal mapping.
	if resp.Status == 400 {
		return JQLCheck{
			Query: query, Valid: false,
			Errors: JQLMessages(resp.Body), Method: JQLBySearch,
		}, nil
	}
	if err := transport.Err(resp); err != nil {
		return JQLCheck{}, err
	}

	var parsed struct {
		WarningMessages []string `json:"warningMessages"`
	}
	// A body that does not decode is not worth failing a successful check for:
	// the query parsed, which is what was asked. Warnings are the only thing
	// lost, and they are absent far more often than they are present.
	_ = json.Unmarshal(resp.Body, &parsed)

	return JQLCheck{
		Query: query, Valid: true,
		Warnings: parsed.WarningMessages, Method: JQLBySearch,
	}, nil
}

// JQLMessages pulls Jira's own error text out of a refusal.
//
// The messages are passed through unedited because they carry the position,
// "(line 1, character 20)", and that is the one thing a rewrite would lose.
func JQLMessages(body []byte) []string {
	var parsed struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return []string{"Jira refused the query and gave no readable reason"}
	}

	out := parsed.ErrorMessages
	// Data Center puts a JQL complaint under the "jql" key rather than in the
	// message list, depending on the version.
	for _, key := range []string{"jql", "jqlQuery"} {
		if msg, ok := parsed.Errors[key]; ok {
			out = append(out, msg)
		}
	}
	if len(out) == 0 {
		return []string{"Jira refused the query and gave no readable reason"}
	}
	return out
}
