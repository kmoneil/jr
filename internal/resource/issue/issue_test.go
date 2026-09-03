package issue_test

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
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

// TestParsePageTokenAnswersOnlyWhatNeedsNoSite pins the seam the split was made
// on.
//
// ParsePageToken exists so a command can refuse a garbled token before it
// builds a session, because the deployment probe behind Connect is a round trip
// and a token that cannot decode is not worth one. The line has to fall in
// exactly one place: everything a caller typed wrong is local, and which server
// minted the token is not knowable without a server. So a wrong-deployment
// token must *pass* here and still be refused by DecodePageToken — if it were
// refused here too, the shape check would need the site and could not run
// early; if the malformed ones passed here, the split would have bought
// nothing.
func TestParsePageTokenAnswersOnlyWhatNeedsNoSite(t *testing.T) {
	cloudToken := issue.EncodePageToken(issue.PageToken{
		Deployment: site.Cloud, Cursor: "CURSOR-PAGE-2",
	})

	got, err := issue.ParsePageToken(cloudToken)
	if err != nil {
		t.Fatalf("a well-formed token was refused with no site to judge it against: %v", err)
	}
	if got.Deployment != site.Cloud || got.Cursor != "CURSOR-PAGE-2" {
		t.Errorf("parsed %+v, want the Cloud cursor intact", got)
	}
	if _, err := issue.DecodePageToken(cloudToken, site.DataCenter); err == nil {
		t.Error("the deployment check did not survive the split")
	}

	// Everything that is wrong with the string itself is still answered here.
	for _, bad := range []string{"garbage", "!!!!", "eyJub3QiOiJ2YWxpZCJ9", "0"} {
		if _, err := issue.ParsePageToken(bad); err == nil {
			t.Errorf("--page-token %q parsed", bad)
		} else if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("--page-token %q exits %v, want %v",
				bad, errs.ExitOf(err), exitcode.Usage)
		}
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
			// The everyday query. One excluded status is an inequality and
			// several are a NOT IN, the same collapse --status already makes.
			"statuses excluded",
			issue.QueryOptions{NotStatuses: []string{"Done", "Closed"}},
			`status NOT IN ("Done", "Closed")` + byKey,
		},
		{
			"one status excluded",
			issue.QueryOptions{NotStatuses: []string{"Closed"}},
			`status != "Closed"` + byKey,
		},
		{
			"statuses in and out",
			issue.QueryOptions{
				Statuses:    []string{"Open", "In Review"},
				NotStatuses: []string{"Closed"},
			},
			`status IN ("Open", "In Review") AND status != "Closed"` + byKey,
		},
		{
			// The other half of the pair. Sub-tasks are the everyday exclusion
			// and the reason --not-type is not a symmetry exercise.
			"types excluded",
			issue.QueryOptions{NotTypes: []string{"Sub-task", "Epic"}},
			`issuetype NOT IN ("Sub-task", "Epic")` + byKey,
		},
		{
			// --order with no --sort orders the field that is there anyway,
			// rather than being dropped for want of a --sort beside it.
			"order without sort",
			issue.QueryOptions{Order: "asc"},
			`ORDER BY issuekey ASC`,
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
		{Order: "desc"},
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
		// so a cursor taken from one cannot resume the other. All three of
		// these build an ascending key order: a named field with no direction
		// ascends, and --order now reaches the default field too.
		{Sort: "issuekey", Order: "asc"},
		{Sort: "issuekey"},
		{Order: "asc"},
	}
	for _, opt := range no {
		if opt.SortsByKey() {
			t.Errorf("SortsByKey(%+v) = true", opt)
		}
	}
}

// TestRawJQLCannotEscapeTheProjectScope is the incumbent's worst bug, asserted
// at the level a user would hit it.
//
// The fragment here brings no parentheses of its own, which is why this passing
// was not enough: one pair of parentheses contains a fragment only if the
// fragment's own parentheses balance. That case is
// TestAnUnbalancedFragmentIsRefusedBeforeItIsSent.
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

// TestAnUnbalancedFragmentIsRefusedBeforeItIsSent covers the other half of the
// containment guarantee.
//
// Wrapping is not containment on its own. `a) OR (1=1` closes the wrapper and
// opens a new group after it, and because AND binds tighter than OR the query
// below reads as `(project = "ENG" AND a) OR (1=1)`:
//
//	project = "ENG" AND (a) OR (1=1) ORDER BY issuekey DESC
//
// Nothing about the result would say so. It comes back complete, at the
// declared schema version, holding every issue in every project the credential
// can see.
//
// The refusal has to come from Validate rather than from BuildQuery: issue list
// streams, so a rejection raised in the body would arrive after the TSV header
// was already on stdout.
func TestAnUnbalancedFragmentIsRefusedBeforeItIsSent(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	validate := func(fragment string) error {
		flags := registry.NewFlags()
		flags.SetString("jql", fragment)
		return cmd.Validate(t.Context(), &registry.Invocation{
			Jira:  &stubSession{project: "ENG"},
			Flags: flags, Limit: registry.Limit{N: 50},
			Progress: registry.NoProgress,
		})
	}

	refused := map[string]struct{ fragment, code string }{
		// Both of these balance at zero and go negative along the way, which is
		// the case a count of parentheses would call fine.
		"closes the wrapper and reopens": {`a) OR (1=1`, "JQL_SYNTAX"},
		"closes and reopens under AND":   {`a) AND (b`, "JQL_SYNTAX"},
		"leads with the close":           {`) OR (1=1`, "JQL_SYNTAX"},
		"never closes what it opens":     {`(project = ENG`, "JQL_SYNTAX"},
		// Not unbalanced, but it would reach Jira as the clause `(   )`. A flag
		// that was passed and does nothing is not silently dropped.
		"blank": {"   ", "EMPTY_JQL"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			err := validate(tc.fragment)
			if err == nil {
				t.Fatalf("issue list accepted %q", tc.fragment)
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
			if got := errs.ExitOf(err); got != exitcode.Usage {
				t.Errorf("exit = %v, want %v", got, exitcode.Usage)
			}
		})
	}

	// The guard refuses what cannot be contained, not what is merely
	// parenthesized. A fragment carrying balanced groups of its own is ordinary
	// input and stays accepted.
	for _, ok := range []string{
		`labels IN (retry, transport)`,
		`(priority = Highest OR labels = urgent) AND status != Done`,
		`summary ~ "x" OR priority = Highest`,
		"", // --jql unset: harvest records every string flag, passed or not
	} {
		if err := validate(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// TestParticipationFiltersBecomeJQL covers the filters that ask who touched an
// issue rather than who owns it.
func TestParticipationFiltersBecomeJQL(t *testing.T) {
	const byKey = " ORDER BY issuekey DESC"

	cases := []struct {
		name string
		opt  issue.QueryOptions
		want string
	}{
		{
			"creator is not reporter",
			issue.QueryOptions{Creator: "currentUser"},
			`creator = currentUser()` + byKey,
		},
		{
			"watcher and voter",
			issue.QueryOptions{Watcher: "currentUser", Voter: "currentUser"},
			`watcher = currentUser() AND voter = currentUser()` + byKey,
		},
		{
			"work logged in a window",
			issue.QueryOptions{
				WorklogAuthor: "currentUser",
				WorklogAfter:  "-7d", WorklogBefore: "-1d",
			},
			`worklogAuthor = currentUser() AND worklogDate >= "-7d" ` +
				`AND worklogDate <= "-1d"` + byKey,
		},
		{
			// One clause, not three. Three separate clauses would ask for an
			// issue whose status changed at some point, and was changed by this
			// person at some point, and changed in this window at some point —
			// three facts that need not be the same event.
			"changed by, in a window, is one clause",
			issue.QueryOptions{
				ChangedBy: "currentUser", ChangedAfter: "-7d", ChangedBefore: "-1d",
			},
			`status CHANGED BY currentUser() AFTER "-7d" BEFORE "-1d"` + byKey,
		},
		{
			"a named field changed",
			issue.QueryOptions{ChangedField: "assignee", ChangedAfter: "-7d"},
			`assignee CHANGED AFTER "-7d"` + byKey,
		},
		{
			"changed dates alone need no author",
			issue.QueryOptions{ChangedAfter: "-1w"},
			`status CHANGED AFTER "-1w"` + byKey,
		},
		{
			"was assigned, whoever holds it now",
			issue.QueryOptions{WasAssignee: "ada@example.invalid"},
			`assignee WAS "ada@example.invalid"` + byKey,
		},
		{
			// The disjunction is one condition, so the builder parenthesizes it
			// beside the project. Four separate clauses would AND, and ask for
			// an issue where one person is all four at once.
			"involving is an OR bundle inside the scope",
			issue.QueryOptions{Project: "ENG", Involving: "currentUser"},
			`project = "ENG" AND (assignee = currentUser() OR ` +
				`reporter = currentUser() OR creator = currentUser() OR ` +
				`worklogAuthor = currentUser())` + byKey,
		},
		{
			"updated-before closes the window created-before already had",
			issue.QueryOptions{UpdatedAfter: "-7d", UpdatedBefore: "-1d"},
			`updated >= "-7d" AND updated <= "-1d"` + byKey,
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

// TestInvolvingCoversTheFieldsItAdvertises holds the flag's help text to what
// the query does.
//
// The help names the fields because a bundle that does not say what it covers
// is a bundle whose result is short for reasons the caller cannot see. Naming
// them in prose and building something else would be worse than saying nothing.
func TestInvolvingCoversTheFieldsItAdvertises(t *testing.T) {
	got, err := issue.BuildQuery(issue.QueryOptions{Involving: "ada@example.invalid"})
	if err != nil {
		t.Fatalf("BuildQuery: %v", err)
	}
	for _, field := range issue.InvolvingFields {
		if !strings.Contains(got, field+` = "ada@example.invalid"`) {
			t.Errorf("--involving advertises %s and the query does not use it: %s",
				field, got)
		}
	}

	cmd, _ := registry.Lookup("issue.list")
	for _, f := range cmd.Flags {
		if f.Name != "involving" {
			continue
		}
		for _, field := range issue.InvolvingFields {
			if !strings.Contains(f.Usage, field) {
				t.Errorf("--involving covers %s and does not say so: %q", field, f.Usage)
			}
		}
	}
}

// TestChangedFieldAloneIsRefused covers the flag that cannot narrow anything by
// itself. It selects what --changed-by asks about, so on its own it would
// change no output at all.
func TestChangedFieldAloneIsRefused(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")
	flags := registry.NewFlags()
	flags.SetString("changed-field", "assignee")

	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: flags,
		Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("--changed-field was accepted on its own")
	}
	if code := errs.Coerce(err).Code; code != "INCOMPLETE_FILTER" {
		t.Errorf("code = %q", code)
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}

	// With a companion it is exactly what it says it is.
	flags.SetString("changed-after", "-7d")
	if err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: flags,
		Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	}); err != nil {
		t.Errorf("--changed-field with --changed-after: %v", err)
	}
}

// TestSentinelsAreRefusedWhereTheyMeanNothing covers the words that name a
// state of the assignee field and no state of anything else.
//
// `creator IS EMPTY` matches nothing and `CHANGED BY EMPTY` is not JQL, so
// accepting the word everywhere would turn a nonsense filter into an empty
// result set and exit 0 — a wrong answer that looks like a right one.
func TestSentinelsAreRefusedWhereTheyMeanNothing(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	for _, name := range []string{"creator", "involving", "changed-by", "was-assignee"} {
		t.Run(name, func(t *testing.T) {
			flags := registry.NewFlags()
			flags.SetString(name, "unassigned")
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Jira: &stubSession{project: "ENG"}, Flags: flags,
				Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
			})
			if err == nil {
				t.Fatalf("--%s accepted a word that names nobody", name)
			}
			if code := errs.Coerce(err).Code; code != "INVALID_USER" {
				t.Errorf("code = %q", code)
			}
		})
	}

	// --assignee is the one filter for which it is a real answer.
	flags := registry.NewFlags()
	flags.SetString("assignee", "unassigned")
	if err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{project: "ENG"}, Flags: flags,
		Limit: registry.Limit{N: 50}, Progress: registry.NoProgress,
	}); err != nil {
		t.Errorf("--assignee unassigned was refused: %v", err)
	}
}

func TestBuildQueryRejectsBadInput(t *testing.T) {
	cases := map[string]issue.QueryOptions{
		"impossible date":         {CreatedAfter: "2020-13-45"},
		"word as a date":          {UpdatedAfter: "yesterday"},
		"bad order":               {Sort: "updated", Order: "sideways"},
		"impossible changed date": {ChangedAfter: "2020-13-45"},
		"word as a worklog date":  {WorklogAfter: "yesterday"},
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
		found := slices.Contains(fields, want)
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

// TestAnEmptyPageThatIsNotTheLastStops covers the one truncation path nothing
// had ever executed.
//
// Cloud is authoritative about the end of a result set: a page is the last one
// when it says isLast, or when it carries no further token. A page that says
// neither and holds no rows is a server telling this client to keep going and
// giving it nothing to go on. Without the guard the loop asks again with the
// same cursor forever.
//
// It is not reachable on Data Center, where an empty page is the end by
// definition, and no recording has ever produced it on Cloud — a page whose
// rows were all filtered by permissions would. The branch was correct as
// written and was the one nothing ran, which is the state this card exists to
// end.
func TestAnEmptyPageThatIsNotTheLastStops(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "empty-page.cloud.json"),
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
	client := &issue.Client{Transport: conn, Site: site.Info{Kind: site.Cloud}}

	result, err := client.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"}, Limit: registry.Limit{All: true},
		PageSize: 2, Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("an empty page became an error rather than a short result: %v", err)
	}

	// What was fetched before the empty page is kept, and reported as partial.
	if len(result.Issues) != 1 {
		t.Errorf("kept %d rows, want the page that had rows", len(result.Issues))
	}
	if result.Complete {
		t.Error("a run stopped by an empty page claimed to be complete")
	}
	if result.Requests != 2 {
		t.Errorf("made %d requests, want 2; a third means the loop did not stop",
			result.Requests)
	}
	if result.NextPageToken == "" {
		t.Fatal("no way to resume after the empty page")
	}
	// The cursor is the one the empty page supplied, not the one that fetched
	// it: resuming has to move forward or it is the same loop by hand.
	token, err := issue.DecodePageToken(result.NextPageToken, site.Cloud)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if token.Cursor != "CURSOR-PAGE-3" {
		t.Errorf("resume cursor = %q, want the one the empty page supplied",
			token.Cursor)
	}
}

// TestALimitOfNothingAsksForNothing covers the other truncation path no test
// reached.
//
// It is the guard on a zero-value registry.Limit, which the CLI never builds —
// --limit refuses 0 — and any in-repo caller can. Without it, `want` becomes
// min(pageSize, 0) and the client asks the server for a page of no rows.
//
// The result is deliberately not complete. Nothing was fetched because nothing
// was asked for, and saying complete would claim the result set itself was
// empty.
func TestALimitOfNothingAsksForNothing(t *testing.T) {
	doer := &stubDoer{body: `{"issues":[],"isLast":true}`}
	client := &issue.Client{Transport: doer, Site: site.Info{Kind: site.Cloud}}

	result, err := client.List(t.Context(), issue.ListOptions{
		Query: issue.QueryOptions{Project: "ENG"}, Limit: registry.Limit{N: 0},
		Fields: issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("a limit of zero became an error: %v", err)
	}
	if doer.calls != 0 {
		t.Errorf("made %d requests for a limit of zero", doer.calls)
	}
	if len(result.Issues) != 0 {
		t.Errorf("returned %d rows for a limit of zero", len(result.Issues))
	}
	if result.Complete {
		t.Error("a limit of zero reported the result set as complete")
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
		found := slices.Contains(fields, needed)
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

// subresourceCatalogue is the shapes a real instance reports, taken from
// `/rest/api/2/field` on the Data Center rig rather than invented. The pairs
// that matter are the ones the schema cannot tell apart: `issuelinks` and
// `subtasks` are both `array` of `issuelinks`, and only one of them has a
// value a column can hold.
func subresourceCatalogue() *site.Catalogue {
	return &site.Catalogue{Fields: []site.Field{
		{ID: "comment", Name: "Comment", Type: "comments-page"},
		{ID: "worklog", Name: "Log Work", Type: "array", Items: "worklog"},
		{ID: "attachment", Name: "Attachment", Type: "array", Items: "attachment"},
		{ID: "issuelinks", Name: "Linked Issues", Type: "array", Items: "issuelinks"},
		{ID: "subtasks", Name: "Sub-tasks", Type: "array", Items: "issuelinks"},
		{ID: "timetracking", Name: "Time Tracking", Type: "timetracking"},
		{ID: "progress", Name: "Progress", Type: "progress"},
		{ID: "votes", Name: "Votes", Type: "votes"},
		{ID: "watches", Name: "Watchers", Type: "watches"},
		{ID: "labels", Name: "Labels", Type: "array", Items: "string"},
		{ID: "fixVersions", Name: "Fix versions", Type: "array", Items: "version"},
		{ID: "duedate", Name: "Due Date", Type: "date"},
		{ID: "customfield_10042", Name: "Story Points", Custom: true, Type: "number"},
		{ID: "customfield_10500", Name: "Anything", Custom: true, Type: "any"},
	}}
}

// TestASubresourceIsNotAColumn is the defect this test was written for:
// `--field comment` resolved, was fetched, and printed the whole serialized
// object into a TSV cell — 196 KB across five rows, gravatar URLs included.
//
// scalarize reduces a structure by looking for `value`, `name`, `displayName`
// or `key` and falls back to the raw JSON when a map holds none of them, so
// every field here used to dump. The flag either affects the output or does not
// exist, and printing a response body into a column is neither.
func TestASubresourceIsNotAColumn(t *testing.T) {
	// Each of these is refused, and the four with a command that reads them
	// properly have to name it: a caller who typed --field comment wanted the
	// comments, and a refusal that does not say where they are is a dead end.
	for field, verb := range map[string]string{
		"comment":      "issue get <key> --with-comments",
		"worklog":      "issue worklog list <key>",
		"attachment":   "issue attachment list <key>",
		"issuelinks":   "issue link list <key>",
		"timetracking": "",
		"progress":     "",
		"votes":        "",
		"watches":      "",
	} {
		_, err := issue.ResolveFields(subresourceCatalogue(), []string{field})
		if err == nil {
			t.Errorf("--field %s was accepted", field)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "UNRENDERABLE_FIELD" {
			t.Errorf("--field %s: code = %q, want UNRENDERABLE_FIELD", field, e.Code)
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("--field %s: exit = %v, want %v", field, errs.ExitOf(err), exitcode.Usage)
		}
		if verb != "" && !strings.Contains(e.Remedy, verb) {
			t.Errorf("--field %s: remedy %q does not name %q", field, e.Remedy, verb)
		}
	}

	// And these keep working. subtasks shares an element type with issuelinks
	// and scalarizes to a list of keys, which is a column somebody has today;
	// `any` is what the server says when it will not say, and refusing it would
	// take out five real custom fields on a stock instance.
	for _, ok := range []string{
		"subtasks", "labels", "fixVersions", "duedate",
		"customfield_10042", "customfield_10500",
	} {
		if _, err := issue.ResolveFields(subresourceCatalogue(), []string{ok}); err != nil {
			t.Errorf("--field %s was refused: %v", ok, err)
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

// TestASubresourceIsRefusedBeforeAnyOutput is the same §5.2 row for a field
// that resolves. `--field comment` used to resolve, be requested, come back,
// and print its whole serialized object into a cell, so the refusal has to
// reach the caller from Validate for the same reason an unknown name does:
// `issue list` streams, and a refusal from the body arrives after the header.
//
// Both commands take the flag from one declaration and both are driven here,
// because a shared declaration is not shared wiring.
func TestASubresourceIsRefusedBeforeAnyOutput(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"issue.list", nil},
		{"issue.get", []string{"ENG-101"}},
	} {
		cmd, ok := registry.Lookup(tc.command)
		if !ok {
			t.Fatalf("%s is not registered", tc.command)
		}

		session := &stubSession{doer: &stubDoer{body: subresourceCatalogueJSON}}
		inv := &registry.Invocation{
			Jira: session, Args: tc.args, Flags: registry.NewFlags(),
		}
		inv.Flags.SetString("field", "Comment")

		err := cmd.Validate(t.Context(), inv)
		if err == nil {
			t.Errorf("%s: --field Comment reached the request", tc.command)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "UNRENDERABLE_FIELD" {
			t.Errorf("%s: code = %q, want UNRENDERABLE_FIELD", tc.command, e.Code)
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%s: exit = %v, want %v", tc.command, errs.ExitOf(err), exitcode.Usage)
		}
		// The caller asked for the comments. The refusal is only useful if it
		// says where they actually are.
		if !strings.Contains(e.Remedy, "--with-comments") {
			t.Errorf("%s: remedy %q does not say where to read them", tc.command, e.Remedy)
		}
	}
}

// subresourceCatalogueJSON is /field with a subresource in it, in the shape the
// Data Center rig reports rather than a shape convenient for the test.
const subresourceCatalogueJSON = `[
	{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
	{"id":"comment","name":"Comment","custom":false,
	 "schema":{"type":"comments-page"}},
	{"id":"customfield_10042","name":"Story Points","custom":true,
	 "schema":{"type":"number"}}
]`

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
		// The key is what a real invocation carries — cobra enforces the
		// arity — and issue get now parses it before it resolves a field,
		// because a parse needs no network and the catalogue does.
		Jira: &stubSession{doer: doer}, Args: []string{"ENG-101"},
		Flags: registry.NewFlags(),
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
	// site overrides what Site reports, for a test about provenance.
	site   string
	fields []string
	doer   *stubDoer
	meta   *site.Metadata
	// conn and kind are set when a test needs the command to reach a recorded
	// conversation rather than only the catalogue.
	conn *transport.Client
	kind site.Kind
	// ledger backs the mutating verbs; nil means no idempotency protection.
	ledger *idem.Ledger
	// project overrides the default scope, and unscoped removes it entirely.
	project  string
	unscoped bool
	// jqlVerdict is what the server says about a raw --jql, as the body its
	// endpoint would return. Empty means a clean verdict, which is what almost
	// every test here wants: they pass a --jql to exercise something else and
	// are not about whether Jira understands it.
	jqlVerdict string
	// metaClient answers metadata lookups. It is separate from doer so a test
	// can point transitions at a recorded conversation while the field
	// catalogue stays a stub.
	metaClient site.Doer
	// baseURL is what the deployment reports as its own address. Empty is the
	// realistic default here — most tests do not care — and is also the case
	// --url has to refuse.
	baseURL string
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	kind := s.kind
	if kind == "" {
		kind = site.Cloud
	}
	return s.conn, site.Info{Kind: kind, BaseURL: s.baseURL}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	if s.meta == nil {
		kind := s.kind
		if kind == "" {
			kind = site.Cloud
		}
		// A nil *stubDoer is not a nil site.Doer: the interface holds a typed
		// nil, so a lookup this test did not think about reaches Do and
		// dereferences it. That was every test here until validation grew a
		// second metadata call, and the failure is a segfault in the harness
		// rather than a missing stub. An empty document is what a site with
		// nothing to say answers.
		var client site.Doer = &stubDoer{body: "{}"}
		switch {
		case s.metaClient != nil:
			client = s.metaClient
		case s.doer != nil:
			client = s.doer
		}
		s.meta = &site.Metadata{
			Client: jqlChecked{inner: client, verdict: s.jqlVerdict, kind: kind},
			Info:   site.Info{Kind: kind},
		}
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

// Fields is the context default field set. Empty unless a test sets it.
func (s *stubSession) Fields() []string { return s.fields }

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
//
// byPath overrides body for a request whose path contains the key. Metadata is
// no longer one endpoint — resolving an assignee searches the user directory
// while the field catalogue is a separate call — and a stub that answered the
// same JSON to both handed the field list back as a list of users.
type stubDoer struct {
	body   string
	byPath map[string]string
	calls  int
}

func (s *stubDoer) Do(_ context.Context, r transport.Request) (*transport.Response, error) {
	s.calls++
	body := s.body
	for path, override := range s.byPath {
		if strings.Contains(r.Path, path) {
			body = override
			break
		}
	}
	return &transport.Response{
		Status: 200,
		Body:   []byte(body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// jqlChecked answers the server-side query check and passes everything else to
// the client underneath.
//
// Every raw --jql is checked against Jira before the command runs, on the
// metadata client. A stub built to answer a catalogue lookup answers that check
// with a catalogue, which is not a parse result, so without this every test
// that passes a --jql fails on a request it was never about. A test that *is*
// about the verdict sets stubSession.jqlVerdict.
//
// Intercepting a POST to /search is safe on this client and only on this one:
// the metadata client fetches fields, users, labels and transitions, and never
// searches. It is the command's own transport that runs the query.
type jqlChecked struct {
	inner   site.Doer
	verdict string
	kind    site.Kind
}

func (j jqlChecked) Do(ctx context.Context, r transport.Request) (*transport.Response, error) {
	parse := j.kind == site.Cloud && strings.Contains(r.Path, "/jql/parse")
	search := j.kind != site.Cloud && r.Method == transport.MethodPost &&
		strings.HasSuffix(r.Path, "/search")
	if !parse && !search {
		return j.inner.Do(ctx, r)
	}
	body := j.verdict
	if body == "" {
		body = `{"queries":[{"query":"","errors":[],"warnings":[]}]}`
		if search {
			body = `{"issues":[],"warningMessages":[]}`
		}
	}
	return &transport.Response{
		Status: 200,
		Body:   []byte(body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestAQueryJiraWouldAnswerEmptyIsRefused is the defect this check exists for.
//
// Cloud's search endpoint answers a query it knows is meaningless with HTTP 200
// and no rows, so `--jql 'nosuchfield = 1'` came back well-formed, complete,
// exit 0 and empty: indistinguishable from an honest "nothing matches". Data
// Center refuses three of the four classes and answers the unknown-user class
// the same confident empty, so the check runs on both.
//
// A warning refuses as firmly as an error, because the warning is where Jira
// reports a value that does not exist for a field. `assignee = "nobody"` is
// valid JQL naming nobody, and it is the same wrong answer as a misspelled
// field. It also makes --jql agree with --assignee, which refuses a user it
// cannot resolve.
func TestAQueryJiraWouldAnswerEmptyIsRefused(t *testing.T) {
	const (
		unknownField = `{"queries":[{"query":"x","errors":` +
			`["Field nosuchfield does not exist or you do not have permission to view it."],` +
			`"warnings":[]}]}`
		unknownUser = `{"queries":[{"query":"x","errors":[],"warnings":` +
			`["The value nobody-xyz does not exist for the field assignee."]}]}`
		clean = `{"queries":[{"query":"x","errors":[],"warnings":[]}]}`
		// Data Center says it in the body of the bounded search instead.
		dcWarning = `{"issues":[],"warningMessages":` +
			`["The value nobody-xyz does not exist for the field assignee."]}`
		dcClean = `{"issues":[],"warningMessages":[]}`
	)

	cases := []struct {
		name    string
		kind    site.Kind
		verdict string
		says    string
	}{
		{"cloud, unknown field", site.Cloud, unknownField, "nosuchfield"},
		{"cloud, unknown user", site.Cloud, unknownUser, "nobody-xyz"},
		{"cloud, clean", site.Cloud, clean, ""},
		{"data center, unknown user", site.DataCenter, dcWarning, "nobody-xyz"},
		{"data center, clean", site.DataCenter, dcClean, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.list")
			if !ok {
				t.Fatal("issue list is not registered")
			}
			flags := registry.NewFlags()
			flags.SetString("jql", "assignee = \"nobody-xyz\"")
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Jira:  &stubSession{project: "ENG", kind: c.kind, jqlVerdict: c.verdict},
				Flags: flags, Limit: registry.Limit{N: 50},
				Progress: registry.NoProgress,
			})

			if c.says == "" {
				if err != nil {
					t.Fatalf("a query Jira understands was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a query Jira would answer with no rows was accepted")
			}
			if code := errs.Coerce(err).Code; code != "JQL_NOT_UNDERSTOOD" {
				t.Errorf("code = %q, want JQL_NOT_UNDERSTOOD", code)
			}
			if got := errs.ExitOf(err); got != exitcode.Usage {
				t.Errorf("exit = %v, want %v", got, exitcode.Usage)
			}
			// Jira's own words, because they name the field or the value and
			// carry the position, and a rewrite would lose both.
			if detail := errs.Coerce(err).Detail; !strings.Contains(detail, c.says) {
				t.Errorf("detail %q does not carry Jira's words about %q", detail, c.says)
			}
		})
	}
}

// A command with no --jql pays nothing: the check is the only request in
// Validate that a typed flag never triggers.
func TestAQueryCheckCostsNothingWithoutRawJQL(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	doer := &stubDoer{body: "{}"}
	flags := registry.NewFlags()
	flags.SetString("assignee", "currentUser")
	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira:  &stubSession{project: "ENG", metaClient: doer},
		Flags: flags, Limit: registry.Limit{N: 50},
		Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	// currentUser resolves locally, so nothing here should have asked the
	// server anything at all.
	if doer.calls != 0 {
		t.Errorf("made %d request(s) with no --jql; the check must be free "+
			"for every invocation that does not pass one", doer.calls)
	}
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

	// A field this package *does* model is accepted and dropped from the
	// requested set, because the node already renders it and a second element
	// named for it would be a duplicate.
	//
	// The column is a different question, and conflating the two is what made
	// `--field components` a no-op: it was already an *element* and had never
	// been a *column*, so on TSV — the default format — the flag did nothing.
	// The element stays single and the column appears.
	resolved, err := issue.ResolveFields(catalogue, []string{"components"})
	if err != nil {
		t.Fatalf("a natively-reported field was refused: %v", err)
	}
	if got := issue.ExtraFieldNames(resolved); len(got) != 0 {
		t.Errorf("ExtraFieldNames = %v, want the native field dropped so the "+
			"node does not carry it twice", got)
	}
	if got := issue.ExtraColumns(resolved); len(got) != 1 {
		t.Errorf("ExtraColumns = %v, want one column addressing the element "+
			"the node already renders", got)
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

// guardFlagValue is a value for each filter that narrows a query without
// needing a network: currentUser resolves through JQL rather than the
// directory, and a relative date parses locally.
var guardFlagValue = map[string]string{
	"jql": "labels = retry", "status": "Open", "not-status": "Closed",
	"label": "retry", "not-label": "wontfix",
	"type": "Bug", "not-type": "Sub-task",
	"assignee": "currentUser", "reporter": "currentUser",
	"creator": "currentUser", "involving": "currentUser",
	"watcher": "currentUser", "voter": "currentUser",
	"worklog-author": "currentUser", "was-assignee": "currentUser",
	"changed-by":    "currentUser",
	"created-after": "-7d", "created-before": "-1d",
	"updated-after": "-7d", "updated-before": "-1d",
	"changed-after": "-7d", "changed-before": "-1d",
	"worklog-after": "-7d", "worklog-before": "-1d",
}

// notAFilter are the flags on `issue list` that do not narrow the result set,
// with the reason each one does not.
var notAFilter = map[string]string{
	"sort":              "ordering an unbounded query does not make it smaller",
	"order":             "same: it turns the ordering around and drops no rows",
	"field":             "it widens each row rather than narrowing the set",
	"no-context-fields": "it removes columns, not rows",
	"all-projects":      "it is the way past the guard, not a way to satisfy it",
	"page-size":         "transport tuning",
	"page-token":        "a resume point, which the guard has already run for",
	"changed-field":     "it selects what --changed-by asks about and filters nothing",
	"url":               "it adds a column, not a condition",
	"age":               "same: it renders a timestamp the row already carries",
	// Same family as --url and --age: derived from what the row already
	// carries. Worth spelling out separately because this one looks like it
	// might be about writing, and a flag that narrows nothing must not be
	// offered as a way past the unbounded-query guard however useful it is.
	"precondition": "it encodes a timestamp the row already carries",
	// It makes each row bigger and the set no smaller, which is the opposite
	// of what the guard is for. Offering it as a way past an unbounded query
	// would be the worst possible advice: a whole-instance sweep that also
	// drags every comment thread back with it.
	"with-comments": "it widens each row rather than narrowing the set",
}

// TestAnyFilterSatisfiesTheGuard covers what counts as constraining, one flag
// at a time, because a guard that fires on a query somebody did narrow is worse
// than no guard.
//
// It is driven off ConstrainingFlags rather than a list of its own. That list
// is what the refusal offers the caller as a way out, and QueryOptions
// .Constrained is what actually decides — two lists that have to agree and
// have no reason to. A flag offered as a remedy that does not work is a dead
// end with a helpful tone; the reverse, a filter Constrained does not see, is
// how a --watcher query becomes a full-instance sweep.
func TestAnyFilterSatisfiesTheGuard(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	for _, flag := range issue.ConstrainingFlags() {
		name := strings.TrimPrefix(flag, "--")
		if name == "project" {
			continue // Comes from the context, not from a flag; covered below.
		}
		t.Run(flag, func(t *testing.T) {
			value, ok := guardFlagValue[name]
			if !ok {
				t.Fatalf("%s is offered as a way to constrain a query and this "+
					"test has no value for it", flag)
			}
			flags := registry.NewFlags()
			flags.SetString(name, value)
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Jira:  &stubSession{unscoped: true},
				Flags: flags, Limit: registry.Limit{All: true},
				Progress: registry.NoProgress,
			})
			if err != nil {
				t.Errorf("%s did not satisfy the guard: %v", flag, err)
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

// TestEveryFlagIsAFilterOrIsNot makes adding a filter and forgetting the guard
// a failing test rather than a silent widening.
//
// Every flag on `issue list` has to be classified: either it narrows the result
// set, in which case it belongs in ConstrainingFlags and in
// QueryOptions.Constrained, or it does not, in which case notAFilter says why.
// A new filter lands in neither list by default, and the failure it would
// otherwise cause is invisible — the guard keeps passing, and one day it passes
// on a query nobody narrowed.
func TestEveryFlagIsAFilterOrIsNot(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")

	constraining := map[string]bool{}
	for _, flag := range issue.ConstrainingFlags() {
		constraining[strings.TrimPrefix(flag, "--")] = true
	}

	for _, f := range cmd.Flags {
		if constraining[f.Name] {
			if _, both := notAFilter[f.Name]; both {
				t.Errorf("--%s is listed as constraining and as not a filter", f.Name)
			}
			continue
		}
		if _, ok := notAFilter[f.Name]; !ok {
			t.Errorf("--%s is neither offered as a constraint nor explained as "+
				"not being one: add it to constrainingFlags and to "+
				"QueryOptions.Constrained, or to notAFilter with the reason",
				f.Name)
		}
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

// TestTheOrderingFlagsReachTheQuery walks the flags from the shape a caller
// types to the JQL that goes out, for the two that were easiest to believe were
// working.
//
// --order without --sort was harvested, passed along, and then dropped by the
// ordering policy, so `-o asc` answered with a descending result set and
// nothing said so. And a date filter orders nothing: --updated-after -1d with
// no --sort comes back in key order, which on a busy project looks enough like
// "recent first" to be mistaken for it.
func TestTheOrderingFlagsReachTheQuery(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(registry.Flags)
		want string
	}{
		{
			name: "a date filter does not order the result",
			set: func(f registry.Flags) {
				f.SetString("updated-after", "-1d")
			},
			want: `updated >= "-1d" ORDER BY issuekey DESC`,
		},
		{
			name: "the direction alone turns the key ordering around",
			set: func(f registry.Flags) {
				f.SetString("order", "asc")
			},
			want: `ORDER BY issuekey ASC`,
		},
		{
			name: "asking for it by update time",
			set: func(f registry.Flags) {
				f.SetString("updated-after", "-1d")
				f.SetString("sort", "updated")
				f.SetString("order", "desc")
			},
			want: `updated >= "-1d" ORDER BY updated DESC, issuekey DESC`,
		},
		{
			name: "several statuses excluded",
			set: func(f registry.Flags) {
				f.SetString("not-status", "Closed")
				f.SetString("not-status", "Done")
			},
			want: `status NOT IN ("Closed", "Done") ORDER BY issuekey DESC`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := registry.NewFlags()
			tc.set(flags)
			got, err := issue.BuildQuery(issue.ListQueryFor(&registry.Invocation{
				Jira: &stubSession{unscoped: true}, Flags: flags,
			}))
			if err != nil {
				t.Fatalf("BuildQuery: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
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

// TestAContextFieldSetReachesTheRequest is the test that would have caught the
// defect this fixes.
//
// `context edit --field 'Story Points'` validated the name, wrote it to
// config.toml, and printed it back from `context show`. Nothing sent it:
// registry.Session had Project() and Board() and no Fields(), so validateFields
// asked the flag and nothing else. Every existing test round-tripped the value
// through the config, which is why they all passed while the feature did
// nothing.
//
// So this drives the command, not the config, and asserts on the columns a
// caller sees rather than on stored state.
func TestAContextFieldSetReachesTheRequest(t *testing.T) {
	const catalogue = `[{"id":"customfield_10042","name":"Story Points","custom":true,
		"schema":{"type":"number"}},
		{"id":"customfield_10077","name":"Team","custom":true,"schema":{"type":"string"}}]`

	for _, tc := range []struct {
		name    string
		context []string
		flag    []string
		noCtx   bool
		want    []string
	}{
		{
			name:    "the context's set is sent with no flag at all",
			context: []string{"Story Points"},
			want:    []string{"customfield_10042"},
		},
		{
			// The whole point of the union. Replace would have dropped
			// customfield_10042 here, which is the original --field bug.
			name:    "a flag adds to the context rather than replacing it",
			context: []string{"Story Points"},
			flag:    []string{"Team"},
			want:    []string{"customfield_10042", "customfield_10077"},
		},
		{
			name:    "context first, so a flag does not reorder the columns",
			context: []string{"Team"},
			flag:    []string{"Story Points"},
			want:    []string{"customfield_10077", "customfield_10042"},
		},
		{
			// Two spellings of one field are one column, not two.
			name:    "a field named in both places appears once",
			context: []string{"Story Points"},
			flag:    []string{"customfield_10042"},
			want:    []string{"customfield_10042"},
		},
		{
			name:    "--no-context-fields leaves only what was typed",
			context: []string{"Story Points"},
			flag:    []string{"Team"},
			noCtx:   true,
			want:    []string{"customfield_10077"},
		},
		{
			name:    "--no-context-fields with no flag asks for nothing extra",
			context: []string{"Story Points"},
			noCtx:   true,
			want:    nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.list")
			if !ok {
				t.Fatal("issue list is not registered")
			}

			flags := registry.NewFlags()
			for _, f := range tc.flag {
				// SetString appends for a repeatable flag, which is how a real
				// `--field a --field b` arrives.
				flags.SetString("field", f)
			}
			if tc.noCtx {
				flags.SetBool("no-context-fields", true)
			}
			inv := &registry.Invocation{
				Jira: &stubSession{
					fields:     tc.context,
					metaClient: &stubDoer{body: catalogue},
					doer:       &stubDoer{body: catalogue},
				},
				Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}

			// The columns are what a caller sees, and they are computed from
			// the ids validation resolved — so asserting here covers the whole
			// path without reaching into unexported state.
			var extra []string
			for _, col := range cmd.ColumnsFor(inv) {
				if strings.HasPrefix(col.Header, "customfield_") {
					extra = append(extra, col.Header)
				}
			}
			if strings.Join(extra, ",") != strings.Join(tc.want, ",") {
				t.Errorf("extra columns = %v, want %v", extra, tc.want)
			}
		})
	}
}

// TestAStaleContextFieldSaysWhereItCameFrom covers the cost of the union: a
// field the context names and Jira no longer has now fails every read, not just
// the invocations that passed --field.
//
// The refusal is correct — §5.2, nothing is guessed — but it has to point at
// the context, because the caller did not type this name on this command line
// and has no reason to look there.
func TestAStaleContextFieldSaysWhereItCameFrom(t *testing.T) {
	cmd, _ := registry.Lookup("issue.list")
	inv := &registry.Invocation{
		Jira: &stubSession{
			fields:     []string{"Retired Field"},
			metaClient: &stubDoer{body: `[{"id":"customfield_10042","name":"Story Points","custom":true}]`},
			doer:       &stubDoer{body: `[]`},
		},
		Flags: registry.NewFlags(), Stderr: io.Discard, Progress: registry.NoProgress,
	}

	err := cmd.Validate(t.Context(), inv)
	if err == nil {
		t.Fatal("a context field that resolves to nothing was accepted")
	}
	remedy := errs.Coerce(err).Remedy
	if !strings.Contains(remedy, "context") {
		t.Errorf("the remedy does not point at the context: %q", remedy)
	}
	if !strings.Contains(remedy, "--unset field") {
		t.Errorf("the remedy does not say how to clear it: %q", remedy)
	}
}

func runGetWithComments(t *testing.T, fixture string, withComments bool) (*render.Doc, *transport.Replayer) {
	t.Helper()
	cmd, ok := registry.Lookup("issue.get")
	if !ok {
		t.Fatal("issue get is not registered")
	}
	conn, replayer := replayConn(t, fixture)

	flags := registry.NewFlags()
	if withComments {
		flags.SetBool("with-comments", true)
	}
	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.DataCenter}, Args: []string{"ENG-101"},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return doc, replayer
}

// TestWithCommentsFoldsTheThreadIntoTheRecord covers the ordinary case, and the
// one that must stay true when the flag is absent: comments cost a second
// request and nobody pays it unasked.
func TestWithCommentsFoldsTheThreadIntoTheRecord(t *testing.T) {
	doc, replayer := runGetWithComments(t, "get-comments.datacenter.json", true)
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the comments were never fetched: %v", unplayed)
	}

	comments, ok := doc.Record.ChildNamed("comments")
	if !ok {
		t.Fatal("the record carries no comments")
	}
	if count, _ := comments.AttrValue("count"); count != "2" {
		t.Errorf("count = %q, want 2", count)
	}
	if complete, _ := comments.AttrValue(render.CompleteAttr); complete != "true" {
		t.Errorf("complete = %q, want true for a whole thread", complete)
	}
	if !doc.IsComplete() {
		t.Error("a record holding a whole thread reports itself partial")
	}
}

// TestCommentsCostNothingWithoutTheFlag is the other half. A second request
// nobody asked for is a second request against --max-requests.
func TestCommentsCostNothingWithoutTheFlag(t *testing.T) {
	doc, replayer := runGetWithComments(t, "get-comments.datacenter.json", false)
	if _, has := doc.Record.ChildNamed("comments"); has {
		t.Error("comments arrived without --with-comments")
	}
	// The comment interaction is deliberately left unplayed here, which is the
	// assertion: the cassette holds it and the command must not reach for it.
	unplayed := replayer.Unplayed()
	if len(unplayed) != 1 || !strings.Contains(unplayed[0], "/comment") {
		t.Errorf("unplayed = %v, want exactly the comment request", unplayed)
	}
}

// TestATruncatedThreadIsNeverReportedAsComplete is the assertion this whole
// design decision exists for.
//
// A record had no way to say it held part of something: `complete` lived only
// on a collection envelope, and Doc.IsComplete said so in its own comment —
// "a record is always complete". A comment thread is paged, so a bounded one is
// the normal case rather than an edge, and reporting it as whole is the single
// failure this output contract exists to prevent.
func TestATruncatedThreadIsNeverReportedAsComplete(t *testing.T) {
	doc, _ := runGetWithComments(t, "get-comments-truncated.datacenter.json", true)

	comments, ok := doc.Record.ChildNamed("comments")
	if !ok {
		t.Fatal("the record carries no comments")
	}
	if complete, _ := comments.AttrValue(render.CompleteAttr); complete != "false" {
		t.Errorf("complete = %q, want false: 2 of 57 comments is not a thread", complete)
	}
	if doc.IsComplete() {
		t.Fatal("a record holding part of a thread reports itself complete")
	}

	// The warning that accompanies exit 3 has to name what was cut, because
	// "this issue is partial" does not say which part.
	var warning strings.Builder
	if err := render.WriteTruncationWarning(&warning, doc, render.XML); err != nil {
		t.Fatalf("warning: %v", err)
	}
	for _, want := range []string{"RESULT_TRUNCATED", "comments", "issue.get"} {
		if !strings.Contains(warning.String(), want) {
			t.Errorf("the warning does not mention %q:\n%s", want, warning.String())
		}
	}
}

// TestEveryDefaultFetchedFieldGetsAColumn is the fix for a flag that resolved
// its argument, fetched it, and produced nothing.
//
// `--field created` was accepted, `created` was already in DefaultFields so it
// was fetched, the node already rendered it — and TSV, the default format,
// looked exactly as it had before. `--field Team` worked, so the flag looked
// fine wherever anybody tested it. The reasoning that produced the bug is in a
// comment on nativeFields: "a caller naming one of these changes nothing,
// because it is already in the output", which was true of the XML and false of
// the projection almost everybody reads.
//
// It drives every field in DefaultFields by name rather than a representative,
// because the defect is precisely that one subset behaved differently from
// another. Four of them are already columns and must stay one column; the rest
// have to appear.
func TestEveryDefaultFetchedFieldGetsAColumn(t *testing.T) {
	already := map[string]bool{
		"summary": true, "status": true, "assignee": true, "updated": true,
	}

	defaults := issue.ListColumns()
	for _, field := range issue.DefaultFields() {
		cols := append(issue.ListColumns(), issue.ExtraColumns([]string{field})...)

		if already[field] {
			if len(cols) != len(defaults) {
				t.Errorf("--field %s added a column to a field already projected: %v",
					field, headersOf(cols))
			}
			continue
		}
		if len(cols) != len(defaults)+1 {
			t.Errorf("--field %s produced %d columns, want one more than the "+
				"default set: %v", field, len(cols), headersOf(cols))
			continue
		}
		// The column has to point at something. A path naming an element the
		// node never emits is the same defect one layer along.
		added := cols[len(cols)-1]
		if added.Path == "" {
			t.Errorf("--field %s added a column with no path", field)
		}
	}
}

// TestAColumnForANativeFieldPointsAtTheNode holds each mapped path to the shape
// Issue.Node actually produces, because a path is a claim about the document.
//
// The names differ on purpose: `issuetype` is rendered as the `type` attribute
// and `fixVersions` as the `fix-versions` list, so a column named for the field
// would address nothing.
func TestAColumnForANativeFieldPointsAtTheNode(t *testing.T) {
	full := issue.Issue{
		Key: "ENG-1", ID: "1", Summary: "s", Type: "Story", Priority: "High",
		Project: "ENG", Resolution: "Done", Parent: "ENG-9",
		Created: "2026-08-10T00:00:00Z", Updated: "2026-08-10T00:00:00Z",
		Reporter:    issue.User{ID: "ada", Display: "Ada Lovelace"},
		Description: "why", BodyFormat: issue.BodyWiki,
		Labels:     []string{"transport"},
		Components: []string{"api"}, FixVersions: []string{"1.2.0"},
	}
	node := full.Node()

	for field, want := range map[string]string{
		"reporter":    "Ada Lovelace",
		"priority":    "High",
		"issuetype":   "Story",
		"project":     "ENG",
		"created":     "2026-08-10T00:00:00Z",
		"resolution":  "Done",
		"parent":      "ENG-9",
		"description": "why",
		"labels":      "transport",
		"components":  "api",
		"fixversions": "1.2.0",
	} {
		cols := issue.ExtraColumns([]string{field})
		if len(cols) != 1 {
			t.Errorf("--field %s produced %d columns", field, len(cols))
			continue
		}
		got, ok := node.Lookup(cols[0].Path)
		if !ok {
			t.Errorf("--field %s: path %q does not address the node",
				field, cols[0].Path)
			continue
		}
		if got != want {
			t.Errorf("--field %s: path %q gave %q, want %q",
				field, cols[0].Path, got, want)
		}
	}
}

// TestTwoSpellingsOfOneFieldAreOneColumn covers the pair the catalogue can
// return either way. A caller naming a field twice wants it once.
func TestTwoSpellingsOfOneFieldAreOneColumn(t *testing.T) {
	cols := issue.ExtraColumns([]string{"fixVersions", "fixversions"})
	if len(cols) != 1 {
		t.Errorf("ExtraColumns = %v, want one fix-versions column", headersOf(cols))
	}
	if len(cols) == 1 && cols[0].Header != "fix-versions" {
		t.Errorf("header = %q, want the name the node uses", cols[0].Header)
	}
}

// TestTheReporterReachesTheOutput covers the field that was fetched on every
// request and rendered nowhere.
//
// It is in DefaultFields, it is parsed into Issue.Reporter, and until
// issue.list v4 no format showed it — so `--reporter ada` filtered on a value
// no output could carry, and there was no way to check what the filter had
// done. Found by asking why `--field reporter` did nothing, which it also did.
func TestTheReporterReachesTheOutput(t *testing.T) {
	node := issue.Issue{
		Key: "ENG-1", ID: "1",
		Reporter: issue.User{ID: "ada", Display: "Ada Lovelace"},
	}.Node()

	child, ok := node.ChildNamed("reporter")
	if !ok {
		t.Fatal("an issue reports no reporter, and every request asks for one")
	}
	if display, _ := child.AttrValue("display"); display != "Ada Lovelace" {
		t.Errorf("reporter display = %q", display)
	}
	// Present and empty when Jira discloses nobody, on the same terms as the
	// assignee: absent and nobody are different facts.
	empty, ok := issue.Issue{Key: "ENG-2", ID: "2"}.Node().ChildNamed("reporter")
	if !ok {
		t.Fatal("the reporter element vanishes when there is no reporter")
	}
	if display, _ := empty.AttrValue("display"); display != "" {
		t.Errorf("empty reporter display = %q", display)
	}
}

// headersOf names the columns for a failure message.
func headersOf(cols []render.Column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Header)
	}
	return out
}

// TestANamedFieldIsAlsoFetched covers the half of --field that decides what
// goes to Jira, as opposed to what comes out.
//
// ExtraFieldNames answers "which ids need an element of their own", so it drops
// the natives — and five of those are modelled by this package and are *not* in
// DefaultFields, because a listing does not fetch them. Building the request
// from it meant `--field parent` produced a column over a field nobody had
// asked for, and every cell in it was empty. Which ids need an element and
// which need fetching are two questions, and one answer was being used for
// both.
func TestANamedFieldIsAlsoFetched(t *testing.T) {
	for _, field := range []string{
		// Native, modelled, and absent from a listing's request.
		"description", "resolution", "parent", "components", "fixVersions",
		// Custom, which always worked.
		"customfield_10042",
	} {
		got := issue.RequestedFields([]string{field})
		if !slices.Contains(got, field) {
			t.Errorf("--field %s is not in the request: %v", field, got)
		}
	}

	// A field already in the default set is asked for once. Jira accepts a
	// repeat, and a request that names the same field twice is a request this
	// tool did not mean to build.
	got := issue.RequestedFields([]string{"summary", "summary", "created"})
	if len(got) != len(issue.DefaultFields()) {
		t.Errorf("RequestedFields = %v, want no duplicate of a default", got)
	}
}

// TestAgeIsCoarseAndInOneUnit covers where the units change, which is the only
// interesting thing about this function and the only part a clock would make
// hard to test. It is pure for that reason.
func TestAgeIsCoarseAndInOneUnit(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string {
		return now.Add(-d).UTC().Format(time.RFC3339)
	}

	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0 seconds"},
		{time.Second, "1 second"},
		{59 * time.Second, "59 seconds"},
		// Every boundary from both sides. A unit that changes one tick early
		// reports an hour-old issue as 60 minutes.
		{time.Minute, "1 minute"},
		{89 * time.Second, "1 minute"},
		{59*time.Minute + 59*time.Second, "59 minutes"},
		{time.Hour, "1 hour"},
		{23*time.Hour + 59*time.Minute, "23 hours"},
		{24 * time.Hour, "1 day"},
		{47 * time.Hour, "1 day"},
		{48 * time.Hour, "2 days"},
		// It stops at days on purpose: a month has no fixed length and a year
		// has two, so a coarser unit would mean whichever divisor this file
		// happened to pick and the caller could not tell which.
		{400 * 24 * time.Hour, "400 days"},
	} {
		if got := issue.Age(at(tc.d), now); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestAgeSaysNothingRatherThanGuessing covers the inputs there is no honest
// answer for.
func TestAgeSaysNothingRatherThanGuessing(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	// Absent stays absent. "0 seconds" would claim the issue was updated just
	// now, which is a different thing from not knowing when it was.
	if got := issue.Age("", now); got != "" {
		t.Errorf("Age(\"\") = %q, want nothing", got)
	}
	if got := issue.Age("not a timestamp", now); got != "" {
		t.Errorf("Age of an unparseable stamp = %q, want nothing", got)
	}
	// A server clock ahead of this machine's. Reporting a negative age would
	// report the skew, which is not what the column is about.
	future := now.Add(2 * time.Hour).UTC().Format(time.RFC3339)
	if got := issue.Age(future, now); got != "0 seconds" {
		t.Errorf("Age of a future stamp = %q, want the floor", got)
	}
}

// TestAgeNeverReplacesTheTimestamp is the contract half, and the reason --age
// adds a column instead of changing one.
//
// Every timestamp this tool emits is RFC 3339 in UTC and consumers parse it.
// Rendering `updated` as "3 hours" would break them silently, months after
// somebody put the flag in a shell alias.
func TestAgeNeverReplacesTheTimestamp(t *testing.T) {
	stamp := "2026-08-10T09:00:00Z"
	node := issue.Issue{
		Key: "ENG-1", ID: "1", Updated: stamp,
		Age: issue.Age(stamp, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)),
	}.Node()

	updated, ok := node.ChildNamed("updated")
	if !ok || updated.Text != stamp {
		t.Errorf("updated = %+v, want the RFC 3339 instant untouched", updated)
	}
	age, ok := node.ChildNamed("age")
	if !ok || age.Text != "3 hours" {
		t.Errorf("age = %+v, want the rendering beside it", age)
	}
	// And absent entirely when nobody asked, so a row costs nothing extra.
	plain := issue.Issue{Key: "ENG-1", ID: "1", Updated: stamp}.Node()
	if _, ok := plain.ChildNamed("age"); ok {
		t.Error("an age appeared without --age")
	}
}

// TestBothCommandsDeclareTheAgeFlag keeps the pair from drifting: --age exists
// on the listing and on the record, or a caller learns which by trying.
func TestBothCommandsDeclareTheAgeFlag(t *testing.T) {
	for _, name := range []string{"issue.list", "issue.get"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !slices.ContainsFunc(cmd.Flags, func(f registry.Flag) bool {
			return f.Name == "age"
		}) {
			t.Errorf("%s declares no --age", name)
		}
	}
}

// Site implements registry.Session. A fixed value, because provenance is a
// property of the answer and these tests assert documents.
func (s *stubSession) Site() string {
	if s.site != "" {
		return s.site
	}
	return "https://recorded.invalid"
}
