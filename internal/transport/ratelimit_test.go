package transport_test

import (
	"net/http"
	"testing"

	"github.com/kmoneil/jr/internal/transport"
)

// TestARateLimitDisclosureIsReadInTheSpellingTheServerSends covers the reason
// this reads through http.Header rather than a map lookup: Cloud sends
// `X-RateLimit-Limit` and the canonical form is `X-Ratelimit-Limit`, so a
// literal comparison would read nothing and report a site with a policy as a
// site without one.
func TestARateLimitDisclosureIsReadInTheSpellingTheServerSends(t *testing.T) {
	header := http.Header{}
	header.Set("Ratelimit-Policy", `"jira-burst-based";q=100;w=1`)
	header.Set("Ratelimit", `"jira-burst-based";r=348;t=1`)
	header.Set("X-RateLimit-Limit", "350")
	header.Set("X-RateLimit-Remaining", "348")

	got := transport.LimitsFrom(&transport.Response{Header: header})

	if !got.Disclosed() {
		t.Fatal("a response advertising a policy disclosed nothing")
	}
	if got.Limit != "350" || got.Remaining != "348" {
		t.Errorf("limit/remaining = %q/%q, want 350/348", got.Limit, got.Remaining)
	}
	if got.Policy != `"jira-burst-based";q=100;w=1` {
		t.Errorf("policy = %q, want the header verbatim", got.Policy)
	}
}

// TestNoResponseDisclosesNothing is the case a diagnostic hits: it asks after a
// request that failed, and a nil response is not a panic.
func TestNoResponseDisclosesNothing(t *testing.T) {
	if got := transport.LimitsFrom(nil); got.Disclosed() {
		t.Errorf("LimitsFrom(nil) = %+v, want nothing disclosed", got)
	}
	if got := transport.LimitsFrom(&transport.Response{}); got.Disclosed() {
		t.Errorf("a response with no headers disclosed %+v", got)
	}
}
