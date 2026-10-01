//go:build !windows

// The scripts under test are the development toolchain, which runs on macOS
// and Linux. Nothing under scripts/ ships, and under Git Bash on Windows MSYS
// rewrites their arguments before they run, so what these would measure there
// is the emulation.
package lint_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ghForLand stands in for gh against one pull request on a bare repository. It
// answers what land and ci-wait ask, reports every commit whose checks it is
// asked about as green, and merges the way the repository's ruleset does: only
// a head whose checks were seen green, and only the head --match-head-commit
// names when it names one. A merge is a squash onto main, and the branch goes,
// as --delete-branch does on the server.
//
// GH_STALE_HEAD is the head GitHub reports for the pull request. Set to the
// commit a rebase replaced, it is the gap between a push and GitHub
// registering it, which is when land read the replaced head as the PR's.
const ghForLand = `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
origin() { git --git-dir="$GH_ORIGIN" "$@"; }
case "$*" in
"repo view"*) echo example/jr ;;
"pr view"*state*) echo OPEN ;;
"pr view"*headRefName*) echo "$GH_BRANCH" ;;
"pr view"*baseRefName*) echo main ;;
"pr view"*headRefOid*) echo "$GH_STALE_HEAD" ;;
"api repos/"*check-runs*)
	sha=${2#*/commits/}
	echo "${sha%%/*}" >> "$GH_LOG.green"
	printf '1\t0\t0\n' ;;
"pr merge"*)
	head=$(origin rev-parse "refs/heads/$GH_BRANCH")
	if ! grep -qx "$head" "$GH_LOG.green" 2>/dev/null; then
		echo "GraphQL: Repository rule violations found" >&2
		echo "Required status checks are expected on $head. (mergePullRequest)" >&2
		exit 1
	fi
	case "$*" in
	*"--match-head-commit $head"*) ;;
	*--match-head-commit*) echo "Head branch was modified. (mergePullRequest)" >&2; exit 1 ;;
	esac
	squash=$(origin commit-tree "$head^{tree}" -p "$(origin rev-parse refs/heads/main)" -m squash)
	origin update-ref refs/heads/main "$squash"
	origin update-ref -d "refs/heads/$GH_BRANCH" ;;
*) echo "gh stub: nothing scripted for: $*" >&2; exit 2 ;;
esac
`

// TestLandWaitsOnTheHeadItPushed is PR 197 and PR 228. A branch behind main is
// rebased and pushed, and GitHub has not yet registered the push, so the pull
// request still names the head the push replaced. land asked GitHub for the
// head, found the replaced one green, and merged a head whose checks had not
// started, which the ruleset refused. It now waits on the commit it pushed,
// and has the server refuse to merge any other.
func TestLandWaitsOnTheHeadItPushed(t *testing.T) {
	const branch = "fix/behind"
	origin, clone, replaced := landRepos(t, branch)
	commit(t, clone, "elsewhere")
	git(t, clone, "push", "-q", "origin", "main")

	code, out, calls := runLand(t, clone, origin, branch, replaced)
	if code != 0 {
		t.Fatalf("land exited %d:\n%s", code, out)
	}
	pushed := revParse(t, clone, branch)
	if pushed == replaced {
		t.Fatal("land did not rebase a branch that was behind, so this proves nothing")
	}
	if !strings.Contains(calls, "--match-head-commit "+pushed) {
		t.Errorf("the merge did not name the head land pushed, %s:\n%s", pushed, calls)
	}
	if got, want := revParse(t, clone, "main^{tree}"), revParse(t, clone, pushed+"^{tree}"); got != want {
		t.Errorf("main after landing holds tree %s, want the pushed head's %s", got, want)
	}
}

// TestLandFromAWorktreeLeavesTheBaseWhereItIs is PR 230. land ran in a second
// worktree while main was checked out in the first, so git would not switch to
// main there. The failure was discarded, and the pull that followed acted on
// the pull request's own branch: it refused to fast-forward, and land exited
// 128 over a merge that had succeeded. A branch that was an ancestor of main
// would have been moved to main without a word. land now says where main is
// and leaves it, and the branch it ran on, alone.
func TestLandFromAWorktreeLeavesTheBaseWhereItIs(t *testing.T) {
	const branch = "feat/elsewhere"
	origin, clone, head := landRepos(t, branch)
	tree := filepath.Join(filepath.Dir(clone), "tree")
	git(t, clone, "worktree", "add", "-q", tree, branch)
	mainBefore := revParse(t, clone, "main")

	code, out, _ := runLand(t, tree, origin, branch, head)
	if code != 0 {
		t.Fatalf("land exited %d over a merge that succeeded:\n%s", code, out)
	}
	where, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatalf("resolving the clone's path: %v", err)
	}
	if !strings.Contains(out, where) {
		t.Errorf("land did not say main is checked out at %s:\n%s", where, out)
	}
	if got := revParse(t, tree, "HEAD"); got != head {
		t.Errorf("the worktree's branch moved from %s to %s", head, got)
	}
	if got := revParse(t, clone, "main"); got != mainBefore {
		t.Errorf("main in the other checkout moved from %s to %s", mainBefore, got)
	}
}

// TestLandRefusesAHeadOriginHasNotSeen keeps the commit land waits on the one
// GitHub has. A local commit nobody pushed has no checks that will ever run,
// so waiting on it is a half-hour timeout, and merging the pushed head instead
// leaves the local commit behind without saying so.
func TestLandRefusesAHeadOriginHasNotSeen(t *testing.T) {
	const branch = "feat/unpushed"
	origin, clone, head := landRepos(t, branch)
	git(t, clone, "switch", "-q", branch)
	commit(t, clone, "local")

	code, out, calls := runLand(t, clone, origin, branch, head)
	if code == 0 {
		t.Fatalf("land landed a branch holding a commit origin has never seen:\n%s", out)
	}
	if strings.Contains(calls, "pr merge") {
		t.Errorf("land asked to merge:\n%s", calls)
	}
	if !strings.Contains(out, "not on origin") {
		t.Errorf("the refusal does not say what is wrong:\n%s", out)
	}
}

// landRepos builds a bare origin holding main and a pushed pull-request
// branch, and a clone of it on main, which is where land runs unless a case
// says otherwise. It returns the origin, the clone, and the branch's head.
func landRepos(t *testing.T, branch string) (origin, clone, head string) {
	t.Helper()
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	clone = filepath.Join(root, "work")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "clone", "-q", origin, clone)
	commit(t, clone, "base")
	git(t, clone, "push", "-q", "origin", "main")
	git(t, clone, "switch", "-q", "-c", branch)
	commit(t, clone, "change")
	git(t, clone, "push", "-q", "-u", "origin", branch)
	head = revParse(t, clone, "HEAD")
	git(t, clone, "switch", "-q", "main")
	return origin, clone, head
}

// runLand runs scripts/land, and the ci-wait beside it, in dir against the gh
// stub, with make stubbed out for the generators a rebase re-runs.
func runLand(t *testing.T, dir, origin, branch, staleHead string) (code int, out, ghCalls string) {
	t.Helper()
	stubs := t.TempDir()
	for name, body := range map[string]string{"gh": ghForLand, "make": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(body), 0o755); err != nil { //nolint:gosec // a stub that must be executable.
			t.Fatalf("writing the %s stub: %v", name, err)
		}
	}
	script, err := filepath.Abs(filepath.Join(repoRoot, "scripts/land"))
	if err != nil {
		t.Fatalf("locating land: %v", err)
	}
	log := filepath.Join(stubs, "gh.log")
	cmd := exec.Command("bash", script, "7") //nolint:gosec // a script in this repository.
	cmd.Dir = dir
	cmd.Env = append(withoutParentMake(gitEnv(dir)),
		"PATH="+stubs+":/usr/bin:/bin",
		"GH_LOG="+log, "GH_ORIGIN="+origin, "GH_BRANCH="+branch,
		"GH_STALE_HEAD="+staleHead, "POLL=0", "TIMEOUT=5",
	)
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running land: %v", err)
		}
		code = exit.ExitCode()
	}
	calls, _ := os.ReadFile(log) //nolint:gosec // the stub's own log, in a temporary directory.
	return code, buf.String(), string(calls)
}

// revParse is the commit, or the object, a revision names in dir.
func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	cmd.Env = gitEnv(dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}
