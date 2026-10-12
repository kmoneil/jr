package jql_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jql"
)

// TestExplainParenthesizesTheRawFragment is the whole reason `jql explain`
// exists. Without the parentheses,
//
//	--jql 'status = Open OR status = Closed' --project ENG
//
// means "in ENG and open, or closed anywhere": an OR that escapes the project
// scope and returns issues from projects the caller never named.
func TestExplainParenthesizesTheRawFragment(t *testing.T) {
	got, err := jql.Explain("status = Open OR status = Closed", []string{"ENG"}, "", "")
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
		got, err := jql.Explain("labels = retry", nil, tc.sort, tc.order)
		if err != nil {
			t.Fatalf("sort=%q: %v", tc.sort, err)
		}
		if !strings.HasSuffix(got.Query, tc.want) {
			t.Errorf("sort=%q order=%q: %q does not end with %q",
				tc.sort, tc.order, got.Query, tc.want)
		}
	}
}

// TestExplainReportsFieldsByTokenizing covers the difference between this and a
// regular expression. The text inside a value is a value: the incumbent's regex
// equivalent reads `summary ~ "project = FOO"` as a project scope and then
// drops the one it thought was already there.
func TestExplainReportsFieldsByTokenizing(t *testing.T) {
	got, err := jql.Explain(`summary ~ "project = FOO" AND labels = retry`, nil, "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if strings.Join(got.Fields, ",") != "summary,labels" {
		t.Errorf("fields = %v, want summary and labels; the text inside the "+
			"value is not a field reference", got.Fields)
	}
}

// TestExplainRefusesWhatItCannotCompose walks every refusal, one input per
// return. The refusals are part of the contract: a diagnostic that
// approximated a broken input would report a query nothing would send.
func TestExplainRefusesWhatItCannotCompose(t *testing.T) {
	for _, tc := range []struct {
		name, fragment, project, sort, order, code string
	}{
		{
			"an unterminated quote does not tokenize",
			`summary ~ "unclosed`, "", "", "", "JQL_SYNTAX",
		},
		{
			"an unbalanced fragment would escape its wrapper",
			"(labels = retry", "", "", "", "JQL_SYNTAX",
		},
		{
			"an empty fragment is a missing query",
			"   ", "", "", "", "EMPTY_JQL",
		},
		{
			"a direction that is not one",
			"labels = retry", "", "updated", "sideways", "INVALID_ORDER",
		},
		{
			"a scope the quoter refuses",
			"labels = retry", "EN\xffG", "", "", "INVALID_ENCODING",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var scope []string
			if tc.project != "" {
				scope = []string{tc.project}
			}
			_, err := jql.Explain(tc.fragment, scope, tc.sort, tc.order)
			if err == nil {
				t.Fatal("the input composed")
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

// TestExplainScopesToEveryProjectGiven is the query a repeated --project
// builds, holding the key that sent issue 216's reporter to hand-written JQL.
// DO is a reserved word, and Jira refuses `project in (OPS, NET, DO)` for it.
// Every key goes through the quoter here, so a caller never has to know which
// of theirs is one, and the OR in the fragment still cannot reach outside the
// set.
func TestExplainScopesToEveryProjectGiven(t *testing.T) {
	got, err := jql.Explain("status = Open OR status = Closed",
		[]string{"OPS", "NET", "DO"}, "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	want := `project IN ("OPS", "NET", "DO") AND (status = Open OR status = Closed) ` +
		`ORDER BY issuekey DESC`
	if got.Query != want {
		t.Errorf("query  = %s\nwanted = %s", got.Query, want)
	}
	if got.Project != "" {
		t.Errorf("a set of projects was also reported as the one project %q", got.Project)
	}
	if strings.Join(got.Projects, " ") != "OPS NET DO" {
		t.Errorf("Projects = %q, want the three given", got.Projects)
	}
}
