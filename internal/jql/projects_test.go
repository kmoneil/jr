package jql_test

import (
	"slices"
	"testing"

	"github.com/kmoneil/jr/internal/jql"
)

// TestProjectsSelectedReadsWhatAQueryCanReturn covers the shapes a fragment
// uses to name a project, and the ones that name one without selecting it.
func TestProjectsSelectedReadsWhatAQueryCanReturn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		// The direct forms.
		{"project equals", "project = ENG", []string{"ENG"}},
		{"project in", "project in (ENG, OPS)", []string{"ENG", "OPS"}},
		{"project quoted", `project = "ENG"`, []string{"ENG"}},
		{"lowercased is folded", "project = eng", []string{"ENG"}},

		// An issue key carries its project in front of the hyphen. This is the
		// form that cost the review its afternoon: `key in (…)` with 103 keys
		// across three projects, against a context set to one of them.
		{"key equals", "key = GOV-208", []string{"GOV"}},
		{"key in", "key in (UPF-1407, GOV-208)", []string{"UPF", "GOV"}},
		{"issuekey spelling", "issuekey = ENG-1", []string{"ENG"}},
		{"parent", "parent = ENG-1", []string{"ENG"}},
		{"id as a key", "id = ENG-1", []string{"ENG"}},

		// Deduplicated, in first-appearance order.
		{"repeats collapse", "key in (ENG-1, ENG-2, OPS-9)", []string{"ENG", "OPS"}},
		{"project and key agree", "project = ENG AND key = ENG-1", []string{"ENG"}},

		// Negation names a project and does not select it. Warning on these
		// would fire on a caller who is deliberately excluding something.
		{"not equals", "project != ENG", nil},
		{"not in", "project not in (ENG, OPS)", nil},

		// Neither does a comparison that cannot carry a key.
		{"like", "project ~ ENG", nil},
		{"is empty", "parent is EMPTY", nil},
		{"was", "project WAS ENG", nil},

		// A value that names no project statically.
		{"numeric id", "id = 10021", nil},
		{"no hyphen", "key = ENG", nil},
		{"empty prefix", "key = -1", nil},
		{"function value", "project = projectsLeadByUser()", []string{"PROJECTSLEADBYUSER"}},

		// Fields that carry no project at all.
		{"other fields", "status = Open AND assignee = ada", nil},
		{"empty-ish", "labels in (a, b)", nil},

		// The regex trap this package exists to avoid: the text inside a string
		// value is a value, not a field.
		{"a project inside a value", `summary ~ "project = FOO"`, nil},
		{"a key inside a value", `summary ~ "key = GOV-208"`, nil},

		// Lists this cannot read whole yield nothing rather than a partial
		// answer, because a partial answer warns about a query that also names
		// the scope it is checked against.
		{"unclosed list", "project in (ENG, OPS", nil},
		{"nested list", "project in (ENG, (OPS))", nil},
		{"list of numbers", "id in (10021, 10022)", nil},

		// Truncated inputs, which is where an index walks off the end.
		{"trailing in", "project in", nil},
		{"trailing equals", "project =", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jql.ProjectsSelected(tc.query)
			if err != nil {
				t.Fatalf("ProjectsSelected(%q): %v", tc.query, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ProjectsSelected(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestProjectsSelectedRefusesWhatItCannotLex passes the tokenizer's error up
// rather than reporting no projects, because "this names nothing" and "this
// does not lex" are different answers and only one of them is safe to act on.
func TestProjectsSelectedRefusesWhatItCannotLex(t *testing.T) {
	if _, err := jql.ProjectsSelected(`project = "unclosed`); err == nil {
		t.Error("an unlexable query reported its projects instead of failing")
	}
}

// TestProjectsSelectedIsNotAFieldScan is the guard that keeps this from being
// re-implemented as "does the query mention a project".
//
// A query naming a project-bearing field in a way that selects nothing has to
// come back empty. If it did not, every --jql carrying a `key` clause would
// warn, and a warning that fires on correct queries is one nobody reads.
func TestProjectsSelectedIsNotAFieldScan(t *testing.T) {
	const query = "key != ENG-1 AND project not in (OPS) AND parent is EMPTY"
	got, err := jql.ProjectsSelected(query)
	if err != nil {
		t.Fatalf("ProjectsSelected: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a query that selects no project reported %v; every clause "+
			"here names a project-bearing field and none of them selects one", got)
	}
}
