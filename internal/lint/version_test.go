package lint_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/buildinfo"
)

// semver is the shape a version has to have, written here rather than imported
// so this check does not share a definition with the thing it checks.
//
// MAJOR.MINOR.PATCH, an optional prerelease, an optional build metadata suffix.
// Deliberately not the full grammar — it does not reject a leading zero in a
// numeric prerelease identifier, which is a conformance detail no consumer of
// this string will act on. What it does reject is what actually shipped: a bare
// commit hash.
var semver = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?` +
		`(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`,
)

// TestTheStampedVersionIsSemver runs the script the Makefile stamps builds with
// and holds its answer to the shape a version has.
//
// `jr version` and the User-Agent both carry this string, and the User-Agent is
// what a Jira administrator sees in their access logs. It used to be whatever
// `git describe --tags --always --dirty` produced, which on a tree with no tags
// is a bare commit hash: `jr/786d271` names no release, sorts against nothing,
// and does not announce that it is not a version. Nothing checked, because
// nothing had ever looked at the value.
//
// This runs in the repository it is testing, so it exercises whichever case
// that tree happens to be in. That is one case out of the documented four, and
// this tree is the untagged one — which is why the branch every release will
// take is covered by TestTheVersionScriptCoversEveryDocumentedCase instead, in
// repositories it builds.
func TestTheStampedVersionIsSemver(t *testing.T) {
	cmd := exec.Command("sh", "scripts/version.sh")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("scripts/version.sh: %v\n%s", err, stderrOf(err))
	}

	got := strings.TrimSpace(string(out))
	if got == "" {
		t.Fatal("scripts/version.sh printed nothing; a build would be stamped empty")
	}
	if !semver.MatchString(got) {
		t.Errorf("scripts/version.sh printed %q, which is not a semantic version.\n"+
			"This string reaches a Jira administrator's access log as the "+
			"User-Agent; a value that does not parse as a version tells them "+
			"nothing about which release is talking to them.", got)
	}
}

// releaseDefault matches the declaration `Release = "0.0.0-unknown"`.
var releaseDefault = regexp.MustCompile(`(?m)^\s*Release\s*=\s*"([^"]*)"`)

// TestTheDefaultVersionIsSemver covers the build the Makefile did not make.
//
// `go build ./cmd/jr` stamps nothing, so the binary reports whatever the
// package default is. That default used to be `0.1.0-dev`, which is a
// plausible-looking release number for a build that has no idea what it is.
// 0.0.0 is the honest answer, and it still has to parse.
//
// The literal is read out of the source rather than through buildinfo.Release,
// which is a package variable any test may pin — `internal/cli/cli_test.go`
// pins it to keep golden output stable. Nothing in this package does today, so
// reading the variable was correct today, which is a different thing from
// correct: the day something here pins it, this test would go on passing while
// checking the pinned value instead of the default.
func TestTheDefaultVersionIsSemver(t *testing.T) {
	const path = "../buildinfo/buildinfo.go"

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := releaseDefault.FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s no longer declares Release with a literal default; this "+
			"test cannot see what an unstamped build reports", path)
	}
	got := string(m[1])

	if !semver.MatchString(got) {
		t.Errorf("buildinfo.Release defaults to %q, which is not a semantic version", got)
	}
	if !strings.HasPrefix(got, "0.0.0") {
		t.Errorf("buildinfo.Release defaults to %q, which claims a release number. "+
			"An unstamped build does not know what it is and should say so", got)
	}
}

// TestTheVersionScriptCoversEveryDocumentedCase builds a repository per case
// and runs the script in it.
//
// The script's header names four cases and `docs/output-contract.md` promises
// all four produce a semantic version. Until this existed, exactly one of them
// ran under test — whichever one this repository was in, which is the untagged
// one — and the test's own comment said the others "are exercised by the
// script's own logic". The script's logic is the thing under test, so that
// sentence asserted nothing, and the tagged branch is the branch every release
// takes.
func TestTheVersionScriptCoversEveryDocumentedCase(t *testing.T) {
	const sha = `[0-9a-f]{7,}`

	cases := []struct {
		name  string
		build func(t *testing.T, dir string)
		want  *regexp.Regexp
	}{{
		name:  "no git at all",
		build: func(*testing.T, string) {},
		want:  regexp.MustCompile(`^0\.0\.0-unknown$`),
	}, {
		name:  "never tagged",
		build: func(t *testing.T, dir string) { initRepo(t, dir) },
		want:  regexp.MustCompile(`^0\.0\.0-untagged\+` + sha + `$`),
	}, {
		name: "never tagged, dirty",
		build: func(t *testing.T, dir string) {
			initRepo(t, dir)
			// Untracked rather than modified, because that is the case
			// `git describe --dirty` misses and this script does not: an
			// untracked .go file in a package is compiled into the binary.
			writeFile(t, dir, "untracked.go", "package x\n")
		},
		want: regexp.MustCompile(`^0\.0\.0-untagged\+` + sha + `\.dirty$`),
	}, {
		name: "tagged, clean",
		build: func(t *testing.T, dir string) {
			initRepo(t, dir)
			git(t, dir, "tag", "v1.2.0")
		},
		want: regexp.MustCompile(`^1\.2\.0$`),
	}, {
		name: "tagged, moved on",
		build: func(t *testing.T, dir string) {
			initRepo(t, dir)
			git(t, dir, "tag", "v1.2.0")
			commit(t, dir, "second")
		},
		want: regexp.MustCompile(`^1\.2\.0\+1\.g` + sha + `$`),
	}, {
		name: "tagged, moved on, dirty",
		build: func(t *testing.T, dir string) {
			initRepo(t, dir)
			git(t, dir, "tag", "v1.2.0")
			commit(t, dir, "second")
			writeFile(t, dir, "untracked.go", "package x\n")
		},
		want: regexp.MustCompile(`^1\.2\.0\+1\.g` + sha + `\.dirty$`),
	}, {
		// At the tag but not at what the tag points to. The count is 0 and the
		// version is still not 1.2.0, which is the whole reason the dirty flag
		// is consulted before the count.
		name: "tagged, at the tag, dirty",
		build: func(t *testing.T, dir string) {
			initRepo(t, dir)
			git(t, dir, "tag", "v1.2.0")
			writeFile(t, dir, "untracked.go", "package x\n")
		},
		want: regexp.MustCompile(`^1\.2\.0\+0\.g` + sha + `\.dirty$`),
	}}

	script := versionScript(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.build(t, dir)

			stdout, stderr, err := run(t, script, dir)
			if err != nil {
				t.Fatalf("scripts/version.sh: %v\n%s", err, stderr)
			}
			got := strings.TrimSpace(stdout)
			if !c.want.MatchString(got) {
				t.Errorf("scripts/version.sh printed %q, want %v", got, c.want)
			}
			if !semver.MatchString(got) {
				t.Errorf("scripts/version.sh printed %q, which is not a semantic version", got)
			}
		})
	}
}

// TestTheVersionScriptRefusesATagThatIsNotAVersion covers the branch the
// guarantee was written for and never had.
//
// `version=${tag#v}` passes anything through. Every tag below used to reach the
// binary and the access log: `nightly`, `rel/2024` — which holds a character
// semver does not allow at all — and `v1.2.0+meta`, whose own build metadata
// collides with the commit count this script appends.
//
// The refusal has to be silent on stdout as well as non-zero. The Makefile
// reads this script with `$(shell)`, which keeps the output and discards the
// status, so anything printed here is stamped into a binary whatever the exit
// code says.
func TestTheVersionScriptRefusesATagThatIsNotAVersion(t *testing.T) {
	cases := []struct {
		name  string
		tag   string
		moved bool
	}{
		{name: "a word", tag: "nightly"},
		{name: "a word, moved on", tag: "nightly", moved: true},
		{name: "a path, holding a character semver forbids", tag: "rel/2024"},
		{name: "a two-part version", tag: "v1.2"},
		{name: "a tag carrying its own build metadata", tag: "v1.2.0+meta", moved: true},
	}

	script := versionScript(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			initRepo(t, dir)
			git(t, dir, "tag", c.tag)
			if c.moved {
				commit(t, dir, "second")
			}

			stdout, stderr, err := run(t, script, dir)
			if err == nil {
				t.Fatalf("the tag %q was accepted and produced %q", c.tag, strings.TrimSpace(stdout))
			}
			if stdout != "" {
				t.Errorf("the tag %q was refused and %q still reached stdout, "+
					"which is what the Makefile stamps", c.tag, stdout)
			}
			if !strings.Contains(stderr, c.tag) {
				t.Errorf("the refusal for %q does not name the tag, and `git tag` "+
					"is the fix:\n%s", c.tag, stderr)
			}
		})
	}
}

// versionBanner is the shape buildinfo.Display prints, and userAgentBanner the
// shape internal/cli builds for the wire. Both are asserted against the code
// below before any documentation is read against them.
var (
	versionBanner   = regexp.MustCompile(`\bjr (\S+) \((\w+); tags=([^)]*)\)`)
	userAgentBanner = regexp.MustCompile(`\bjr/(\S+) \((\w+)\)`)
)

// TestTheWorkedVersionExamplesAreOnesTheCodeCouldPrint reads every version
// banner written into a doc or a doc comment and holds it to the code.
//
// The example used to be `0.1.0-dev`, which was literally what an unstamped
// binary printed; it became `1.2.0`, which no build of this tree produces. That
// is fine as a placeholder and not fine as an unchecked one — the number is
// illustrative, but the shape around it is a contract, and `profiles_test.go`
// parses the table and the tag block out of build-profiles.md while never
// looking at the fenced example below them.
//
// So the release is held to the semver grammar rather than to a literal, and
// the profile and its tag set are held to the ones the Makefile ships. A
// rendering change in Display or userAgent fails at the first check, before any
// file is read.
func TestTheWorkedVersionExamplesAreOnesTheCodeCouldPrint(t *testing.T) {
	if !versionBanner.MatchString(buildinfo.Display()) {
		t.Fatalf("buildinfo.Display() prints %q, which this test no longer "+
			"recognises; the pattern and every documented example need the "+
			"same edit", buildinfo.Display())
	}

	shipped := map[string]string{}
	for _, p := range profilesFromMakefile(t) {
		shipped[p.name] = p.tags
	}

	files := []string{
		"../../docs/build-profiles.md",
		"../../docs/output-contract.md",
		// The on-ramp prints a banner too, and was not read here for as long
		// as this test existed. Its example named a real release and the tag
		// set of a build three tags ago, and the check that would have caught
		// the second half was this one, looking elsewhere.
		"../../docs/getting-started.md",
		"../../internal/buildinfo/buildinfo.go",
		"../../internal/cli/session.go",
	}

	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		found := 0
		for _, m := range versionBanner.FindAllStringSubmatch(string(body), -1) {
			found++
			release, name, tags := m[1], m[2], m[3]
			if !semver.MatchString(release) {
				t.Errorf("%s: the example %q shows a release that is not a "+
					"semantic version", path, m[0])
			}
			want, ok := shipped[name]
			if !ok {
				t.Errorf("%s: the example %q names a %q profile the Makefile "+
					"does not ship", path, m[0], name)
				continue
			}
			if tags != want {
				t.Errorf("%s: the example %q gives the %s profile tags=%q and "+
					"the Makefile builds it with %q", path, m[0], name, tags, want)
			}
		}

		for _, m := range userAgentBanner.FindAllStringSubmatch(string(body), -1) {
			found++
			release, name := m[1], m[2]
			if !semver.MatchString(release) {
				t.Errorf("%s: the example %q shows a release that is not a "+
					"semantic version", path, m[0])
			}
			if _, ok := shipped[name]; !ok {
				t.Errorf("%s: the example %q names a %q profile the Makefile "+
					"does not ship", path, m[0], name)
			}
		}

		// A file that matched nothing is a file this test read and did not
		// check. Each of the four carries a worked example today; one that
		// stops matching has either lost it or been reformatted past what the
		// patterns above recognise, and both need a person.
		if found == 0 {
			t.Errorf("%s holds no version example this test recognises", path)
		}
	}
}

// versionScript is the absolute path of the script under test, because every
// case below runs it with the working directory set to a repository it built.
func versionScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../scripts/version.sh")
	if err != nil {
		t.Fatalf("locate scripts/version.sh: %v", err)
	}
	return path
}

// gitEnv isolates a git invocation from whoever is running the test. A signing
// key, a commit template, or an alternative default branch in the developer's
// own config would otherwise change what these repositories look like — and
// GIT_CEILING_DIRECTORIES stops the "no git at all" case from finding a
// repository somewhere above the temporary directory.
func gitEnv(dir string) []string {
	return append(
		inheritedGitEnv(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CEILING_DIRECTORIES="+dir,
		"GIT_AUTHOR_NAME=jr test",
		"GIT_AUTHOR_EMAIL=jr@example.invalid",
		"GIT_COMMITTER_NAME=jr test",
		"GIT_COMMITTER_EMAIL=jr@example.invalid",
	)
}

// pointerEnv are the variables git exports to a hook to say which repository it
// is operating on. They are removed rather than overridden, because the correct
// value for a fresh temporary repository is "unset": git then discovers the
// repository from the working directory, which is what cmd.Dir already says.
//
// **cmd.Dir does not override them.** GIT_INDEX_FILE wins over the directory a
// process was started in, so `git add .` inside a temporary repository writes
// into whatever index the variable names.
var pointerEnv = []string{
	"GIT_INDEX_FILE",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_PREFIX",
}

// inheritedGitEnv is the environment with git's own pointers stripped out.
//
// This was found on 2026-09-03, from a release that could not be tagged. Every
// commit ran the pre-commit hook, the hook ran `go test ./...`, and this file's
// `git add .` wrote `first.txt` into the real repository's index, because git
// runs a hook with GIT_INDEX_FILE set to the index being committed. The blob
// went into the temporary repository's object store, so the real index then
// held an entry pointing at an object that does not exist there:
//
//	error: invalid object 100644 9c59e24b… for 'first.txt'
//	error: Error building trees
//
// It is invisible to CI, which runs `make test` directly and sets none of these,
// and it only bites when the test runs uncached inside a hook. That is the worst
// shape a defect can have: it fails on the developer's machine, at the moment
// they are trying to commit, and never where the gates run.
func inheritedGitEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(pointerEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// git runs one git command in dir and fails the test if it does not succeed.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initRepo builds a repository with one commit in it, which is the least a
// version can be computed from.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, "first")
}

// commit adds a tracked file and commits it, so the tree is clean afterwards.
func commit(t *testing.T, dir, name string) {
	t.Helper()
	writeFile(t, dir, name+".txt", name+"\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", name)
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// run executes the version script in dir and returns both streams separately.
// They are separate because the split is the assertion: a refusal writes to
// stderr and must leave stdout empty.
func run(t *testing.T, script, dir string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command("sh", script)
	cmd.Dir = dir
	cmd.Env = gitEnv(dir)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err = cmd.Run()
	return out.String(), errs.String(), err
}

// TestTheVersionHarnessNeverTouchesTheRealIndex is the guard for the defect
// that produced inheritedGitEnv.
//
// It sets GIT_INDEX_FILE the way git sets it for a hook, runs the same helpers
// the tests above run, and requires that nothing was written there. Before the
// fix this file left two entries in it, `first.txt` and `second.txt`, pointing
// at blobs living in a temporary repository's object store. In a real commit
// that index is the repository's own, and the next `git commit` fails with
// "invalid object ... Error building trees" against a tree it cannot build.
//
// Asserting on the file rather than on the environment is deliberate. A test
// that checked `gitEnv` omits the variable would pass on a helper that omits it
// and a `git` call that does not use the helper, which is exactly how this got
// in: gitEnv was careful about config and silent about pointers.
func TestTheVersionHarnessNeverTouchesTheRealIndex(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "sentinel-index")
	t.Setenv("GIT_INDEX_FILE", sentinel)

	// The same two calls every version test makes, which between them write a
	// file, stage it, and commit it.
	dir := t.TempDir()
	initRepo(t, dir)
	commit(t, dir, "second")

	if _, err := os.Stat(sentinel); err == nil {
		data, _ := os.ReadFile(sentinel)
		t.Fatalf("the harness wrote %d bytes into the index git pointed it at.\n"+
			"In a pre-commit hook that file is the repository's own index, and "+
			"the entries name blobs that live in a temporary repository, so the "+
			"commit fails building its tree.", len(data))
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", sentinel, err)
	}
}
