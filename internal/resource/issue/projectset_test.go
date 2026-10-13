package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// TestARepeatedProjectScopesTheQueryToEveryKey is issue 216 from the flag to
// the JQL. Several projects used to mean `--all-projects --jql 'project in
// (...)'`, with the quoting left to the caller, and one of the three keys here
// is a reserved word Jira refuses unquoted. The scope is a set on the query
// options, never also a single project, and every key goes out quoted.
func TestARepeatedProjectScopesTheQueryToEveryKey(t *testing.T) {
	set := []string{"OPS", "NET", "DO"}
	inv := &registry.Invocation{
		Jira: &stubSession{projects: set}, Flags: registry.NewFlags(),
	}

	opt := issue.ListQueryFor(inv)
	if opt.Project != "" {
		t.Errorf("Project = %q beside a set of three", opt.Project)
	}
	if strings.Join(opt.Projects, " ") != "OPS NET DO" {
		t.Fatalf("Projects = %q, want the three given", opt.Projects)
	}
	if !opt.Constrained() {
		t.Error("a scope of three projects reads as an unconstrained sweep")
	}
	query, err := issue.BuildQuery(opt)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if want := `project IN ("OPS", "NET", "DO") ORDER BY issuekey DESC`; query != want {
		t.Errorf("query  = %s\nwanted = %s", query, want)
	}

	// --all-projects lifts a set as it lifts one project.
	flags := registry.NewFlags()
	flags.SetBool("all-projects", true)
	lifted := issue.ListQueryFor(&registry.Invocation{
		Jira: &stubSession{projects: set}, Flags: flags,
	})
	if lifted.Project != "" || len(lifted.Projects) != 0 {
		t.Errorf("--all-projects left the scope %q %q", lifted.Project, lifted.Projects)
	}
}

// TestASetOfProjectsIsNeverPagedByKey holds the keyset walk to one project.
//
// `issuekey <` does not compare across projects (measured on both deployments,
// 2026-09-04, and written on canKeyset), so a bound taken from the last row of
// a page selects inside that row's project and drops every project the walk
// has not reached. The walk then reads a short page as the end and reports the
// first project alone as complete. A set of projects is exactly the query that
// would do it, and it has to take the offset walk, which checks its anchor.
func TestASetOfProjectsIsNeverPagedByKey(t *testing.T) {
	client, _ := replayClient(t, site.DataCenter)

	one := issue.ListOptions{Query: issue.QueryOptions{Project: "ENG"}}
	if !issue.CanKeysetForTest(client, one) {
		t.Fatal("one project is not paged by key on Data Center, so this test " +
			"cannot tell a set from a walk that never pages by key")
	}
	set := issue.ListOptions{Query: issue.QueryOptions{Projects: []string{"ENG", "OPS"}}}
	if issue.CanKeysetForTest(client, set) {
		t.Error("a set of projects is paged by issue key, which reports the " +
			"first project alone as the whole answer")
	}
	// And through the flags, which is the path a caller takes.
	opt := issue.ListOptions{Query: issue.ListQueryFor(&registry.Invocation{
		Jira: &stubSession{projects: []string{"ENG", "OPS"}}, Flags: registry.NewFlags(),
	})}
	if issue.CanKeysetForTest(client, opt) {
		t.Error("a repeated --project reaches the walk as something it pages by key")
	}
}

// TestAnUnknownProjectInASetIsRefused is the floor a set needs before it is
// searched, which one project does not.
//
// Cloud's search answers `project in ("ENG","NOPE")` with HTTP 200, ENG's rows,
// and no word about NOPE (measured 2026-10-12). One unknown key answers with
// nothing, and is asked about then: see norows_test.go. Among several it
// contributes nothing and the answer reads as the union that was asked for, so
// there is no empty answer to wait for. The parse endpoint does name it, in
// the words below, so the scope clause is checked there before any search, on
// each of the three commands that take a set.
func TestAnUnknownProjectInASetIsRefused(t *testing.T) {
	// Cloud's own answer for `project IN ("ET", "NOPE")`, less the structure.
	const unknown = `{"queries":[{"query":"x","errors":` +
		`["The value 'NOPE' does not exist for the field 'project'."]}]}`

	for _, name := range []string{"issue.list", "issue.activity", "issue.changes"} {
		t.Run(name, func(t *testing.T) {
			cmd, ok := registry.Lookup(name)
			if !ok {
				t.Fatalf("%s is not registered", name)
			}
			validate := func(s *stubSession) error {
				flags := registry.NewFlags()
				if name != "issue.list" {
					flags.SetString("since", "-1d")
				}
				return cmd.Validate(t.Context(), &registry.Invocation{
					Jira: s, Flags: flags, Limit: registry.Limit{N: 50},
					Progress: registry.NoProgress,
				})
			}

			err := validate(&stubSession{
				projects: []string{"ENG", "NOPE"}, kind: site.Cloud, jqlVerdict: unknown,
			})
			if err == nil {
				t.Fatal("a set holding a project Jira does not know was admitted")
			}
			e := errs.Coerce(err)
			if e.Code != "UNKNOWN_PROJECT" || e.Exit != exitcode.NotFound {
				t.Errorf("refusal = %s at exit %v, want UNKNOWN_PROJECT at %v",
					e.Code, e.Exit, exitcode.NotFound)
			}
			if !strings.Contains(e.Detail, "NOPE") {
				t.Errorf("detail %q does not carry Jira's words naming the key", e.Detail)
			}

			// A set Jira knows is admitted.
			if err := validate(&stubSession{
				projects: []string{"ENG", "OPS"}, kind: site.Cloud,
			}); err != nil {
				t.Errorf("a set of projects Jira knows was refused: %v", err)
			}

			// One project never asks before the search. The server here would
			// refuse it, and it is admitted, because asking first is the price
			// of a set and a scope of one is every invocation that came before.
			// It is asked about after a search that found nothing.
			if err := validate(&stubSession{
				project: "NOPE", kind: site.Cloud, jqlVerdict: unknown,
			}); err != nil {
				t.Errorf("one project paid for the check a set needs: %v", err)
			}
		})
	}
}

// TestTheRecordedSetOfProjectsIsCheckedAndThenPagedToTheEnd replays what a
// Cloud instance said to `issue list --project AGL --project OPS --page-size 4
// --limit all`: the parse of the scope clause, and five pages.
//
// The tests above decide both halves of their exchanges. This one establishes
// that the two requests a set adds are ones Jira accepts as this code builds
// them, with every key quoted, and that the cursor walks across the boundary
// between the projects: the third page holds the last of OPS and the first of
// AGL. The order is by project key and then by number, which is what
// Key.Compare implements and what a keyset bound cannot follow.
func TestTheRecordedSetOfProjectsIsCheckedAndThenPagedToTheEnd(t *testing.T) {
	conn, replayer := replayConn(t, "list-projects-recorded.cloud.json")
	info := site.Info{Kind: site.Cloud, Version: "test"}
	set := []string{"AGL", "OPS"}

	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	inv := &registry.Invocation{
		Jira: &stubSession{
			projects: set, kind: site.Cloud,
			meta: &site.Metadata{Client: conn, Info: info},
		},
		Flags: registry.NewFlags(), Limit: registry.Limit{All: true},
		Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("the scope clause this code builds is not one the server "+
			"called valid: %v", err)
	}

	client := &issue.Client{Transport: conn, Site: info}
	result, err := client.List(t.Context(), issue.ListOptions{
		Query:    issue.ListQueryFor(inv),
		Limit:    registry.Limit{All: true},
		PageSize: 4,
		Fields:   issue.DefaultFields(),
	})
	if err != nil {
		t.Fatalf("the search this code builds is not the one the server "+
			"answered: %v", err)
	}
	if result.Keyset {
		t.Error("a set of projects was paged by issue key")
	}
	if !result.Complete {
		t.Error("an exhausted listing was reported incomplete")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded exchange was never requested: %v", unplayed)
	}

	var keys []string
	for _, i := range result.Issues {
		keys = append(keys, i.Key)
	}
	want := "OPS-10 OPS-9 OPS-8 OPS-7 OPS-6 OPS-5 OPS-4 OPS-3 OPS-2 OPS-1 " +
		"AGL-9 AGL-8 AGL-7 AGL-6 AGL-5 AGL-4 AGL-3 AGL-2 AGL-1"
	if got := strings.Join(keys, " "); got != want {
		t.Errorf("keys = %s\nwant   %s", got, want)
	}
}

// TestTheRecordedUnknownProjectIsRefusedInJirasWords replays what each
// deployment said about a set holding a key it does not know, which is the one
// request `issue list --project <known> --project NOPE` makes. The refusal
// carries the server's sentence, and nothing was searched: neither recording
// holds a search to answer one.
//
// The two servers reach that sentence differently, and the check is what makes
// them one answer. Cloud's parse endpoint reports it at HTTP 200, beside a
// search that would have said nothing. Data Center 10.4.0 refuses the
// validating search itself with a 400, as it would the real one, so without
// the check a set there fails as BAD_REQUEST at exit 2 and on Cloud does not
// fail at all. With it both are UNKNOWN_PROJECT at exit 5.
func TestTheRecordedUnknownProjectIsRefusedInJirasWords(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	for _, tc := range []struct {
		kind    site.Kind
		fixture string
		known   string
	}{
		{site.Cloud, "unknown-project-recorded.cloud.json", "AGL"},
		{site.DataCenter, "unknown-project-recorded.datacenter.json", "ENG"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			conn, replayer := recordedConn(t, tc.fixture)
			info := site.Info{Kind: tc.kind, Version: "test"}

			err := cmd.Validate(t.Context(), &registry.Invocation{
				Jira: &stubSession{
					projects: []string{tc.known, "NOPE"}, kind: tc.kind,
					meta: &site.Metadata{Client: conn, Info: info},
				},
				Flags: registry.NewFlags(), Limit: registry.Limit{N: 50},
				Progress: registry.NoProgress,
			})
			if err == nil {
				t.Fatal("a set holding a project the server does not know was admitted")
			}
			e := errs.Coerce(err)
			if e.Code != "UNKNOWN_PROJECT" || e.Exit != exitcode.NotFound {
				t.Errorf("refusal = %s at exit %v, want UNKNOWN_PROJECT at %v",
					e.Code, e.Exit, exitcode.NotFound)
			}
			if want := "The value 'NOPE' does not exist for the field 'project'."; e.Detail != want {
				t.Errorf("detail = %q, want the server's own sentence %q", e.Detail, want)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the recorded check was never requested: %v", unplayed)
			}
		})
	}
}
