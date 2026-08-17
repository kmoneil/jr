package site_test

import (
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedCloudProbeIsAConversationAServerHad pays off the last row of
// the noCassettes ledger.
//
// This package is where every deployment difference in the tool lives, and
// until 2026-08-11 it had no cassettes at all, so it was absent from the
// evidence grouping entirely and passed by being invisible. Three Data Center
// recordings closed that half. The Cloud half stayed open because it needed the
// sandbox and nothing else, which made it the cheapest outstanding row in the
// tree and also the easiest to keep not doing.
//
// What it establishes that a hand-written fixture could not: Cloud really does
// answer /rest/api/2/serverInfo, really does spell the field `deploymentType`,
// and really does put the string `Cloud` in it. Every one of those is a belief
// this package acts on before it has any other information about the site, and
// guessing wrong sends v3 to a v2 server or offset pagination to a cursor API.
//
// The v2 path is the part worth stating. This is the one request the tool makes
// before it knows which API version to use, so it is deliberately asked at v2,
// which both deployments serve. A recording is what makes that more than an
// assumption about Cloud.
func TestTheRecordedCloudProbeIsAConversationAServerHad(t *testing.T) {
	client, replayer := recordedClientAt(t,
		"probe-recorded.cloud.json", "https://recorded.invalid")

	probedAt := time.Unix(0, 0).UTC()
	info, err := site.Probe(t.Context(), client, probedAt)
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}

	if info.Kind != site.Cloud {
		t.Errorf("kind = %q, want %q. This is the field that decides the API "+
			"version and the pagination model.", info.Kind, site.Cloud)
	}
	if info.BaseURL == "" {
		t.Error("the probe returned no baseUrl, and the site provenance in " +
			"every result document comes from it")
	}
	if info.Version == "" {
		t.Error("the probe returned no version")
	}
	if !info.ProbedAt.Equal(probedAt) {
		t.Errorf("probedAt = %v, want the clock it was given (%v); the cache "+
			"TTL is measured from it", info.ProbedAt, probedAt)
	}

	// The account fetch is the second exchange in the cassette. This test
	// drives the probe directly, so leaving it unplayed is expected, and naming
	// it is better than dropping the check: an unplayed exchange otherwise
	// means a request that stopped being made.
	for _, unplayed := range replayer.Unplayed() {
		if !contains([]string{"GET /rest/api/3/myself"}, unplayed) {
			t.Errorf("a recorded exchange was never requested: %s", unplayed)
		}
	}
}
