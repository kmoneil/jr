package cli_test

import (
	"context"
	"flag"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/exitcode"
)

var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

// TestMain pins every part of the build identity that varies between machines,
// so the golden output is a function of the code alone.
func TestMain(m *testing.M) {
	buildinfo.Release = "0.0.0-test"
	buildinfo.Commit = "0000000"
	buildinfo.Built = "2026-01-01T00:00:00Z"
	buildinfo.GoVersion = "go0.0.0"
	buildinfo.Platform = "test/test"
	os.Exit(m.Run())
}

type result struct {
	exit   exitcode.Code
	stdout string
	stderr string
}

// isolate points the three XDG roots at a temporary directory.
//
// Without it a test would read, and `context create` would write, the config of
// whoever ran the suite. A test that can damage the developer's own setup is one
// nobody runs twice.
func isolate(t *testing.T, env map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	maps.Copy(out, env)

	dir := t.TempDir()
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if _, set := out[key]; !set {
			out[key] = filepath.Join(dir, strings.ToLower(strings.TrimSuffix(
				strings.TrimPrefix(key, "XDG_"), "_HOME",
			)))
		}
	}
	if _, set := out["HOME"]; !set {
		out["HOME"] = dir
	}
	// An inherited NETRC would make the credential chain depend on the machine
	// the suite runs on.
	if _, set := out["NETRC"]; !set {
		out["NETRC"] = filepath.Join(dir, "nonexistent-netrc")
	}
	return out
}

func run(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	return runWithStdin(t, env, nil, args...)
}

func runWithStdin(t *testing.T, env map[string]string, stdin io.Reader, args ...string) result {
	t.Helper()
	resolved := isolate(t, env)

	var out, errOut strings.Builder
	code := cli.Main(context.Background(), args, cli.Options{
		Stdout: &out,
		Stderr: &errOut,
		Stdin:  stdin,
		Getenv: func(k string) string { return resolved[k] },
	})
	return result{exit: code, stdout: out.String(), stderr: errOut.String()}
}

func TestVersionDefaultsToXML(t *testing.T) {
	got := run(t, nil, "version")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr is not empty on success:\n%s", got.stderr)
	}
	if !strings.HasPrefix(got.stdout, `<?xml`) {
		t.Errorf("a record did not default to XML:\n%s", got.stdout)
	}
	assertGolden(t, "version.xml", got.stdout)
}

func TestSchemaDefaultsToTSV(t *testing.T) {
	got := run(t, nil, "schema")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if strings.HasPrefix(got.stdout, "<") || strings.HasPrefix(got.stdout, "{") {
		t.Errorf("a collection did not default to TSV:\n%s", got.stdout)
	}
	assertGolden(t, "schema.tsv", got.stdout)
}

// TestSchemaIsCompleteWithNoFlags is a regression with a date on it.
//
// `jr schema` inherited the fifty-result default that exists so an unbounded
// remote query does not page forever. The command surface is neither remote nor
// unbounded — the binary holds the whole list before the command runs — and the
// default was invisible right up until the build passed fifty commands, at
// which point the tool's own self-description started arriving truncated with
// exit 3. Honest, and useless: an agent introspecting the binary would have
// seen most of it.
//
// The count is deliberately not asserted. What matters is that whatever the
// build contains, asking it what it contains returns all of it.
func TestSchemaIsCompleteWithNoFlags(t *testing.T) {
	got := run(t, nil, "schema")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, want 0: the self-description was truncated\n%s",
			got.exit, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr is not empty, so something was withheld:\n%s", got.stderr)
	}

	rows := strings.Count(strings.TrimRight(got.stdout, "\n"), "\n")
	bounded := run(t, nil, "schema", "--limit", "all")
	if want := strings.Count(strings.TrimRight(bounded.stdout, "\n"), "\n"); rows != want {
		t.Errorf("schema returned %d rows and `schema --limit all` returned %d",
			rows, want)
	}
	// --limit still bounds it when asked, so the flag keeps meaning what it
	// says rather than becoming decoration.
	if one := run(t, nil, "schema", "--limit", "1"); one.exit != exitcode.Partial {
		t.Errorf("--limit 1 exit = %v, want PARTIAL", one.exit)
	}
}

// TestTruncationExitsPartial is the contract's load-bearing behavior: a
// truncated result is data on stdout, a structured warning on stderr, and
// exit 3 — never a quiet success.
func TestTruncationExitsPartial(t *testing.T) {
	got := run(t, nil, "schema", "--limit", "1")
	if got.exit != exitcode.Partial {
		t.Fatalf("exit = %v, want %v (PARTIAL)", got.exit, exitcode.Partial)
	}
	if strings.Count(strings.TrimRight(got.stdout, "\n"), "\n") != 1 {
		t.Errorf("--limit 1 did not return exactly one row:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "RESULT_TRUNCATED") {
		t.Errorf("truncation warning missing from stderr:\n%s", got.stderr)
	}
	assertGolden(t, "schema-truncated.stderr", got.stderr)
}

// TestCompleteResultIsSilent asserts the converse: an exhaustive result says
// nothing on stderr at all.
func TestCompleteResultIsSilent(t *testing.T) {
	got := run(t, nil, "schema", "--limit", "all")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("a complete result wrote to stderr:\n%s", got.stderr)
	}
}

func TestSchemaOfOneCommandIsARecord(t *testing.T) {
	got := run(t, nil, "schema", "schema")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, `kind="schema.command"`) {
		t.Errorf("naming a command did not switch to the record kind:\n%s", got.stdout)
	}
	assertGolden(t, "schema-command.xml", got.stdout)
}

func TestDescribeDoesNotRunTheCommand(t *testing.T) {
	got := run(t, nil, "version", "--describe")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, `kind="schema.command"`) {
		t.Errorf("--describe did not emit the command schema:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "<display>") {
		t.Error("--describe ran the command instead of describing it")
	}
}

func TestContractListsEveryKind(t *testing.T) {
	got := run(t, nil, "--contract", "--format", "xml")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	for _, kind := range []string{"version", "schema.commands", "schema.command", "contract"} {
		if !strings.Contains(got.stdout, `name="`+kind+`"`) {
			t.Errorf("--contract omits kind %q:\n%s", kind, got.stdout)
		}
	}

	// The inventory — which kinds this build emits, at what version, and from
	// which commands — is a property of the profile, so it is goldened per
	// profile. Each kind's shape is not: it is the same wherever the kind
	// exists, and lives once per version in testdata/kinds.
	inventory := run(t, nil, "--contract", "--format", "tsv")
	if inventory.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", inventory.exit, inventory.stderr)
	}
	assertGolden(t, "contract.tsv", inventory.stdout)
}

// TestErrorsGoToStderrAndLeaveStdoutClean is the stdout discipline: a failing
// command emits no data at all, so a consumer piping stdout never parses a
// half-result.
func TestErrorsGoToStderrAndLeaveStdoutClean(t *testing.T) {
	cases := []struct {
		name string
		args []string
		exit exitcode.Code
		code string
	}{
		{"unknown command", []string{"bogus"}, exitcode.Usage, "UNKNOWN_COMMAND"},
		{"unknown subcommand", []string{"schema", "--format", "csv"}, exitcode.Usage, "INVALID_FORMAT"},
		{"unknown flag", []string{"version", "--nope"}, exitcode.Usage, "INVALID_USAGE"},
		{"too many args", []string{"schema", "a", "b"}, exitcode.Usage, "INVALID_USAGE"},
		{"unknown schema name", []string{"schema", "nope"}, exitcode.NotFound, "UNKNOWN_COMMAND"},
		{"bad limit", []string{"schema", "--limit", "0"}, exitcode.Usage, "INVALID_LIMIT"},
		{"offset-shaped limit", []string{"schema", "--limit", "50:2"}, exitcode.Usage, "INVALID_LIMIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, nil, tc.args...)
			if got.exit != tc.exit {
				t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, tc.exit, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a failing command wrote to stdout:\n%s", got.stdout)
			}
			if !strings.Contains(got.stderr, "<code>"+tc.code+"</code>") {
				t.Errorf("stderr does not carry code %s:\n%s", tc.code, got.stderr)
			}
			if !strings.Contains(got.stderr, "<retryable>") {
				t.Errorf("stderr does not state whether the failure is retryable:\n%s", got.stderr)
			}
		})
	}
}

func TestErrorFormatFollowsTheRequestedFormat(t *testing.T) {
	got := run(t, nil, "schema", "nope", "--format", "json")
	if got.exit != exitcode.NotFound {
		t.Fatalf("exit = %v, want %v", got.exit, exitcode.NotFound)
	}
	if !strings.Contains(got.stderr, `"code": "UNKNOWN_COMMAND"`) {
		t.Errorf("--format json did not apply to the error:\n%s", got.stderr)
	}
	assertGolden(t, "error-not-found.json", got.stderr)
}

// TestBadFormatStillProducesAReadableError asserts an unparseable --format does
// not cause a second failure inside the error renderer.
func TestBadFormatStillProducesAReadableError(t *testing.T) {
	got := run(t, nil, "version", "--format", "csv")
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want %v", got.exit, exitcode.Usage)
	}
	if !strings.HasPrefix(got.stderr, "<?xml") {
		t.Errorf("an unparseable --format did not fall back to the record default:\n%s", got.stderr)
	}
	for _, f := range []string{"tsv", "xml", "json", "yaml"} {
		if !strings.Contains(got.stderr, f) {
			t.Errorf("the error does not list %q as a valid format:\n%s", f, got.stderr)
		}
	}
}

func TestEnvSetsTheDefaultFormat(t *testing.T) {
	env := map[string]string{cli.EnvFormat: "json"}
	got := run(t, env, "schema")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.HasPrefix(got.stdout, "{") {
		t.Errorf("%s did not set the default format:\n%s", cli.EnvFormat, got.stdout)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	env := map[string]string{cli.EnvFormat: "json"}
	got := run(t, env, "schema", "--format", "tsv")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if strings.HasPrefix(got.stdout, "{") {
		t.Errorf("--format did not override %s:\n%s", cli.EnvFormat, got.stdout)
	}
}

func TestBadEnvFormatIsAUsageError(t *testing.T) {
	env := map[string]string{cli.EnvFormat: "csv"}
	got := run(t, env, "schema")
	if got.exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Usage, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("a bad %s still produced output:\n%s", cli.EnvFormat, got.stdout)
	}
}

// TestEveryFormatRendersEveryCommand asserts the flag is the contract: all four
// formats work on every command, whatever the per-content default is.
//
// It goes through --describe because most commands need input to do anything,
// and the point here is that the format flag is honored everywhere — not that
// every command can run bare.
func TestEveryFormatRendersEveryCommand(t *testing.T) {
	for _, c := range cli.Registry().All() {
		for _, f := range []string{"tsv", "xml", "json", "yaml"} {
			args := append(strings.Split(c.UseLine(), " "), "--describe", "--format", f)
			got := run(t, nil, args...)
			if got.exit != exitcode.OK {
				t.Errorf("%s --describe --format %s: exit = %v\nstderr: %s",
					c.UseLine(), f, got.exit, got.stderr)
			}
			if got.stdout == "" {
				t.Errorf("%s --describe --format %s produced no output", c.UseLine(), f)
			}
		}
	}
}

// bareRunnable are the commands that produce a result with no input at all.
// Anything else needs a site, a name, or a token, and says so.
var bareRunnable = []string{"schema", "version", "contract", "context list", "context show"}

// TestBareRunnableCommandsRenderInEveryFormat covers the real output path, as
// opposed to the schema path --describe exercises.
func TestBareRunnableCommandsRenderInEveryFormat(t *testing.T) {
	for _, use := range bareRunnable {
		if _, ok := cli.Registry().Lookup(strings.ReplaceAll(use, " ", ".")); !ok {
			t.Errorf("%q is listed as bare-runnable but is not registered", use)
			continue
		}
		for _, f := range []string{"tsv", "xml", "json", "yaml"} {
			args := append(strings.Split(use, " "), "--format", f)
			got := run(t, nil, args...)
			if got.exit != exitcode.OK {
				t.Errorf("%s --format %s: exit = %v\nstderr: %s", use, f, got.exit, got.stderr)
			}
			if got.stdout == "" {
				t.Errorf("%s --format %s produced no output", use, f)
			}
		}
	}
}

func TestUnknownCommandSuggestsNearMatches(t *testing.T) {
	got := run(t, nil, "schema", "versionn")
	if got.exit != exitcode.NotFound {
		t.Fatalf("exit = %v, want %v", got.exit, exitcode.NotFound)
	}
	if !strings.Contains(got.stderr, "version") {
		t.Errorf("a near miss produced no suggestion:\n%s", got.stderr)
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	got := run(t, nil, "--help")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, "Available Commands:") {
		t.Errorf("--help did not reach stdout:\n%s", got.stdout)
	}
	assertGolden(t, "help.txt", got.stdout)
}

// TestHelpListsOnlyRegisteredCommands asserts --help and `jr schema` cannot
// disagree about what this binary contains.
func TestHelpListsOnlyRegisteredCommands(t *testing.T) {
	help := run(t, nil, "--help")
	registered := map[string]bool{}
	for _, c := range cli.Registry().All() {
		registered[c.Path[0]] = true
	}

	_, listed, found := strings.Cut(help.stdout, "Available Commands:")
	if !found {
		t.Fatalf("--help lists no commands:\n%s", help.stdout)
	}
	listed, _, _ = strings.Cut(listed, "\nFlags:")
	for line := range strings.SplitSeq(strings.TrimSpace(listed), "\n") {
		name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if name == "" || name == "help" {
			// `help` is the human help path, not a result-producing command.
			continue
		}
		if !registered[name] {
			t.Errorf("--help offers %q, which is not in the registry, "+
				"so `jr schema` does not know about it", name)
		}
	}
}

// TestRunsAreIndependent asserts two invocations in one process do not collide,
// which is what lets the MCP server run commands in-process.
func TestRunsAreIndependent(t *testing.T) {
	first := run(t, nil, "schema")
	second := run(t, nil, "schema")
	if first.stdout != second.stdout {
		t.Errorf("two identical runs differed:\n--- first ---\n%s\n--- second ---\n%s",
			first.stdout, second.stdout)
	}
	if second.exit != exitcode.OK {
		t.Errorf("the second run exited %v: %s", second.exit, second.stderr)
	}
}

// goldenProfiles are the shipped builds this package records a golden set for.
//
// Output legitimately differs between them: `jr schema` lists fewer commands in
// a reader build, `jr version` names the tags it was built with, and
// `jr contract` lists only the kinds that build can emit. One set therefore
// cannot cover all four, so each gets its own directory under testdata and
// compares against that one. `make golden` records all four and
// `make test-profiles` checks all four.
//
// The per-kind *shapes* are not in here: a kind's schema is the same in every
// build that has the kind, so it is pinned once per version in testdata/kinds.
// See kinds_test.go.
var goldenProfiles = []string{"full", "agent", "reader", "ci"}

// goldenSet names the directory holding this build's golden files.
//
// A tag set that is not a shipped profile has no recorded set, and recording
// one would be pinning the output of a build nobody ships. That is the only
// skip left: all four shipped profiles have a set, and `make test-profiles`
// runs every one of them. internal/lint asserts the four directories exist and
// agree on which files they hold, so a profile whose set was never recorded
// fails in every build rather than skipping quietly in its own.
func goldenSet(t *testing.T) string {
	t.Helper()

	profile := buildinfo.Profile()
	if !slices.Contains(goldenProfiles, profile) {
		t.Skipf("tags=%s is not a shipped profile, so no golden set is recorded "+
			"for it; run `make test-profiles`", buildinfo.TagList())
	}
	return profile
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	assertGoldenAt(t, filepath.Join("testdata", goldenSet(t), name), got)
}

// assertGoldenAt compares got against an exact path, or rewrites it under
// -update.
func assertGoldenAt(t *testing.T, path, got string) {
	t.Helper()

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nrun: make golden", path, err)
	}
	if got != string(want) {
		t.Errorf("output does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
