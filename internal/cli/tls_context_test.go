package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The TLS settings a context can carry, from the command line down to the file
// and back out again. What they *do* is asserted in internal/transport, against
// a real handshake; this is about a caller being able to say them and see them.

// TestAContextCarriesItsOwnTLSSettings is the case the feature exists for: one
// login and many contexts, where a Data Center site sits behind an internal CA
// and a Cloud site does not. The environment's answer, SSL_CERT_FILE, is global
// and would apply to both.
func TestAContextCarriesItsOwnTLSSettings(t *testing.T) {
	env := session(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "corporate-root.pem")
	cert := filepath.Join(dir, "client.pem")
	key := filepath.Join(dir, "client.key")

	mustRun(t, env, "context", "create", "onprem",
		"--site", "jira.acme.invalid",
		"--ca-bundle", bundle,
		"--client-cert", cert,
		"--client-key", key)

	shown := mustRun(t, env, "context", "show", "onprem")
	for _, want := range []string{
		`ca-bundle="` + bundle + `"`,
		`client-cert="` + cert + `"`,
		`client-key="` + key + `"`,
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("context show does not report %s:\n%s", want, shown.stdout)
		}
	}

	// And a context that sets none says nothing about them, because "the system
	// trust store" is the common case and an attribute stating it on every
	// context is noise on every context.
	mustRun(t, env, "context", "create", "cloud", "--site", "cloud.acme.invalid")
	plain := mustRun(t, env, "context", "show", "cloud")
	for _, unwanted := range []string{"ca-bundle=", "client-cert=", "client-key="} {
		if strings.Contains(plain.stdout, unwanted) {
			t.Errorf("a context with no TLS settings reports %s anyway:\n%s",
				unwanted, plain.stdout)
		}
	}
}

// TestTheTLSSettingsSurviveAnEdit holds the same rule the rest of the context
// does: an edit changes what it names and leaves the rest alone. Re-stating a
// context to change its project is how a board and a field set were dropped
// once, and a CA bundle is a worse thing to drop — the failure it produces is a
// verification error that names the site rather than the setting.
func TestTheTLSSettingsSurviveAnEdit(t *testing.T) {
	env := session(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "corporate-root.pem")

	mustRun(t, env, "context", "create", "onprem",
		"--site", "jira.acme.invalid", "--ca-bundle", bundle)
	mustRun(t, env, "context", "edit", "onprem", "--project", "ENG")

	shown := mustRun(t, env, "context", "show", "onprem")
	if !strings.Contains(shown.stdout, `ca-bundle="`+bundle+`"`) {
		t.Errorf("editing the project dropped the CA bundle:\n%s", shown.stdout)
	}
	if !strings.Contains(shown.stdout, `project="ENG"`) {
		t.Errorf("the edit did not apply:\n%s", shown.stdout)
	}
}

// TestUnsetClearsATLSSetting. An empty flag value and an absent one arrive
// identically, so clearing needs its own spelling here as everywhere else.
func TestUnsetClearsATLSSetting(t *testing.T) {
	env := session(t)
	dir := t.TempDir()

	mustRun(t, env, "context", "create", "onprem",
		"--site", "jira.acme.invalid",
		"--ca-bundle", filepath.Join(dir, "root.pem"),
		"--client-cert", filepath.Join(dir, "client.pem"),
		"--client-key", filepath.Join(dir, "client.key"))
	mustRun(t, env, "context", "edit", "onprem",
		"--unset", "ca-bundle", "--unset", "client-cert", "--unset", "client-key")

	shown := mustRun(t, env, "context", "show", "onprem")
	for _, unwanted := range []string{"ca-bundle=", "client-cert=", "client-key="} {
		if strings.Contains(shown.stdout, unwanted) {
			t.Errorf("--unset left %s behind:\n%s", unwanted, shown.stdout)
		}
	}
}

// TestTheFlagOverridesTheContextsBundle covers the precedence a one-off needs:
// somebody debugging a chain should be able to point at a different bundle for
// one invocation without editing the context they are trying to diagnose.
func TestTheFlagOverridesTheContextsBundle(t *testing.T) {
	env := session(t)
	dir := t.TempDir()
	stored := filepath.Join(dir, "stored.pem")
	once := filepath.Join(dir, "once.pem")

	mustRun(t, env, "context", "create", "onprem",
		"--site", "jira.acme.invalid", "--ca-bundle", stored)
	mustRun(t, env, "context", "use", "onprem")

	// `context show` with no argument reports what is in effect rather than
	// what is stored, which is the only place a flag can be seen at all.
	shown := mustRun(t, env, "--ca-bundle", once, "context", "show")
	if !strings.Contains(shown.stdout, `ca-bundle="`+once+`"`) {
		t.Errorf("--ca-bundle did not override the context's:\n%s", shown.stdout)
	}
	if strings.Contains(shown.stdout, stored) {
		t.Errorf("the context's bundle is still in effect:\n%s", shown.stdout)
	}
}

// TestTheEnvironmentSetsTheBundleForAWholeShell, which is what a CI job wants:
// the bundle is a property of the network the job runs on rather than of any
// one command.
func TestTheEnvironmentSetsTheBundleForAWholeShell(t *testing.T) {
	env := session(t)
	dir := t.TempDir()
	stored := filepath.Join(dir, "stored.pem")
	fromEnv := filepath.Join(dir, "from-env.pem")

	mustRun(t, env, "context", "create", "onprem",
		"--site", "jira.acme.invalid", "--ca-bundle", stored)
	mustRun(t, env, "context", "use", "onprem")

	env["JIRA_CA_BUNDLE"] = fromEnv
	shown := mustRun(t, env, "context", "show")
	if !strings.Contains(shown.stdout, `ca-bundle="`+fromEnv+`"`) {
		t.Errorf("JIRA_CA_BUNDLE did not take effect:\n%s", shown.stdout)
	}
}

// TestABundleIsNotReadUntilARequestIsMade. `context show` reports the setting
// whether or not the file is there, because it is reporting configuration
// rather than verifying it, and a report that failed on a missing file would be
// unusable for the one job it has: telling somebody what is configured while
// they work out why the file is missing.
func TestABundleIsNotReadUntilARequestIsMade(t *testing.T) {
	env := session(t)
	missing := filepath.Join(t.TempDir(), "never-created.pem")
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("this test needs a path that does not exist")
	}

	mustRun(t, env, "context", "create", "onprem",
		"--site", "jira.acme.invalid", "--ca-bundle", missing)
	shown := mustRun(t, env, "context", "show", "onprem")
	if !strings.Contains(shown.stdout, `ca-bundle="`+missing+`"`) {
		t.Errorf("context show refused to report a bundle that is not there:\n%s",
			shown.stdout)
	}
}
