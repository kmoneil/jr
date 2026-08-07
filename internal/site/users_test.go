package site_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// cloudDirectory and dcDirectory carry different identifiers on purpose.
//
// Cloud names a user by accountId and Data Center by name. A fixture carrying
// both would hide which one the code picked — which is exactly how a /myself
// fixture once tested nothing at all.
const cloudDirectory = `[
	{"accountId":"712020:8f3a","displayName":"Ada Lovelace",
	 "emailAddress":"ada@example.invalid","active":true,"accountType":"atlassian"},
	{"accountId":"712020:9c1b","displayName":"Grace Hopper",
	 "emailAddress":"grace@example.invalid","active":true,"accountType":"atlassian"}
]`

const dcDirectory = `[
	{"name":"ada","key":"ada","displayName":"Ada Lovelace",
	 "emailAddress":"ada@example.invalid","active":true},
	{"name":"grace","key":"grace","displayName":"Grace Hopper",
	 "emailAddress":"grace@example.invalid","active":true}
]`

// queryRecordingDoer remembers the request, so a test can see which parameter
// a deployment was sent as well as what came back.
type queryRecordingDoer struct {
	stubDoer
	bodies   map[string]string
	requests []transport.Request
}

func (q *queryRecordingDoer) Do(
	ctx context.Context, r transport.Request,
) (*transport.Response, error) {
	q.requests = append(q.requests, r)

	// Longest key first: "/user" is a prefix of "/user/search", and a map
	// iterated in whatever order handed the search endpoint the single-user
	// body about a third of the time.
	paths := slices.SortedFunc(maps.Keys(q.bodies), func(a, b string) int {
		return len(b) - len(a)
	})
	for _, path := range paths {
		if strings.Contains(r.Path, path) {
			q.body = q.bodies[path]
			break
		}
	}
	return q.stubDoer.Do(ctx, r)
}

func directory(kind site.Kind) *queryRecordingDoer {
	body := cloudDirectory
	if kind != site.Cloud {
		body = dcDirectory
	}
	return &queryRecordingDoer{bodies: map[string]string{"/user/search": body}}
}

// TestSearchUsersSendsTheParameterTheDeploymentTakes is the split that has to
// live in one place. Sending the wrong parameter is not an error — it is an
// empty result, which reads as "nobody by that name" and is not.
func TestSearchUsersSendsTheParameterTheDeploymentTakes(t *testing.T) {
	for _, tc := range []struct {
		kind  site.Kind
		param string
		path  string
	}{
		{site.Cloud, "query", "/rest/api/3/user/search"},
		{site.DataCenter, "username", "/rest/api/2/user/search"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			doer := directory(tc.kind)
			if _, err := site.SearchUsers(
				context.Background(), doer, site.Info{Kind: tc.kind}, "ada", 20,
			); err != nil {
				t.Fatalf("search: %v", err)
			}

			req := doer.requests[0]
			if req.Path != tc.path {
				t.Errorf("path = %q, want %q", req.Path, tc.path)
			}
			if got := req.Query.Get(tc.param); got != "ada" {
				t.Errorf("%s = %q, want the query", tc.param, got)
			}
		})
	}
}

// TestResolveUserFindsTheIdThisDeploymentWants is the whole point: a caller
// types a name, and each deployment gets the identifier it recognises.
func TestResolveUserFindsTheIdThisDeploymentWants(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		want string
	}{
		{site.Cloud, "712020:8f3a"},
		{site.DataCenter, "ada"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			for _, input := range []string{
				"Ada Lovelace",        // the display name
				"ada lovelace",        // and it does not care about case
				"ada@example.invalid", // the email address
				tc.want,               // the id, for a caller who already has one
			} {
				got, err := site.ResolveUser(
					context.Background(), directory(tc.kind), site.Info{Kind: tc.kind}, input,
				)
				if err != nil {
					t.Fatalf("resolve %q: %v", input, err)
				}
				if got.ID != tc.want {
					t.Errorf("resolve %q = %q, want %q", input, got.ID, tc.want)
				}
			}
		})
	}
}

// TestResolveUserRefusesAPartialMatch is the decision this rests on.
//
// The server's search is fuzzy and `ada` matches exactly one person today.
// Resolving it would mean the same command names somebody else the day a
// second Ada joins, and nothing in between would report a problem. The near
// miss is named instead, so the caller can copy what would have worked.
func TestResolveUserRefusesAPartialMatch(t *testing.T) {
	_, err := site.ResolveUser(
		context.Background(), directory(site.Cloud), site.Info{Kind: site.Cloud}, "ada",
	)
	if err == nil {
		t.Fatal("a partial match was resolved")
	}

	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_USER" {
		t.Errorf("code = %q, want UNKNOWN_USER", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
	}
	// Naming the candidate is what makes the refusal actionable rather than a
	// dead end: the id is what a caller passes to end the argument.
	if !strings.Contains(e.Detail, "Ada Lovelace") || !strings.Contains(e.Detail, "712020:8f3a") {
		t.Errorf("detail %q names neither the user nor the id", e.Detail)
	}
}

// TestResolveUserRefusesAnAmbiguousName covers §5.2 directly. Two people can
// share a display name, and picking one assigns somebody else's work to them.
func TestResolveUserRefusesAnAmbiguousName(t *testing.T) {
	const twoAdas = `[
		{"accountId":"712020:8f3a","displayName":"Ada Lovelace",
		 "emailAddress":"ada@example.invalid","active":true},
		{"accountId":"712020:0d4e","displayName":"Ada Lovelace",
		 "emailAddress":"ada.l@example.invalid","active":false}
	]`

	doer := &queryRecordingDoer{bodies: map[string]string{"/user/search": twoAdas}}
	_, err := site.ResolveUser(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "Ada Lovelace",
	)
	if err == nil {
		t.Fatal("an ambiguous name was resolved to one of them")
	}

	e := errs.Coerce(err)
	if e.Code != "AMBIGUOUS_USER" {
		t.Errorf("code = %q, want AMBIGUOUS_USER", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
	}
	for _, want := range []string{"712020:8f3a", "712020:0d4e"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("detail %q omits candidate %s", e.Detail, want)
		}
	}
	// Which of two accounts is still in use is most of how a caller tells them
	// apart, and it is the one thing the display name cannot say.
	if !strings.Contains(e.Detail, "inactive") {
		t.Errorf("detail %q does not say one of them is inactive", e.Detail)
	}
}

// TestResolveUserAsksDirectlyWhenSearchFindsNothing covers the caller who
// already has an id. Cloud's search matches a display name and an email and
// not an accountId, so the only spelling that used to work would otherwise
// have stopped working.
func TestResolveUserAsksDirectlyWhenSearchFindsNothing(t *testing.T) {
	const one = `{"accountId":"712020:8f3a","displayName":"Ada Lovelace","active":true}`

	doer := &queryRecordingDoer{bodies: map[string]string{
		"/user/search": `[]`,
		"/user":        one,
	}}
	got, err := site.ResolveUser(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "712020:8f3a",
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != "712020:8f3a" {
		t.Errorf("id = %q", got.ID)
	}
	if len(doer.requests) != 2 {
		t.Fatalf("made %d requests, want a search and then a lookup", len(doer.requests))
	}
	if param := doer.requests[1].Query.Get("accountId"); param != "712020:8f3a" {
		t.Errorf("the lookup asked for accountId=%q", param)
	}
}

// TestResolveUserSaysWhereToLookWhenNobodyMatches keeps the dead end from
// being a dead end.
func TestResolveUserSaysWhereToLookWhenNobodyMatches(t *testing.T) {
	doer := &queryRecordingDoer{bodies: map[string]string{
		"/user/search": `[]`,
		"/user":        `{}`,
	}}
	_, err := site.ResolveUser(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "nobody",
	)
	if err == nil {
		t.Fatal("a name matching nothing was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_USER" {
		t.Errorf("code = %q, want UNKNOWN_USER", e.Code)
	}
	if !strings.Contains(e.Remedy, "user list") {
		t.Errorf("remedy %q does not say how to find one", e.Remedy)
	}
}

// TestAnEntryWithNoIdIsNotACandidate covers a directory row this deployment
// gave no identifier to. Offering it names somebody who cannot be passed back.
func TestAnEntryWithNoIdIsNotACandidate(t *testing.T) {
	const nameless = `[{"displayName":"Ghost","active":true}]`

	doer := &queryRecordingDoer{bodies: map[string]string{"/user/search": nameless}}
	got, err := site.SearchUsers(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "ghost", 20,
	)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Users) != 0 {
		t.Errorf("got %+v, want an entry with no id dropped", got.Users)
	}
}

// TestResolveUserRefusesAnEmptyName covers the degenerate input, so it fails
// here rather than searching the directory for nothing.
func TestResolveUserRefusesAnEmptyName(t *testing.T) {
	doer := &queryRecordingDoer{bodies: map[string]string{"/user/search": cloudDirectory}}
	if _, err := site.ResolveUser(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "  ",
	); err == nil {
		t.Fatal("an empty name was accepted")
	}
	if len(doer.requests) != 0 {
		t.Errorf("made %d requests for a name that cannot match", len(doer.requests))
	}
}

// TestAnImplausibleCandidateIsNotOffered covers what Cloud does with a query
// it cannot match: it answers with whatever it has. "Atlassian Assist" as the
// closest thing to "Nobody At All" reads as a near miss and is not one.
func TestAnImplausibleCandidateIsNotOffered(t *testing.T) {
	const apps = `[
		{"accountId":"630db2cd","displayName":"Atlas for Jira Cloud",
		 "active":true,"accountType":"app"},
		{"accountId":"5cb4ae0e","displayName":"Atlassian Assist",
		 "active":true,"accountType":"app"}
	]`

	doer := &queryRecordingDoer{bodies: map[string]string{
		"/user/search": apps,
		"/user":        `{}`,
	}}
	_, err := site.ResolveUser(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "Nobody At All",
	)
	if err == nil {
		t.Fatal("a name matching nothing was accepted")
	}

	e := errs.Coerce(err)
	if e.Detail != "" {
		t.Errorf("detail %q offers a candidate that shares nothing with the input", e.Detail)
	}
	if !strings.Contains(e.Remedy, "user list") {
		t.Errorf("remedy %q does not say how to find one", e.Remedy)
	}
}

// TestACandidateSaysWhatKindOfAccountItIs covers the app account. Jira accepts
// one as an assignee and a person never looks at it.
func TestACandidateSaysWhatKindOfAccountItIs(t *testing.T) {
	const mixed = `[
		{"accountId":"630db2cd","displayName":"Assist Bot",
		 "active":true,"accountType":"app"},
		{"accountId":"712020:8f3a","displayName":"Assist Ada",
		 "active":true,"accountType":"atlassian"}
	]`

	doer := &queryRecordingDoer{bodies: map[string]string{
		"/user/search": mixed,
		"/user":        `{}`,
	}}
	_, err := site.ResolveUser(
		context.Background(), doer, site.Info{Kind: site.Cloud}, "assist",
	)
	if err == nil {
		t.Fatal("a partial match was resolved")
	}

	e := errs.Coerce(err)
	if !strings.Contains(e.Detail, "[app]") {
		t.Errorf("detail %q does not say which candidate is an app account", e.Detail)
	}
	if strings.Contains(e.Detail, "[atlassian]") {
		t.Errorf("detail %q labels an ordinary account, which is noise", e.Detail)
	}
}
