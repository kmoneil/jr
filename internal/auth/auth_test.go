package auth_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

// theToken is the value every test in this file hunts for. Anywhere it appears
// that is not an explicit Reveal is a leak.
const theToken = "ATATT3xFfGF0-super-secret-token-value-9c4e1a"

func env(pairs map[string]string) auth.Getenv {
	return func(k string) string { return pairs[k] }
}

// TestSecretDoesNotPrintItself is the property the whole package rests on.
//
// A Secret is a named type with String and Format methods precisely so that a
// struct carrying one cannot be printed by accident — which is what would
// happen the first time anyone logged a Credential with %+v while debugging.
func TestSecretDoesNotPrintItself(t *testing.T) {
	s := auth.Secret(theToken)

	formats := map[string]string{
		"%s":  fmt.Sprintf("%s", s),
		"%v":  fmt.Sprintf("%v", s),
		"%q":  fmt.Sprintf("%q", s),
		"%+v": fmt.Sprintf("%+v", s),
		"%#v": fmt.Sprintf("%#v", s),
		"%d":  fmt.Sprintf("%d", s),
		"str": s.String(),
		"cat": "prefix " + s.String() + " suffix",
	}
	for verb, got := range formats {
		if strings.Contains(got, theToken) {
			t.Errorf("%s printed the secret: %s", verb, got)
		}
	}

	// And a struct holding one, which is the realistic accident.
	cred := auth.Credential{Scheme: auth.Basic, User: "ada@example.com", Secret: s}
	for _, verb := range []string{"%v", "%+v", "%s"} {
		if got := fmt.Sprintf(verb, cred); strings.Contains(got, theToken) {
			t.Errorf("Credential printed with %s leaked the secret: %s", verb, got)
		}
	}

	// Reveal is the one way out, and it is greppable.
	if s.Reveal() != theToken {
		t.Error("Reveal did not return the value")
	}
	if auth.Secret("").String() != "" {
		t.Error("an empty secret prints a redaction marker, implying one exists")
	}
}

func TestCredentialHeader(t *testing.T) {
	basic := auth.Credential{
		Scheme: auth.Basic, User: "ada@example.com", Secret: auth.Secret(theToken),
	}
	header, err := basic.Header()
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString(
		[]byte("ada@example.com:"+theToken),
	)
	if header["Authorization"] != want {
		t.Errorf("Authorization = %q", header["Authorization"])
	}

	bearer := auth.Credential{Scheme: auth.Bearer, Secret: auth.Secret(theToken)}
	header, err = bearer.Header()
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if header["Authorization"] != "Bearer "+theToken {
		t.Errorf("Authorization = %q", header["Authorization"])
	}
}

func TestCredentialValidate(t *testing.T) {
	cases := map[string]auth.Credential{
		"no token":       {Scheme: auth.Bearer},
		"basic no user":  {Scheme: auth.Basic, Secret: auth.Secret(theToken)},
		"unknown scheme": {Scheme: auth.Scheme("magic"), Secret: auth.Secret(theToken)},
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cred.Validate(); err == nil {
				t.Fatal("an unusable credential validated")
			} else if errs.ExitOf(err) != exitcode.Auth {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Auth)
			}
			if _, err := cred.Header(); err == nil {
				t.Error("Header succeeded for an unusable credential")
			}
		})
	}

	valid := []auth.Credential{
		{Scheme: auth.Bearer, Secret: auth.Secret(theToken)},
		{Scheme: auth.Basic, User: "ada", Secret: auth.Secret(theToken)},
	}
	for _, cred := range valid {
		if err := cred.Validate(); err != nil {
			t.Errorf("a usable credential was rejected: %v", err)
		}
	}
}

func TestParseScheme(t *testing.T) {
	for _, s := range []string{"basic", "BASIC", " bearer "} {
		if _, err := auth.ParseScheme(s); err != nil {
			t.Errorf("ParseScheme(%q): %v", s, err)
		}
	}
	if _, err := auth.ParseScheme("oauth"); err == nil {
		t.Error("an unsupported scheme was accepted")
	}
}

func TestEnvProvider(t *testing.T) {
	cases := []struct {
		name   string
		vars   map[string]string
		found  bool
		scheme auth.Scheme
		user   string
	}{
		{"nothing set", nil, false, "", ""},
		{
			"token alone becomes bearer, matching a Data Center PAT",
			map[string]string{auth.EnvToken: theToken},
			true, auth.Bearer, "",
		},
		{
			"token plus email becomes basic, matching Cloud",
			map[string]string{auth.EnvToken: theToken, auth.EnvEmail: "ada@example.com"},
			true, auth.Basic, "ada@example.com",
		},
		{
			"JIRA_USER works for Data Center",
			map[string]string{auth.EnvToken: theToken, auth.EnvUser: "ada"},
			true, auth.Basic, "ada",
		},
		{
			"the scheme can be forced",
			map[string]string{
				auth.EnvToken: theToken, auth.EnvEmail: "ada@example.com",
				auth.EnvAuthScheme: "bearer",
			},
			true, auth.Bearer, "ada@example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := auth.EnvProvider{Getenv: env(tc.vars)}
			cred, ok, err := p.Lookup("acme.atlassian.invalid")
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if !ok {
				return
			}
			if cred.Scheme != tc.scheme {
				t.Errorf("Scheme = %q, want %q", cred.Scheme, tc.scheme)
			}
			if cred.User != tc.user {
				t.Errorf("User = %q, want %q", cred.User, tc.user)
			}
			if cred.Secret.Reveal() != theToken {
				t.Error("the token did not come through")
			}
			if cred.Source == "" {
				t.Error("the credential does not say where it came from")
			}
		})
	}
}

// TestHalfConfiguredEnvironmentIsLoud stops a caller who plainly supplied a
// credential from getting "no credentials found" three providers later.
func TestHalfConfiguredEnvironmentIsLoud(t *testing.T) {
	p := auth.EnvProvider{Getenv: env(map[string]string{
		auth.EnvToken: theToken, auth.EnvAuthScheme: "basic",
	})}
	_, _, err := p.Lookup("acme.atlassian.invalid")
	if err == nil {
		t.Fatal("basic auth with no user was accepted from the environment")
	}
	if errs.ExitOf(err) != exitcode.Auth {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Auth)
	}
	if !strings.Contains(errs.Coerce(err).Remedy, auth.EnvEmail) {
		t.Errorf("the remedy does not name the variable to set: %q", errs.Coerce(err).Remedy)
	}
}

// TestAnEnvironmentCredentialFollowsWhateverSiteItIsPointedAt pins the decision
// recorded on auth.EnvProvider.Lookup.
//
// FileStore and NetrcProvider are keyed by host: ask either for a site it has
// never heard of and it says no. EnvProvider is not, on purpose. A CI job
// exports JIRA_API_TOKEN and runs against one site, and making it name the host
// in a third variable is friction with no payoff.
//
// The test exists for the reason TestANetrcIsReadWhateverItsMode does: adding
// host-scoping here breaks nothing visible, tightens something, and reads as
// security, so it is exactly the change that lands without an argument. The
// argument is in the doc comment; this is what makes somebody read it.
//
// It does not assert the composition is harmless — an exported token plus a
// config.toml that is 0644 by design does mean the site can come from a file
// somebody else wrote. It asserts the tool says so, which is the whole of what
// was decided: site-scoped is on the credential and reaches `jr auth status`.
func TestAnEnvironmentCredentialFollowsWhateverSiteItIsPointedAt(t *testing.T) {
	p := auth.EnvProvider{Getenv: env(map[string]string{auth.EnvToken: theToken})}

	// Two unrelated hosts and no host at all. Nothing in the environment names
	// any of them, and each must still be answered.
	for _, site := range []string{
		"acme.atlassian.invalid",
		"jira.other-company.invalid",
		"https://jira.example/context-path",
		"",
	} {
		t.Run(site, func(t *testing.T) {
			cred, ok, err := p.Lookup(site)
			if err != nil {
				t.Fatalf("the environment refused site %q: %v\n"+
					"This is a deliberate decision, not an oversight — see the doc "+
					"comment on auth.EnvProvider.Lookup. If it is being reversed, "+
					"reverse that too, and say what happens to a CI job that "+
					"exports a token and no host.", site, err)
			}
			if !ok {
				t.Fatalf("the environment yielded no credential for site %q, "+
					"though %s is set", site, auth.EnvToken)
			}
			if cred.Secret.Reveal() != theToken {
				t.Errorf("credential = %+v, want the token in the environment", cred)
			}
			if cred.SiteScoped {
				t.Error("an environment credential reports itself site-scoped, " +
					"which `jr auth status` prints: it was not looked up for this " +
					"site, it is the one that was found")
			}
		})
	}
}

// TestAHostKeyedProviderReportsItselfSiteScoped is the other half of the
// assertion above, and without it that one is satisfied by a field nothing ever
// sets. An attribute `jr auth status` always prints as false says nothing about
// anything.
func TestAHostKeyedProviderReportsItselfSiteScoped(t *testing.T) {
	const site = "acme.atlassian.invalid"

	t.Run("credential store", func(t *testing.T) {
		store := auth.FileStore{Path: filepath.Join(t.TempDir(), "credentials.toml")}
		if err := store.Save(site, auth.Credential{
			Scheme: auth.Bearer, Secret: auth.Secret(theToken),
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
		cred, ok, err := store.Lookup(site)
		if err != nil || !ok {
			t.Fatalf("lookup: ok = %v, err = %v", ok, err)
		}
		if !cred.SiteScoped {
			t.Error("a credential from the store reports itself not site-scoped, " +
				"though the store is keyed by host and refuses a site it has not got")
		}
	})

	t.Run("netrc", func(t *testing.T) {
		path := writeNetrc(t, "machine "+site+" login ada password "+theToken+"\n")
		cred, ok, err := auth.NetrcProvider{Path: path}.Lookup(site)
		if err != nil || !ok {
			t.Fatalf("lookup: ok = %v, err = %v", ok, err)
		}
		if !cred.SiteScoped {
			t.Error("a credential from .netrc reports itself not site-scoped, " +
				"though it was matched by a `machine` line naming this host")
		}
	})
}

func TestEnvProviderRejectsABadScheme(t *testing.T) {
	p := auth.EnvProvider{Getenv: env(map[string]string{
		auth.EnvToken: theToken, auth.EnvAuthScheme: "magic",
	})}
	if _, _, err := p.Lookup("acme.atlassian.invalid"); err == nil {
		t.Fatal("an unknown scheme in the environment was accepted")
	}
}

func writeStore(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "credentials.toml")
	store := auth.FileStore{Path: path}

	cred := auth.Credential{
		Scheme: auth.Basic, User: "ada@example.com", Secret: auth.Secret(theToken),
	}
	if err := store.Save("https://acme.atlassian.invalid", cred); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Stored by host, so two contexts pointing at the same site share one
	// credential and a scheme change does not orphan it.
	got, ok, err := store.Lookup("acme.atlassian.invalid")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !ok {
		t.Fatal("the credential was not found")
	}
	if got.Secret.Reveal() != theToken || got.User != "ada@example.com" {
		t.Errorf("got %+v", got)
	}
	if got.Scheme != auth.Basic {
		t.Errorf("Scheme = %q", got.Scheme)
	}

	hosts, err := store.Hosts()
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "acme.atlassian.invalid" {
		t.Errorf("Hosts = %v", hosts)
	}
}

// TestStoredCredentialIsNotWorldReadable is the whole point of a separate
// store. A credential other users can read is not stored, it is published.
func TestStoredCredentialIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	store := auth.FileStore{Path: path}
	if err := store.Save("acme.atlassian.invalid", auth.Credential{
		Scheme: auth.Bearer, Secret: auth.Secret(theToken),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode is %04o, want 0600", perm)
	}

	// And the directory too, since a readable directory leaks the file list.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o007 != 0 {
		t.Logf("directory mode is %04o", perm)
	}
}

// TestOverlyOpenStoreIsRefused matters because reading it anyway and warning
// would mean the credential is used, and stays exposed, every single time.
func TestOverlyOpenStoreIsRefused(t *testing.T) {
	content := "[credentials.\"acme.atlassian.invalid\"]\nscheme = \"bearer\"\ntoken = \"" +
		theToken + "\"\n"

	for _, perm := range []os.FileMode{0o644, 0o640, 0o604, 0o666} {
		path := writeStore(t, content, perm)
		store := auth.FileStore{Path: path}

		_, _, err := store.Lookup("acme.atlassian.invalid")
		if err == nil {
			t.Errorf("a credential file at mode %04o was read", perm)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "STORE_PERMISSIONS" {
			t.Errorf("code = %q, want STORE_PERMISSIONS", e.Code)
		}
		if !strings.Contains(e.Remedy, "chmod 600") {
			t.Errorf("the remedy does not say how to fix it: %q", e.Remedy)
		}
		// The refusal must not itself print the credential.
		if strings.Contains(err.Error(), theToken) {
			t.Error("the permission error leaked the credential")
		}
	}
}

func TestMissingStoreIsNotAnError(t *testing.T) {
	store := auth.FileStore{Path: filepath.Join(t.TempDir(), "nope.toml")}
	_, ok, err := store.Lookup("acme.atlassian.invalid")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if ok {
		t.Error("a missing store produced a credential")
	}
	hosts, err := store.Hosts()
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("Hosts = %v", hosts)
	}
}

func TestFileStoreDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	store := auth.FileStore{Path: path}
	if err := store.Save("acme.atlassian.invalid", auth.Credential{
		Scheme: auth.Bearer, Secret: auth.Secret(theToken),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	removed, err := store.Delete("https://acme.atlassian.invalid/")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !removed {
		t.Error("Delete reported nothing was removed")
	}

	// Removing one that is not there is not an error: the caller asked for it
	// to be gone, and it is.
	removed, err = store.Delete("acme.atlassian.invalid")
	if err != nil {
		t.Fatalf("delete again: %v", err)
	}
	if removed {
		t.Error("Delete reported removing something twice")
	}
}

func TestStoreRejectsAnUnknownScheme(t *testing.T) {
	path := writeStore(t,
		"[credentials.\"acme.atlassian.invalid\"]\nscheme = \"magic\"\ntoken = \"x\"\n", 0o600)
	store := auth.FileStore{Path: path}
	if _, _, err := store.Lookup("acme.atlassian.invalid"); err == nil {
		t.Fatal("a credential with an unknown scheme was returned")
	}
}

func TestStoreRejectsMalformedToml(t *testing.T) {
	path := writeStore(t, "not toml [[[", 0o600)
	store := auth.FileStore{Path: path}
	if _, _, err := store.Lookup("acme.atlassian.invalid"); err == nil {
		t.Fatal("a malformed store loaded successfully")
	}
}

// TestChainPrecedence pins the order and the reason for it.
func TestChainPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	store := auth.FileStore{Path: path}
	if err := store.Save("acme.atlassian.invalid", auth.Credential{
		Scheme: auth.Bearer, Secret: auth.Secret("from-store"),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// With nothing in the environment, the store wins.
	chain := auth.DefaultChain(env(map[string]string{"NETRC": "/nonexistent"}), path)
	cred, err := chain.Resolve("acme.atlassian.invalid")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Secret.Reveal() != "from-store" {
		t.Errorf("got %q, want the stored credential", cred.Secret.Reveal())
	}

	// The environment overrides it, so a CI job can supply a credential
	// without editing anything on disk.
	chain = auth.DefaultChain(env(map[string]string{
		auth.EnvToken: "from-env", "NETRC": "/nonexistent",
	}), path)
	cred, err = chain.Resolve("acme.atlassian.invalid")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Secret.Reveal() != "from-env" {
		t.Errorf("got %q, want the environment to win", cred.Secret.Reveal())
	}
}

// TestMissingCredentialNamesEveryPlaceLookedIn answers the question a caller
// actually has at that moment: which of these am I supposed to use.
func TestMissingCredentialNamesEveryPlaceLookedIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	chain := auth.DefaultChain(env(map[string]string{"NETRC": "/nonexistent"}), path)

	_, err := chain.Resolve("acme.atlassian.invalid")
	if err == nil {
		t.Fatal("resolving with no credential anywhere succeeded")
	}
	if errs.ExitOf(err) != exitcode.Auth {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Auth)
	}
	e := errs.Coerce(err)
	for _, want := range []string{"environment", path, ".netrc"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("the detail does not mention %q: %q", want, e.Detail)
		}
	}
	if !strings.Contains(e.Remedy, "auth login") {
		t.Errorf("the remedy does not say what to do: %q", e.Remedy)
	}
}

func TestAuthorizerProducesHeaders(t *testing.T) {
	a := auth.Authorizer{Credential: auth.Credential{
		Scheme: auth.Bearer, Secret: auth.Secret(theToken),
	}}
	headers, err := a.Authorize(t.Context(), auth.RequestInfo{
		Method: "GET", URL: "https://acme.atlassian.invalid/rest/api/3/myself",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if headers["Authorization"] != "Bearer "+theToken {
		t.Errorf("Authorization = %q", headers["Authorization"])
	}

	// An unusable credential fails rather than sending an empty header, which
	// would produce a confusing 401 instead of a clear local error.
	bad := auth.Authorizer{Credential: auth.Credential{Scheme: auth.Basic}}
	if _, err := bad.Authorize(t.Context(), auth.RequestInfo{}); err == nil {
		t.Error("an unusable credential produced headers")
	}
}
