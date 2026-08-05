package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kmoneil/jira-cli/internal/auth"
)

func writeNetrc(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".netrc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestNetrcFormats(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		host     string
		found    bool
		login    string
		password string
	}{
		{
			"one line",
			"machine acme.atlassian.net login ada@example.com password " + theToken + "\n",
			"acme.atlassian.net", true, "ada@example.com", theToken,
		},
		{
			// The format is a token stream, not a line-oriented one: these two
			// spellings are the same file.
			"spread across lines",
			"machine acme.atlassian.net\n  login ada@example.com\n  password " + theToken + "\n",
			"acme.atlassian.net", true, "ada@example.com", theToken,
		},
		{
			"several machines",
			"machine github.com login x password y\n" +
				"machine acme.atlassian.net login ada password " + theToken + "\n",
			"acme.atlassian.net", true, "ada", theToken,
		},
		{
			"the right machine is chosen, not the first",
			"machine acme.atlassian.net login ada password " + theToken + "\n" +
				"machine other.atlassian.net login bob password nope\n",
			"other.atlassian.net", true, "bob", "nope",
		},
		{
			"user is an accepted spelling of login",
			"machine acme.atlassian.net user ada password " + theToken + "\n",
			"acme.atlassian.net", true, "ada", theToken,
		},
		{
			"quoted password with spaces",
			"machine acme.atlassian.net login ada password \"two words\"\n",
			"acme.atlassian.net", true, "ada", "two words",
		},
		{
			"comments are ignored",
			"# a comment\nmachine acme.atlassian.net login ada password " + theToken +
				" # trailing\n",
			"acme.atlassian.net", true, "ada", theToken,
		},
		{
			"default entry matches an unnamed host",
			"machine github.com login x password y\ndefault login ada password " + theToken + "\n",
			"acme.atlassian.net", true, "ada", theToken,
		},
		{
			"an explicit entry beats default",
			"default login fallback password fallbackpw\n" +
				"machine acme.atlassian.net login ada password " + theToken + "\n",
			"acme.atlassian.net", true, "ada", theToken,
		},
		{
			"host case is ignored",
			"machine ACME.Atlassian.NET login ada password " + theToken + "\n",
			"acme.atlassian.net", true, "ada", theToken,
		},
		{
			"no match",
			"machine github.com login x password y\n",
			"acme.atlassian.net", false, "", "",
		},
		{
			"an entry with no password is not a credential",
			"machine acme.atlassian.net login ada\n",
			"acme.atlassian.net", false, "", "",
		},
		{"empty file", "", "acme.atlassian.net", false, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := auth.NetrcProvider{Path: writeNetrc(t, tc.content)}
			cred, ok, err := p.Lookup(tc.host)
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if !ok {
				return
			}
			if cred.User != tc.login {
				t.Errorf("User = %q, want %q", cred.User, tc.login)
			}
			if cred.Secret.Reveal() != tc.password {
				t.Errorf("password did not match")
			}
			if cred.Source == "" {
				t.Error("the credential does not say which file it came from")
			}
		})
	}
}

// TestNetrcMatchesOnHostNotURL covers a site written as a full URL with a path,
// which is common for Data Center.
func TestNetrcMatchesOnHostNotURL(t *testing.T) {
	p := auth.NetrcProvider{Path: writeNetrc(t,
		"machine jira.acme.internal login ada password "+theToken+"\n")}

	for _, site := range []string{
		"jira.acme.internal",
		"https://jira.acme.internal",
		"https://jira.acme.internal/jira",
		"https://jira.acme.internal/jira/",
	} {
		_, ok, err := p.Lookup(site)
		if err != nil {
			t.Fatalf("lookup(%q): %v", site, err)
		}
		if !ok {
			t.Errorf("lookup(%q) found nothing", site)
		}
	}
}

func TestMissingNetrcIsNotAnError(t *testing.T) {
	p := auth.NetrcProvider{Path: filepath.Join(t.TempDir(), "nope")}
	_, ok, err := p.Lookup("acme.atlassian.net")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if ok {
		t.Error("a missing netrc produced a credential")
	}
}

func TestNetrcPathResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".netrc")
	if err := os.WriteFile(path,
		[]byte("machine acme.atlassian.net login ada password "+theToken+"\n"),
		0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// From HOME.
	p := auth.NetrcProvider{Getenv: env(map[string]string{"HOME": dir})}
	if _, ok, err := p.Lookup("acme.atlassian.net"); err != nil || !ok {
		t.Errorf("HOME-based lookup failed: ok=%v err=%v", ok, err)
	}

	// NETRC wins over HOME.
	other := filepath.Join(t.TempDir(), "custom")
	if err := os.WriteFile(other,
		[]byte("machine acme.atlassian.net login bob password other\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p = auth.NetrcProvider{Getenv: env(map[string]string{"HOME": dir, "NETRC": other})}
	cred, ok, err := p.Lookup("acme.atlassian.net")
	if err != nil || !ok {
		t.Fatalf("NETRC lookup failed: ok=%v err=%v", ok, err)
	}
	if cred.User != "bob" {
		t.Errorf("User = %q, want NETRC to win over HOME", cred.User)
	}
}

// FuzzNetrcDoesNotPanic asserts arbitrary file contents cannot crash the
// parser. A .netrc is often hand-edited and shared with other tools, so it is
// exactly the kind of input that arrives malformed.
func FuzzNetrcDoesNotPanic(f *testing.F) {
	for _, seed := range []string{
		"", "machine", "machine acme login", "default",
		"machine acme.atlassian.net login ada password x",
		"machine\nmachine\nmachine", `machine a login "unterminated`,
		"macdef init\nsomething\n\nmachine a login b password c",
		"\x00", "password", "login x",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), ".netrc")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Skip()
		}
		p := auth.NetrcProvider{Path: path}
		cred, ok, err := p.Lookup("acme.atlassian.net")
		if err != nil {
			t.Fatalf("a readable netrc produced an error: %v", err)
		}
		// Anything reported as found must be usable, never a half credential.
		if ok && cred.Secret.IsZero() {
			t.Fatal("a credential was reported found with no secret")
		}
	})
}
