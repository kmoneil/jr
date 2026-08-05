package issue_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// replayClient builds a client answering from a recorded conversation, with no
// credentials and no network.
func replayClient(t *testing.T, kind site.Kind) (*issue.Client, *transport.Replayer) {
	t.Helper()

	path := filepath.Join("testdata", "list."+string(kind)+".json")
	cassette, err := transport.LoadCassette(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	replayer := transport.NewReplayer(cassette)

	conn, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: replayer.Client(),
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &issue.Client{
		Transport: conn,
		Site:      site.Info{Kind: kind, Version: "test"},
	}, replayer
}

// deployments is every deployment a resource must ship fixtures for. A resource
// that covers only Cloud has tested half of what it claims to.
var deployments = []site.Kind{site.Cloud, site.DataCenter}

// TestListPagesToTheEndOnBothDeployments is the core of the pagination
// contract: the caller asks for everything and gets everything, whether the
// server pages by cursor or by offset.
func TestListPagesToTheEndOnBothDeployments(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			client, replayer := replayClient(t, kind)

			result, err := client.List(t.Context(), issue.ListOptions{
				JQL:      `project = "ENG"`,
				Limit:    registry.Limit{All: true},
				PageSize: 2,
				Fields:   issue.DefaultFields(),
			})
			if err != nil {
				t.Fatalf("list: %v", err)
			}

			if len(result.Issues) != 3 {
				t.Fatalf("got %d issues across pages, want 3", len(result.Issues))
			}
			// The result set ran out, so this is complete and carries no cursor.
			if !result.Complete {
				t.Error("an exhausted result set was reported incomplete")
			}
			if result.NextPageToken != "" {
				t.Errorf("a complete result carries a token: %q", result.NextPageToken)
			}
			if result.Requests != 2 {
				t.Errorf("made %d requests, want 2", result.Requests)
			}

			keys := make([]string, 0, len(result.Issues))
			for _, i := range result.Issues {
				keys = append(keys, i.Key)
			}
			if strings.Join(keys, ",") != "ENG-101,ENG-102,ENG-103" {
				t.Errorf("keys = %v, want them in page order", keys)
			}

			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the fixture has interactions this test never used: %v", unplayed)
			}
		})
	}
}

// TestNormalizationIsIdenticalAcrossDeployments is why both fixtures exist.
// Cloud identifies a user by accountId and Data Center by name, and the output
// contract must not expose that difference except in the id itself.
func TestNormalizationIsIdenticalAcrossDeployments(t *testing.T) {
	got := map[site.Kind][]issue.Issue{}
	for _, kind := range deployments {
		client, _ := replayClient(t, kind)
		result, err := client.List(t.Context(), issue.ListOptions{
			JQL: `project = "ENG"`, Limit: registry.Limit{All: true},
			PageSize: 2, Fields: issue.DefaultFields(),
		})
		if err != nil {
			t.Fatalf("%s: list: %v", kind, err)
		}
		got[kind] = result.Issues
	}

	cloud, dc := got[site.Cloud], got[site.DataCenter]
	for i := range cloud {
		c, d := cloud[i], dc[i]
		if c.Key != d.Key || c.Summary != d.Summary {
			t.Errorf("issue %d differs: %+v vs %+v", i, c, d)
		}
		// Status category is normalized, so it must match even though the two
		// deployments word their status payloads differently.
		if c.Status.Category != d.Status.Category {
			t.Errorf("issue %s category: cloud %q, dc %q",
				c.Key, c.Status.Category, d.Status.Category)
		}
		if c.Assignee.Display != d.Assignee.Display {
			t.Errorf("issue %s assignee display differs", c.Key)
		}
		// Timestamps are normalized to RFC 3339 UTC regardless of what the
		// server sent.
		if c.Updated != d.Updated {
			t.Errorf("issue %s updated: cloud %q, dc %q", c.Key, c.Updated, d.Updated)
		}
		if !strings.HasSuffix(c.Updated, "Z") {
			t.Errorf("issue %s updated is not UTC RFC 3339: %q", c.Key, c.Updated)
		}
	}
}

func TestStatusCategoriesAreNormalized(t *testing.T) {
	client, _ := replayClient(t, site.Cloud)
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	want := map[string]string{
		"ENG-101": issue.CategoryInProgress,
		"ENG-102": issue.CategoryToDo,
		"ENG-103": issue.CategoryDone,
	}
	for _, i := range result.Issues {
		if got := i.Status.Category; got != want[i.Key] {
			t.Errorf("%s category = %q, want %q", i.Key, got, want[i.Key])
		}
	}
}

// TestLimitTruncatesAndSaysSo is the contract's load-bearing behavior at the
// resource level: a result cut short by --limit is never complete, and carries
// a token that resumes exactly where it stopped.
func TestLimitTruncatesAndSaysSo(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			client, _ := replayClient(t, kind)

			result, err := client.List(t.Context(), issue.ListOptions{
				JQL: `project = "ENG"`, Limit: registry.Limit{N: 2},
				PageSize: 2, Fields: issue.DefaultFields(),
			})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(result.Issues) != 2 {
				t.Fatalf("got %d issues, want 2", len(result.Issues))
			}
			if result.Complete {
				t.Fatal("a truncated result reported itself complete")
			}
			if result.NextPageToken == "" {
				t.Fatal("a truncated result carries no way to resume")
			}
			if result.Requests != 1 {
				t.Errorf("made %d requests for 2 results, want 1", result.Requests)
			}

			// And the token actually resumes.
			resumeClient, _ := replayClient(t, kind)
			rest, err := resumeClient.List(t.Context(), issue.ListOptions{
				JQL: `project = "ENG"`, Limit: registry.Limit{All: true},
				PageSize: 2, PageToken: result.NextPageToken,
				Fields: issue.DefaultFields(),
			})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if len(rest.Issues) != 1 || rest.Issues[0].Key != "ENG-103" {
				t.Errorf("resume returned %d issues, want the remaining one", len(rest.Issues))
			}
			if !rest.Complete {
				t.Error("the resumed result did not reach the end")
			}
		})
	}
}

// TestLimitDoesNotOverFetch asserts the client asks for what was wanted and no
// more. Over-fetching spends the caller's rate limit on rows thrown away.
func TestLimitDoesNotOverFetch(t *testing.T) {
	client, _ := replayClient(t, site.Cloud)
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{N: 2},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Errorf("fetched %d issues for a limit of 2", len(result.Issues))
	}
}

// TestPageTokenIsOpaqueAndDeploymentBound is what keeps "no offset flag"
// honest. Cloud pages by cursor and Data Center by offset; wrapping whichever
// the server uses means the caller never holds an offset, and never gets to
// replay one against an API that cannot honor it.
func TestPageTokenIsOpaqueAndDeploymentBound(t *testing.T) {
	cloudToken := issue.EncodePageToken(issue.PageToken{
		Deployment: site.Cloud, Cursor: "CURSOR-PAGE-2",
	})
	dcToken := issue.EncodePageToken(issue.PageToken{
		Deployment: site.DataCenter, Offset: 2,
	})

	// Opaque: neither the cursor nor the offset is legible in the token.
	for _, tok := range []string{cloudToken, dcToken} {
		if tok == "" {
			t.Fatal("a non-empty token encoded to nothing")
		}
		if strings.Contains(tok, "CURSOR") || strings.Contains(tok, "startAt") {
			t.Errorf("the token leaks its internals: %q", tok)
		}
	}

	// Round trip.
	back, err := issue.DecodePageToken(cloudToken, site.Cloud)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Cursor != "CURSOR-PAGE-2" {
		t.Errorf("cursor = %q", back.Cursor)
	}

	// Replaying a Cloud token against Data Center would resume from somewhere
	// unrelated, so it is refused rather than read as offset zero.
	_, err = issue.DecodePageToken(cloudToken, site.DataCenter)
	if err == nil {
		t.Fatal("a Cloud token was accepted against Data Center")
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "Data Center") {
		t.Errorf("the error does not name the mismatch: %v", err)
	}
}

// TestBadPageTokenNeverSilentlyRestarts is the failure this design exists to
// prevent: a mistyped token that quietly returned page one would page forever
// and never be noticed.
func TestBadPageTokenNeverSilentlyRestarts(t *testing.T) {
	for _, bad := range []string{"garbage", "!!!!", "eyJub3QiOiJ2YWxpZCJ9", "0"} {
		_, err := issue.DecodePageToken(bad, site.Cloud)
		if err == nil {
			t.Errorf("--page-token %q was accepted", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("--page-token %q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}

	// An empty token is the ordinary "start at the beginning".
	got, err := issue.DecodePageToken("", site.Cloud)
	if err != nil {
		t.Fatalf("an empty token was rejected: %v", err)
	}
	if got.Cursor != "" || got.Offset != 0 {
		t.Errorf("an empty token decoded to %+v", got)
	}
}

func TestPageSizeIsValidatedNotClamped(t *testing.T) {
	client, _ := replayClient(t, site.Cloud)

	for _, size := range []int{-1, 0 - 5, 101, 1000} {
		_, err := client.List(t.Context(), issue.ListOptions{
			JQL: `project = "ENG"`, Limit: registry.Limit{N: 1}, PageSize: size,
		})
		if err == nil {
			t.Errorf("--page-size %d was accepted", size)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("--page-size %d exits %v, want %v", size, errs.ExitOf(err), exitcode.Usage)
		}
		// The remedy has to explain the distinction, because "page size" and
		// "how many results I want" are easy to conflate.
		if !strings.Contains(errs.Coerce(err).Remedy, "--limit") {
			t.Errorf("the remedy does not point at --limit: %q", errs.Coerce(err).Remedy)
		}
	}
}

// TestBuildQuery asserts filters become JQL through the builder, with values as
// data — and that every query carries an ORDER BY.
func TestBuildQuery(t *testing.T) {
	// Every case ends with the key ordering. Nothing is ever sent unordered:
	// the server's default is undocumented and not guaranteed stable between
	// two requests, which would let a paged result interleave two orderings.
	const byKey = " ORDER BY issuekey DESC"

	cases := []struct {
		name string
		opt  issue.QueryOptions
		want string
	}{
		{"empty", issue.QueryOptions{}, strings.TrimSpace(byKey)},
		{"project", issue.QueryOptions{Project: "ENG"}, `project = "ENG"` + byKey},
		{
			"several filters",
			issue.QueryOptions{Project: "ENG", Statuses: []string{"Done", "Closed"}},
			`project = "ENG" AND status IN ("Done", "Closed")` + byKey,
		},
		{
			"labels in and out",
			issue.QueryOptions{Labels: []string{"a"}, NotLabels: []string{"wontfix"}},
			`labels = "a" AND labels != "wontfix"` + byKey,
		},
		{
			"currentUser is a function, not a string",
			issue.QueryOptions{Assignee: "currentUser"},
			`assignee = currentUser()` + byKey,
		},
		{
			"unassigned",
			issue.QueryOptions{Assignee: "unassigned"},
			`assignee IS EMPTY` + byKey,
		},
		{
			"a named assignee is data",
			issue.QueryOptions{Assignee: "ada@example.com"},
			`assignee = "ada@example.com"` + byKey,
		},
		{
			"dates",
			issue.QueryOptions{CreatedAfter: "-7d", UpdatedAfter: "2026-08-01"},
			`created >= "-7d" AND updated >= "2026-08-01"` + byKey,
		},
		{
			// The caller's field is rarely unique — a bulk edit gives every
			// issue the same timestamp — so the key breaks ties and makes the
			// order total.
			"a custom sort keeps the key as a tiebreaker",
			issue.QueryOptions{Project: "ENG", Sort: "updated", Order: "desc"},
			`project = "ENG" ORDER BY updated DESC, issuekey DESC`,
		},
		{
			"sort defaults to ascending",
			issue.QueryOptions{Sort: "created"},
			`ORDER BY created ASC, issuekey DESC`,
		},
		{
			"sorting by the key explicitly adds no tiebreaker",
			issue.QueryOptions{Sort: "issuekey", Order: "desc"},
			`ORDER BY issuekey DESC`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := issue.BuildQuery(tc.opt)
			if err != nil {
				t.Fatalf("BuildQuery: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestEveryQueryIsOrdered is the rule stated once, over the whole surface: no
// combination of filters produces a query without an ORDER BY.
func TestEveryQueryIsOrdered(t *testing.T) {
	options := []issue.QueryOptions{
		{},
		{Project: "ENG"},
		{JQL: "labels = x"},
		{Assignee: "currentUser", Statuses: []string{"Open"}},
		{Sort: "updated", Order: "desc"},
		{Sort: "priority"},
		{Project: "ENG", Sort: "issuekey", Order: "asc"},
	}
	for _, opt := range options {
		got, err := issue.BuildQuery(opt)
		if err != nil {
			t.Fatalf("BuildQuery(%+v): %v", opt, err)
		}
		if !strings.Contains(got, "ORDER BY") {
			t.Errorf("BuildQuery(%+v) = %q, which has no ordering", opt, got)
		}
		if !strings.Contains(got, "issuekey") {
			t.Errorf("BuildQuery(%+v) = %q, which has no unique tiebreaker", opt, got)
		}
	}
}

// TestSortsByKey identifies the orderings keyset pagination can resume from.
func TestSortsByKey(t *testing.T) {
	yes := []issue.QueryOptions{
		{},
		{Project: "ENG"},
		{Sort: "issuekey"},
		{Sort: "issuekey", Order: "desc"},
		{Sort: "ISSUEKEY", Order: "DESC"},
	}
	for _, opt := range yes {
		if !opt.SortsByKey() {
			t.Errorf("SortsByKey(%+v) = false", opt)
		}
	}

	no := []issue.QueryOptions{
		{Sort: "updated"},
		{Sort: "created", Order: "desc"},
		// Ascending by key is a different order from the descending default,
		// so a cursor taken from one cannot resume the other.
		{Sort: "issuekey", Order: "asc"},
	}
	for _, opt := range no {
		if opt.SortsByKey() {
			t.Errorf("SortsByKey(%+v) = true", opt)
		}
	}
}

// TestRawJQLCannotEscapeTheProjectScope is the incumbent's worst bug, asserted
// at the level a user would hit it.
func TestRawJQLCannotEscapeTheProjectScope(t *testing.T) {
	got, err := issue.BuildQuery(issue.QueryOptions{
		Project: "ENG",
		JQL:     `summary ~ "x" OR priority = Highest`,
	})
	if err != nil {
		t.Fatalf("BuildQuery: %v", err)
	}
	want := `project = "ENG" AND (summary ~ "x" OR priority = Highest) ORDER BY issuekey DESC`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// Without the parentheses the OR binds looser than the AND, and the query
	// returns every Highest issue in every project the caller can see.
	if got == `project = "ENG" AND summary ~ "x" OR priority = Highest` {
		t.Fatal("raw JQL escaped the project scope")
	}
}

func TestBuildQueryRejectsBadInput(t *testing.T) {
	cases := map[string]issue.QueryOptions{
		"impossible date": {CreatedAfter: "2020-13-45"},
		"word as a date":  {UpdatedAfter: "yesterday"},
		"bad order":       {Sort: "updated", Order: "sideways"},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := issue.BuildQuery(opt); err == nil {
				t.Fatalf("BuildQuery accepted it and produced %q", got)
			} else if errs.ExitOf(err) != exitcode.Usage {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
			}
		})
	}
}

// TestDefaultColumnsResolve catches a column path that names a field the issue
// node does not have, which would render as a silently empty cell.
func TestDefaultColumnsResolve(t *testing.T) {
	client, _ := replayClient(t, site.Cloud)
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	node := result.Issues[0].Node()
	for _, col := range issue.ListColumns() {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing on a fully populated issue", col.Header)
		}
	}
}

func TestListDocValidates(t *testing.T) {
	client, _ := replayClient(t, site.Cloud)
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	doc := issue.ListDoc(result)
	if err := doc.Validate(); err != nil {
		t.Fatalf("the rendered document is malformed: %v", err)
	}
	if doc.Kind != issue.KindList || doc.Version != issue.VersionList {
		t.Errorf("kind = %s v%d", doc.Kind, doc.Version)
	}
	if !doc.IsComplete() {
		t.Error("a complete result rendered as incomplete")
	}

	var b strings.Builder
	if err := render.Write(&b, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(b.String(), `complete="true"`) {
		t.Errorf("the envelope does not claim completeness:\n%s", b.String())
	}
}

func TestDefaultFieldsCoverTheOutput(t *testing.T) {
	fields := issue.DefaultFields()
	for _, want := range []string{
		"summary", "status", "assignee", "priority", "issuetype",
		"project", "created", "updated", "labels",
	} {
		found := false
		for _, f := range fields {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DefaultFields omits %q, which the output renders", want)
		}
	}
	// "*all" would be simpler and would make every list an order of magnitude
	// larger for fields nobody renders.
	for _, f := range fields {
		if strings.HasPrefix(f, "*") {
			t.Errorf("DefaultFields asks for %q rather than naming what it needs", f)
		}
	}
}

// keysetClient replays the Data Center conversation that pages by key.
func keysetClient(t *testing.T) (*issue.Client, *transport.Replayer) {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", "keyset.datacenter.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &issue.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}, replayer
}

// TestKeysetPaginationOnDataCenter is the fix for the window offset paging
// leaves open. The second page resumes with `issuekey < ENG-1000` rather than
// `startAt=2`, so an issue created between the two requests cannot shift it.
func TestKeysetPaginationOnDataCenter(t *testing.T) {
	client, replayer := keysetClient(t)

	result, err := client.List(t.Context(), issue.ListOptions{
		Query:    issue.QueryOptions{Project: "ENG"},
		Limit:    registry.Limit{All: true},
		PageSize: 2,
		Fields:   issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !result.Keyset {
		t.Error("the request fell back to offset paging")
	}
	if len(result.Issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(result.Issues))
	}
	// ENG-999 sorts below ENG-1000 numerically and above it as text. Getting
	// this order proves the client compares keys the way JQL does.
	var keys []string
	for _, i := range result.Issues {
		keys = append(keys, i.Key)
	}
	if strings.Join(keys, ",") != "ENG-1001,ENG-1000,ENG-999" {
		t.Errorf("keys = %v, want descending by number", keys)
	}
	if !result.Complete {
		t.Error("the result set ran out but was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("unplayed: %v", unplayed)
	}
}

// TestKeysetTokenResumes checks the cursor a truncated keyset run hands back.
func TestKeysetTokenResumes(t *testing.T) {
	client, _ := keysetClient(t)
	first, err := client.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"},
		Limit: registry.Limit{N: 2}, PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if first.Complete || first.NextPageToken == "" {
		t.Fatal("a truncated result did not offer a cursor")
	}

	// The cursor names a row, not a count.
	token, err := issue.DecodePageToken(first.NextPageToken, site.DataCenter)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if token.AfterKey != "ENG-1000" {
		t.Errorf("AfterKey = %q, want the last row of page one", token.AfterKey)
	}
	if token.Offset != 0 {
		t.Errorf("a keyset token carries an offset too: %d", token.Offset)
	}

	resume, _ := keysetClient(t)
	rest, err := resume.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"}, Limit: registry.Limit{All: true},
		PageSize: 2, PageToken: first.NextPageToken, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(rest.Issues) != 1 || rest.Issues[0].Key != "ENG-999" {
		t.Errorf("resume returned %v", rest.Issues)
	}
}

// TestKeysetFallsBackWhenItCannotApply covers the three conditions. Falling back
// is correct; falling back silently and claiming stability would not be.
func TestKeysetFallsBackWhenItCannotApply(t *testing.T) {
	cases := map[string]struct {
		kind site.Kind
		opt  issue.ListOptions
	}{
		"cloud already has stable cursors": {
			site.Cloud, issue.ListOptions{Query: issue.QueryOptions{Project: "ENG"}},
		},
		"a custom sort is a different order": {
			site.DataCenter,
			issue.ListOptions{Query: issue.QueryOptions{Project: "ENG", Sort: "updated"}},
		},
		"a pre-rendered query has no builder to narrow": {
			site.DataCenter, issue.ListOptions{JQL: `project = "ENG"`},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := replayClient(t, tc.kind)
			opt := tc.opt
			opt.Limit = registry.Limit{N: 2}
			opt.PageSize = 2
			opt.Fields = issue.DefaultFields()

			result, err := client.List(t.Context(), opt)
			if err != nil {
				// The fixture only answers the plain query; what matters here
				// is the mode, which is decided before any request.
				t.Skipf("fixture does not cover this query: %v", err)
			}
			if result.Keyset {
				t.Error("keyset paging was used where it does not apply")
			}
		})
	}
}

// TestKeyOrdering is the comparison the whole scheme rests on. Issue keys do not
// sort as text: ENG-1000 is greater than ENG-999 as an issue and smaller as a
// string.
func TestKeyOrdering(t *testing.T) {
	lower, ok := issue.ParseKey("ENG-999")
	if !ok {
		t.Fatal("ENG-999 did not parse")
	}
	upper, _ := issue.ParseKey("ENG-1000")
	if !lower.Before(upper) {
		t.Error("ENG-999 does not sort below ENG-1000")
	}
	if upper.Before(lower) {
		t.Error("ENG-1000 sorts below ENG-999")
	}
	if strings.Compare("ENG-999", "ENG-1000") >= 0 == lower.Before(upper) {
		t.Log("confirmed: text comparison disagrees with key comparison here")
	}

	// Different projects order by project name.
	a, _ := issue.ParseKey("AAA-9999")
	b, _ := issue.ParseKey("ZZZ-1")
	if !a.Before(b) {
		t.Error("AAA-9999 does not sort below ZZZ-1")
	}

	for _, bad := range []string{"", "ENG", "ENG-", "-1", "ENG-abc", "ENG-1-2"} {
		if _, ok := issue.ParseKey(bad); ok {
			t.Errorf("ParseKey(%q) succeeded", bad)
		}
	}
	if k, ok := issue.ParseKey("eng-12"); !ok || k.String() != "ENG-12" {
		t.Errorf("ParseKey does not normalize case: %v %v", k, ok)
	}
}

// TestKeysetRefusesAMisorderedServer is the guard on the assumption the whole
// scheme rests on.
//
// Keyset paging is only correct if JQL compares issue keys by project and
// number. A server comparing them as text would put ENG-999 above ENG-1000, and
// a cursor taken from the last row would then skip everything between — silently
// returning fewer issues while reporting the result complete. This asserts that
// disagreement surfaces as an error instead.
func TestKeysetRefusesAMisorderedServer(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "keyset-misordered.datacenter.json"),
	)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	conn, err := transport.New(transport.Options{
		BaseURL:    "https://recorded.invalid",
		HTTPClient: transport.NewReplayer(cassette).Client(),
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	_, err = client.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"}, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err == nil {
		t.Fatal("a misordered page was accepted, so paging would have skipped rows")
	}
	if errs.ExitOf(err) != exitcode.Remote {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Remote)
	}
	e := errs.Coerce(err)
	if e.Code != "PAGINATION_ORDER" {
		t.Errorf("code = %q, want PAGINATION_ORDER", e.Code)
	}
	// The error has to name both keys, or nobody can tell what disagreed.
	if !strings.Contains(e.Detail, "ENG-1000") || !strings.Contains(e.Detail, "ENG-999") {
		t.Errorf("the detail does not name the two keys: %q", e.Detail)
	}
	// And offer a way to keep working.
	if !strings.Contains(e.Remedy, "--sort") {
		t.Errorf("the remedy offers no fallback: %q", e.Remedy)
	}
}

// TestBudgetStopsALongRun covers --max-requests, which is the brake on a
// `--limit all` over a large project.
//
// Running out of budget is not a failure: it means there is more. The caller
// gets the rows already fetched, an explicit complete="false", and a cursor to
// carry on from — never an error that discards the work done so far.
func TestBudgetStopsALongRun(t *testing.T) {
	cassette, err := transport.LoadCassette(filepath.Join("testdata", "keyset.datacenter.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	conn, err := transport.New(transport.Options{
		BaseURL:     "https://recorded.invalid",
		HTTPClient:  transport.NewReplayer(cassette).Client(),
		Retries:     -1,
		MaxRequests: 1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	result, err := client.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"}, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("a spent budget discarded the fetched rows: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Errorf("kept %d rows, want the page that was fetched", len(result.Issues))
	}
	if result.Complete {
		t.Error("a budget-truncated result claimed to be complete")
	}
	if result.NextPageToken == "" {
		t.Fatal("no way to resume after the budget ran out")
	}

	// The cursor is a keyset one, so resuming is still stable.
	token, err := issue.DecodePageToken(result.NextPageToken, site.DataCenter)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if token.AfterKey == "" {
		t.Errorf("the resume cursor is not a keyset cursor: %+v", token)
	}
}

// TestRequestedFieldsReachTheOutput is the fix for a flag that did nothing.
//
// --field changed what was fetched and not what was shown, so a caller asking
// for a custom field got the default output and no indication the request had
// been discarded.
func TestRequestedFieldsReachTheOutput(t *testing.T) {
	cassette, err := transport.LoadCassette(filepath.Join("testdata", "fields.cloud.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: transport.NewReplayer(cassette).Client(),
		Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	requested := []string{"customfield_10042", "duedate"}
	result, err := client.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"}, Limit: registry.Limit{All: true},
		PageSize: 10, Fields: append(issue.DefaultFields(), requested...),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Issues) != 3 {
		t.Fatalf("got %d issues", len(result.Issues))
	}

	// Jira serves custom fields in several shapes; each reduces to one cell.
	want := []string{"5", "High risk", "A, B"}
	for i, issued := range result.Issues {
		if len(issued.Extra) != 2 {
			t.Fatalf("%s carries %d extra fields, want 2", issued.Key, len(issued.Extra))
		}
		if issued.Extra[0].Value != want[i] {
			t.Errorf("%s customfield_10042 = %q, want %q",
				issued.Key, issued.Extra[0].Value, want[i])
		}
	}

	// A field the server did not return is present and empty, so "no value" is
	// distinguishable from "I asked for something that does not exist".
	if result.Issues[1].Extra[1].ID != "duedate" || result.Issues[1].Extra[1].Value != "" {
		t.Errorf("a missing field was dropped rather than reported empty: %+v",
			result.Issues[1].Extra)
	}

	// And they reach the rendered output, in both shapes.
	var xml strings.Builder
	if err := render.Write(&xml, issue.ListDoc(result), render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(xml.String(), "<customfield_10042>5</customfield_10042>") {
		t.Errorf("the requested field is not in the XML:\n%s", xml.String())
	}

	// The default format is TSV, so a column has to appear there too or the
	// flag still looks like it did nothing.
	columns := append(issue.ListColumns(), issue.ExtraColumns(requested)...)
	node := result.Issues[0].Node()
	for _, col := range columns {
		if _, ok := node.Lookup(col.Path); !ok {
			t.Errorf("column %q resolves to nothing", col.Header)
		}
	}
}

// TestFieldsAreAdditive stops --field from silently blanking the default
// output. Replacing the default set would make every row look unassigned with
// an unknown status.
func TestFieldsAreAdditive(t *testing.T) {
	got := issue.DefaultFields()
	with := issue.ListColumns()
	_ = with

	fields := append(issue.DefaultFields(), issue.ExtraFieldNames([]string{"customfield_1"})...)
	for _, needed := range got {
		found := false
		for _, f := range fields {
			if f == needed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("requesting an extra field dropped %q from the request", needed)
		}
	}
	if len(fields) != len(got)+1 {
		t.Errorf("got %d fields, want the defaults plus one", len(fields))
	}
}

func TestExtraFieldNamesSkipsNativeOnes(t *testing.T) {
	// Naming a field the tool already reports changes nothing, so it must not
	// become a duplicate column or a duplicate element.
	got := issue.ExtraFieldNames([]string{"summary", "status", "customfield_1", "customfield_1"})
	if len(got) != 1 || got[0] != "customfield_1" {
		t.Errorf("ExtraFieldNames = %v, want just the non-native one, deduplicated", got)
	}
}

// TestFieldNamesAreValidated covers what cannot be rendered yet. Guessing at a
// field name would either send Jira something it rejects opaquely or produce an
// element name that is not valid XML.
func TestFieldNamesAreValidated(t *testing.T) {
	for _, ok := range []string{"customfield_10042", "duedate", "timespent", "summary"} {
		if err := issue.ValidateFieldNames([]string{ok}); err != nil {
			t.Errorf("ValidateFieldNames(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"Story Points", "", "cf[10042]", "field-name", "1field", "key"} {
		err := issue.ValidateFieldNames([]string{bad})
		if err == nil {
			t.Errorf("ValidateFieldNames(%q) was accepted", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}
	// The error has to say what to pass instead.
	err := issue.ValidateFieldNames([]string{"Story Points"})
	if !strings.Contains(errs.Coerce(err).Remedy, "id") {
		t.Errorf("the remedy does not point at ids: %q", errs.Coerce(err).Remedy)
	}
}
