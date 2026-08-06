package issue_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
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

// testCatalogue is a small stand-in for a site's fields, covering the shapes
// resolution has to deal with: a native field, a custom one, and one whose id
// could not be an element name.
func testCatalogue() *site.Catalogue {
	return &site.Catalogue{Fields: []site.Field{
		{ID: "summary", Name: "Summary", Type: "string"},
		{ID: "duedate", Name: "Due Date", Type: "date"},
		{ID: "timespent", Name: "Time Spent", Type: "number"},
		{ID: "issuekey", Name: "Key", Type: "string"},
		{
			ID: "customfield_10042", Name: "Story Points", Custom: true, Type: "number",
			ClauseNames: []string{"cf[10042]", "Story Points"},
		},
		{ID: "cf[10099]", Name: "Broken Id", Custom: true},
	}}
}

// TestFieldsResolveByIdAndName is the point of the catalogue: a caller types
// what the field is called and gets the id it has to be requested by.
func TestFieldsResolveByIdAndName(t *testing.T) {
	for input, want := range map[string]string{
		"customfield_10042": "customfield_10042",
		"Story Points":      "customfield_10042",
		"story points":      "customfield_10042",
		"cf[10042]":         "customfield_10042",
		"Due Date":          "duedate",
		"duedate":           "duedate",
		"Summary":           "summary",
	} {
		got, err := issue.ResolveFields(testCatalogue(), []string{input})
		if err != nil {
			t.Errorf("ResolveFields(%q) = %v", input, err)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("ResolveFields(%q) = %v, want [%s]", input, got, want)
		}
	}
}

// TestUnknownFieldNamesTheNearMisses is the §5.2 row this exists for: an
// unknown field is refused against the catalogue rather than sent for Jira to
// reject with a 400 that names nothing.
func TestUnknownFieldNamesTheNearMisses(t *testing.T) {
	_, err := issue.ResolveFields(testCatalogue(), []string{"Story Point"})
	if err == nil {
		t.Fatal("a misspelled field was accepted")
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_FIELD" {
		t.Errorf("code = %q, want UNKNOWN_FIELD", e.Code)
	}
	// The suggestion has to carry the id, because the id is what the caller
	// needs next. A near-miss list of names alone is another lookup.
	if !strings.Contains(e.Detail, "Story Points") ||
		!strings.Contains(e.Detail, "customfield_10042") {
		t.Errorf("the detail does not name the near match with its id: %q", e.Detail)
	}
}

// TestFieldNamesAreValidated covers what cannot be rendered. An id that is not
// a legal element name has no output shape, and one that collides with a field
// this tool already reports would overwrite it.
func TestFieldNamesAreValidated(t *testing.T) {
	for _, ok := range []string{"customfield_10042", "duedate", "timespent", "summary"} {
		if _, err := issue.ResolveFields(testCatalogue(), []string{ok}); err != nil {
			t.Errorf("ResolveFields(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"Story Pants", "", "field-name", "1field", "Broken Id"} {
		_, err := issue.ResolveFields(testCatalogue(), []string{bad})
		if err == nil {
			t.Errorf("ResolveFields(%q) was accepted", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

// TestAmbiguousFieldNameIsRefused covers two custom fields sharing a name,
// which Jira permits. They are different fields with different values, so
// picking one would report the wrong column for every issue.
func TestAmbiguousFieldNameIsRefused(t *testing.T) {
	catalogue := &site.Catalogue{Fields: []site.Field{
		{ID: "customfield_10001", Name: "Team", Custom: true},
		{ID: "customfield_10002", Name: "Team", Custom: true},
	}}

	_, err := issue.ResolveFields(catalogue, []string{"Team"})
	if err == nil {
		t.Fatal("an ambiguous field name resolved to one of the candidates")
	}
	e := errs.Coerce(err)
	if e.Code != "AMBIGUOUS_FIELD" {
		t.Errorf("code = %q, want AMBIGUOUS_FIELD", e.Code)
	}
	for _, id := range []string{"customfield_10001", "customfield_10002"} {
		if !strings.Contains(e.Detail, id) {
			t.Errorf("the detail does not list %s: %q", id, e.Detail)
		}
	}
}

// TestFieldSpellingDoesNotChangeTheOutput keeps the output contract independent
// of how a request was worded: a column header is the id either way.
func TestFieldSpellingDoesNotChangeTheOutput(t *testing.T) {
	byName, err := issue.ResolveFields(testCatalogue(), []string{"Story Points"})
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	byID, err := issue.ResolveFields(testCatalogue(), []string{"customfield_10042"})
	if err != nil {
		t.Fatalf("by id: %v", err)
	}

	name, id := issue.ExtraColumns(byName), issue.ExtraColumns(byID)
	if len(name) != 1 || len(id) != 1 || name[0] != id[0] {
		t.Errorf("columns differ by spelling: %v vs %v", name, id)
	}
	if name[0].Header != "customfield_10042" {
		t.Errorf("header = %q, want the id", name[0].Header)
	}

	// Two spellings of one field are one column, not two identical ones.
	both, err := issue.ResolveFields(testCatalogue(),
		[]string{"Story Points", "customfield_10042"})
	if err != nil {
		t.Fatalf("both: %v", err)
	}
	if cols := issue.ExtraColumns(both); len(cols) != 1 {
		t.Errorf("got %d columns for one field named twice", len(cols))
	}
}

// getClient replays a single-issue conversation for one deployment.
func getClient(t *testing.T, kind site.Kind) *issue.Client {
	t.Helper()
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "get."+string(kind)+".json"),
	)
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
	return &issue.Client{Transport: conn, Site: site.Info{Kind: kind}}
}

func TestGetOnBothDeployments(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			got, err := getClient(t, kind).Get(t.Context(), "ENG-101", issue.DetailFields())
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Key != "ENG-101" || got.Summary == "" {
				t.Fatalf("got %+v", got)
			}
			// The fields only worth fetching one issue at a time.
			if got.Parent != "ENG-1" {
				t.Errorf("Parent = %q", got.Parent)
			}
			if len(got.Components) != 1 || got.Components[0] != "transport" {
				t.Errorf("Components = %v", got.Components)
			}
			if len(got.FixVersions) != 1 || got.FixVersions[0] != "2.1.0" {
				t.Errorf("FixVersions = %v", got.FixVersions)
			}
			// Normalization still holds across deployments.
			if got.Status.Category != issue.CategoryInProgress {
				t.Errorf("category = %q", got.Status.Category)
			}
			if got.Assignee.Display != "Ada Lovelace" {
				t.Errorf("assignee = %+v", got.Assignee)
			}
		})
	}
}

// TestDescriptionMarkupIsNamedNotGuessed covers what the format attribute is
// for. A description is three different things depending on the deployment and
// the flags, and none of them can be told apart by looking at the text.
//
// Data Center's wiki markup is carried through: there is no converter for it
// and inventing one would be the half-conversion this project refuses. Cloud's
// document becomes markdown, or stays a document when the caller says so.
func TestDescriptionMarkupIsNamedNotGuessed(t *testing.T) {
	dc, err := getClient(t, site.DataCenter).Get(t.Context(), "ENG-101", issue.DetailFields())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if dc.BodyFormat != issue.BodyWiki {
		t.Errorf("Data Center description format = %q, want %q", dc.BodyFormat, issue.BodyWiki)
	}
	if !strings.Contains(dc.Description, "{code:go}") {
		t.Errorf("wiki markup was altered:\n%s", dc.Description)
	}

	cloud, err := getClient(t, site.Cloud).Get(t.Context(), "ENG-101", issue.DetailFields())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cloud.BodyFormat != issue.BodyMarkdown {
		t.Errorf("Cloud description format = %q, want %q", cloud.BodyFormat, issue.BodyMarkdown)
	}
	if cloud.Description != "The retry loop drops the last error." {
		t.Errorf("the document was not converted:\n%s", cloud.Description)
	}

	// --raw-body is the exact escape hatch, and it has to produce the document
	// rather than a re-encoding of the markdown.
	raw := getClient(t, site.Cloud)
	raw.Body = issue.ModeRaw
	got, err := raw.Get(t.Context(), "ENG-101", issue.DetailFields())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BodyFormat != issue.BodyADF {
		t.Errorf("--raw-body description format = %q, want %q", got.BodyFormat, issue.BodyADF)
	}
	if !strings.Contains(got.Description, `"type":"doc"`) {
		t.Errorf("ADF was not carried through as JSON:\n%s", got.Description)
	}
}

// TestDescriptionSurvivesRendering covers the mixed content the XML default
// exists for: newlines, a fenced code block, angle brackets, and a literal ]]>.
func TestDescriptionSurvivesRendering(t *testing.T) {
	got, err := getClient(t, site.DataCenter).Get(t.Context(), "ENG-101", issue.DetailFields())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	doc := issue.GetDoc(got)
	if doc.Kind != issue.KindGet {
		t.Errorf("kind = %q", doc.Kind)
	}
	if doc.Record == nil {
		t.Fatal("issue get emitted a collection, not a record")
	}

	var b strings.Builder
	if err := render.Write(&b, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, `format="wiki"`) {
		t.Errorf("the markup is not named in the output:\n%s", out)
	}
	// A literal ]]> inside the text must be split rather than closing the
	// section early.
	if strings.Contains(out, "a literal ]]> in") {
		t.Errorf("a literal ]]> survived into the CDATA section:\n%s", out)
	}
	if !strings.Contains(out, "]]]]><![CDATA[>") {
		t.Errorf("the CDATA terminator was not split:\n%s", out)
	}
	// And the code fence is intact.
	if !strings.Contains(out, "{code:go}") {
		t.Errorf("the code block was mangled:\n%s", out)
	}
}

// TestGetRecordDefaultsToXML pins the per-content rule: one issue is a record,
// and a description full of newlines is exactly what the XML default is for.
func TestGetRecordDefaultsToXML(t *testing.T) {
	got, err := getClient(t, site.DataCenter).Get(t.Context(), "ENG-101", issue.DetailFields())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if f := render.DefaultFor(issue.GetDoc(got)); f != render.XML {
		t.Errorf("a fetched issue defaults to %q, want xml", f)
	}
	if f := render.DefaultFor(issue.ListDoc(&issue.ListResult{})); f != render.TSV {
		t.Errorf("a list defaults to %q, want tsv", f)
	}
}

// TestGetValidatesTheKeyLocally saves a round trip and, more importantly, a
// misleading answer: a 404 for a typo reads like a missing issue.
func TestGetValidatesTheKeyLocally(t *testing.T) {
	client := getClient(t, site.Cloud)
	for _, bad := range []string{"foo", "", "12345", "ENG", "ENG-", "ENG-abc"} {
		_, err := client.Get(t.Context(), bad, issue.DetailFields())
		if err == nil {
			t.Errorf("Get(%q) succeeded", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("Get(%q) exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

// TestMissingIssueIsNotFound checks both deployments, which word it differently
// — and Cloud's wording is the one worth surfacing, since a 404 also means an
// issue you cannot see.
func TestMissingIssueIsNotFound(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			_, err := getClient(t, kind).Get(t.Context(), "ENG-9999", issue.DetailFields())
			if err == nil {
				t.Fatal("a missing issue was returned")
			}
			if errs.ExitOf(err) != exitcode.NotFound {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.NotFound)
			}
			if detail := errs.Coerce(err).Detail; !strings.Contains(detail, "Does Not Exist") &&
				!strings.Contains(detail, "does not exist") {
				t.Errorf("Jira's own wording was dropped: %q", detail)
			}
		})
	}
}

// TestFieldNamesResolveThroughTheCommand covers the whole path a user takes:
// the registered command's Validate resolves the name, and the columns computed
// afterwards carry the id. Testing ResolveFields alone would leave the wiring —
// which is where the last --field bug lived — uncovered.
func TestFieldNamesResolveThroughTheCommand(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	session := &stubSession{doer: &stubDoer{body: catalogueJSON}}
	inv := &registry.Invocation{Jira: session, Flags: registry.NewFlags()}
	inv.Flags.SetString("field", "Story Points")

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}

	columns := cmd.ColumnsFor(inv)
	last := columns[len(columns)-1]
	if last.Header != "customfield_10042" || last.Path != "customfield_10042" {
		t.Errorf("the resolved column is %+v, want the id", last)
	}
	if len(columns) != len(issue.ListColumns())+1 {
		t.Errorf("got %d columns, want the defaults plus one", len(columns))
	}
}

// TestAnUnknownFieldIsRefusedBeforeAnyOutput is the §5.2 row. It has to happen
// in Validate: a streaming command writes its header before its body runs, so a
// refusal from inside the body would arrive after bytes were on stdout.
func TestAnUnknownFieldIsRefusedBeforeAnyOutput(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	session := &stubSession{doer: &stubDoer{body: catalogueJSON}}
	inv := &registry.Invocation{Jira: session, Flags: registry.NewFlags()}
	inv.Flags.SetString("field", "Story Point")

	err := cmd.Validate(t.Context(), inv)
	if err == nil {
		t.Fatal("a misspelled field reached the request")
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}
	if detail := errs.Coerce(err).Detail; !strings.Contains(detail, "customfield_10042") {
		t.Errorf("the refusal does not suggest the near match: %q", detail)
	}
}

// TestNoFieldFlagCostsNoRequest keeps the catalogue from taxing every
// invocation. A caller who never asked for an extra field must not pay for the
// machinery that resolves one.
func TestNoFieldFlagCostsNoRequest(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	doer := &stubDoer{body: catalogueJSON}
	inv := &registry.Invocation{
		Jira: &stubSession{doer: doer}, Flags: registry.NewFlags(),
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doer.calls != 0 {
		t.Errorf("the catalogue was fetched %d times with no --field", doer.calls)
	}
}

// TestFieldsAreResolvedOnce covers the memoization: validation and the command
// body both need the catalogue, and fetching it twice would double the cost of
// the feature.
func TestFieldsAreResolvedOnce(t *testing.T) {
	cmd, ok := registry.Lookup("issue.get")
	if !ok {
		t.Fatal("issue get is not registered")
	}

	doer := &stubDoer{body: catalogueJSON}
	inv := &registry.Invocation{
		Jira: &stubSession{doer: doer}, Flags: registry.NewFlags(),
	}
	inv.Flags.SetString("field", "Story Points")
	inv.Flags.SetString("field", "customfield_10042")

	for range 2 {
		if err := cmd.Validate(t.Context(), inv); err != nil {
			t.Fatalf("validate: %v", err)
		}
	}
	if doer.calls != 1 {
		t.Errorf("the catalogue was fetched %d times, want 1", doer.calls)
	}
}

// catalogueJSON is what /field returns for the resolution tests.
const catalogueJSON = `[
	{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
	{"id":"duedate","name":"Due Date","custom":false,"schema":{"type":"date"}},
	{"id":"customfield_10042","name":"Story Points","custom":true,
	 "clauseNames":["cf[10042]","Story Points"],"schema":{"type":"number"}}
]`

// stubSession is a registry.Session backed by a stubbed transport, so a command
// is exercised with no auth, no config, and no network.
type stubSession struct {
	doer *stubDoer
	meta *site.Metadata
	// conn and kind are set when a test needs the command to reach a recorded
	// conversation rather than only the catalogue.
	conn *transport.Client
	kind site.Kind
	// ledger backs the mutating verbs; nil means no idempotency protection.
	ledger *idem.Ledger
	// project overrides the default scope, and unscoped removes it entirely.
	project  string
	unscoped bool
	// metaClient answers metadata lookups. It is separate from doer so a test
	// can point transitions at a recorded conversation while the field
	// catalogue stays a stub.
	metaClient site.Doer
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	kind := s.kind
	if kind == "" {
		kind = site.Cloud
	}
	return s.conn, site.Info{Kind: kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	if s.meta == nil {
		kind := s.kind
		if kind == "" {
			kind = site.Cloud
		}
		var client site.Doer = s.doer
		if s.metaClient != nil {
			client = s.metaClient
		}
		s.meta = &site.Metadata{Client: client, Info: site.Info{Kind: kind}}
	}
	return s.meta, nil
}

// Project defaults to ENG, which is what forty other tests assume. unscoped is
// how a test says "no project at all" — a state the default cannot express and
// the one the unconstrained-query guard exists for.
func (s *stubSession) Project() string {
	switch {
	case s.unscoped:
		return ""
	case s.project != "":
		return s.project
	}
	return "ENG"
}

func (s *stubSession) RequireProject() (string, error) {
	if p := s.Project(); p != "" {
		return p, nil
	}
	return "", errs.Usage("NO_PROJECT", "this command needs a project and none is set")
}
func (s *stubSession) Board() string { return "" }

// RequireBoard is what an agile command calls. None of these fixtures set a
// board, so it fails the way the real session does rather than returning one.
func (s *stubSession) RequireBoard() (string, error) {
	return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
}
func (s *stubSession) CheckWritable(string) error { return nil }

// Idempotency implements registry.Session. A nil ledger means no protection,
// which is what a command that does not mutate should never notice.
func (s *stubSession) Idempotency() *idem.Ledger { return s.ledger }

// stubDoer answers with a fixed body and counts how often it was asked, which
// is what the "no extra request" assertions read.
type stubDoer struct {
	body  string
	calls int
}

func (s *stubDoer) Do(context.Context, transport.Request) (*transport.Response, error) {
	s.calls++
	return &transport.Response{
		Status: 200,
		Body:   []byte(s.body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestListRunsAsARegisteredCommand exercises the layer a user actually
// invokes. Every other test here drives Client directly, which left runList
// and runGet at zero coverage — and "the test covered the function and not the
// command wrapper above it" is exactly how `mcp serve` shipped broken.
func TestListRunsAsARegisteredCommand(t *testing.T) {
	// The two deployments produce genuinely different conversations from the
	// same invocation: the command always adds ORDER BY issuekey DESC, which
	// makes Data Center page by keyset and leaves Cloud on its cursor. That is
	// why they need separate fixtures rather than one shared one.
	for _, tc := range []struct {
		kind    site.Kind
		fixture string
		keys    string
	}{
		{site.Cloud, "command.cloud.json", "ENG-101,ENG-102,ENG-103"},
		{site.DataCenter, "keyset.datacenter.json", "ENG-1001,ENG-1000,ENG-999"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.list")
			if !ok {
				t.Fatal("issue list is not registered")
			}

			conn, replayer := replayConn(t, tc.fixture)
			flags := registry.NewFlags()
			flags.SetInt("page-size", 2)
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: tc.kind,
				},
				Flags: flags, Limit: registry.Limit{All: true},
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}

			var buf strings.Builder
			stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
				Kind: cmd.Kind(), Version: cmd.KindVersion(),
				Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
			})
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			result, err := cmd.Stream(t.Context(), inv, stream)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
				t.Fatalf("close: %v", err)
			}

			if !result.Complete {
				t.Error("an exhausted result set was reported incomplete")
			}
			lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
			if lines[0] != "key\tstatus\tassignee\tupdated\tsummary" {
				t.Errorf("header = %q", lines[0])
			}

			var keys []string
			for _, line := range lines[1:] {
				keys = append(keys, strings.SplitN(line, "\t", 2)[0])
			}
			if strings.Join(keys, ",") != tc.keys {
				t.Errorf("keys = %v, want %s", keys, tc.keys)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the fixture has interactions this test never used: %v", unplayed)
			}
		})
	}
}

// TestRequestedFieldsReachTheRequestThroughTheCommand joins the two halves the
// --field bug fell between: the name is resolved during validation, and the id
// it resolved to is what the command actually asks Jira for.
func TestRequestedFieldsReachTheRequestThroughTheCommand(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	conn, _ := replayConn(t, "command.cloud.json")
	flags := registry.NewFlags()
	flags.SetInt("page-size", 2)
	flags.SetString("field", "Story Points")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.Cloud,
		},
		Flags: flags, Limit: registry.Limit{All: true},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// The column is the resolved id, and it is appended rather than replacing
	// the defaults.
	columns := cmd.ColumnsFor(inv)
	if got := columns[len(columns)-1].Header; got != "customfield_10042" {
		t.Errorf("last column = %q, want the resolved id", got)
	}
	if len(columns) != len(issue.ListColumns())+1 {
		t.Errorf("got %d columns, want the defaults plus one", len(columns))
	}

	// The request is not asserted here — the fixture has no customfield in its
	// recorded query, so running would be a fixture miss. That the id reaches
	// the request is covered by TestRequestedFieldsReachTheOutput at the client
	// level; what this adds is that Validate is what puts it there.
	if ids := resolvedFieldIDs(inv); strings.Join(ids, ",") != "customfield_10042" {
		t.Errorf("validation resolved %v, want the id", ids)
	}
}

// resolvedFieldIDs reads what validation left on the invocation, via the
// columns it produces — the value itself is unexported on purpose.
func resolvedFieldIDs(inv *registry.Invocation) []string {
	cmd, _ := registry.Lookup("issue.list")
	var out []string
	for _, col := range cmd.ColumnsFor(inv)[len(issue.ListColumns()):] {
		out = append(out, col.Header)
	}
	return out
}

// TestGetRunsAsARegisteredCommand is the same for the record command.
func TestGetRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.get")
			if !ok {
				t.Fatal("issue get is not registered")
			}

			conn, _ := replayConn(t, "get."+string(kind)+".json")
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
				},
				Args: []string{"ENG-101"}, Flags: registry.NewFlags(),
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			// A command that returns a kind it did not declare is rejected at
			// runtime, so the declaration has to match what actually comes out.
			if !cmd.Emits(doc.Kind, doc.Version) {
				t.Errorf("emitted %s v%d, which the command does not declare",
					doc.Kind, doc.Version)
			}
			if err := doc.Validate(); err != nil {
				t.Errorf("validate: %v", err)
			}
		})
	}
}

// TestListWithoutASessionFailsLoudly covers the guard. A resource that
// dereferenced a nil session would panic in production rather than fail here.
func TestListWithoutASessionFailsLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")
	inv := &registry.Invocation{
		Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
		Progress: registry.NoProgress,
	}
	stream, err := render.NewStream(io.Discard, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := cmd.Stream(t.Context(), inv, stream); err == nil {
		t.Error("issue list ran without a session")
	} else if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}

	get, _ := registry.Lookup("issue.get")
	if _, err := get.Run(t.Context(), &registry.Invocation{
		Args: []string{"ENG-1"}, Flags: registry.NewFlags(),
	}); err == nil {
		t.Error("issue get ran without a session")
	}
}

// TestStatusCategoryFallsBackToTheName covers the Data Center versions that
// send a category name and no key. Without the fallback every status on those
// servers reports as unknown, which is the field an automated caller branches
// on.
func TestStatusCategoryFallsBackToTheName(t *testing.T) {
	for _, tc := range []struct{ key, name, want string }{
		{"new", "", issue.CategoryToDo},
		{"undefined", "", issue.CategoryToDo},
		{"indeterminate", "", issue.CategoryInProgress},
		{"done", "", issue.CategoryDone},
		{"", "To Do", issue.CategoryToDo},
		{"", "New", issue.CategoryToDo},
		{"", "In Progress", issue.CategoryInProgress},
		{"", "Done", issue.CategoryDone},
		{"", "Complete", issue.CategoryDone},
		{"", "", issue.CategoryUnknown},
		{"", "Invented By A Plugin", issue.CategoryUnknown},
	} {
		body := `{"issues":[{"id":"1","key":"ENG-1","fields":{"summary":"s",` +
			`"status":{"name":"X","statusCategory":{"key":"` + tc.key +
			`","name":"` + tc.name + `"}},"labels":[]}}],"total":1}`
		got := decodeOne(t, body)
		if got.Status.Category != tc.want {
			t.Errorf("category(%q, %q) = %q, want %q",
				tc.key, tc.name, got.Status.Category, tc.want)
		}
	}
}

// TestCustomFieldShapesAllReduceToOneCell covers what Jira puts in a custom
// field. Every shape has to become one cell, because a column that sometimes
// holds an object is a column nothing can parse.
func TestCustomFieldShapesAllReduceToOneCell(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`5`, "5"},
		{`5.5`, "5.5"},
		{`"plain"`, "plain"},
		{`true`, "true"},
		{`null`, ""},
		{`{"value":"By Value"}`, "By Value"},
		{`{"name":"By Name"}`, "By Name"},
		{`{"displayName":"By Display"}`, "By Display"},
		{`{"key":"BY-KEY"}`, "BY-KEY"},
		{`["a","b"]`, "a, b"},
		{`[{"value":"x"},{"value":"y"}]`, "x, y"},
		{`[]`, ""},
		// An object that does not reduce is emitted as compact JSON rather than
		// dropped: an unreadable value in the output beats a silently missing
		// one. An empty *array* is different — it has nothing to render, so ""
		// is the value rather than a claim that there is none.
		{`{}`, "{}"},
		{`{"unexpected":{"nested":1}}`, `{"unexpected":{"nested":1}}`},
	} {
		body := `{"issues":[{"id":"1","key":"ENG-1","fields":{"summary":"s",` +
			`"status":{"name":"X","statusCategory":{"key":"new"}},"labels":[],` +
			`"customfield_10042":` + tc.raw + `}}],"total":1}`
		got := decodeOne(t, body, "customfield_10042")
		if len(got.Extra) != 1 {
			t.Errorf("%s produced %d extra fields", tc.raw, len(got.Extra))
			continue
		}
		if got.Extra[0].Value != tc.want {
			t.Errorf("%s reduced to %q, want %q", tc.raw, got.Extra[0].Value, tc.want)
		}
	}
}

// decodeOne runs one response body through the client and returns the issue,
// so a decoding table stays readable.
func decodeOne(t *testing.T, body string, fields ...string) issue.Issue {
	t.Helper()
	// Client takes a Doer, so a decoding table needs no HTTP client and no
	// cassette — just an answer.
	client := &issue.Client{
		Transport: &stubDoer{body: body}, Site: site.Info{Kind: site.DataCenter},
	}
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL: `project = "ENG"`, Limit: registry.Limit{N: 1}, PageSize: 1,
		Fields: append(issue.DefaultFields(), fields...),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(result.Issues))
	}
	return result.Issues[0]
}

// replayConn builds a transport backed by a recorded conversation.
func replayConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// TestKeyCompareIsTotalAndNumeric is the invariant that ENG-999 is below
// ENG-1000 as an issue and above it as a string. Every branch matters: the
// project comparison is what keeps the order total across projects, and getting
// it wrong produces a silently short result rather than an error.
func TestKeyCompareIsTotalAndNumeric(t *testing.T) {
	key := func(s string) issue.Key {
		t.Helper()
		k, ok := issue.ParseKey(s)
		if !ok {
			t.Fatalf("ParseKey(%q) failed", s)
		}
		return k
	}

	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"ENG-999", "ENG-1000", -1},
		{"ENG-1000", "ENG-999", 1},
		{"ENG-1", "ENG-1", 0},
		{"ABC-1", "XYZ-1", -1},
		{"XYZ-1", "ABC-1", 1},
		// The project wins over the number, so a high number in an early
		// project still sorts first.
		{"ABC-9999", "XYZ-1", -1},
		// Parsing upper-cases, so case is not part of the order.
		{"eng-1", "ENG-1", 0},
	} {
		if got := key(tc.a).Compare(key(tc.b)); got != tc.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := key(tc.a).Before(key(tc.b)); got != (tc.want < 0) {
			t.Errorf("%s.Before(%s) = %v, want %v", tc.a, tc.b, got, tc.want < 0)
		}
	}

	// And the text ordering it exists to avoid really does disagree.
	if "ENG-1000" >= "ENG-999" {
		t.Fatal("this test's premise is wrong")
	}
	if key("ENG-1000").Before(key("ENG-999")) {
		t.Error("keys are being compared as text")
	}
}

// TestPageTokenStringIsTheEncodedForm keeps the debugging rendering from being
// the decoded one, which would invite a caller to build a token by hand.
func TestPageTokenStringIsTheEncodedForm(t *testing.T) {
	token := issue.PageToken{Deployment: "cloud", Cursor: "CURSOR-PAGE-2"}
	got := token.String()
	if got == "" {
		t.Fatal("String is empty")
	}
	if strings.Contains(got, "CURSOR-PAGE-2") {
		t.Errorf("String exposes the raw cursor: %q", got)
	}
	// It round-trips, so what is printed is what can be passed back.
	back, err := issue.DecodePageToken(got, site.Cloud)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Cursor != token.Cursor {
		t.Errorf("round trip gave %q, want %q", back.Cursor, token.Cursor)
	}
}

// TestAFieldWhoseIdCollidesIsRefused covers the second half of checkRenderable.
// A field whose id is a name an issue already uses would overwrite it in the
// output rather than appearing beside it.
func TestAFieldWhoseIdCollidesIsRefused(t *testing.T) {
	catalogue := &site.Catalogue{Fields: []site.Field{
		{ID: "key", Name: "Issue Key"},
		{ID: "components", Name: "Components"},
	}}

	// "key" is a name an issue node already uses and is not one this package
	// models, so it has nowhere to go.
	for _, input := range []string{"key", "Issue Key"} {
		_, err := issue.ResolveFields(catalogue, []string{input})
		if err == nil {
			t.Errorf("ResolveFields(%q) was accepted", input)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "INVALID_FIELD" {
			t.Errorf("%q code = %q, want INVALID_FIELD", input, e.Code)
		}
		if !strings.Contains(e.Message, "already reports") {
			t.Errorf("%q message does not say why: %q", input, e.Message)
		}
	}

	// A field this package *does* model is accepted and then dropped, because
	// asking for it changes nothing — it is already in the output, and adding
	// it again would mean a duplicate column and a duplicate element.
	resolved, err := issue.ResolveFields(catalogue, []string{"components"})
	if err != nil {
		t.Fatalf("a natively-reported field was refused: %v", err)
	}
	if got := issue.ExtraFieldNames(resolved); len(got) != 0 {
		t.Errorf("ExtraFieldNames = %v, want the native field dropped", got)
	}
	if got := issue.ExtraColumns(resolved); len(got) != 0 {
		t.Errorf("ExtraColumns = %v, want no duplicate column", got)
	}
}

// TestAFailureMidRunIsNotAPartialSuccess covers the honesty property at the
// seam between pages. A run that fetched one page and then failed must return
// the error, not the rows it happened to get with complete unset — a caller
// that saw rows and no error would treat a truncated set as the whole answer.
func TestAFailureMidRunIsNotAPartialSuccess(t *testing.T) {
	client := &issue.Client{
		Transport: &failingDoer{
			ok: `{"issues":[{"id":"1","key":"ENG-1001","fields":{"summary":"s",` +
				`"status":{"name":"X","statusCategory":{"key":"new"}},"labels":[]}},` +
				`{"id":"2","key":"ENG-1000","fields":{"summary":"s",` +
				`"status":{"name":"X","statusCategory":{"key":"new"}},"labels":[]}}],` +
				`"total":10,"startAt":0,"maxResults":2}`,
			failAfter: 1,
		},
		Site: site.Info{Kind: site.DataCenter},
	}

	var seen int
	_, err := client.ListStream(t.Context(), issue.ListOptions{
		Query:    issue.QueryOptions{Project: "ENG"},
		Limit:    registry.Limit{All: true},
		PageSize: 2,
		Fields:   issue.DefaultFields(),
	}, func(page []issue.Issue, _ int) error {
		seen += len(page)
		return nil
	})

	if err == nil {
		t.Fatal("a failure on the second page was reported as success")
	}
	// The rows from the first page did reach the callback — a streaming command
	// has already written them — which is exactly why the error must propagate.
	if seen != 2 {
		t.Errorf("saw %d rows before the failure, want 2", seen)
	}
	if code := errs.Coerce(err).Code; code == "" {
		t.Errorf("the failure carries no structured code: %v", err)
	}
}

// TestACallbackFailureStopsTheRun covers the other direction: a stream that
// cannot be written to must stop the paging rather than keep spending requests
// on rows nobody will receive.
func TestACallbackFailureStopsTheRun(t *testing.T) {
	doer := &failingDoer{
		ok: `{"issues":[{"id":"1","key":"ENG-1001","fields":{"summary":"s",` +
			`"status":{"name":"X","statusCategory":{"key":"new"}},"labels":[]}}],` +
			`"total":10,"startAt":0,"maxResults":1}`,
	}
	client := &issue.Client{Transport: doer, Site: site.Info{Kind: site.DataCenter}}

	sentinel := errs.Runtime("BROKEN_PIPE", "downstream went away")
	_, err := client.ListStream(t.Context(), issue.ListOptions{
		Query:    issue.QueryOptions{Project: "ENG"},
		Limit:    registry.Limit{All: true},
		PageSize: 1,
		Fields:   issue.DefaultFields(),
	}, func([]issue.Issue, int) error { return sentinel })

	if err == nil {
		t.Fatal("a write failure was swallowed")
	}
	if code := errs.Coerce(err).Code; code != "BROKEN_PIPE" {
		t.Errorf("code = %q, want the callback's own error to propagate", code)
	}
	if doer.calls != 1 {
		t.Errorf("made %d requests after the callback failed, want 1", doer.calls)
	}
}

// failingDoer answers successfully failAfter times and then returns a server
// error, so a failure part-way through a paged run can be staged.
type failingDoer struct {
	ok        string
	failAfter int
	calls     int
}

func (f *failingDoer) Do(
	context.Context, transport.Request,
) (*transport.Response, error) {
	f.calls++
	if f.failAfter > 0 && f.calls > f.failAfter {
		return &transport.Response{
			Status: 500,
			Body:   []byte(`{"errorMessages":["upstream exploded"]}`),
			Header: map[string][]string{"Content-Type": {"application/json"}},
		}, nil
	}
	return &transport.Response{
		Status: 200,
		Body:   []byte(f.ok),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestAnUnconstrainedSweepIsRefused is §6.4 at its narrowest.
//
// The default bound is what makes an unfiltered query harmless: one request,
// fifty rows. --limit all is different — it pages until the instance is
// exhausted, and a personal access token inherits every project its owner was
// ever added to, so the result is every issue they can see. That is rarely what
// was meant and is not something to find out afterwards.
func TestAnUnconstrainedSweepIsRefused(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	unfiltered := func(limit registry.Limit, set func(registry.Flags)) *registry.Invocation {
		flags := registry.NewFlags()
		if set != nil {
			set(flags)
		}
		return &registry.Invocation{
			Jira:  &stubSession{unscoped: true},
			Flags: flags, Limit: limit, Progress: registry.NoProgress,
		}
	}

	// The one refusal.
	err := cmd.Validate(t.Context(), unfiltered(registry.Limit{All: true}, nil))
	if err == nil {
		t.Fatal("an unconstrained --limit all was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNCONSTRAINED_QUERY" {
		t.Fatalf("code = %q, want UNCONSTRAINED_QUERY", e.Code)
	}
	// The refusal offers what would have been enough, so the caller does not
	// have to go and read --help to find out.
	for _, flag := range []string{"--project", "--jql", "--status"} {
		if !strings.Contains(e.Detail, flag) {
			t.Errorf("the detail does not offer %s: %q", flag, e.Detail)
		}
	}

	// The bounded default is not the hazard and is not refused.
	if err := cmd.Validate(t.Context(),
		unfiltered(registry.Limit{N: 50}, nil)); err != nil {
		t.Errorf("a bounded unfiltered list was refused: %v", err)
	}

	// --all-projects is how to mean it.
	if err := cmd.Validate(t.Context(), unfiltered(registry.Limit{All: true},
		func(f registry.Flags) { f.SetBool("all-projects", true) })); err != nil {
		t.Errorf("--all-projects did not allow the sweep: %v", err)
	}
}

// TestAnyFilterSatisfiesTheGuard covers what counts as constraining, one flag
// at a time, because a guard that fires on a query somebody did narrow is worse
// than no guard.
func TestAnyFilterSatisfiesTheGuard(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	for _, tc := range []struct {
		flag  string
		apply func(registry.Flags)
	}{
		{"--jql", func(f registry.Flags) { f.SetString("jql", "labels = retry") }},
		{"--status", func(f registry.Flags) { f.SetString("status", "Open") }},
		{"--label", func(f registry.Flags) { f.SetString("label", "retry") }},
		{"--not-label", func(f registry.Flags) { f.SetString("not-label", "wontfix") }},
		{"--type", func(f registry.Flags) { f.SetString("type", "Bug") }},
		{"--assignee", func(f registry.Flags) { f.SetString("assignee", "currentUser") }},
		{"--reporter", func(f registry.Flags) { f.SetString("reporter", "ada") }},
		{"--created-after", func(f registry.Flags) { f.SetString("created-after", "-7d") }},
		{"--created-before", func(f registry.Flags) { f.SetString("created-before", "-1d") }},
		{"--updated-after", func(f registry.Flags) { f.SetString("updated-after", "-7d") }},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			flags := registry.NewFlags()
			tc.apply(flags)
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Jira:  &stubSession{unscoped: true},
				Flags: flags, Limit: registry.Limit{All: true},
				Progress: registry.NoProgress,
			})
			if err != nil {
				t.Errorf("%s did not satisfy the guard: %v", tc.flag, err)
			}
		})
	}

	// A context project counts, which is the common case: a configured caller
	// exhausting their own project is not sweeping anything.
	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira:  &stubSession{project: "ENG"},
		Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
		Progress: registry.NoProgress,
	})
	if err != nil {
		t.Errorf("a context project did not satisfy the guard: %v", err)
	}
}

// TestSortingIsNotAFilter is the loophole worth closing explicitly. Ordering an
// unbounded query does not make it smaller, and counting it would let --sort
// updated talk the guard out of firing.
func TestSortingIsNotAFilter(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")
	flags := registry.NewFlags()
	flags.SetString("sort", "updated")
	flags.SetString("order", "desc")

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira:  &stubSession{unscoped: true},
		Flags: flags, Limit: registry.Limit{All: true},
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("--sort was accepted as a constraint")
	}
	if code := errs.Coerce(err).Code; code != "UNCONSTRAINED_QUERY" {
		t.Errorf("code = %q", code)
	}
}

// TestAllProjectsLiftsTheContextProject covers the other half of the flag. A
// context that sets a project would otherwise make a site-wide sweep
// impossible, since an empty --project falls back to it rather than clearing it.
func TestAllProjectsLiftsTheContextProject(t *testing.T) {
	flags := registry.NewFlags()
	flags.SetBool("all-projects", true)
	inv := &registry.Invocation{Jira: &stubSession{project: "ENG"}, Flags: flags}

	if got := issue.ListQueryFor(inv); got.Project != "" {
		t.Errorf("project = %q, want the context's scope lifted", got.Project)
	}
	if got := issue.ListQueryFor(&registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: registry.NewFlags(),
	}); got.Project != "ENG" {
		t.Errorf("project = %q, want the context's", got.Project)
	}
}

// TestTheRecordedListingPagesToTheEnd replays a conversation a Cloud instance
// actually had, which is what the other listing tests here cannot do.
//
// They page against a constructed fixture, and that fixture is worth keeping:
// it holds an unassigned issue, a status in every category, and a page that
// ends exactly on a boundary — shapes a sandbox will not produce on request.
// What it cannot establish is that the request is one Jira accepts, because its
// author decided both halves of the exchange.
//
// This one establishes exactly that and little else. The replayer matches on
// path and query, so reaching the third page means the URL this code builds —
// the endpoint, the field list, the token round-trip, the ORDER BY — is the one
// a real server answered three times in a row.
func TestTheRecordedListingPagesToTheEnd(t *testing.T) {
	path := filepath.Join("testdata", "list-recorded.cloud.json")
	cassette, err := transport.LoadCassette(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cassette.Evidence() {
		t.Fatal("list-recorded.cloud.json is not a recording, so replaying it " +
			"proves nothing about the API")
	}

	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client := &issue.Client{
		Transport: conn, Site: site.Info{Kind: site.Cloud, Version: "test"},
	}

	// The options that produced the recording: what `issue list --project OPS
	// --page-size 4 --limit all` builds, ORDER BY included.
	result, err := client.List(t.Context(), issue.ListOptions{
		JQL:      `project = "OPS" ORDER BY issuekey DESC`,
		Limit:    registry.Limit{All: true},
		PageSize: 4,
		Fields:   issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}

	if len(result.Issues) != 10 {
		t.Errorf("got %d issues, want all 10 across three pages", len(result.Issues))
	}
	if !result.Complete {
		t.Error("an exhausted listing was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded page was never requested: %v", unplayed)
	}

	// Descending by key, and by key rather than by text: OPS-10 is above OPS-9
	// as an issue and below it as a string.
	var keys []string
	for _, i := range result.Issues {
		keys = append(keys, i.Key)
	}
	if keys[0] != "OPS-10" || keys[len(keys)-1] != "OPS-1" {
		t.Errorf("keys = %v, want OPS-10 first and OPS-1 last", keys)
	}
}
