package lint_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// hostPattern finds anything shaped like a hostname.
var hostPattern = regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9-]*(?:\.[a-z0-9][a-z0-9-]*)+)\b`)

// realTLDs are the public suffixes a hostname in a test could actually resolve
// under.
//
// The check is framed this way round on purpose. Flagging anything that is not
// a reserved TLD catches every dotted identifier in the suite — output kinds
// like schema.command, TOML keys like contexts.work — and a guard that cries
// wolf gets deleted. Only a real suffix can resolve, so only a real suffix is
// worth objecting to.
var realTLDs = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "dev": true, "co": true,
	"app": true, "cloud": true, "ai": true, "me": true, "tv": true, "info": true,
	"biz": true, "edu": true, "gov": true, "mil": true, "int": true,
	"uk": true, "de": true, "fr": true, "jp": true, "cn": true, "ru": true,
	"us": true, "ca": true, "au": true, "nz": true, "in": true, "br": true,
	"it": true, "es": true, "nl": true, "se": true, "no": true, "fi": true,
	"ch": true, "at": true, "be": true, "pl": true, "cz": true, "dk": true,
	"ie": true, "pt": true, "gr": true, "tr": true, "za": true, "mx": true,
	"sg": true, "hk": true, "kr": true, "tw": true, "id": true, "xyz": true,
}

// allowedHosts are the real names a test may contain: module paths, the
// loopback a test server binds to, and names that appear in prose or as an
// email domain and are never dialled.
var allowedHosts = map[string]bool{
	"github.com": true, "gopkg.in": true, "mvdan.cc": true, "golang.org": true,
	"127.0.0.1": true, "0.0.0.0": true,
	"example.com": true, "atlassian.com": true,
}

// commandNames are dotted strings that are this tool's own command names and
// happen to end in something that is also a real top-level domain.
//
// They are kept apart from allowedHosts deliberately. That list is real names a
// test may contain; this one is names that are not hosts at all, and conflating
// the two would let a genuine host in under cover of looking like a command.
// "me" is a real TLD, so `user.me` trips the check for a reason that has
// nothing to do with what the guard exists to catch.
var commandNames = map[string]bool{
	"user.me": true,
}

// fileSuffixes make a dotted string a filename rather than a host.
var fileSuffixes = []string{
	".go", ".json", ".toml", ".txt", ".tsv", ".xml", ".yaml", ".yml",
	".golden", ".stderr", ".jsonl", ".md",
}

// TestReservedTLDsAreNotFlagged pins the intent: a reserved suffix is exactly
// what a test host should use, and must never trip the guard.
func TestReservedTLDsAreNotFlagged(t *testing.T) {
	for _, safe := range []string{
		"acme.atlassian.invalid", "https://jira.corp.invalid/jira",
		"foo.test", "bar.example", "schema.command", "contexts.work",
		"0.0.0-test", "127.0.0.1:8080", "fake.list",
	} {
		if host, bad := suspiciousHost(safe); bad {
			t.Errorf("suspiciousHost(%q) flagged %q, which cannot resolve", safe, host)
		}
	}
}

// TestRealHostsAreFlagged is the other half: the exact strings that caused the
// leak must be caught.
func TestRealHostsAreFlagged(t *testing.T) {
	for _, unsafe := range []string{
		"acme.atlassian.net",
		"https://jira.corp.com/jira",
		"jira.internal.example.com",
		"evil.example.org",
	} {
		if _, bad := suspiciousHost(unsafe); !bad {
			t.Errorf("suspiciousHost(%q) did not flag a resolvable host", unsafe)
		}
	}
}

// TestTestsCannotReachRealHosts is a guard learned the hard way.
//
// When `auth login` began verifying credentials, this suite started making real
// requests to a plausible-looking test domain that turned out to exist —
// sending test tokens to somebody else's server. Nothing in the tests had
// changed: a behaviour change several packages away turned inert strings into
// network calls.
//
// So every host named in a test must sit under a reserved TLD. A test naming a
// resolvable host is one behaviour change away from dialling it.
func TestTestsCannotReachRealHosts(t *testing.T) {
	const root = ".."
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		// This file names real hosts on purpose, as the data proving the guard
		// catches them. They are compared against, never dialled.
		if filepath.Base(path) == "hosts_test.go" {
			return nil
		}

		// Parsing rather than scanning lines, so only real string literals are
		// examined and a selector such as tc.name is never read as a host.
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value := lit.Value
			if unquoted, unqErr := strconv.Unquote(value); unqErr == nil {
				value = unquoted
			}
			if host, bad := suspiciousHost(value); bad {
				t.Errorf("%s:%d names host %q.\n"+
					"Use a reserved TLD (.invalid, .test, .example) so a test "+
					"cannot reach somebody else's server.",
					rel, fset.Position(lit.Pos()).Line, host)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// suspiciousHost reports a hostname in s that could resolve.
func suspiciousHost(s string) (string, bool) {
	// An import path is not a host to dial.
	if strings.Contains(s, "/") && !strings.Contains(s, "://") &&
		!strings.HasPrefix(s, "/") {
		return "", false
	}
	for _, suffix := range fileSuffixes {
		if strings.HasSuffix(s, suffix) {
			return "", false
		}
	}

	for _, m := range hostPattern.FindAllStringSubmatch(s, -1) {
		host := strings.ToLower(m[1])
		if commandNames[host] {
			return "", false
		}
		if allowedHosts[host] {
			continue
		}
		if last := host[strings.LastIndex(host, ".")+1:]; !realTLDs[last] {
			continue
		}
		return host, true
	}
	return "", false
}
