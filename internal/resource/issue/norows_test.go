package issue_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// What Cloud's parse endpoint says of `project = "NOPE"`, less the structure,
// and what it says of a project it knows.
const (
	parseUnknownProject = `{"queries":[{"query":"x","errors":` +
		`["The value 'NOPE' does not exist for the field 'project'."]}]}`
	parseClean = `{"queries":[{"query":"x","errors":[],"warnings":[]}]}`
)

// quietSite is a Jira whose searches come back with the issues a test gives
// it, which is none unless it says otherwise, in the shape each deployment
// pages with. It also answers the clock, which a feed reads first.
type quietSite struct {
	kind   site.Kind
	issues []string
	// unfinished is a Cloud that sends an empty page and says there is more.
	unfinished bool
}

func (q *quietSite) RoundTrip(r *http.Request) (*http.Response, error) {
	rows := strings.Join(q.issues, ",")
	body := `{}`
	switch {
	case strings.HasSuffix(r.URL.Path, "/serverInfo"):
		body = fmt.Sprintf(`{"deploymentType":"Cloud","version":"1001.0.0",
			"serverTime":%q}`, feedServerTime)
	case strings.Contains(r.URL.Path, "/search") && q.unfinished:
		body = `{"issues":[],"isLast":false,"nextPageToken":"more"}`
	case strings.Contains(r.URL.Path, "/search") && q.kind == site.Cloud:
		body = fmt.Sprintf(`{"issues":[%s],"isLast":true}`, rows)
	case strings.Contains(r.URL.Path, "/search"):
		body = fmt.Sprintf(`{"startAt":0,"maxResults":50,"total":%d,"issues":[%s]}`,
			len(q.issues), rows)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// projectJudge is the metadata client of a site that may be asked about a
// project scope. It answers with what the test says Jira thinks and keeps every
// query it was asked to judge, so a test can tell a floor that was asked for
// from one that was not.
//
// On Data Center the verdict route is a search bounded to zero rows, which is
// also how a sweep is counted, so both arrive here and scopeAlone tells them
// apart by what they asked about.
type projectJudge struct {
	inner   site.Doer
	verdict string
	asked   []string
}

func (j *projectJudge) Do(ctx context.Context, r transport.Request) (*transport.Response, error) {
	answer := func(status int, body string) (*transport.Response, error) {
		return &transport.Response{
			Status: status,
			Body:   []byte(body),
			Header: map[string][]string{"Content-Type": {"application/json"}},
		}, nil
	}
	switch {
	case strings.Contains(r.Path, "/jql/parse"):
		j.asked = append(j.asked, string(r.Body))
		if j.verdict == "" {
			return answer(http.StatusOK, parseClean)
		}
		return answer(http.StatusOK, j.verdict)
	case strings.HasSuffix(r.Path, "/search/approximate-count"):
		return answer(http.StatusOK, `{"count":0}`)
	case r.Method == transport.MethodPost && strings.HasSuffix(r.Path, "/search"):
		j.asked = append(j.asked, string(r.Body))
		return answer(http.StatusOK, `{"issues":[],"warningMessages":[],"total":0}`)
	}
	return j.inner.Do(ctx, r)
}

// scopeAlone counts the questions that were about the project scope and
// nothing else: the clause, closed by the quote that ends the JSON string it
// was sent in.
func (j *projectJudge) scopeAlone(key string) int {
	n := 0
	for _, body := range j.asked {
		if strings.Contains(body, `project = \"`+key+`\""`) {
			n++
		}
	}
	return n
}

// noRows is one run of a command over a project scope, against a site whose
// search answers with what the test gave it.
type noRows struct {
	stdout string
	result registry.StreamResult
	err    error
	judge  *projectJudge
}

// overProject validates and then runs one of the three commands that search a
// project scope, in TSV, the one format that could have put bytes on stdout
// before the command finished.
func overProject(
	t *testing.T, name string, kind site.Kind, scope []string, verdict string,
	issues []string, set ...func(registry.Flags),
) noRows {
	t.Helper()
	return overSite(t, name, &quietSite{kind: kind, issues: issues}, scope, verdict, set...)
}

// overSite is overProject against a site the test built itself.
func overSite(
	t *testing.T, name string, server *quietSite, scope []string, verdict string,
	set ...func(registry.Flags),
) noRows {
	t.Helper()

	kind := server.kind
	cmd, ok := registry.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	conn, err := transport.New(transport.Options{
		BaseURL:    "https://quiet.invalid",
		HTTPClient: &http.Client{Transport: server},
		Retries:    -1,
	})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	judge := &projectJudge{
		inner: &stubDoer{
			body:   catalogueJSON,
			byPath: map[string]string{"myself": accountJSON},
		},
	}

	flags := registry.NewFlags()
	if name != "issue.list" {
		flags.SetString("since", "-1d")
	}
	for _, apply := range set {
		apply(flags)
	}
	session := &stubSession{
		conn: conn, kind: kind,
		meta: &site.Metadata{Client: judge, Info: site.Info{Kind: kind}},
	}
	switch len(scope) {
	case 0:
		session.unscoped = true
	case 1:
		session.project = scope[0]
	default:
		session.projects = scope
	}
	inv := &registry.Invocation{
		Jira: session, Flags: flags, Limit: registry.Limit{N: 50},
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// What Validate asks is its own business: a set is checked there, before
	// anything is searched, and it is admitted here so the run is reached.
	// These tests are about what the run asks, and what it is told.
	judge.asked, judge.verdict = nil, verdict

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	return noRows{stdout: buf.String(), result: result, err: err, judge: judge}
}

// projectScoped is the three commands that search a project scope.
var projectScoped = []string{"issue.list", "issue.activity", "issue.changes"}

// TestNoRowsOverAProjectJiraDoesNotKnowIsRefused is the floor under a scope of
// one project.
//
// Cloud's search answers `project = "NOPE"` with HTTP 200 and no rows, and that
// went out as a complete, empty collection at exit 0 with EMPTY_RESULT naming
// the key (measured 2026-10-12): the document a real project with nothing in
// it gives. So a mistyped key, and a context whose project is gone, read as
// "nothing matches", and a feed polling one said "nothing changed" on every
// tick. An answer with no rows is now asked about, once, and refused in Jira's
// words when the project is not there.
//
// Nothing reaches stdout first, which is what makes it a refusal and not a
// warning beside an answer. TSV is the format that could have: it is the one
// that writes before Close.
func TestNoRowsOverAProjectJiraDoesNotKnowIsRefused(t *testing.T) {
	for _, name := range projectScoped {
		t.Run(name, func(t *testing.T) {
			got := overProject(t, name, site.Cloud, []string{"NOPE"}, parseUnknownProject, nil)
			if got.err == nil {
				t.Fatalf("no rows over a project Jira does not know was answered "+
					"as an empty result: %+v", got.result)
			}
			e := errs.Coerce(got.err)
			if e.Code != "UNKNOWN_PROJECT" || e.Exit != exitcode.NotFound {
				t.Errorf("refusal = %s at exit %v, want UNKNOWN_PROJECT at %v",
					e.Code, e.Exit, exitcode.NotFound)
			}
			if want := "The value 'NOPE' does not exist for the field 'project'."; e.Detail != want {
				t.Errorf("detail = %q, want Jira's own sentence %q", e.Detail, want)
			}
			if got.stdout != "" {
				t.Errorf("the refusal came after bytes were on stdout: %q", got.stdout)
			}
			if n := got.judge.scopeAlone("NOPE"); n != 1 {
				t.Errorf("the scope was asked about %d times, want once: %q",
					n, got.judge.asked)
			}
		})
	}
}

// TestNoRowsOverAProjectJiraKnowsIsStillAnAnswer is the other verdict, and the
// reason the floor is a question and not a rule: a project with nothing in it
// is an honest empty result, and stays one.
func TestNoRowsOverAProjectJiraKnowsIsStillAnAnswer(t *testing.T) {
	for _, name := range projectScoped {
		t.Run(name, func(t *testing.T) {
			got := overProject(t, name, site.Cloud, []string{"ENG"}, parseClean, nil)
			if got.err != nil {
				t.Fatalf("an empty project was refused: %v", got.err)
			}
			if !got.result.Complete {
				t.Errorf("an empty project was reported incomplete: %+v", got.result)
			}
			if n := got.judge.scopeAlone("ENG"); n != 1 {
				t.Errorf("the scope was asked about %d times, want once: %q",
					n, got.judge.asked)
			}
		})
	}
}

// TestAProjectThatAnsweredIsNotAskedAbout holds the floor to the one answer
// that needs it, which is what keeps it off the common path.
//
// The judge here would refuse the project if it were asked, so a question that
// should not have been sent fails the run and not only the count.
func TestAProjectThatAnsweredIsNotAskedAbout(t *testing.T) {
	// An issue with nothing on it: a row for `issue list`, and a candidate
	// with no event and no change for the two that report those.
	quiet := []string{feedIssue("NOPE-1", 0)}

	for _, name := range projectScoped {
		t.Run(name, func(t *testing.T) {
			got := overProject(t, name, site.Cloud, []string{"NOPE"}, parseUnknownProject, quiet)
			if got.err != nil {
				t.Fatalf("a project that sent an issue back was refused as "+
					"unknown: %v", got.err)
			}
			if n := len(got.judge.asked); n != 0 {
				t.Errorf("a project that answered was asked about anyway: %q",
					got.judge.asked)
			}
			// The two that report events wrote none, and that is the point:
			// what is counted is the issues read, not the rows written.
			rows := strings.Count(got.stdout, "\n")
			if name != "issue.list" && rows != 0 {
				t.Errorf("%s wrote %d line(s) from an issue with nothing on it:\n%s",
					name, rows, got.stdout)
			}
		})
	}
}

// TestTheFloorIsAskedOnlyWhereItCanAnswer is every scope that is not one
// project on Cloud, each of which must cost nothing here.
//
// The judge refuses in every case, so asking is a failure twice over.
func TestTheFloorIsAskedOnlyWhereItCanAnswer(t *testing.T) {
	allProjects := func(f registry.Flags) { f.SetBool("all-projects", true) }

	for _, tc := range []struct {
		name     string
		commands []string
		kind     site.Kind
		scope    []string
		set      []func(registry.Flags)
	}{
		// Data Center 10.4.0 refuses the search itself, so an empty answer
		// there already has the project's word, and its only verdict route is
		// that search again.
		{"data center", projectScoped, site.DataCenter, []string{"NOPE"}, nil},
		// A set was asked about before anything was searched.
		{"several projects", projectScoped, site.Cloud, []string{"ENG", "NOPE"}, nil},
		// No scope, so nothing to ask about.
		{
			"--all-projects",
			[]string{"issue.list", "issue.activity"},
			site.Cloud,
			[]string{"NOPE"},
			[]func(registry.Flags){allProjects},
		},
	} {
		for _, name := range tc.commands {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				got := overProject(t, name, tc.kind, tc.scope, parseUnknownProject, nil, tc.set...)
				if got.err != nil {
					t.Fatalf("refused: %v", got.err)
				}
				for _, key := range tc.scope {
					if n := got.judge.scopeAlone(key); n != 0 {
						t.Errorf("the run asked about %s alone %d time(s): %q",
							key, n, got.judge.asked)
					}
				}
			})
		}
	}
}

// TestAWalkThatDidNotFinishIsNotAskedAbout is the other half of "no rows": the
// walk has to have ended.
//
// A page with nothing on it that does not claim to be the last stops the walk,
// which would otherwise loop, and the answer is reported as cut short. That is
// already not a claim that there is nothing, so there is nothing for the floor
// to stand under, and a question here would be a request spent on an answer
// that calls itself incomplete either way.
func TestAWalkThatDidNotFinishIsNotAskedAbout(t *testing.T) {
	for _, name := range projectScoped {
		t.Run(name, func(t *testing.T) {
			got := overSite(t, name, &quietSite{kind: site.Cloud, unfinished: true},
				[]string{"NOPE"}, parseUnknownProject)
			if got.err != nil {
				t.Fatalf("a walk that did not finish was refused as an unknown "+
					"project: %v", got.err)
			}
			if got.result.Complete {
				t.Fatalf("a walk the server left open was reported complete, so "+
					"this is not the case under test: %+v", got.result)
			}
			if n := len(got.judge.asked); n != 0 {
				t.Errorf("an unfinished walk was asked about: %q", got.judge.asked)
			}
		})
	}
}

// TestTheRecordedEmptyAnswersAreToldApart replays what a Cloud instance said to
// `issue list --project NOPE` and to `issue list --project AGL --type Bug`: the
// same search result, HTTP 200 and `{"issues":[],"isLast":true}`, and then the
// parse of the scope clause, which is the only place the two differ.
//
// The tests above decide both halves of their exchanges. This one establishes
// that the search really is indistinguishable, byte for byte, and that the
// question is one Jira accepts as this code builds it and answers in the
// sentence the refusal carries.
func TestTheRecordedEmptyAnswersAreToldApart(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	run := func(t *testing.T, fixture, project string, set func(registry.Flags)) error {
		t.Helper()
		conn, replayer := recordedConn(t, fixture)
		info := site.Info{Kind: site.Cloud, Version: "test"}

		flags := registry.NewFlags()
		if set != nil {
			set(flags)
		}
		inv := &registry.Invocation{
			Jira: &stubSession{
				project: project, conn: conn, kind: site.Cloud,
				meta: &site.Metadata{Client: conn, Info: info},
			},
			Flags: flags, Limit: registry.Limit{N: 50},
			Stderr: io.Discard, Progress: registry.NoProgress,
		}
		var buf strings.Builder
		stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
			Kind: cmd.Kind(), Version: cmd.KindVersion(),
			Name: cmd.CollectionName, Columns: cmd.Columns,
		})
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		_, runErr := cmd.Stream(t.Context(), inv, stream)
		if buf.Len() != 0 {
			t.Errorf("bytes reached stdout before the stream was closed: %q", buf.String())
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("a recorded exchange was never requested: %v", unplayed)
		}
		return runErr
	}

	t.Run("a project Jira does not know", func(t *testing.T) {
		err := run(t, "no-rows-unknown-project-recorded.cloud.json", "NOPE", nil)
		if err == nil {
			t.Fatal("the recorded empty answer over an unknown project was accepted")
		}
		e := errs.Coerce(err)
		if e.Code != "UNKNOWN_PROJECT" || e.Exit != exitcode.NotFound {
			t.Errorf("refusal = %s at exit %v, want UNKNOWN_PROJECT at %v",
				e.Code, e.Exit, exitcode.NotFound)
		}
		if want := "The value 'NOPE' does not exist for the field 'project'."; e.Detail != want {
			t.Errorf("detail = %q, want the server's own sentence %q", e.Detail, want)
		}
	})

	t.Run("a project with nothing that matches", func(t *testing.T) {
		err := run(t, "no-rows-known-project-recorded.cloud.json", "AGL",
			func(f registry.Flags) { f.SetString("type", "Bug") })
		if err != nil {
			t.Fatalf("the recorded empty answer over a real project was refused: %v", err)
		}
	})
}
