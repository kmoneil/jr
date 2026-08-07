package jctx_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/jctx"
)

func env(pairs map[string]string) jctx.Getenv {
	return func(k string) string { return pairs[k] }
}

func TestDefaultPathsAreXDG(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{
		"XDG_CONFIG_HOME": "/cfg",
		"XDG_STATE_HOME":  "/state",
		"XDG_CACHE_HOME":  "/cache",
		"HOME":            "/home/ada",
	}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if paths.Config != "/cfg/jr" {
		t.Errorf("Config = %q", paths.Config)
	}
	if paths.State != "/state/jr" {
		t.Errorf("State = %q", paths.State)
	}
	if paths.Cache != "/cache/jr" {
		t.Errorf("Cache = %q", paths.Cache)
	}

	// The config file is $XDG_CONFIG_HOME/jr/config.toml — not a hidden file
	// inside a hidden directory inside a namespaced directory inside .config.
	if got := paths.ConfigFile(nil); got != "/cfg/jr/config.toml" {
		t.Errorf("ConfigFile = %q", got)
	}
	if strings.Contains(paths.ConfigFile(nil), "/.") {
		t.Errorf("the config path hides a component: %s", paths.ConfigFile(nil))
	}
}

func TestDefaultPathsFallBackToHome(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{"HOME": "/home/ada"}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	want := map[string]string{
		"config": "/home/ada/.config/jr",
		"state":  "/home/ada/.local/state/jr",
		"cache":  "/home/ada/.cache/jr",
	}
	got := map[string]string{"config": paths.Config, "state": paths.State, "cache": paths.Cache}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// TestRelativeXDGIsIgnored matters because resolving a relative XDG variable
// against the working directory would put a user's contexts somewhere different
// depending on where they ran the command.
func TestRelativeXDGIsIgnored(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{
		"XDG_CONFIG_HOME": "relative/path",
		"HOME":            "/home/ada",
	}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if paths.Config != "/home/ada/.config/jr" {
		t.Errorf("Config = %q, want the fallback", paths.Config)
	}
}

func TestConfigFileOverride(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{"HOME": "/home/ada"}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	getenv := env(map[string]string{jctx.EnvConfigFile: "/tmp/other.toml"})
	if got := paths.ConfigFile(getenv); got != "/tmp/other.toml" {
		t.Errorf("ConfigFile = %q", got)
	}
}

// TestCredentialsLiveOutsideTheConfigDirectory is deliberate: the config is
// meant to be hand-edited and kept in a dotfiles repository, and a credential
// swept along with it would be published by the first person who tried.
func TestCredentialsLiveOutsideTheConfigDirectory(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{"HOME": "/home/ada"}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	creds := paths.CredentialsFile()
	if strings.HasPrefix(creds, paths.Config) {
		t.Errorf("the credential store %q sits inside the config directory %q",
			creds, paths.Config)
	}
	if !strings.HasPrefix(creds, paths.State) {
		t.Errorf("the credential store %q is not under the state directory %q",
			creds, paths.State)
	}
}

// TestTheIdempotencyLedgerLivesUnderState is the same distinction applied to
// the other file that must survive. A cache is disposable by definition, and
// losing this one means a retried create makes a second issue — so it must not
// sit anywhere a "clear the cache" would reach.
func TestTheIdempotencyLedgerLivesUnderState(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{"HOME": "/home/ada"}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	ledger := paths.IdempotencyFile()
	if !strings.HasPrefix(ledger, paths.State) {
		t.Errorf("the ledger %q is not under the state directory %q", ledger, paths.State)
	}
	if strings.HasPrefix(ledger, paths.Cache) {
		t.Errorf("the ledger %q sits inside the cache directory %q", ledger, paths.Cache)
	}
	if strings.HasPrefix(ledger, paths.Config) {
		t.Errorf("the ledger %q sits inside the config directory %q", ledger, paths.Config)
	}
	// And it is not the credential store, which has stricter rules of its own.
	if ledger == paths.CredentialsFile() {
		t.Error("the ledger and the credential store are the same file")
	}
}

func TestSiteCacheIsOnePathElement(t *testing.T) {
	paths, err := jctx.DefaultPaths(env(map[string]string{"HOME": "/home/ada"}))
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	cases := map[string]string{
		"acme.atlassian.invalid":         "acme.atlassian.invalid",
		"https://acme.atlassian.invalid": "acme.atlassian.invalid",
		"http://acme.atlassian.invalid/": "acme.atlassian.invalid",
		"ACME.Atlassian.INVALID":         "acme.atlassian.invalid",
		"jira.acme.invalid:8080":         "jira.acme.invalid_8080",
		"jira.acme.invalid/jira":         "jira.acme.invalid_jira",
		"../../../etc/passwd":            ".._.._.._etc_passwd",
		"":                               "unknown",
		// The escape that worked, and the one this table missed for as long as
		// it existed. `../../../etc/passwd` looks like the dangerous input and
		// is not: `/` maps to `_`, so it flattens to a literal. A bare `..`
		// passes the allowlist unchanged — `.` has to be on it, hostnames need
		// it — and Join then resolves it one level up, where Cache.Clear does
		// RemoveAll. A table of inputs that look hostile is not a table of
		// inputs that are.
		"..":         "unknown",
		"https://..": "unknown",
		".":          "unknown",
		"....":       "....",
	}
	for site, want := range cases {
		got := paths.SiteCache(site)
		if filepath.Base(got) != want {
			t.Errorf("SiteCache(%q) = %q, want the element %q", site, got, want)
		}
		if err := underCacheRoot(paths.Cache, got); err != nil {
			t.Errorf("SiteCache(%q): %v", site, err)
		}
	}
}

// underCacheRoot reports whether a site cache directory is strictly inside the
// cache root.
//
// filepath.Rel rather than a string prefix: `/home/ada/.cache/jr-evil` has
// `/home/ada/.cache/jr` as a prefix and is not inside it, so the cheap spelling
// of this check passes exactly the case worth catching.
func underCacheRoot(root, dir string) error {
	rel, err := filepath.Rel(root, filepath.Clean(dir))
	if err != nil {
		return fmt.Errorf("%q is not relative to %q: %w", dir, root, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q escapes %q (relative path %q)", dir, root, rel)
	}
	return nil
}

// FuzzSiteCacheStaysUnderTheCacheRoot asserts the postcondition rather than the
// allowlist.
//
// The allowlist is a statement about characters; the guarantee is about what
// they compose into, and the two came apart at `..`. A table cannot hold this
// because the cases that break it are the ones nobody thought to write down —
// this one shipped with `../../../etc/passwd` in it and without `..`.
func FuzzSiteCacheStaysUnderTheCacheRoot(f *testing.F) {
	for _, seed := range []string{
		"", "..", ".", "....", "https://..", "../..", "/", "//", "\\",
		"acme.atlassian.invalid", "ACME.INVALID:8080", "..%2f..", "\x00",
		"é.invalid", strings.Repeat(".", 300),
	} {
		f.Add(seed)
	}

	paths, err := jctx.DefaultPaths(env(map[string]string{"HOME": "/home/ada"}))
	if err != nil {
		f.Fatalf("DefaultPaths: %v", err)
	}

	f.Fuzz(func(t *testing.T, site string) {
		dir := paths.SiteCache(site)
		if err := underCacheRoot(paths.Cache, dir); err != nil {
			t.Fatalf("SiteCache(%q): %v", site, err)
		}
		// One element, not a tree: two sites must not be able to nest, and a
		// separator surviving is how one site's --refresh clears another's.
		if rel, _ := filepath.Rel(paths.Cache, dir); strings.ContainsRune(rel, filepath.Separator) {
			t.Fatalf("SiteCache(%q) = %q is more than one element", site, dir)
		}
	})
}

func TestNormalizeSite(t *testing.T) {
	cases := map[string]string{
		"acme.atlassian.invalid":         "https://acme.atlassian.invalid",
		"https://acme.atlassian.invalid": "https://acme.atlassian.invalid",
		// A bare hostname gets https; downgrading to http silently would be a
		// security change nobody asked for.
		"ACME.atlassian.invalid":     "https://ACME.atlassian.invalid",
		"http://localhost:8080":      "http://localhost:8080",
		"acme.atlassian.invalid/":    "https://acme.atlassian.invalid",
		"jira.acme.invalid/jira":     "https://jira.acme.invalid/jira",
		"jira.acme.invalid:8443":     "https://jira.acme.invalid:8443",
		"  acme.atlassian.invalid  ": "https://acme.atlassian.invalid",
	}
	for in, want := range cases {
		got, err := jctx.NormalizeSite(in)
		if err != nil {
			t.Errorf("NormalizeSite(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeSite(%q) = %q, want %q", in, got, want)
		}
	}

	for _, in := range []string{"", "   ", "ftp://acme.atlassian.invalid", "file:///etc/passwd", "-bad"} {
		if got, err := jctx.NormalizeSite(in); err == nil {
			t.Errorf("NormalizeSite(%q) = %q, want an error", in, got)
		} else if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("NormalizeSite(%q) exits %v, want %v", in, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"work", "a", "team-1", "my.context", "a_b", "x1"} {
		if err := jctx.ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", "Work", "-lead", ".hidden", "has space", "a/b", strings.Repeat("x", 65)} {
		if err := jctx.ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", name)
		}
	}
}

func TestValidateNameSuggestsAFix(t *testing.T) {
	err := jctx.ValidateName("My Work Context")
	if err == nil {
		t.Fatal("an invalid name was accepted")
	}
	if !strings.Contains(errs.Coerce(err).Remedy, "my-work-context") {
		t.Errorf("no usable suggestion: %q", errs.Coerce(err).Remedy)
	}
}

func newConfig(t *testing.T) *jctx.Config {
	t.Helper()
	cfg, err := jctx.Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// TestMissingConfigIsNotAnError covers a first run: nothing exists yet, and
// every command that needs a context says so itself with a remedy.
func TestMissingConfigIsNotAnError(t *testing.T) {
	cfg := newConfig(t)
	if len(cfg.Names()) != 0 {
		t.Errorf("a missing config produced contexts: %v", cfg.Names())
	}
	if cfg.Current != "" {
		t.Errorf("Current = %q", cfg.Current)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	cfg, err := jctx.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if err := cfg.Set("work", jctx.Context{
		Site: "acme.atlassian.invalid", Project: "ENG", Board: "42",
		Fields: []string{"summary", "status"},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := cfg.Set("audit", jctx.Context{
		Site: "acme.atlassian.invalid", ReadOnly: true,
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := jctx.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// The first context created becomes current, so a caller who makes one
	// never has to run `context use` to make it take effect.
	if loaded.Current != "work" {
		t.Errorf("Current = %q, want the first context created", loaded.Current)
	}

	work, ok := loaded.Get("work")
	if !ok {
		t.Fatal("work was not saved")
	}
	if work.Site != "https://acme.atlassian.invalid" {
		t.Errorf("the site was not normalized on save: %q", work.Site)
	}
	if work.Project != "ENG" || work.Board != "42" {
		t.Errorf("work = %+v", work)
	}
	if strings.Join(work.Fields, ",") != "summary,status" {
		t.Errorf("fields = %v", work.Fields)
	}
	audit, _ := loaded.Get("audit")
	if !audit.ReadOnly {
		t.Error("readonly was not persisted")
	}
}

// TestConfigNeverHoldsACredential is why Context has a credential *reference*.
func TestConfigNeverHoldsACredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := jctx.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Set("work", jctx.Context{Site: "acme.atlassian.invalid"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	for _, forbidden := range []string{"token", "password", "secret"} {
		lower := strings.ToLower(text)
		// The header mentions credentials on purpose; the fields must not.
		body := lower[strings.Index(lower, "[contexts"):]
		if strings.Contains(body, forbidden) {
			t.Errorf("the config body mentions %q:\n%s", forbidden, text)
		}
	}
}

func TestGetReturnsACopy(t *testing.T) {
	cfg := newConfig(t)
	if err := cfg.Set("work", jctx.Context{
		Site: "acme.atlassian.invalid", Fields: []string{"summary"},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, _ := cfg.Get("work")
	got.Fields[0] = "mutated"
	got.Project = "OTHER"

	again, _ := cfg.Get("work")
	if again.Fields[0] != "summary" || again.Project != "" {
		t.Errorf("a returned context aliases the stored one: %+v", again)
	}
}

func TestUseAndDelete(t *testing.T) {
	cfg := newConfig(t)
	for _, name := range []string{"work", "personal"} {
		if err := cfg.Set(name, jctx.Context{Site: "acme.atlassian.invalid"}); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}

	if err := cfg.Use("personal"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if cfg.Current != "personal" {
		t.Errorf("Current = %q", cfg.Current)
	}
	if err := cfg.Use("nope"); err == nil {
		t.Error("selecting an unknown context succeeded")
	}

	// Deleting the current context with exactly one left selects it, because
	// the choice is unambiguous.
	if err := cfg.Delete("personal"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if cfg.Current != "work" {
		t.Errorf("Current = %q, want the single remaining context", cfg.Current)
	}

	if err := cfg.Delete("work"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if cfg.Current != "" {
		t.Errorf("Current = %q, want empty", cfg.Current)
	}
	if err := cfg.Delete("work"); err == nil {
		t.Error("deleting a missing context succeeded")
	}
}

// TestDeletingCurrentAmongManyLeavesNoneSelected is the other half: with a
// real choice to make, the tool does not make it silently.
func TestDeletingCurrentAmongManyLeavesNoneSelected(t *testing.T) {
	cfg := newConfig(t)
	for _, name := range []string{"a", "b", "c"} {
		if err := cfg.Set(name, jctx.Context{Site: "acme.atlassian.invalid"}); err != nil {
			t.Fatalf("set: %v", err)
		}
	}
	if err := cfg.Use("b"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if err := cfg.Delete("b"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if cfg.Current != "" {
		t.Errorf("Current = %q; with several left, the choice is the caller's", cfg.Current)
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	cases := map[string]string{
		"malformed toml":   "this is not toml [[[",
		"invalid name":     "[contexts.\"Bad Name\"]\nsite = \"acme.atlassian.invalid\"\n",
		"context no site":  "[contexts.work]\nproject = \"ENG\"\n",
		"bad site":         "[contexts.work]\nsite = \"ftp://acme\"\n",
		"dangling current": "current = \"nope\"\n[contexts.work]\nsite = \"acme.atlassian.invalid\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := jctx.Load(path); err == nil {
				t.Fatal("a broken config loaded successfully")
			} else if errs.ExitOf(err) != exitcode.Usage {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
			}
		})
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg, err := jctx.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Set("work", jctx.Context{Site: "acme.atlassian.invalid"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// No temporary file may survive a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.toml" {
			t.Errorf("Save left %q behind", e.Name())
		}
	}
}

func TestCredentialRef(t *testing.T) {
	cases := []struct{ site, credential, want string }{
		{"acme.atlassian.invalid", "", "acme.atlassian.invalid"},
		{"https://acme.atlassian.invalid", "", "acme.atlassian.invalid"},
		{"https://jira.acme.invalid/jira", "", "jira.acme.invalid"},
		{"acme.atlassian.invalid", "shared", "shared"},
	}
	for _, tc := range cases {
		ctx := jctx.Context{Site: tc.site, Credential: tc.credential}
		if got := ctx.CredentialRef(); got != tc.want {
			t.Errorf("CredentialRef(%+v) = %q, want %q", ctx, got, tc.want)
		}
	}
}
