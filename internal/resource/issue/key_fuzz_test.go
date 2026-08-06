package issue_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/resource/issue"
)

// safeSegment is the property every accepted key has to satisfy.
//
// An issue key ends up as a URL path segment. Most callers pass it through
// url.PathEscape and are fine whatever it contains; the one that concatenated
// it was not, and that is not a property worth resting on every future caller
// remembering. So the parser guarantees it instead: what ParseKey accepts is
// already safe unescaped.
func safeSegment(s string) bool {
	return s != "" && s == url.PathEscape(s) && !strings.Contains(s, ".")
}

// FuzzParseKeyProducesASafePathSegment is the regression for a real bug.
//
// ParseKey checked that the project was non-empty and nothing else, so
// "../../admin-1" parsed as the project "../../ADMIN" — and `epic add` built
// its path by concatenation, which turned a caller's argument into a request to
// a different endpoint with the caller's credential attached.
//
// The seeds carry the crashers rather than only a corpus file, so the
// regression is visible in the test source.
func FuzzParseKeyProducesASafePathSegment(f *testing.F) {
	for _, seed := range []string{
		"ENG-1", "eng-12", "IDO-1000", "A1_B-7",
		// The bug, in the shapes it took.
		"../../admin-1", "a/b-1", "ENG/x-9", "%2e%2e-1",
		"a b-1", "a\n-1", "ENG-+1", "ENG--1", ".-1", "..-1",
		// Neighbours of the boundary.
		"", "-", "ENG", "ENG-", "-1", "ENG-abc", "ENG-1-2", "1ENG-1",
		"ENG-99999999999999999999", "\x00-1", "É-1", "ＥＮＧ-1", "ENG-١",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		key, ok := issue.ParseKey(in)
		if !ok {
			// A rejected key must stay rejected: nothing downstream sees it.
			return
		}

		rendered := key.String()
		if !safeSegment(rendered) {
			t.Fatalf("ParseKey(%q) accepted, and String() = %q is not safe in a "+
				"URL path unescaped", in, rendered)
		}
		if !safeSegment(key.Project) {
			t.Fatalf("ParseKey(%q) accepted with project %q", in, key.Project)
		}
		if key.Number < 0 {
			t.Fatalf("ParseKey(%q) accepted with number %d", in, key.Number)
		}

		// Parsing the rendered form gives the same key back. Without this the
		// charset check could be satisfied by a parser that quietly rewrote its
		// input into something else that happened to be safe.
		again, ok := issue.ParseKey(rendered)
		if !ok {
			t.Fatalf("ParseKey(%q) rendered %q, which does not parse", in, rendered)
		}
		if again != key {
			t.Fatalf("ParseKey(%q) = %+v, but reparsing %q gives %+v",
				in, key, rendered, again)
		}
	})
}

// FuzzKeyCompareIsATotalOrder guards the comparison the whole keyset pagination
// scheme rests on. A comparator that is not antisymmetric or not transitive
// gives sort.Slice licence to produce any order at all, and the failure shows
// up as a paged result that silently skips rows.
func FuzzKeyCompareIsATotalOrder(f *testing.F) {
	f.Add("ENG-1", "ENG-2", "ENG-10")
	f.Add("IDO-999", "IDO-1000", "IDO-1")
	f.Add("AAA-9999", "ZZZ-1", "MMM-500")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		ka, oka := issue.ParseKey(a)
		kb, okb := issue.ParseKey(b)
		kc, okc := issue.ParseKey(c)
		if !oka || !okb || !okc {
			return
		}

		// Antisymmetry: reversing the arguments reverses the sign.
		if got, want := ka.Compare(kb), -kb.Compare(ka); got != want {
			t.Fatalf("Compare(%s,%s) = %d, Compare(%s,%s) = %d",
				ka, kb, got, kb, ka, kb.Compare(ka))
		}
		// Reflexivity.
		if ka.Compare(ka) != 0 {
			t.Fatalf("%s does not equal itself", ka)
		}
		// Transitivity, in the direction that matters for a sort.
		if ka.Compare(kb) <= 0 && kb.Compare(kc) <= 0 && ka.Compare(kc) > 0 {
			t.Fatalf("%s <= %s <= %s, but %s > %s", ka, kb, kc, ka, kc)
		}
		// Before agrees with Compare, since callers use both.
		if ka.Before(kb) != (ka.Compare(kb) < 0) {
			t.Fatalf("Before and Compare disagree on %s, %s", ka, kb)
		}
	})
}
