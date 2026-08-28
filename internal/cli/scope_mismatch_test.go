package cli_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// TestAnOutOfScopeJQLSaysSo is the review's costliest finding, reproduced and
// then answered.
//
// The invocation is theirs. Against a context whose project was IDO, a --jql
// naming GOV- and UPF- keys returned 195 rows of a 252-row answer with
// complete="true" and exit 0, and a second one returned nothing at all, which
// is the same bytes as an honest "nothing matched". Six calls and four turns
// went into finding out why, and the answer was that jr had ANDed its own
// context onto the query and said nothing.
func TestAnOutOfScopeJQLSaysSo(t *testing.T) {
	var jql atomic.Value
	url := emptyJira(t, &jql)

	scoped := func(t *testing.T) map[string]string {
		t.Helper()
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
		return env
	}

	t.Run("a key from another project", func(t *testing.T) {
		got := run(t, scoped(t), "issue", "list", "--jql", "key = OPS-208")
		assertScopeWarning(t, got, "OPS", "ENG")
	})

	t.Run("a project from another project", func(t *testing.T) {
		got := run(t, scoped(t), "issue", "list", "--jql", "project = OPS")
		assertScopeWarning(t, got, "OPS", "ENG")
	})

	t.Run("a list, and every excluded project is named", func(t *testing.T) {
		got := run(t, scoped(t), "issue", "list",
			"--jql", "key in (ENG-1, OPS-208, UPF-1407)")
		assertScopeWarning(t, got, "OPS", "ENG")
		if !strings.Contains(got.stderr, "UPF") {
			t.Errorf("only one of two excluded projects was named:\n%s", got.stderr)
		}
		// The one that is in scope must not be reported as excluded. Read the
		// list itself rather than searching the whole message: the message
		// names the scope too, so "does ENG appear" is a question that is
		// always yes and was the first version of this assertion.
		if listed := excludedList(t, got.stderr); strings.Contains(listed, "ENG") {
			t.Errorf("the in-scope project is in the excluded list %q:\n%s",
				listed, got.stderr)
		}
	})

	t.Run("issue activity, which is the command they were running", func(t *testing.T) {
		got := run(t, scoped(t), "issue", "activity", "--since", "-7d",
			"--jql", "key = OPS-208")
		assertScopeWarning(t, got, "OPS", "ENG")
	})

	t.Run("issue changes", func(t *testing.T) {
		got := run(t, scoped(t), "issue", "changes", "--since", "-7d",
			"--jql", "key = OPS-208")
		assertScopeWarning(t, got, "OPS", "ENG")
	})
}

// TestTheScopeWarningStaysQuietOnQueriesThatAreFine is the half that makes the
// warning worth shipping.
//
// A warning that fires on correct queries is one nobody reads, and every case
// here is a query somebody writes on an ordinary day.
func TestTheScopeWarningStaysQuietOnQueriesThatAreFine(t *testing.T) {
	var jql atomic.Value
	url := emptyJira(t, &jql)

	for _, tc := range []struct {
		name string
		args []string
		why  string
	}{
		{
			"no --jql at all",
			[]string{"issue", "list"},
			"the common invocation must not pay for this check",
		},
		{
			"a --jql that names no project",
			[]string{"issue", "list", "--jql", "status = Open"},
			"most fragments name no project and every one of them is fine",
		},
		{
			"a --jql inside the scope",
			[]string{"issue", "list", "--jql", "key = ENG-1"},
			"naming the project you are scoped to is not a mismatch",
		},
		{
			"--all-projects lifts the scope",
			[]string{
				"issue", "list", "--all-projects", "--limit", "all",
				"--jql", "key = OPS-1",
			},
			"there is no scope to be outside of",
		},
		{
			"--project widens to the one named",
			[]string{"--project", "OPS", "issue", "list", "--jql", "key = OPS-1"},
			"the flag is how a caller says which scope they meant",
		},
		{
			"a negation names a project without selecting it",
			[]string{"issue", "list", "--jql", "project != OPS"},
			"excluding a project is not asking for it",
		},
		{
			"a project key inside a string value",
			[]string{"issue", "list", "--jql", `summary ~ "key = OPS-1"`},
			"the text inside a value is a value, which is why this reads tokens",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url,
				"--project", "ENG")

			got := run(t, env, tc.args...)
			if got.exit != exitcode.OK {
				t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
			}
			if strings.Contains(got.stderr, "SCOPE_MISMATCH") {
				t.Errorf("warned on a query that is fine (%s):\n%s",
					tc.why, got.stderr)
			}
		})
	}
}

// excludedList returns just the projects the warning names as excluded, from
// between "selects projects " and the scope clause that follows.
func excludedList(t *testing.T, stderr string) string {
	t.Helper()
	_, after, found := strings.Cut(stderr, "selects projects ")
	if !found {
		t.Fatalf("the warning does not list several projects:\n%s", stderr)
	}
	before, found := strings.CutSuffix(
		strings.SplitN(after, ", and the scope is", 2)[0], "")
	if !found {
		t.Fatalf("the warning does not name the scope after the list:\n%s", stderr)
	}
	return before
}

// assertScopeWarning checks the warning fired, named both halves of the
// contradiction, and left the command alone.
func assertScopeWarning(t *testing.T, got result, excluded, scope string) {
	t.Helper()

	// Exit 0 and the rows still come. The query is legal and the caller may
	// have meant it; not being able to tell is what was wrong.
	if got.exit != exitcode.OK {
		t.Fatalf("the warning changed the exit code: %v\nstderr: %s",
			got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "SCOPE_MISMATCH") {
		t.Fatalf("no SCOPE_MISMATCH on stderr:\n%s", got.stderr)
	}
	// Both halves, because either alone sends the reader to the wrong place.
	if !strings.Contains(got.stderr, excluded) {
		t.Errorf("the warning does not name the excluded project %s:\n%s",
			excluded, got.stderr)
	}
	if !strings.Contains(got.stderr, scope) {
		t.Errorf("the warning does not name the scope %s, so it reads as a "+
			"complaint about the query alone:\n%s", scope, got.stderr)
	}
	// stdout is data only, in every build and on every path.
	if strings.Contains(got.stdout, "SCOPE_MISMATCH") {
		t.Errorf("the warning reached stdout:\n%s", got.stdout)
	}
}
