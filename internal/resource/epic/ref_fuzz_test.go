package epic_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/resource/epic"
)

// FuzzValidateRefProducesASafePathSegment holds this resource to the same
// promise the issue key parser makes.
//
// An epic reference goes straight into a URL path. `epic get` escapes it, so
// nothing here is currently exploitable — but the sibling case in
// internal/workflow was not escaped, and the difference between the two was
// which author remembered. What a validator accepts should be safe on its own,
// so the escaping is a second layer rather than the only one.
func FuzzValidateRefProducesASafePathSegment(f *testing.F) {
	for _, seed := range []string{
		"ENG-42", "10101", "A1_B-7", "eng-42",
		// The shapes that motivated the check.
		"../..-1", "a/b-1", "../../admin-1", "%2e%2e-1", ".-1",
		// Neighbours of the boundary.
		"", "-", "ENG", "ENG-", "-42", "ENG-abc", "ENG-4-2", "1ENG-1",
		"10101/", "+42", " 42", "42 ", "\x00-1", "É-1",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		if err := epic.ValidateRef(in); err != nil {
			return
		}
		if in == "" || in != url.PathEscape(in) || strings.Contains(in, ".") {
			t.Fatalf("ValidateRef(%q) accepted a value that is not safe in a URL "+
				"path unescaped", in)
		}
	})
}

// FuzzValidateBoardIDAcceptsOnlyDigits is the narrower sibling. A board id
// reaches a path too, and "digits" is the whole rule — so the fuzzer's job is to
// find the character that slips through it, not to explore a grammar.
func FuzzValidateBoardIDAcceptsOnlyDigits(f *testing.F) {
	for _, seed := range []string{
		"42", "0", "", "-1", "+1", "4 2", "42/../../admin", "٤٢", "４２", "4e2", "0x2a",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		if err := epic.ValidateBoardID(in); err != nil {
			return
		}
		if in == "" {
			t.Fatal("ValidateBoardID accepted an empty id")
		}
		for _, r := range in {
			if r < '0' || r > '9' {
				t.Fatalf("ValidateBoardID(%q) accepted, but %q is not a decimal digit",
					in, string(r))
			}
		}
	})
}
