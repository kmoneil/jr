package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// theToken is what these tests hunt for in any output that is not `auth token`.
const theToken = "ATATT3xFfGF0-super-secret-token-value-9c4e1a"

// session keeps one XDG root across several invocations, so a test can create a
// context and then use it.
func session(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"),
		"XDG_STATE_HOME":  filepath.Join(dir, "state"),
		"XDG_CACHE_HOME":  filepath.Join(dir, "cache"),
		"HOME":            dir,
		"NETRC":           filepath.Join(dir, "nonexistent-netrc"),
	}
}

func mustRun(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	got := run(t, env, args...)
	if got.exit != exitcode.OK {
		t.Fatalf("%v: exit = %v\nstderr: %s", args, got.exit, got.stderr)
	}
	return got
}

// TestContextLifecycle walks the sequence from the spec: create, use, list,
// show, delete.
func TestContextLifecycle(t *testing.T) {
	env := session(t)

	mustRun(t, env, "context", "create", "work",
		"--site", "acme.atlassian.net", "--project", "ENG", "--board", "42")
	mustRun(t, env, "context", "create", "personal",
		"--site", "personal.atlassian.net", "--project", "HOME")

	list := mustRun(t, env, "context", "list")
	for _, want := range []string{"work", "personal", "https://acme.atlassian.net"} {
		if !strings.Contains(list.stdout, want) {
			t.Errorf("list is missing %q:\n%s", want, list.stdout)
		}
	}
	// The first context created becomes current, so one context needs no
	// second command to take effect.
	if !strings.Contains(list.stdout, "work\ttrue") {
		t.Errorf("the first context created is not current:\n%s", list.stdout)
	}

	mustRun(t, env, "context", "use", "personal")
	show := mustRun(t, env, "context", "show")
	if !strings.Contains(show.stdout, `project="HOME"`) {
		t.Errorf("the selected context did not take effect:\n%s", show.stdout)
	}

	// --context is a one-off and does not change what is current.
	oneOff := mustRun(t, env, "context", "show", "--context", "work")
	if !strings.Contains(oneOff.stdout, `project="ENG"`) {
		t.Errorf("--context was ignored:\n%s", oneOff.stdout)
	}
	after := mustRun(t, env, "context", "show")
	if !strings.Contains(after.stdout, `project="HOME"`) {
		t.Errorf("--context changed the current context:\n%s", after.stdout)
	}

	// Deleting needs --yes in every build, because a headless binary has no
	// prompt to fall back to.
	blocked := run(t, env, "context", "delete", "work")
	if blocked.exit != exitcode.Blocked {
		t.Errorf("delete without --yes exited %v, want %v", blocked.exit, exitcode.Blocked)
	}
	if blocked.stdout != "" {
		t.Errorf("a blocked delete wrote to stdout:\n%s", blocked.stdout)
	}

	mustRun(t, env, "context", "delete", "work", "--yes")
	gone := mustRun(t, env, "context", "list")
	if strings.Contains(gone.stdout, "work") {
		t.Errorf("work survived the delete:\n%s", gone.stdout)
	}
}

// TestContextShowExplainsTheEffectiveSettings is the command for "why is it
// using that project", so the answer has to include where each value came from.
func TestContextShowExplainsTheEffectiveSettings(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work",
		"--site", "acme.atlassian.net", "--project", "ENG")

	got := mustRun(t, env, "context", "show", "--project", "OVERRIDE")
	if !strings.Contains(got.stdout, `project="OVERRIDE"`) {
		t.Errorf("--project did not override the context:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, `name="work"`) {
		t.Errorf("the output does not name the context it started from:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, `effective="true"`) {
		t.Errorf("the output does not mark itself as effective rather than stored:\n%s",
			got.stdout)
	}
	// Showing a stored context is a different question and must not apply the
	// override.
	stored := mustRun(t, env, "context", "show", "work", "--project", "OVERRIDE")
	if !strings.Contains(stored.stdout, `project="ENG"`) {
		t.Errorf("showing a stored context applied a flag override:\n%s", stored.stdout)
	}
}

func TestContextErrors(t *testing.T) {
	env := session(t)

	cases := []struct {
		name string
		args []string
		exit exitcode.Code
		code string
	}{
		{
			"invalid name",
			[]string{"context", "create", "Bad Name", "--site", "acme.atlassian.net"},
			exitcode.Usage, "INVALID_CONTEXT_NAME",
		},
		{
			"missing site",
			[]string{"context", "create", "work"},
			exitcode.Usage, "MISSING_REQUIRED_FLAG",
		},
		{
			"bad site",
			[]string{"context", "create", "work", "--site", "ftp://acme"},
			exitcode.Usage, "INVALID_SITE",
		},
		{
			"unknown context",
			[]string{"context", "use", "nope"},
			exitcode.NotFound, "UNKNOWN_CONTEXT",
		},
		{
			"show unknown context",
			[]string{"context", "show", "nope"},
			exitcode.NotFound, "UNKNOWN_CONTEXT",
		},
		{
			"delete unknown context",
			[]string{"context", "delete", "nope", "--yes"},
			exitcode.NotFound, "UNKNOWN_CONTEXT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, env, tc.args...)
			if got.exit != tc.exit {
				t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, tc.exit, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a failing command wrote to stdout:\n%s", got.stdout)
			}
			if !strings.Contains(got.stderr, "<code>"+tc.code+"</code>") {
				t.Errorf("stderr does not carry %s:\n%s", tc.code, got.stderr)
			}
		})
	}
}

// TestAuthLifecycle is the round trip: log in, see the status, get the token,
// log out.
func TestAuthLifecycle(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")

	status := mustRun(t, env, "auth", "status")
	if !strings.Contains(status.stdout, `authenticated="false"`) {
		t.Errorf("a fresh setup reported authenticated:\n%s", status.stdout)
	}

	login := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "acme.atlassian.net",
		"--email", "ada@example.com", "--token-stdin")
	if login.exit != exitcode.OK {
		t.Fatalf("login: exit = %v\nstderr: %s", login.exit, login.stderr)
	}
	// Even the success output must not echo what was just stored.
	if strings.Contains(login.stdout, theToken) {
		t.Fatalf("login echoed the token:\n%s", login.stdout)
	}

	status = mustRun(t, env, "auth", "status")
	if !strings.Contains(status.stdout, `authenticated="true"`) {
		t.Errorf("the stored credential was not found:\n%s", status.stdout)
	}
	if !strings.Contains(status.stdout, `scheme="basic"`) {
		t.Errorf("an email plus a token did not become basic auth:\n%s", status.stdout)
	}
	if strings.Contains(status.stdout, theToken) {
		t.Fatalf("auth status revealed the token:\n%s", status.stdout)
	}

	// `auth token` is the one place a secret is the requested output.
	token := mustRun(t, env, "auth", "token")
	if !strings.Contains(token.stdout, "Basic ") {
		t.Errorf("auth token did not produce a header value:\n%s", token.stdout)
	}

	blocked := run(t, env, "auth", "logout", "--site", "acme.atlassian.net")
	if blocked.exit != exitcode.Blocked {
		t.Errorf("logout without --yes exited %v", blocked.exit)
	}

	mustRun(t, env, "auth", "logout", "--site", "acme.atlassian.net", "--yes")
	status = mustRun(t, env, "auth", "status")
	if !strings.Contains(status.stdout, `authenticated="false"`) {
		t.Errorf("the credential survived logout:\n%s", status.stdout)
	}
}

// TestAuthStatusNeverRevealsTheToken sweeps every format, because a redaction
// that holds in XML and leaks in JSON is not a redaction.
func TestAuthStatusNeverRevealsTheToken(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")
	runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "acme.atlassian.net", "--token-stdin")

	for _, format := range []string{"tsv", "xml", "json", "yaml"} {
		got := mustRun(t, env, "auth", "status", "--format", format)
		if strings.Contains(got.stdout, theToken) {
			t.Errorf("auth status --format %s revealed the token:\n%s", format, got.stdout)
		}
		if got.stderr != "" {
			t.Errorf("auth status --format %s wrote to stderr:\n%s", format, got.stderr)
		}
	}
}

// TestCredentialFromEnvironmentBeatsTheStore is the precedence a CI job depends
// on: supply a token in the environment and nothing on disk has to change.
func TestCredentialFromEnvironmentBeatsTheStore(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")
	runWithStdin(t, env, strings.NewReader("stored-token"),
		"auth", "login", "--site", "acme.atlassian.net", "--token-stdin")

	env["JIRA_API_TOKEN"] = "env-token"
	got := mustRun(t, env, "auth", "status")
	if !strings.Contains(got.stdout, `source="JIRA_API_TOKEN"`) {
		t.Errorf("the environment did not win:\n%s", got.stdout)
	}
}

// TestLogoutCannotRemoveAnEnvironmentCredential stops `auth logout` reporting
// success while the site stays authenticated.
func TestLogoutCannotRemoveAnEnvironmentCredential(t *testing.T) {
	env := session(t)
	env["JIRA_API_TOKEN"] = theToken

	got := run(t, env, "auth", "logout", "--site", "acme.atlassian.net", "--yes")
	if got.exit != exitcode.NotFound {
		t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.NotFound, got.stderr)
	}
	if !strings.Contains(got.stderr, "NO_STORED_CREDENTIAL") {
		t.Errorf("stderr does not explain what happened:\n%s", got.stderr)
	}
}

// TestTokenIsNotAcceptedOnTheCommandLine is why login reads stdin: a token in
// an argument lands in the shell history and the process list.
func TestTokenIsNotAcceptedOnTheCommandLine(t *testing.T) {
	env := session(t)
	got := run(t, env, "auth", "login",
		"--site", "acme.atlassian.net", "--token", theToken)
	if got.exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Usage)
	}
	if !strings.Contains(got.stderr, "unknown flag") {
		t.Errorf("--token appears to exist:\n%s", got.stderr)
	}
}

func TestLoginRejectsEmptyStdin(t *testing.T) {
	env := session(t)
	got := runWithStdin(t, env, strings.NewReader("   \n"),
		"auth", "login", "--site", "acme.atlassian.net", "--token-stdin")
	if got.exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Usage, got.stderr)
	}
	if !strings.Contains(got.stderr, "EMPTY_TOKEN") {
		t.Errorf("stderr does not say stdin was empty:\n%s", got.stderr)
	}
}

// TestLoginTrimsTheToken matters because `echo` adds a newline, and a token
// with one fails authentication in a way that looks like a wrong token.
func TestLoginTrimsTheToken(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")
	if got := runWithStdin(t, env, strings.NewReader(theToken+"\n"),
		"auth", "login", "--site", "acme.atlassian.net", "--token-stdin"); got.exit != exitcode.OK {
		t.Fatalf("login: %v\n%s", got.exit, got.stderr)
	}

	token := mustRun(t, env, "auth", "token")
	if strings.Contains(token.stdout, theToken+"\\n") {
		t.Errorf("the trailing newline was stored:\n%s", token.stdout)
	}
}

// TestCredentialFileIsNotWorldReadable checks the mode through the real
// command path, not just the store's unit test.
func TestCredentialFileIsNotWorldReadable(t *testing.T) {
	env := session(t)
	runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "acme.atlassian.net", "--token-stdin")

	path := filepath.Join(env["XDG_STATE_HOME"], "jr", "credentials.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s is mode %04o, want 0600", path, perm)
	}
}

// TestConfigFileNeverContainsACredential is the reason the two files are
// separate at all.
func TestConfigFileNeverContainsACredential(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")
	runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "acme.atlassian.net", "--token-stdin")

	data, err := os.ReadFile(filepath.Join(env["XDG_CONFIG_HOME"], "jr", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), theToken) {
		t.Fatalf("the credential reached the config file:\n%s", data)
	}
}

// TestReadOnlyContextIsReportedAsSuch covers the latch through the CLI.
func TestReadOnlyContextIsReportedAsSuch(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "audit",
		"--site", "acme.atlassian.net", "--readonly")

	got := mustRun(t, env, "context", "show")
	if !strings.Contains(got.stdout, `readonly="true"`) {
		t.Errorf("the context is not reported read-only:\n%s", got.stdout)
	}

	// Omitting --readonly does not clear it.
	again := mustRun(t, env, "context", "show")
	if !strings.Contains(again.stdout, `readonly="true"`) {
		t.Errorf("read-only was cleared by omitting the flag:\n%s", again.stdout)
	}
}

func TestReadOnlyEnvIsVisible(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")
	env["JIRA_READONLY"] = "1"

	got := mustRun(t, env, "context", "show")
	if !strings.Contains(got.stdout, `readonly="true"`) {
		t.Errorf("JIRA_READONLY was ignored:\n%s", got.stdout)
	}
}

// TestNoContextIsAUsableState covers a first run: nothing configured yet, and
// the commands that need a site say so with a remedy rather than crashing.
func TestNoContextIsAUsableState(t *testing.T) {
	env := session(t)

	list := mustRun(t, env, "context", "list")
	if !strings.Contains(list.stdout, "name\t") {
		t.Errorf("an empty list produced no header:\n%s", list.stdout)
	}

	show := mustRun(t, env, "context", "show")
	if show.exit != exitcode.OK {
		t.Errorf("context show with nothing configured exited %v", show.exit)
	}

	needsSite := run(t, env, "auth", "status")
	if needsSite.exit != exitcode.Usage {
		t.Errorf("auth status with no site exited %v, want %v",
			needsSite.exit, exitcode.Usage)
	}
	if !strings.Contains(needsSite.stderr, "NO_SITE") {
		t.Errorf("stderr does not name the problem:\n%s", needsSite.stderr)
	}
	if !strings.Contains(needsSite.stderr, "--site") {
		t.Errorf("the remedy does not name the flag:\n%s", needsSite.stderr)
	}
}

// TestConfigIsHandEditable checks the file a user is invited to open.
func TestConfigIsHandEditable(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work",
		"--site", "acme.atlassian.net", "--project", "ENG")

	data, err := os.ReadFile(filepath.Join(env["XDG_CONFIG_HOME"], "jr", "config.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "#") {
		t.Errorf("the config has no explanatory header:\n%s", text)
	}
	for _, want := range []string{"current", "[contexts.work]", "acme.atlassian.net", "ENG"} {
		if !strings.Contains(text, want) {
			t.Errorf("the config is missing %q:\n%s", want, text)
		}
	}
}

// TestHandEditedConfigIsRead closes the loop: what the tool invites a user to
// edit, it must then read back.
func TestHandEditedConfigIsRead(t *testing.T) {
	env := session(t)
	dir := filepath.Join(env["XDG_CONFIG_HOME"], "jr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handWritten := `
current = "byhand"

[contexts.byhand]
site = "hand.atlassian.net"
project = "HAND"
readonly = true
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte(handWritten), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := mustRun(t, env, "context", "show")
	for _, want := range []string{`name="byhand"`, `project="HAND"`, `readonly="true"`} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the hand-written config was not read (%q):\n%s", want, got.stdout)
		}
	}
}

// TestTokenFile covers the alternative to a pipe, for a secret manager that
// writes to disk or a shell where quoting a pipeline is awkward.
func TestTokenFile(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work", "--site", "acme.atlassian.net")

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(theToken+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := run(t, env, "auth", "login",
		"--site", "acme.atlassian.net", "--email", "ada@example.com",
		"--token-file", path)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	if strings.Contains(got.stdout, theToken) {
		t.Fatal("login echoed the token")
	}

	status := mustRun(t, env, "auth", "status")
	if !strings.Contains(status.stdout, `authenticated="true"`) {
		t.Errorf("the token file was not stored:\n%s", status.stdout)
	}
}

// TestTokenFileDashMeansStdin follows the universal convention.
func TestTokenFileDashMeansStdin(t *testing.T) {
	env := session(t)
	got := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "acme.atlassian.net", "--token-file", "-")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
}

// TestNoTokenSourceIsRefusedWithEveryOption is the fix for the failure that
// reads as a hang: the command says what it needs instead of waiting.
func TestNoTokenSourceIsRefusedWithEveryOption(t *testing.T) {
	env := session(t)
	got := run(t, env, "auth", "login", "--site", "acme.atlassian.net")
	if got.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Usage, got.stderr)
	}
	if !strings.Contains(got.stderr, "NO_TOKEN_SOURCE") {
		t.Errorf("stderr does not name the problem:\n%s", got.stderr)
	}
	// Every way in has to be listed, because "it did nothing" is the least
	// actionable failure there is.
	for _, want := range []string{"--token-stdin", "--token-file", "JIRA_API_TOKEN"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the remedy does not mention %q:\n%s", want, got.stderr)
		}
	}
}

func TestAmbiguousTokenSourceIsRefused(t *testing.T) {
	env := session(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(theToken), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := runWithStdin(t, env, strings.NewReader("other"),
		"auth", "login", "--site", "acme.atlassian.net",
		"--token-stdin", "--token-file", path)
	if got.exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Usage)
	}
	// Picking one silently would store a credential the caller cannot identify.
	if !strings.Contains(got.stderr, "AMBIGUOUS_TOKEN_SOURCE") {
		t.Errorf("stderr does not explain the conflict:\n%s", got.stderr)
	}
}

func TestMissingTokenFileIsAUsageError(t *testing.T) {
	env := session(t)
	got := run(t, env, "auth", "login",
		"--site", "acme.atlassian.net", "--token-file", "/nonexistent/token")
	if got.exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Usage)
	}
	if !strings.Contains(got.stderr, "TOKEN_FILE_UNREADABLE") {
		t.Errorf("stderr does not name the problem:\n%s", got.stderr)
	}
}

// TestLoginCreatesTheFirstContext closes the gap that made `auth login --site X`
// succeed and the very next command report "no Jira site configured". Storing a
// credential for a site and then acting as though the site was never mentioned
// is the tool ignoring input it accepted.
func TestLoginCreatesTheFirstContext(t *testing.T) {
	env := session(t)

	got := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "jira.corp.com", "--token-stdin")
	if got.exit != exitcode.OK {
		t.Fatalf("login: exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	// The name is derived from the host's first label, and the output says so
	// rather than leaving the caller to discover it.
	if !strings.Contains(got.stdout, `context="jira"`) {
		t.Errorf("login did not report the context it created:\n%s", got.stdout)
	}

	list := mustRun(t, env, "context", "list")
	if !strings.Contains(list.stdout, "https://jira.corp.com") {
		t.Errorf("no context was created:\n%s", list.stdout)
	}

	// And the site now resolves, which is the whole point.
	show := mustRun(t, env, "context", "show")
	if !strings.Contains(show.stdout, `site="https://jira.corp.com"`) {
		t.Errorf("the site does not resolve after login:\n%s", show.stdout)
	}
}

// TestLoginDoesNotTouchExistingContexts is the other half of the rule. With a
// setup already in place, guessing which context a new credential belongs to
// would be worse than doing nothing.
func TestLoginDoesNotTouchExistingContexts(t *testing.T) {
	env := session(t)
	mustRun(t, env, "context", "create", "work",
		"--site", "acme.atlassian.net", "--project", "ENG")

	got := runWithStdin(t, env, strings.NewReader(theToken),
		"auth", "login", "--site", "other.atlassian.net", "--token-stdin")
	if got.exit != exitcode.OK {
		t.Fatalf("login: exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	if strings.Contains(got.stdout, "context=") {
		t.Errorf("login created a context despite one existing:\n%s", got.stdout)
	}

	list := mustRun(t, env, "context", "list")
	if strings.Contains(list.stdout, "other.atlassian.net") {
		t.Errorf("login added a context:\n%s", list.stdout)
	}
	show := mustRun(t, env, "context", "show")
	if !strings.Contains(show.stdout, `project="ENG"`) {
		t.Errorf("the current context changed:\n%s", show.stdout)
	}
}

// TestContextNameIsDerivedFromTheHost covers the fallback for a host whose
// first label would not be a usable context name.
func TestContextNameIsDerivedFromTheHost(t *testing.T) {
	cases := map[string]string{
		"jira.corp.com":      "jira",
		"acme.atlassian.net": "acme",
		"127.0.0.1:8080":     "default",
		"localhost:8080":     "localhost",
	}
	for host, want := range cases {
		t.Run(host, func(t *testing.T) {
			env := session(t)
			got := runWithStdin(t, env, strings.NewReader(theToken),
				"auth", "login", "--site", host, "--token-stdin")
			if got.exit != exitcode.OK {
				t.Fatalf("login: exit = %v\nstderr: %s", got.exit, got.stderr)
			}
			if !strings.Contains(got.stdout, `context="`+want+`"`) {
				t.Errorf("context name for %s is not %q:\n%s", host, want, got.stdout)
			}
		})
	}
}
