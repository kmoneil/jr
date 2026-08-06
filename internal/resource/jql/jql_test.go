package jql_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	jqlcmd "github.com/kmoneil/jira-cli/internal/resource/jql"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

const (
	validQuery   = "project = ENG AND status = Open"
	invalidQuery = "project = ENG status = Open"
)

// TestBothDeploymentsAnswerTheSameQuestion is why both fixtures exist. Cloud
// has an endpoint for this; Data Center does not, and a search bounded to zero
// rows is the closest thing it has. A caller sees one shape either way.
func TestBothDeploymentsAnswerTheSameQuestion(t *testing.T) {
	for _, tc := range []struct {
		kind    site.Kind
		fixture string
		method  string
	}{
		{site.Cloud, "parse", jqlcmd.MethodParse},
		{site.DataCenter, "search", jqlcmd.MethodSearch},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			conn, replayer := replayConn(t, tc.fixture+"."+string(tc.kind)+".json")
			client := &jqlcmd.Client{Transport: conn, Site: site.Info{Kind: tc.kind}}

			got, err := client.Check(t.Context(), validQuery)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if !got.Valid {
				t.Errorf("a valid query was reported invalid: %v", got.Errors)
			}
			if got.Query != validQuery {
				t.Errorf("query = %q", got.Query)
			}
			// The method is reported because "valid" from a parse and "valid"
			// from a zero-row search are not quite the same claim.
			if got.Method != tc.method {
				t.Errorf("method = %q, want %q", got.Method, tc.method)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the request was never sent: %v", unplayed)
			}
		})
	}
}

// TestAMalformedQueryIsAResultNotAnError is the distinction this command rests
// on. It was asked whether the query is valid; finding out that it is not is
// the command succeeding.
func TestAMalformedQueryIsAResultNotAnError(t *testing.T) {
	for _, kind := range []site.Kind{site.Cloud, site.DataCenter} {
		conn, replayer := replayConn(t, "invalid."+string(kind)+".json")
		client := &jqlcmd.Client{Transport: conn, Site: site.Info{Kind: kind}}

		got, err := client.Check(t.Context(), invalidQuery)
		if err != nil {
			t.Fatalf("%s: a bad query failed the command instead of answering it: %v",
				kind, err)
		}
		if got.Valid {
			t.Errorf("%s: a malformed query was reported valid", kind)
		}
		if len(got.Errors) == 0 {
			t.Fatalf("%s: invalid with no reason given", kind)
		}
		// Jira's message, unedited, position and all. Rewriting it into this
		// tool's own words would lose the one thing worth having.
		if !strings.Contains(got.Errors[0], "line 1, character 15") {
			t.Errorf("%s: error = %q, want Jira's own position", kind, got.Errors[0])
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: the request was never sent: %v", kind, unplayed)
		}
	}
}

// TestDataCenterPutsSomeComplaintsUnderErrorsJql covers the shape that is not
// errorMessages. A version that reports this way would otherwise produce
// "invalid" with no reason at all.
func TestDataCenterPutsSomeComplaintsUnderErrorsJql(t *testing.T) {
	conn, _ := replayConn(t, "errorskey.datacenter.json")
	client := &jqlcmd.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	got, err := client.Check(t.Context(), invalidQuery)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if got.Valid {
		t.Error("a refused query was reported valid")
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0], "does not exist") {
		t.Errorf("errors = %v, want the message from errors.jql", got.Errors)
	}
}

// TestWarningsSurviveAValidQuery covers what Jira flags without refusing — a
// status that exists in one project and not another. Dropping it would report a
// query as clean when the server said it was not.
func TestWarningsSurviveAValidQuery(t *testing.T) {
	conn, _ := replayConn(t, "warnings.datacenter.json")
	client := &jqlcmd.Client{Transport: conn, Site: site.Info{Kind: site.DataCenter}}

	got, err := client.Check(t.Context(), validQuery)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !got.Valid {
		t.Error("a query Jira accepted was reported invalid")
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %v, want the one Jira sent", got.Warnings)
	}
	node := got.Node()
	if _, ok := node.ChildNamed("warning"); !ok {
		t.Error("the warning did not reach the output")
	}
}

// TestAQueryThatCannotLexCostsNoRoundTrip covers the local checks running
// first. They are not a parser and never claim to be, but an unterminated
// string is not worth a request.
//
// It is reported rather than refused. Refusing would mean the same bad query
// produced a document when Jira caught it and an error when this did, so which
// shape a caller got would depend on how the query happened to be wrong.
func TestAQueryThatCannotLexCostsNoRoundTrip(t *testing.T) {
	cmd, ok := registry.Lookup("jql.validate")
	if !ok {
		t.Fatal("jql validate is not registered")
	}
	conn, replayer := replayConn(t, "parse.cloud.json")
	flags := registry.NewFlags()
	flags.SetString("jql", `summary ~ "unterminated`)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("a local refusal failed the command instead of reporting it: %v", err)
	}
	if valid, _ := doc.Record.AttrValue("valid"); valid != "false" {
		t.Errorf("valid = %q, want false", valid)
	}
	if method, _ := doc.Record.AttrValue("method"); method != jqlcmd.MethodLocal {
		t.Errorf("method = %q, want %q", method, jqlcmd.MethodLocal)
	}
	if _, ok := doc.Record.ChildNamed("error"); !ok {
		t.Error("the local verdict carried no reason")
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("a request was made anyway: %v", unmatched)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) != 1 {
		t.Errorf("unplayed = %v, want the recorded parse untouched", unplayed)
	}
}

// TestAnInvalidQueryStillExitsZero pins the decision, because it is the kind
// that gets quietly reversed. The reasons are the product — an agent checking
// before it acts needs to know what is wrong — and an exit code cannot carry a
// list of them.
func TestAnInvalidQueryStillExitsZero(t *testing.T) {
	cmd, _ := registry.Lookup("jql.validate")
	for _, code := range cmd.ExitCodes {
		if code == 2 {
			t.Error("jql validate declares exit 2; an invalid query is a result")
		}
	}

	conn, _ := replayConn(t, "invalid.cloud.json")
	flags := registry.NewFlags()
	flags.SetString("jql", invalidQuery)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("an invalid query failed the command: %v", err)
	}
	if valid, _ := doc.Record.AttrValue("valid"); valid != "false" {
		t.Errorf("valid = %q, want false", valid)
	}
}

// TestExplainParenthesizesTheRawFragment is the whole reason `jql explain`
// exists. Without the parentheses,
//
//	--jql 'status = Open OR status = Closed' --project ENG
//
// means "in ENG and open, or closed anywhere" — an OR that escapes the project
// scope and returns issues from projects the caller never named.
func TestExplainParenthesizesTheRawFragment(t *testing.T) {
	got, err := jqlcmd.Explain("status = Open OR status = Closed", "ENG", "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !got.Parenthesized {
		t.Error("the fragment was not reported as parenthesized")
	}

	want := `project = "ENG" AND (status = Open OR status = Closed) ORDER BY issuekey DESC`
	if got.Query != want {
		t.Errorf("query  = %s\nwanted = %s", got.Query, want)
	}
	// The OR must not be able to reach outside the project scope.
	if strings.Contains(got.Query, `project = "ENG" AND status = Open OR`) {
		t.Error("the OR escaped the project scope")
	}
}

// TestExplainShowsTheOrderByThatIsAlwaysThere covers the other invariant this
// command makes visible. An unordered query depends on the server's
// undocumented default, which is not guaranteed stable between two requests.
func TestExplainShowsTheOrderByThatIsAlwaysThere(t *testing.T) {
	for _, tc := range []struct {
		sort, order string
		want        string
	}{
		{"", "", "ORDER BY issuekey DESC"},
		{"updated", "", "ORDER BY updated ASC, issuekey DESC"},
		{"updated", "desc", "ORDER BY updated DESC, issuekey DESC"},
	} {
		got, err := jqlcmd.Explain("labels = retry", "", tc.sort, tc.order)
		if err != nil {
			t.Fatalf("sort=%q: %v", tc.sort, err)
		}
		if !strings.HasSuffix(got.Query, tc.want) {
			t.Errorf("sort=%q order=%q: %q does not end with %q",
				tc.sort, tc.order, got.Query, tc.want)
		}
	}

	if _, err := jqlcmd.Explain("labels = retry", "", "updated", "sideways"); err == nil {
		t.Error("--order sideways was accepted")
	} else if code := errs.Coerce(err).Code; code != "INVALID_ORDER" {
		t.Errorf("code = %q, want INVALID_ORDER", code)
	}
}

// TestExplainReportsFieldsByTokenizing covers the difference between this and a
// regular expression. The text inside a value is a value: the incumbent's regex
// equivalent reads `summary ~ "project = FOO"` as a project scope and then
// drops the one it thought was already there.
func TestExplainReportsFieldsByTokenizing(t *testing.T) {
	got, err := jqlcmd.Explain(`summary ~ "project = FOO" AND labels = retry`, "", "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if strings.Join(got.Fields, ",") != "summary,labels" {
		t.Errorf("fields = %v, want summary and labels — the text inside the "+
			"value is not a field reference", got.Fields)
	}
}

// TestExplainMakesNoRequest is the claim in its description. A command that
// says it explains without asking must not ask.
func TestExplainMakesNoRequest(t *testing.T) {
	cmd, ok := registry.Lookup("jql.explain")
	if !ok {
		t.Fatal("jql explain is not registered")
	}
	conn, replayer := replayConn(t, "parse.cloud.json")
	flags := registry.NewFlags()
	flags.SetString("jql", validQuery)

	doc, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira: &stubSession{conn: conn, kind: site.Cloud, project: "ENG"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("jql explain made a request: %v", unmatched)
	}
	// The context's project is part of the answer, exactly as on issue list.
	project, ok := doc.Record.ChildNamed("project")
	if !ok || project.Text != "ENG" {
		t.Errorf("project = %+v, want the context's", project)
	}
}

// TestBothCommandsAreReadsInEveryBuild keeps the resource out of the write tag.
// Neither changes anything, and validate deliberately runs no query.
func TestBothCommandsAreReadsInEveryBuild(t *testing.T) {
	for _, name := range []string{"jql.validate", "jql.explain"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if cmd.Mutating || cmd.Destructive {
			t.Errorf("%s is a read but declares otherwise", name)
		}
		if len(cmd.RequiresTags) != 0 {
			t.Errorf("%s requires %v; reading needs no tag", name, cmd.RequiresTags)
		}
		if cmd.Paginated {
			t.Errorf("%s is a record, not a collection", name)
		}
	}
}

// TestValidateWithoutASessionFailsLoudly covers the guard the networked half
// carries.
func TestValidateWithoutASessionFailsLoudly(t *testing.T) {
	cmd, _ := registry.Lookup("jql.validate")
	flags := registry.NewFlags()
	flags.SetString("jql", validQuery)

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("jql validate ran without a session")
	}
	if code := errs.Coerce(err).Code; code != "NO_SESSION" {
		t.Errorf("code = %q, want NO_SESSION", code)
	}
}

// TestDocumentsValidate keeps both outputs renderable in every format.
func TestDocumentsValidate(t *testing.T) {
	result := jqlcmd.Result{
		Query: validQuery, Valid: false,
		Errors: []string{"Error in the JQL Query"}, Method: jqlcmd.MethodParse,
	}
	if err := render.Record(jqlcmd.KindValidate, jqlcmd.VersionValidate,
		result.Node()).Validate(); err != nil {
		t.Fatalf("validate result: %v", err)
	}

	explained, err := jqlcmd.Explain("labels = retry", "ENG", "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if err := render.Record(jqlcmd.KindExplain, jqlcmd.VersionExplain,
		explained.Node()).Validate(); err != nil {
		t.Fatalf("validate explanation: %v", err)
	}
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

// stubSession is a registry.Session over a recorded conversation, so a command
// runs with no auth, no config, and no network.
type stubSession struct {
	conn    *transport.Client
	kind    site.Kind
	project string
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, site.Info{Kind: s.kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Info: site.Info{Kind: s.kind}}, nil
}

func (s *stubSession) Idempotency() *idem.Ledger  { return nil }
func (s *stubSession) Project() string            { return s.project }
func (s *stubSession) Board() string              { return "" }
func (s *stubSession) CheckWritable(string) error { return nil }

func (s *stubSession) RequireProject() (string, error) {
	return s.project, nil
}

func (s *stubSession) RequireBoard() (string, error) {
	return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
}
