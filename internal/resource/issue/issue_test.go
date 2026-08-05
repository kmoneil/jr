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

// TestBuildQueryUsesTheBuilder asserts filters become JQL through the builder,
// with values as data.
func TestBuildQuery(t *testing.T) {
	cases := []struct {
		name string
		opt  issue.QueryOptions
		want string
	}{
		{"empty", issue.QueryOptions{}, ""},
		{"project", issue.QueryOptions{Project: "ENG"}, `project = "ENG"`},
		{
			"several filters",
			issue.QueryOptions{Project: "ENG", Statuses: []string{"Done", "Closed"}},
			`project = "ENG" AND status IN ("Done", "Closed")`,
		},
		{
			"labels in and out",
			issue.QueryOptions{Labels: []string{"a"}, NotLabels: []string{"wontfix"}},
			`labels = "a" AND labels != "wontfix"`,
		},
		{
			"currentUser is a function, not a string",
			issue.QueryOptions{Assignee: "currentUser"},
			`assignee = currentUser()`,
		},
		{
			"unassigned",
			issue.QueryOptions{Assignee: "unassigned"},
			`assignee IS EMPTY`,
		},
		{
			"a named assignee is data",
			issue.QueryOptions{Assignee: "ada@example.com"},
			`assignee = "ada@example.com"`,
		},
		{
			"dates",
			issue.QueryOptions{CreatedAfter: "-7d", UpdatedAfter: "2026-08-01"},
			`created >= "-7d" AND updated >= "2026-08-01"`,
		},
		{
			"sorting",
			issue.QueryOptions{Project: "ENG", Sort: "updated", Order: "desc"},
			`project = "ENG" ORDER BY updated DESC`,
		},
		{
			"sort defaults to ascending",
			issue.QueryOptions{Sort: "created"},
			`ORDER BY created ASC`,
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
	want := `project = "ENG" AND (summary ~ "x" OR priority = Highest)`
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
