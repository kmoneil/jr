package transport_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/transport"
)

// fuzzBase is the shape the guarantee is hardest to hold under: a site served
// from a context path, which is what a Data Center instance usually is.
const fuzzBase = "https://jira.acme.invalid/jira"

// FuzzRelativeStaysOnTheSite is the assertion the function exists for, made
// against every spelling rather than the handful a table can list.
//
// Relative takes a URL the *server* chose and returns a path this client will
// send. Its output reaches resolve, which joins it onto the site's base — so
// whatever it accepts has to land on the configured origin and inside the
// configured context path. Anything else is a server deciding where this tool's
// credential goes.
//
// The seeds are the escapes this function has actually had. Each was accepted
// once: `//evil.invalid/steal` because url.URL.IsAbs is Scheme != "" and a
// protocol-relative URL has no scheme, `/jiraxyz/a` because containment was a
// string prefix test, and `/jira/../../secure/x` because JoinPath cleans the
// path after the containment check has already looked at it.
func FuzzRelativeStaysOnTheSite(f *testing.F) {
	for _, seed := range []string{
		"https://jira.acme.invalid/jira/secure/attachment/1/a.txt",
		"/jira/secure/attachment/1/a.txt",
		"secure/attachment/1/a.txt",
		"https://jira.acme.invalid/jira/a?v=2#frag",
		// The bugs, in the shapes they took.
		"//evil.invalid/steal",
		"//jira.acme.invalid@evil.invalid/a",
		"/jiraxyz/a",
		"/jira/../../secure/x",
		"/jira/..%2f..%2fsecure/x",
		"https://jira.acme.invalid/jira/%2e%2e/%2e%2e/x",
		// Found by this fuzzer, each on its first run against the fix before it.
		// `/jira//x` trimmed to `//x`, which re-parsed names the host x. `"%3A`
		// came back as `%22:`, which resolve then could not parse at all — a
		// colon in the first segment of a relative reference reads as a scheme.
		"/jira//x",
		"\"%3A",
		"a:b",
		"/jira/a/b:c",
		// Neighbours of the boundary.
		"", "  ", "/", "//", "///a", "/jira", "/jira/", "https://jira.acme.invalid",
		"HTTPS://JIRA.ACME.INVALID/jira/a", "https://jira.acme.invalid:443/jira/a",
		"http://jira.acme.invalid/jira/a", "mailto:someone@acme.invalid",
		"/jira/a\\b", "/jira/a%00b", "/jira/ /x", "/jira//x",
	} {
		f.Add(seed)
	}

	client, err := transport.New(transport.Options{BaseURL: fuzzBase})
	if err != nil {
		f.Fatalf("new client: %v", err)
	}
	base, err := url.Parse(fuzzBase)
	if err != nil {
		f.Fatalf("parse base: %v", err)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := client.Relative(raw)
		if err != nil {
			// A refused URL is the safe outcome and nothing downstream sees it.
			return
		}

		ref, err := url.Parse(got)
		if err != nil {
			t.Fatalf("Relative(%q) = %q, which does not parse: %v", raw, got, err)
		}
		// resolve refuses a path naming anywhere, so accepting one here would
		// turn a server's URL into a failed request at best.
		if ref.Scheme != "" || ref.Host != "" {
			t.Fatalf("Relative(%q) = %q, which names %q://%q",
				raw, got, ref.Scheme, ref.Host)
		}

		// The same join resolve makes, and the same cleaning: what matters is
		// where the request lands, not what the string looked like first.
		target := base.JoinPath(ref.Path)
		if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
			t.Fatalf("Relative(%q) = %q, which resolves to %s", raw, got, target)
		}
		if p := target.EscapedPath(); p != base.EscapedPath() &&
			!strings.HasPrefix(p, base.EscapedPath()+"/") {
			t.Fatalf("Relative(%q) = %q, which resolves to %s, outside %s",
				raw, got, target, base.EscapedPath())
		}
	})
}
