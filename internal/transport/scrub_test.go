package transport_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/transport"
)

// realish is a response shaped like the ones a Cloud instance actually returns,
// with every kind of identifier a recording picks up.
const realish = `{"self":"https://acme.atlassian.invalid/rest/api/3/project/10001",
"key":"KAN","name":"Software Team",
"lead":{"self":"https://acme.atlassian.invalid/rest/api/3/user?accountId=70121%3A5faf8262-12ed-40d0-9358-e554f3352c5a",
"accountId":"70121:5faf8262-12ed-40d0-9358-e554f3352c5a",
"emailAddress":"kevin@acme.invalid","displayName":"Kevin O'Neil",
"avatarUrls":{"48x48":"https://acme.atlassian.invalid/rest/api/3/universal_avatar/view/type/project/avatar/10412"}}}`

func recorded(body string) *transport.Cassette {
	return &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: "GET", Path: "/rest/api/3/project/search",
				Query: "accountId=70121%3A5faf8262-12ed-40d0-9358-e554f3352c5a",
			},
			Response: transport.RecordedResponse{Status: 200, Body: body},
		}},
	}
}

func sandboxScrubber() transport.Scrubber {
	return transport.Scrubber{
		Replace: map[string]string{
			"acme.atlassian.invalid": "recorded.invalid",
			"Kevin O'Neil":           "Ada Lovelace",
			"KAN":                    "ENG",
			"Software Team":          "Engineering",
		},
		Patterns: []transport.PatternRule{
			{Match: transport.AvatarURL, With: "https://recorded.invalid/avatar"},
			{Match: transport.CloudAccountID, With: "000000:00000000-0000-0000-0000-000000000000"},
			{Match: transport.EmailAddress, With: "ada@example.invalid"},
		},
	}
}

// TestNothingIdentifyingSurvivesAScrub is the whole point. A recording is only
// worth having if it can be committed, and a real conversation carries a
// person, their address, their account, their host, and their project keys.
func TestNothingIdentifyingSurvivesAScrub(t *testing.T) {
	c := recorded(realish)
	sandboxScrubber().Scrub(c)

	rendered := c.Interactions[0].Response.Body + c.Interactions[0].Request.Query
	for _, gone := range []string{
		"acme.atlassian.invalid", "Kevin", "O'Neil", "KAN", "Software Team",
		"kevin@acme.invalid", "5faf8262", "universal_avatar",
	} {
		if strings.Contains(rendered, gone) {
			t.Errorf("%q survived the scrub", gone)
		}
	}

	// The shape is what a fixture is for, so it must be intact.
	for _, kept := range []string{
		`"self"`, `"key":"ENG"`, `"name":"Engineering"`, `"accountId"`,
		`"displayName":"Ada Lovelace"`, `"avatarUrls"`,
	} {
		if !strings.Contains(rendered, kept) {
			t.Errorf("the scrub removed %s, which is shape rather than identity", kept)
		}
	}
}

// TestAPercentEncodedIdentifierIsScrubbed is a regression with a date on it.
//
// The same account id appears in a body with a literal colon and in a query
// string with %3A. Matching only the literal form left the encoded one
// untouched in exactly one place — a query parameter — which is precisely the
// kind of survivor nobody would think to grep for.
func TestAPercentEncodedIdentifierIsScrubbed(t *testing.T) {
	c := recorded(realish)
	sandboxScrubber().Scrub(c)

	if q := c.Interactions[0].Request.Query; strings.Contains(q, "5faf8262") {
		t.Errorf("the percent-encoded id survived in the query: %q", q)
	}
}

// TestAShortAccountPrefixIsScrubbed covers the other half of the same bug. The
// prefix on an account id is not a fixed width, and requiring six characters
// let a real five-digit one through.
func TestAShortAccountPrefixIsScrubbed(t *testing.T) {
	for _, id := range []string{
		"70121:5faf8262-12ed-40d0-9358-e554f3352c5a",
		"5:5faf8262-12ed-40d0-9358-e554f3352c5a",
		"557058:f58131cb-b67d-43c7-b30d-6b58d40bd077",
		"5b10a2844c20165700ede21e",
	} {
		got := transport.CloudAccountID.ReplaceAllString(id, "SCRUBBED")
		if strings.Contains(got, "-") || got == id {
			t.Errorf("%q was left as %q", id, got)
		}
	}
}

// TestResidueDoesNotShareThePatternItGuards is the design point, and the reason
// the first version of this was useless.
//
// The guard originally reused the scrubber's own account-id pattern. When that
// pattern turned out to be too narrow, the scrubber left a real identifier and
// the check that existed to catch the miss was blind in exactly the same place.
// A guard that shares a definition with the thing it guards cannot catch that
// definition being wrong.
func TestResidueDoesNotShareThePatternItGuards(t *testing.T) {
	// A bare UUID with no account prefix: CloudAccountID does not match it, so
	// a guard sharing that pattern would report nothing. This is the shape an
	// identifier takes once an encoding the scrubber did not anticipate has
	// pulled the rest of it apart.
	const bare = `{"id":"5faf8262-12ed-40d0-9358-e554f3352c5a"}`

	if transport.CloudAccountID.MatchString(bare) {
		t.Fatal("the premise is gone: CloudAccountID now matches a bare UUID, " +
			"so this no longer proves the two patterns are independent")
	}

	c := recorded(bare)
	c.Interactions[0].Request.Query = ""
	sandboxScrubber().Scrub(c)

	residue := c.Residue()
	if len(residue) == 0 {
		t.Fatal("the guard missed what the scrubber's own pattern cannot see")
	}
	if joined := strings.Join(residue, "\n"); !strings.Contains(joined, "identifier") {
		t.Errorf("residue does not name it:\n%s", joined)
	}
}

// TestAScrubbedRecordingReportsNoResidue closes the loop: the two halves have
// to agree on a recording that really is clean, or the report becomes noise
// that a reader learns to skim.
func TestAScrubbedRecordingReportsNoResidue(t *testing.T) {
	c := recorded(realish)
	sandboxScrubber().Scrub(c)

	if residue := c.Residue(); len(residue) > 0 {
		t.Errorf("a scrubbed recording still reports:\n%s", strings.Join(residue, "\n"))
	}
}

// TestAReservedHostIsNotResidue keeps the guard from flagging the very hosts a
// fixture is supposed to name.
func TestAReservedHostIsNotResidue(t *testing.T) {
	c := recorded(`{"a":"https://recorded.invalid/x","b":"https://acme.example/y",
		"c":"http://localhost:8080/z"}`)
	c.Interactions[0].Request.Query = ""

	for _, r := range c.Residue() {
		if strings.HasPrefix(r, "host ") {
			t.Errorf("a reserved host was reported as residue: %s", r)
		}
	}
}
