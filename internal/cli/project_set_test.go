package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// TestASetOfProjectsIsOneScopeFromTheFlagToTheEnvelope is issue 216 end to end.
//
// A question over several projects used to be `--all-projects --jql 'project
// in (...)'`, which cost three things at once: the quoting became the caller's,
// the envelope named no scope, and SCOPE_MISMATCH had nothing to compare. This
// reads all three off one invocation, and reads the query the server received
// rather than the one the client says it built.
func TestASetOfProjectsIsOneScopeFromTheFlagToTheEnvelope(t *testing.T) {
	var jql atomic.Value
	url := emptyJira(t, &jql)

	scoped := func(t *testing.T) map[string]string {
		t.Helper()
		env := credentialed(t)
		mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
		return env
	}

	t.Run("the query, the envelope and the empty-result note agree", func(t *testing.T) {
		// DO is a reserved JQL word, and is the reason the reporter was writing
		// JQL by hand at all.
		got := run(t, scoped(t), "--project", "OPS", "--project", "NET", "--project", "DO",
			"issue", "list", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		sent, _ := jql.Load().(string)
		if want := `project IN ("OPS", "NET", "DO")`; !strings.HasPrefix(sent, want) {
			t.Errorf("the server was sent %q, want it scoped by %s", sent, want)
		}
		if !strings.Contains(got.stdout, ` projects="OPS NET DO"`) {
			t.Errorf("the envelope does not name the set the rows came from:\n%s", got.stdout)
		}
		// The context's project is not part of it, and a set is never also
		// written as one project.
		if strings.Contains(got.stdout, ` project="`) {
			t.Errorf("the envelope names one project beside a set:\n%s", got.stdout)
		}
		if !strings.Contains(got.stderr, "projects=OPS NET DO") {
			t.Errorf("EMPTY_RESULT does not say which projects were empty:\n%s", got.stderr)
		}
	})

	t.Run("a key given twice is one project, as it was", func(t *testing.T) {
		got := run(t, scoped(t), "--project", "OPS", "--project", "ops",
			"issue", "list", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, ` project="OPS"`) || strings.Contains(got.stdout, "projects=") {
			t.Errorf("one key given twice is not a scope of one:\n%s", got.stdout)
		}
	})

	t.Run("SCOPE_MISMATCH compares against the whole set", func(t *testing.T) {
		got := run(t, scoped(t), "--project", "OPS", "--project", "NET",
			"issue", "list", "--jql", "project in (NET, UPF)")
		assertScopeWarning(t, got, "UPF", "OPS, NET")
		// NET is in the set, so only UPF is excluded: "selects project UPF",
		// in the singular.
		if !strings.Contains(got.stderr, "selects project UPF,") {
			t.Errorf("a project inside the set was reported as excluded:\n%s", got.stderr)
		}
	})

	t.Run("--explain shows the set and sends nothing", func(t *testing.T) {
		jql.Store("")
		got := run(t, scoped(t), "--project", "OPS", "--project", "NET",
			"issue", "list", "--explain", "--format", "xml")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
		}
		for _, want := range []string{
			`<query>project IN ("OPS", "NET") ORDER BY issuekey DESC</query>`,
			`<projects count="2">`, ` projects="OPS NET"`,
		} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("the explanation does not carry %s:\n%s", want, got.stdout)
			}
		}
		if sent, _ := jql.Load().(string); sent != "" {
			t.Errorf("--explain searched: %q", sent)
		}
	})
}

// countingJira answers like a Data Center that has nothing in it, and counts
// every request it is sent.
func countingJira(t *testing.T, requests *atomic.Int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/serverInfo") {
			_, _ = w.Write([]byte(`{"version":"9.12.0","deploymentType":"Server"}`))
			return
		}
		_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":0,"issues":[],"values":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestARepeatedProjectIsRefusedByEveryCommandThatReadsOne is the other half of
// a repeatable global. --project is bound once on the root, so the parser takes
// a second one on every command, and there is no reading of it on a command
// that reads one project that is not a guess between the first key, the last,
// and one run per key.
//
// The cases are the four ways a command stands toward the flag: it filters by
// one project through an endpoint that takes one (`board list`), it needs
// exactly one (`project versions`), it never reads the scope (`issue get`), and
// it has no session at all (`context show`, which would otherwise print a
// resolution with no project in it). Each is refused before a request is made.
func TestARepeatedProjectIsRefusedByEveryCommandThatReadsOne(t *testing.T) {
	var requests atomic.Int64
	url := countingJira(t, &requests)

	for _, args := range [][]string{
		{"board", "list"},
		{"project", "versions"},
		{"issue", "get", "ENG-1"},
		{"context", "show"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
			requests.Store(0)

			got := run(t, env, append([]string{"--project", "OPS", "--project", "NET"}, args...)...)
			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want %v\nstdout: %s\nstderr: %s",
					got.exit, exitcode.Usage, got.stdout, got.stderr)
			}
			if !strings.Contains(got.stderr, "PROJECT_REPEATED") {
				t.Errorf("the refusal does not carry its code:\n%s", got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a refused command wrote to stdout:\n%s", got.stdout)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("%d request(s) were sent before the refusal", n)
			}
		})
	}
}

// TestASetOfProjectsIsHeldToProjectKeysBeforeExplain is the refusal the command
// line has to make on its own. registry.Gate makes it for every other path,
// and --explain answers before Gate runs, so without this an explanation would
// be printed for a scope the command itself refuses.
func TestASetOfProjectsIsHeldToProjectKeysBeforeExplain(t *testing.T) {
	var requests atomic.Int64
	url := countingJira(t, &requests)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url)

	for _, explain := range []bool{true, false} {
		args := []string{"--project", "OPS", "--project", "Platform Team", "issue", "list"}
		if explain {
			args = append(args, "--explain")
		}
		got := run(t, env, args...)
		if got.exit != exitcode.Usage || !strings.Contains(got.stderr, "INVALID_PROJECT_KEY") {
			t.Errorf("explain=%v: exit = %v, want INVALID_PROJECT_KEY at %v\n%s",
				explain, got.exit, exitcode.Usage, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("explain=%v: a refused command wrote to stdout:\n%s", explain, got.stdout)
		}
	}
	// One --project is passed to Jira as typed, name or key, as it always was.
	if got := run(t, env, "--project", "Platform Team", "issue", "list", "--explain"); got.exit != exitcode.OK {
		t.Errorf("one --project holding a name was refused: %v\n%s", got.exit, got.stderr)
	}
}

// TestAnEmptyValueAmongSeveralProjectsIsRefused extends EMPTY_SCOPE to a
// repeat. An empty --project never lifted the scope, and one empty value among
// real ones is the same request: a value nobody means.
func TestAnEmptyValueAmongSeveralProjectsIsRefused(t *testing.T) {
	env := credentialed(t)
	got := run(t, env, "--project", "OPS", "--project", "", "issue", "list")
	if got.exit != exitcode.Usage || !strings.Contains(got.stderr, "EMPTY_SCOPE") {
		t.Errorf("exit = %v, want EMPTY_SCOPE at %v\n%s", got.exit, exitcode.Usage, got.stderr)
	}
}
