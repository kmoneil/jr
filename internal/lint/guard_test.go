package lint_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The em dash, spelled so this file holds none: the guard reads comments, and
// a test of it that tripped it would be a poor start.
const emDash = "\u2014"

// runScript runs one of the repository's scripts in dir, under the same
// scrubbed git environment the version tests use, and reports its exit
// status and stderr. A refusal that also wrote to stdout would be handing a
// caller something to act on, so stdout is returned for the tests to check.
func runScript(t *testing.T, dir, script string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(repoRoot, script))
	if err != nil {
		t.Fatalf("locating %s: %v", script, err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...) //nolint:gosec // a script in this repository.
	cmd.Dir = dir
	cmd.Env = gitEnv(dir)
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running %s: %v", script, err)
		}
		code = exit.ExitCode()
	}
	return code, out.String(), errs.String()
}

// withoutParentMake drops what a make running this test exports to every make
// it starts. `make ci BIN=/tmp/x` puts BIN=/tmp/x in MAKEFLAGS, so a make this
// test runs inherits it as a command-line variable, and the build guard, told
// BIN was a decision, let a foreign binary through. Found by running the gate
// the way the memory says to run it in the dev container.
func withoutParentMake(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEOVERRIDES":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// writeTree writes files under dir, making the directories they need.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// TestACommitOnTheDefaultBranchIsRefused drives the pre-commit branch guard.
//
// main is protected, so the server refuses the push; by then the commit
// exists and moving it off main is a reset and a new branch. It happened
// twice before this guard, once committed and recovered, once caught at the
// full-suite stage. A detached HEAD is let through, because that is a rebase
// or a bisect replaying commits, not somebody starting work.
func TestACommitOnTheDefaultBranchIsRefused(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)

	code, stdout, stderr := runScript(t, dir, "scripts/guard", "branch")
	if code != 1 {
		t.Fatalf("on main the guard exited %d, want 1.\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{"refusing to commit on main", "git switch -c"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, stderr)
		}
	}
	if stdout != "" {
		t.Errorf("the refusal wrote to stdout: %q", stdout)
	}

	git(t, dir, "switch", "-q", "-c", "feat/some-work")
	if code, _, stderr := runScript(t, dir, "scripts/guard", "branch"); code != 0 {
		t.Errorf("on a feature branch the guard exited %d:\n%s", code, stderr)
	}

	git(t, dir, "checkout", "-q", "--detach")
	if code, _, stderr := runScript(t, dir, "scripts/guard", "branch"); code != 0 {
		t.Errorf("on a detached HEAD the guard exited %d:\n%s", code, stderr)
	}
}

// TestTheDefaultBranchIsTheRemotesNotAName reads origin/HEAD when it is set,
// so a repository whose default is not called main is guarded as well, and
// a local branch that merely has that name is not refused there.
func TestTheDefaultBranchIsTheRemotesNotAName(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	git(t, dir, "switch", "-q", "-c", "trunk")
	git(t, dir, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")

	if code, _, stderr := runScript(t, dir, "scripts/guard", "branch"); code != 1 {
		t.Errorf("on trunk, the remote's default, the guard exited %d:\n%s", code, stderr)
	}
	git(t, dir, "switch", "-q", "main")
	if code, _, stderr := runScript(t, dir, "scripts/guard", "branch"); code != 0 {
		t.Errorf("on main, which is not the default here, the guard exited %d:\n%s", code, stderr)
	}
}

// TestAnEmDashIsRefusedOnlyInAddedProse drives the staged-prose guard.
//
// The rule is CLAUDE.md's: none in docs, code comments or commit messages, and
// the ones already there stay until the line is edited for another reason. So
// the guard reads added lines only, and only the prose among them: a Go string
// literal is data, a generated file belongs to its generator, and testdata
// holds what a server said.
func TestAnEmDashIsRefusedOnlyInAddedProse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		// refused names the file:line the guard must report, or is empty
		// when the change must pass.
		refused string
	}{
		{
			name:    "a new line in a hand-written document",
			files:   map[string]string{"docs/guide.md": "Intro.\nOne thing " + emDash + " then another.\n"},
			refused: "docs/guide.md:2:",
		},
		{
			name:    "a Go comment",
			files:   map[string]string{"pkg/a.go": "package pkg\n\n// Sum adds " + emDash + " nothing more.\nfunc Sum() {}\n"},
			refused: "pkg/a.go:3:",
		},
		{
			name:    "a comment in a script with no extension",
			files:   map[string]string{"scripts/tool": "#!/bin/sh\n# does a thing " + emDash + " quickly\n"},
			refused: "scripts/tool:2:",
		},
		{
			name:    "a comment in the Makefile",
			files:   map[string]string{"Makefile": "# builds " + emDash + " everything\nall:\n"},
			refused: "Makefile:1:",
		},
		{
			name:  "a Go string literal, which is data",
			files: map[string]string{"pkg/b.go": "package pkg\n\nconst Dash = \"a " + emDash + " b\"\n"},
		},
		{
			name:  "the generated command reference",
			files: map[string]string{"docs/commands.md": "| `--x` | `string` | " + emDash + " | usage |\n"},
		},
		{
			name:  "the generated skill",
			files: map[string]string{"skills/jr/SKILL.md": "text " + emDash + " text\n"},
		},
		{
			name:  "testdata",
			files: map[string]string{"internal/x/testdata/doc.md": "what a server said " + emDash + " verbatim\n"},
		},
		{
			name:  "a file that is not prose",
			files: map[string]string{"data.json": "{\"k\": \"" + emDash + "\"}\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			initRepo(t, dir)
			writeTree(t, dir, tc.files)
			git(t, dir, "add", ".")

			code, stdout, stderr := runScript(t, dir, "scripts/guard", "prose", "--staged")
			switch {
			case tc.refused == "" && code != 0:
				t.Errorf("the guard refused a change it must pass (exit %d):\n%s", code, stderr)
			case tc.refused != "" && code != 1:
				t.Errorf("the guard exited %d for an em dash in %s, want 1:\n%s",
					code, tc.refused, stderr)
			case tc.refused != "" && !strings.Contains(stderr, tc.refused):
				t.Errorf("the refusal does not point at %s:\n%s", tc.refused, stderr)
			}
			if stdout != "" {
				t.Errorf("the guard wrote to stdout: %q", stdout)
			}
		})
	}
}

// TestAnExistingEmDashDoesNotBlockAnEdit is the half that makes the rule
// livable: a document already holding a dash can be edited anywhere else,
// and a line that only moves is not a line somebody wrote.
func TestAnExistingEmDashDoesNotBlockAnEdit(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	writeTree(t, dir, map[string]string{"docs/old.md": "Old " + emDash + " line.\nSecond.\n"})
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "old")

	writeTree(t, dir, map[string]string{"docs/old.md": "Old " + emDash + " line.\nSecond.\nThird, added.\n"})
	git(t, dir, "add", ".")
	if code, _, stderr := runScript(t, dir, "scripts/guard", "prose", "--staged"); code != 0 {
		t.Errorf("an untouched dash blocked an edit elsewhere (exit %d):\n%s", code, stderr)
	}

	writeTree(t, dir, map[string]string{"docs/old.md": "Old " + emDash + " line.\nSecond " + emDash + " edited.\nThird, added.\n"})
	git(t, dir, "add", ".")
	code, _, stderr := runScript(t, dir, "scripts/guard", "prose", "--staged")
	if code != 1 || !strings.Contains(stderr, "docs/old.md:2:") {
		t.Errorf("an edited line gaining a dash was not refused at docs/old.md:2 (exit %d):\n%s",
			code, stderr)
	}
}

// TestAnEmDashInACommitMessageIsRefused is the commit-msg half. Git's own
// comment lines are skipped, because an editor-written message still holds
// them when the hook runs.
func TestAnEmDashInACommitMessageIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, message string
		want          int
	}{
		{"a dash in the body", "fix(x): a thing\n\nIt broke " + emDash + " badly.\n", 1},
		{"a dash in the header", "fix(x): one " + emDash + " two\n", 1},
		{"a dash only in git's comments", "fix(x): a thing\n\n# Please enter " + emDash + " the message\n", 0},
		{"no dash", "fix(x): a thing\n\nIt broke, badly.\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
			writeTree(t, dir, map[string]string{filepath.Base(path): tc.message})
			code, _, stderr := runScript(t, dir, "scripts/guard", "prose", "--message", path)
			if code != tc.want {
				t.Errorf("exit %d, want %d:\n%s", code, tc.want, stderr)
			}
		})
	}
}

// TestABinaryBuiltElsewhereIsNotReplaced drives the build guard.
//
// On 2026-09-23 `make ci` in the Linux dev container ended in build-all and
// replaced every macOS binary in bin/, and the first sign was the host's jr
// refusing to run. A binary that cannot execute here is somebody else's; the
// guard asks it to run, and 126 is the answer it is looking for. A directory
// named on the command line is a decision and goes through.
func TestABinaryBuiltElsewhereIsNotReplaced(t *testing.T) {
	for _, tc := range []struct {
		name   string
		jr     string // the file at bin/jr, or empty for none
		mode   os.FileMode
		origin string
		want   int
	}{
		// An ELF header and nothing behind it: exec refuses it on every
		// platform, and bash refuses it as a script. Exactly the shape of
		// a macOS build seen from Linux, or the reverse.
		{"a binary for another machine", "\x7fELF\x00\x00\x00\x00", 0o755, "file", 1},
		{"the same, with BIN on the command line", "\x7fELF\x00\x00\x00\x00", 0o755, "command line", 0},
		{"the same, with BIN from make -e", "\x7fELF\x00\x00\x00\x00", 0o755, "environment override", 0},
		{"a binary that runs here", "#!/bin/sh\nexit 0\n", 0o755, "file", 0},
		{"one that runs and fails, which is still this machine's", "#!/bin/sh\nexit 1\n", 0o755, "file", 0},
		{"a file nobody can execute", "not a binary", 0o644, "file", 0},
		{"nothing there yet", "", 0, "file", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if tc.jr != "" {
				if err := os.WriteFile(filepath.Join(bin, "jr"), []byte(tc.jr), tc.mode); err != nil {
					t.Fatalf("writing bin/jr: %v", err)
				}
			}
			code, stdout, stderr := runScript(t, dir, "scripts/guard", "bin", bin, tc.origin)
			if code != tc.want {
				t.Errorf("exit %d, want %d:\n%s", code, tc.want, stderr)
			}
			if tc.want == 1 && !strings.Contains(stderr, "make build-here OUT=") {
				t.Errorf("the refusal does not say how to build somewhere else:\n%s", stderr)
			}
			if stdout != "" {
				t.Errorf("the guard wrote to stdout: %q", stdout)
			}
		})
	}
}

// TestTheBuildGuardRunsFromMake runs the Makefile's own bin-guard target in
// a copy of the repository's Makefile, with a foreign bin/jr at the default
// BIN and at a named one. It is the wiring half: a guard nobody calls, or one
// handed the wrong origin, passes every test above and protects nothing.
func TestTheBuildGuardRunsFromMake(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("no make on PATH: %v", err)
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"Makefile":      readFile(t, filepath.Join(repoRoot, "Makefile")),
		"scripts/guard": readFile(t, filepath.Join(repoRoot, "scripts/guard")),
	})
	if err := os.Chmod(filepath.Join(dir, "scripts/guard"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	for _, sub := range []string{"bin", "elsewhere"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "jr"), []byte("\x7fELF\x00\x00\x00\x00"), 0o755); err != nil { //nolint:gosec // a stub that must be executable by mode.
			t.Fatalf("writing the stub: %v", err)
		}
	}

	runMake := func(args ...string) (int, string) {
		// VERSION is the Makefile's own override for a tree without
		// scripts/version.sh, which this copy is; without it the Makefile
		// refuses to load at all, and every target looks like a refusal.
		cmd := exec.Command("make", append([]string{"-s", "bin-guard", "VERSION=0.0.0"}, args...)...)
		cmd.Dir = dir
		cmd.Env = withoutParentMake(gitEnv(dir))
		out, err := cmd.CombinedOutput()
		if err != nil {
			exit, ok := errors.AsType[*exec.ExitError](err)
			if !ok {
				t.Fatalf("running make: %v", err)
			}
			return exit.ExitCode(), string(out)
		}
		return 0, string(out)
	}

	if code, out := runMake(); code == 0 || !strings.Contains(out, "built for another machine") {
		t.Errorf("make bin-guard with the default BIN over a foreign bin/jr exited %d:\n%s", code, out)
	}
	if code, out := runMake("BIN=elsewhere"); code != 0 {
		t.Errorf("make bin-guard BIN=elsewhere, a decision, exited %d:\n%s", code, out)
	}
}

// TestTheHooksAndTheBuildTargetsCallTheGuards holds the callers to the list.
// Each guard above is only as good as the places that run it.
func TestTheHooksAndTheBuildTargetsCallTheGuards(t *testing.T) {
	preCommit := readFile(t, filepath.Join(repoRoot, ".githooks/pre-commit"))
	for _, want := range []string{"scripts/guard branch", "scripts/guard prose --staged"} {
		i := strings.Index(preCommit, want)
		if i < 0 {
			t.Errorf(".githooks/pre-commit does not run %q", want)
			continue
		}
		// Before the slow half, so a refusal costs nothing.
		if j := strings.Index(preCommit, "go test"); j >= 0 && i > j {
			t.Errorf(".githooks/pre-commit runs %q after the suite", want)
		}
	}
	commitMsg := readFile(t, filepath.Join(repoRoot, ".githooks/commit-msg"))
	if !strings.Contains(commitMsg, `scripts/guard prose --message "$1"`) {
		t.Errorf(".githooks/commit-msg does not run the message guard")
	}

	makefile := readFile(t, filepath.Join(repoRoot, "Makefile"))
	for _, target := range []string{"build", "build-mac", "build-agent", "build-reader", "build-ci"} {
		rule := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:(.*)$`).FindStringSubmatch(makefile)
		if rule == nil {
			t.Errorf("the Makefile has no %s target", target)
			continue
		}
		if !strings.Contains(rule[1], "bin-guard") {
			t.Errorf("%s does not depend on bin-guard: %q", target, rule[0])
		}
	}
	if !strings.Contains(makefile, `scripts/guard bin "$(BIN)" "$(origin BIN)"`) {
		t.Errorf("bin-guard does not hand the guard BIN and its origin")
	}
}
