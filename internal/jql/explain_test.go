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
	got, err := jql.Explain("status = Open OR status = Closed", "ENG", "", "")
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
		got, err := jql.Explain("labels = retry", "", tc.sort, tc.order)
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
	got, err := jql.Explain(`summary ~ "project = FOO" AND labels = retry`, "", "", "")
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
			_, err := jql.Explain(tc.fragment, tc.project, tc.sort, tc.order)
			if err == nil {
				t.Fatal("the input composed")
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
		})
	}
}
