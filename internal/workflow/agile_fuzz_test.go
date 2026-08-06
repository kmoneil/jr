//go:build write

package workflow

// In-package, because parseIssueKeys and epicRef are not exported and exporting
// them to test them would widen the surface for the benefit of a test.

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// FuzzParseIssueKeysBoundsAndCanonicalizes covers the two promises these verbs
// make about their arguments before anything is sent: never more than the API
// moves at once, and never a key in the form the caller happened to type.
func FuzzParseIssueKeysBoundsAndCanonicalizes(f *testing.F) {
	// A fuzzer takes scalars, so the argument list arrives as one string and is
	// split on spaces. That is enough to explore both the count and the shapes.
	for _, seed := range []string{
		"ENG-1",
		"ENG-1 ENG-2",
		"eng-1 eng-2",
		"",
		" ",
		"../../admin-1",
		"ENG-1 a/b-2",
		"ENG-1 ENG-1",
		strings.TrimSpace(strings.Repeat("ENG-1 ", MaxIssuesPerRequest)),
		strings.TrimSpace(strings.Repeat("ENG-1 ", MaxIssuesPerRequest+1)),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, " ")
		keys, err := parseIssueKeys(args)
		if err != nil {
			if keys != nil {
				t.Fatalf("parseIssueKeys(%q) failed and still returned %v", joined, keys)
			}
			return
		}

		if len(keys) == 0 {
			t.Fatalf("parseIssueKeys(%q) succeeded with no keys", joined)
		}
		if len(keys) > MaxIssuesPerRequest {
			t.Fatalf("parseIssueKeys returned %d keys, over the API's cap of %d",
				len(keys), MaxIssuesPerRequest)
		}
		// One key out per argument in. Dropping a key silently would move fewer
		// issues than the caller asked for and report success.
		if len(keys) != len(args) {
			t.Fatalf("parseIssueKeys(%q) returned %d keys for %d arguments",
				joined, len(keys), len(args))
		}

		for _, key := range keys {
			if key != url.PathEscape(key) || strings.Contains(key, ".") {
				t.Fatalf("parseIssueKeys(%q) returned %q, which is not safe in a "+
					"URL path unescaped", joined, key)
			}
			if key != strings.ToUpper(key) {
				t.Fatalf("parseIssueKeys(%q) returned %q uncanonicalized", joined, key)
			}
		}

		// The keys go into a JSON body. Encoding must not be where a surprise
		// shows up, so the round trip is checked here rather than at the wire.
		body, err := json.Marshal(map[string]any{"issues": keys})
		if err != nil {
			t.Fatalf("keys from %q cannot be encoded: %v", joined, err)
		}
		var back struct {
			Issues []string `json:"issues"`
		}
		if err := json.Unmarshal(body, &back); err != nil {
			t.Fatalf("the body built from %q does not decode: %v", joined, err)
		}
		if strings.Join(back.Issues, ",") != strings.Join(keys, ",") {
			t.Fatalf("the body built from %q decodes to %v, not %v",
				joined, back.Issues, keys)
		}
	})
}

// FuzzEpicRefIsSafeInAPath is the direct regression for the bug this package
// shipped: epicRef's result was concatenated into a URL path, and the validator
// it replaced accepted anything issue.ParseKey did — which at the time included
// "../../admin-1".
func FuzzEpicRefIsSafeInAPath(f *testing.F) {
	for _, seed := range []string{
		"ENG-42", "10101", "eng-42",
		"../../admin-1", "a/b-1", "%2e%2e-1", "-42", "+42",
		"", "-", "ENG", "ENG-", "ENG-abc", "10101/../../rest/api/2/myself",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		ref, err := epicRef(in)
		if err != nil {
			if ref != "" {
				t.Fatalf("epicRef(%q) failed and still returned %q", in, ref)
			}
			return
		}
		if ref == "" || ref != url.PathEscape(ref) || strings.Contains(ref, ".") {
			t.Fatalf("epicRef(%q) = %q, which is not safe in a URL path unescaped",
				in, ref)
		}
		// validEpicRef is what Command.Validate calls, so the two must agree —
		// otherwise a command validates and then fails in its body, after a
		// streaming header is already out.
		if err := validEpicRef(in); err != nil {
			t.Fatalf("epicRef accepted %q and validEpicRef refused it: %v", in, err)
		}
	})
}
