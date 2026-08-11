package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kmoneil/jr/internal/auth"
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
			"machine acme.atlassian.invalid login ada@example.com password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada@example.com", theToken,
		},
		{
			// The format is a token stream, not a line-oriented one: these two
			// spellings are the same file.
			"spread across lines",
			"machine acme.atlassian.invalid\n  login ada@example.com\n  password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada@example.com", theToken,
		},
		{
			"several machines",
			"machine github.com login x password y\n" +
				"machine acme.atlassian.invalid login ada password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada", theToken,
		},
		{
			"the right machine is chosen, not the first",
			"machine acme.atlassian.invalid login ada password " + theToken + "\n" +
				"machine other.atlassian.invalid login bob password nope\n",
			"other.atlassian.invalid", true, "bob", "nope",
		},
		{
			"user is an accepted spelling of login",
			"machine acme.atlassian.invalid user ada password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada", theToken,
		},
		{
			"quoted password with spaces",
			"machine acme.atlassian.invalid login ada password \"two words\"\n",
			"acme.atlassian.invalid", true, "ada", "two words",
		},
		{
			"comments are ignored",
			"# a comment\nmachine acme.atlassian.invalid login ada password " + theToken +
				" # trailing\n",
			"acme.atlassian.invalid", true, "ada", theToken,
		},
		{
			"default entry matches an unnamed host",
			"machine github.com login x password y\ndefault login ada password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada", theToken,
		},
		{
			"an explicit entry beats default",
			"default login fallback password fallbackpw\n" +
				"machine acme.atlassian.invalid login ada password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada", theToken,
		},
		{
			"host case is ignored",
			"machine ACME.Atlassian.INVALID login ada password " + theToken + "\n",
			"acme.atlassian.invalid", true, "ada", theToken,
		},
		{
			"no match",
			"machine github.com login x password y\n",
			"acme.atlassian.invalid", false, "", "",
		},
		{
			"an entry with no password is not a credential",
			"machine acme.atlassian.invalid login ada\n",
			"acme.atlassian.invalid", false, "", "",
		},
		{"empty file", "", "acme.atlassian.invalid", false, "", ""},
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
		"machine jira.acme.invalid login ada password "+theToken+"\n")}

	for _, site := range []string{
		"jira.acme.invalid",
		"https://jira.acme.invalid",
		"https://jira.acme.invalid/jira",
		"https://jira.acme.invalid/jira/",
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
	_, ok, err := p.Lookup("acme.atlassian.invalid")
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
		[]byte("machine acme.atlassian.invalid login ada password "+theToken+"\n"),
		0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// From HOME.
	p := auth.NetrcProvider{Getenv: env(map[string]string{"HOME": dir})}
	if _, ok, err := p.Lookup("acme.atlassian.invalid"); err != nil || !ok {
		t.Errorf("HOME-based lookup failed: ok=%v err=%v", ok, err)
	}

	// NETRC wins over HOME.
	other := filepath.Join(t.TempDir(), "custom")
	if err := os.WriteFile(other,
		[]byte("machine acme.atlassian.invalid login bob password other\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p = auth.NetrcProvider{Getenv: env(map[string]string{"HOME": dir, "NETRC": other})}
	cred, ok, err := p.Lookup("acme.atlassian.invalid")
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
		"machine acme.atlassian.invalid login ada password x",
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
		cred, ok, err := p.Lookup("acme.atlassian.invalid")
		if err != nil {
			t.Fatalf("a readable netrc produced an error: %v", err)
		}
		// Anything reported as found must be usable, never a half credential.
		if ok && cred.Secret.IsZero() {
			t.Fatal("a credential was reported found with no secret")
		}
	})
}

// TestANetrcIsReadWhateverItsMode pins the decision recorded on
// auth.NetrcProvider and in docs/architecture.md.
//
// FileStore.load refuses a credential store any other user can read. This does
// the opposite with the same kind of secret, on purpose: the store is a file
// jr creates, writes at 0600, and owns, and .netrc is none of those — it is
// shared with curl and git, it predates jr on most machines that have one, and
// curl reads a 0644 file without complaint. Refusing would make jr the one
// tool that broke over a mode it did not set.
//
// The test exists because that asymmetry is exactly what a later reader would
// "fix". Without it, adding a mode check here breaks nothing and looks like
// tightening security. 0640 is in the table as well as 0644, because a
// group-readable file is the case somebody reaches for when they want a
// middle ground, and the answer is the same.
func TestANetrcIsReadWhateverItsMode(t *testing.T) {
	const content = "machine jira.acme.invalid login ada password hunter2\n"

	for _, mode := range []os.FileMode{0o600, 0o640, 0o644, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			path := writeNetrc(t, content)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			cred, ok, err := auth.NetrcProvider{Path: path}.Lookup("jira.acme.invalid")
			if err != nil {
				t.Fatalf("a .netrc at %04o was refused: %v\n"+
					"This is a deliberate decision, not an oversight — see the doc "+
					"comment on auth.NetrcProvider and the permissions section of "+
					"docs/architecture.md. If it is being reversed, reverse those too.",
					mode.Perm(), err)
			}
			if !ok {
				t.Fatalf("a .netrc at %04o yielded no credential", mode.Perm())
			}
			if cred.User != "ada" || cred.Secret.Reveal() != "hunter2" {
				t.Errorf("credential = %+v, want the one in the file", cred)
			}
		})
	}
}
