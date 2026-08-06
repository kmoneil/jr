package transport_test

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/transport"
)

// realish is a response shaped like the ones a Cloud instance actually returns,
// with every kind of identifier a recording picks up.
const realish = `{"self":"https://acme.atlassian.invalid/rest/api/3/project/10001",
"key":"KAN","name":"Software Team",
"lead":{"self":"https://acme.atlassian.invalid/rest/api/3/user?accountId=61403%3A9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2",
"accountId":"61403:9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2",
"emailAddress":"rowan@acme.invalid","displayName":"Rowan O'Keefe",
"avatarUrls":{"48x48":"https://acme.atlassian.invalid/rest/api/3/universal_avatar/view/type/project/avatar/10412"}}}`

func recorded(body string) *transport.Cassette {
	return &transport.Cassette{
		Deployment: transport.Cloud,
		Interactions: []transport.Interaction{{
			Request: transport.RecordedRequest{
				Method: "GET", Path: "/rest/api/3/project/search",
				Query: "accountId=61403%3A9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2",
			},
			Response: transport.RecordedResponse{Status: 200, Body: body},
		}},
	}
}

func sandboxScrubber() transport.Scrubber {
	return transport.Scrubber{
		Replace: map[string]string{
			"acme.atlassian.invalid": "recorded.invalid",
			"Rowan O'Keefe":          "Ada Lovelace",
			"KAN":                    "ENG",
			"Software Team":          "Engineering",
		},
		Patterns: []transport.PatternRule{
			{Match: transport.AvatarURL, With: "https://recorded.invalid/avatar"},
			{Match: transport.CloudAccountIDEncoded, With: "000000%3A00000000-0000-0000-0000-000000000000"},
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
		"acme.atlassian.invalid", "Rowan", "O'Keefe", "KAN", "Software Team",
		"rowan@acme.invalid", "9d2c7ab4", "universal_avatar",
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

	if q := c.Interactions[0].Request.Query; strings.Contains(q, "9d2c7ab4") {
		t.Errorf("the percent-encoded id survived in the query: %q", q)
	}
}

// TestAShortAccountPrefixIsScrubbed covers the other half of the same bug. The
// prefix on an account id is not a fixed width, and requiring six characters
// let a real five-digit one through.
func TestAShortAccountPrefixIsScrubbed(t *testing.T) {
	for _, id := range []string{
		"61403:9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2",
		"5:9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2",
		"418902:c7a4e10d-2f63-4b58-9e07-1d84af6b2e93",
		"6c21b3955d31276811fed32f",
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
	const bare = `{"id":"9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2"}`

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

// TestAnIdentifierInsideABase64TokenIsScrubbed is the third instance of one
// bug, and the reason the scrubber now decodes.
//
// A Cloud page token is a base64 protobuf carrying the JQL that produced it, so
// the project key a caller searched for rides inside every one. A literal
// replacement cannot see it and neither could the residue guard, which is how a
// real key reached a recording that both halves called clean. The first two
// instances were a percent-encoded colon and a bare UUID; the shared shape is
// an encoding standing between the guard and the identifier.
func TestAnIdentifierInsideABase64TokenIsScrubbed(t *testing.T) {
	// A real token, as recorded, with the real project key inside it.
	const token = "EAIY9N3uwP0zIidwcm9qZWN0ID0gIlNBTTEiIE9SREVSIEJZIGlzc3Vla2V5IERFU0Mq" +
		"AltdMiBURU5BTlRfTk9UX0ZPVU5EX0lOX1RFTkFOVF9TVE9SRQ=="

	c := recorded(`{"nextPageToken":"` + token + `"}`)
	c.Interactions[0].Request.Query = "nextPageToken=" + token

	// The premise: the key is genuinely invisible to a text scan beforehand.
	if strings.Contains(token, "SAM1") {
		t.Fatal("the premise is gone: the key is visible in the token as text, " +
			"so this no longer proves anything about decoding")
	}

	transport.Scrubber{Replace: map[string]string{"SAM1": "OPS"}}.Scrub(c)

	for _, where := range []string{
		c.Interactions[0].Response.Body, c.Interactions[0].Request.Query,
	} {
		for _, run := range regexp.MustCompile(`[A-Za-z0-9+/_-]{24,}={0,2}`).FindAllString(where, -1) {
			decoded, _ := base64.StdEncoding.DecodeString(run + strings.Repeat("=", (4-len(run)%4)%4))
			if bytes.Contains(decoded, []byte("SAM1")) {
				t.Errorf("the key survived inside the token: %q", decoded)
			}
			if bytes.Contains(decoded, []byte("OPS")) {
				return // rewritten, re-encoded, and still a token.
			}
		}
	}
	t.Error("the token was not rewritten at all")
}

// TestEncodedResidueNamesWhatTheTextScanCannotSee gives the guard the same
// reach. Scrubbing is driven by rules somebody wrote; the guard exists for the
// case where those rules were incomplete, and it cannot do that job for encoded
// values unless it decodes too.
func TestEncodedResidueNamesWhatTheTextScanCannotSee(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(
		[]byte(`project = "SECRET1" ORDER BY issuekey DESC padding-padding`),
	)

	c := recorded(`{"nextPageToken":"` + encoded + `"}`)
	c.Interactions[0].Request.Query = ""

	if len(c.Residue()) > 0 {
		t.Fatal("the premise is gone: the text scan already sees inside base64")
	}
	if got := c.EncodedResidue([]string{"SECRET1"}); len(got) == 0 {
		t.Error("the guard did not report an identifier hiding inside base64")
	}
}

// TestDecodingDoesNotFlagOrdinaryOpaqueValues keeps the decode from making the
// report noisy. A hash or a random id will often decode without error into
// bytes that mean nothing, and a guard that reports those is one nobody reads.
func TestDecodingDoesNotFlagOrdinaryOpaqueValues(t *testing.T) {
	c := recorded(`{"etag":"a94a8fe5ccb19ba61c4c0873d391e987982fbbd3",
		"sig":"MEUCIQDx8Zq2vK3nR7tYwLpHcVfGjNbXsAoQeTyUiPkLmNhGdw"}`)
	c.Interactions[0].Request.Query = ""

	if got := c.EncodedResidue([]string{"SAM1", "KAN"}); len(got) > 0 {
		t.Errorf("ordinary opaque values were reported: %v", got)
	}
}

// TestAPageTokenStillPairsAfterScrubbing is the invariant that re-encoding put
// at risk, and it is the one that decides whether a paged recording replays.
//
// A token appears twice: plain in the response that issued it, percent-escaped
// in the query of the request that spends it. The replayer matches a request by
// its query, so the two copies have to come out of the scrub still describing
// the same token. The first version matched only unescaped characters, which
// stopped the run before its `%3D%3D` padding and rewrote the two copies to
// different strings — a paged fixture that replayed fine until page two.
func TestAPageTokenStillPairsAfterScrubbing(t *testing.T) {
	const token = "EAIY9N3uwP0zIidwcm9qZWN0ID0gIlNBTTEiIE9SREVSIEJZIGlzc3Vla2V5IERFU0Mq" +
		"AltdMiBURU5BTlRfTk9UX0ZPVU5EX0lOX1RFTkFOVF9TVE9SRQ=="

	c := recorded(`{"nextPageToken":"` + token + `"}`)
	// Exactly how the next request carries it: escaped, padding and all.
	c.Interactions[0].Request.Query = "maxResults=2&nextPageToken=" + url.QueryEscape(token)

	transport.Scrubber{Replace: map[string]string{"SAM1": "OPS"}}.Scrub(c)

	issued := regexp.MustCompile(`"nextPageToken":"([^"]*)"`).
		FindStringSubmatch(c.Interactions[0].Response.Body)
	spent := regexp.MustCompile(`nextPageToken=(.*)$`).
		FindStringSubmatch(c.Interactions[0].Request.Query)
	if issued == nil || spent == nil {
		t.Fatal("the scrub destroyed the token fields entirely")
	}

	unescaped, err := url.QueryUnescape(spent[1])
	if err != nil {
		t.Fatalf("the query is no longer decodable: %v", err)
	}
	if unescaped != issued[1] {
		t.Errorf("the two copies diverged, so page two would never match:\n"+
			" issued: %q\n  spent: %q", issued[1], unescaped)
	}

	// And it is still a token, not a mangled string.
	if _, err := base64.StdEncoding.DecodeString(issued[1]); err != nil {
		t.Errorf("the rewritten token is not valid base64: %v", err)
	}
}

// TestScrubbingDoesNotChangeHowAValueIsEncoded is the fourth encoding bug and
// the one with the quietest failure.
//
// An account id reaches a query percent-encoded. Replacing it with a stand-in
// that spells the separator as a literal colon produces a cassette that is
// clean, readable, and unmatchable: the tool escapes the separator when it
// builds the request, so the replayer asks for %3A and the recording offers
// `:`. The recording looks right and every test that uses it fails with
// FIXTURE_MISS, which reads like a bug in the code under test.
func TestScrubbingDoesNotChangeHowAValueIsEncoded(t *testing.T) {
	c := recorded(`{"accountId":"61403:9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2"}`)
	c.Interactions[0].Request.Query = "accountId=61403%3A9d2c7ab4-3e51-4f80-a26b-5c8917e0d3f2"

	sandboxScrubber().Scrub(c)

	q := c.Interactions[0].Request.Query
	if strings.Contains(q, ":") {
		t.Errorf("the encoded query gained a literal colon, so no request can "+
			"match it: %q", q)
	}
	if !strings.Contains(q, "%3A") {
		t.Errorf("the query no longer carries an encoded separator: %q", q)
	}
	// The body's literal form stays literal, for the same reason.
	if !strings.Contains(c.Interactions[0].Response.Body, ":") {
		t.Errorf("the body lost its literal separator: %s",
			c.Interactions[0].Response.Body)
	}
}
