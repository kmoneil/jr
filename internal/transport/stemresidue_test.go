package transport_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/transport"
)

// cassetteWith builds a one-interaction cassette from a body and headers.
func cassetteWith(body string, header map[string][]string) *transport.Cassette {
	return &transport.Cassette{
		Deployment: transport.Cloud,
		Source:     transport.Recorded,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: transport.MethodGet, Path: "/rest/agile/1.0/board",
			},
			Response: transport.RecordedResponse{
				Status: 200, Header: header, Body: body,
			},
		}},
	}
}

// TestTheResidueCheckReadsHeaders is the blind spot this closes.
//
// The scrubber has always rewritten header values, with tight patterns tuned to
// known shapes. The residue check carries the loose patterns — the ones
// documented as expected to over-report — and read the path, the query and the
// bodies, never the headers. So the two halves were complementary in exactly
// the wrong direction, and 30 of 30 recordings carried header content that no
// guard had ever looked at.
func TestTheResidueCheckReadsHeaders(t *testing.T) {
	c := cassetteWith(`{"ok":true}`, map[string][]string{
		"X-Trace":   {"16abc156eac44d05b4bf840962c9ecf3"},
		"X-Contact": {"rowan@acme.invalid"},
		"X-Origin":  {"https://cdn-internal/report"},
	})

	got := strings.Join(c.Residue(), "\n")
	for _, want := range []string{
		"16abc156eac44d05b4bf840962c9ecf3",
		"rowan@acme.invalid",
		"cdn-internal",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the residue report does not name %q:\n%s", want, got)
		}
	}
	// The header is named, because most of the judgement is which one it was.
	// Atl-Traceid and Set-Cookie read very differently.
	if !strings.Contains(got, "header X-Trace") {
		t.Errorf("the report does not say which header a hit came from:\n%s", got)
	}
}

// TestStemResidueFindsTheFormTheCallerDidNotDeclare is the defect, exactly as
// it happened.
//
// A two-letter project key cannot be scrubbed bare: `GE` as a
// strings.ReplaceAll target rewrites GET and PAGE. So the caller declares the
// quoted and suffixed forms, `"GE"` cleans "projectKey":"GE", and
// "displayName":"Widgets (GE)" survives — reported by nothing, because a
// project key is not a shape any pattern in this package can know.
func TestStemResidueFindsTheFormTheCallerDidNotDeclare(t *testing.T) {
	body := `{"location":{"projectKey":"ORG",` +
		`"displayName":"Widgets (GE)","name":"Widgets (GE)"}}`
	c := cassetteWith(body, nil)

	// What the caller declared, which is everything except the bare word.
	targets := []string{`"GE"`, "GE-", "GE board"}

	if hits := c.Residue(); len(hits) > 0 {
		t.Errorf("the shape-based check reported %v, so this test is not "+
			"measuring what it claims", hits)
	}
	got := c.StemResidue(targets)
	if len(got) == 0 {
		t.Fatal("a declared identifier survived in an undeclared form and " +
			"nothing said so")
	}
	if !strings.Contains(strings.Join(got, "\n"), "GE") {
		t.Errorf("the report does not name the surviving identifier: %v", got)
	}
}

// TestStemResidueDoesNotFireOnTheWordsInsideOtherWords is the whole difficulty.
//
// The reason a short key cannot be replaced bare is that `GE` appears inside
// GET, PAGE and TARGET. A check that reported those would fire on every
// recording ever made, and a report that always fires is one nobody reads —
// which would leave this no better than the blind spot it replaces.
func TestStemResidueDoesNotFireOnTheWordsInsideOtherWords(t *testing.T) {
	c := cassetteWith(`{"method":"GET","note":"one PAGE of it","x":"TARGET"}`,
		map[string][]string{"Set-Cookie": {"REDACTED"}})

	if got := c.StemResidue([]string{`"GE"`, "GE-"}); len(got) > 0 {
		t.Errorf("stem GE fired on a word containing it: %v", got)
	}
}

// TestStemResidueReadsHeadersToo covers the same gap in the place the other
// half of this change opened up.
func TestStemResidueReadsHeadersToo(t *testing.T) {
	c := cassetteWith(`{"ok":true}`, map[string][]string{
		"X-Project": {"project (GE) is active"},
	})

	got := c.StemResidue([]string{`"GE"`})
	if len(got) == 0 {
		t.Fatal("an undeclared form in a header was not reported")
	}
	if !strings.Contains(strings.Join(got, "\n"), "X-Project") {
		t.Errorf("the report does not name the header: %v", got)
	}
}

// TestStemResidueIsSilentWhenEveryFormWasDeclared is the other direction, and
// the one that keeps this usable. A guard that reports a clean recording is a
// guard people learn to ignore.
func TestStemResidueIsSilentWhenEveryFormWasDeclared(t *testing.T) {
	c := cassetteWith(
		`{"location":{"projectKey":"ORG","displayName":"Widgets (AGL)"}}`,
		map[string][]string{"Content-Type": {"application/json"}},
	)
	if got := c.StemResidue([]string{`"GE"`, "GE-", "(GE)"}); len(got) > 0 {
		t.Errorf("a fully scrubbed recording was reported as residue: %v", got)
	}
}

// TestAStemShorterThanTwoCharactersIsNotHunted pins the floor.
//
// A single character is inside a large fraction of every recording, so hunting
// one produces a report with no information in it. The cost of skipping it is
// that a one-character identifier is unguarded, and there is no such thing: a
// Jira project key is two characters at minimum.
func TestAStemShorterThanTwoCharactersIsNotHunted(t *testing.T) {
	c := cassetteWith(`{"summary":"a b c d e"}`, nil)
	if got := c.StemResidue([]string{`"a"`, "b-"}); len(got) > 0 {
		t.Errorf("a one-character stem was hunted and reported %v", got)
	}
}

// TestStemResidueKeepsTheInteriorOfAMultiWordTarget covers a name rather than a
// key. Stripping every non-word character would turn "Ada Lovelace" into
// "AdaLovelace", which matches nothing and would report a clean recording as
// clean for the wrong reason.
func TestStemResidueKeepsTheInteriorOfAMultiWordTarget(t *testing.T) {
	c := cassetteWith(`{"displayName":"Rowan O'Keefe reviewed it"}`, nil)

	got := c.StemResidue([]string{"'Rowan O'Keefe'"})
	if len(got) == 0 {
		t.Fatal("a multi-word identifier was not found, so its interior was " +
			"stripped along with its wrapping")
	}
}
