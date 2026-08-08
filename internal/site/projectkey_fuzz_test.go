package site_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/site"
)

// FuzzAValidProjectKeyIsASafePathSegment is the property the four `project`
// commands and issue.ParseKey both rest on.
//
// A key this accepts goes into a URL path. Every caller in the tree also runs
// it through url.PathEscape and would be fine whatever it contained, but that
// is a property resting on each future author remembering — and the one that
// concatenated instead turned `epic add "../../../rest/api/2/issue/ENG-1"` into
// a POST to a different endpoint with the caller's credential attached. So the
// grammar guarantees it and escaping stays the second layer.
//
// The seeds carry the shapes that motivated the check, so the regression is
// readable here rather than only in a corpus file.
func FuzzAValidProjectKeyIsASafePathSegment(f *testing.F) {
	for _, seed := range []string{
		"ENG", "eng", "A1_B", "X",
		// What `project get` used to accept and send.
		"../etc", "..", ".", "1foo", "a/b", "%2e%2e", "ENG/x",
		"a b", "a\n", "", "-", "ENG-1", "\x00", "É", "ＥＮＧ", "٢",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		if !site.ValidProjectKey(in) {
			// A rejected key must stay rejected: nothing downstream sees it.
			return
		}
		if in == "" {
			t.Fatal("the empty string was accepted as a project key")
		}
		if escaped := url.PathEscape(in); escaped != in {
			t.Fatalf("ValidProjectKey(%q) accepted a value that is not safe in "+
				"a URL path unescaped; PathEscape gives %q", in, escaped)
		}
		// A dot segment survives PathEscape untouched, so the check above does
		// not cover the case this exists for. `..` is one path element and is
		// also a way out of the one it was meant to be.
		if strings.Contains(in, ".") {
			t.Fatalf("ValidProjectKey(%q) accepted a dot, which is a path "+
				"segment PathEscape carries through unchanged", in)
		}
	})
}
