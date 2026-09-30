package site_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedCloudCountIsAConversationAServerHad is the Cloud half of the
// evidence; the Data Center half is the JQL check's own recording, replayed in
// internal/resource/jql.
//
// Recorded from the sandbox on 2026-09-30 by the refusal itself: `issue
// activity --since -30d --all-projects` with `--max-requests 1` and a warm probe
// cache, so the one request allowed was the count and the refusal after it sent
// nothing. The replayer matches method, path and body, so a count here means
// Cloud accepted the body this code builds, `ORDER BY` included, and answered
// with `count` at the top level.
func TestTheRecordedCloudCountIsAConversationAServerHad(t *testing.T) {
	client, replayer := recordedClientAt(t,
		"count-recorded.cloud.json", "https://recorded.invalid")

	got, err := site.CountIssues(t.Context(), client, site.Info{Kind: site.Cloud},
		`updated >= "-30d" ORDER BY issuekey DESC`)
	if err != nil {
		t.Fatalf("the request the count builds is not the one the server "+
			"answered: %v", err)
	}
	if got.Issues != 4 {
		t.Errorf("count = %d, want the recorded 4", got.Issues)
	}
	if !got.Approximate {
		t.Error("Cloud's estimate was reported as exact")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the count was never sent: %v", unplayed)
	}
}

// TestAnAbsentCountIsNotZero keeps the one wrong reading out.
//
// A count is what an activity sweep is sized by, and zero is the cheapest sweep
// there is. A response with no count in it read as zero would admit every sweep
// the guard exists to refuse, on the day a server changed its shape, with
// nothing to say so. Neither recording can show this, because both servers
// sent a count.
func TestAnAbsentCountIsNotZero(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		body string
	}{
		{site.DataCenter, `{"startAt":0,"maxResults":0,"issues":[]}`},
		{site.Cloud, `{}`},
		{site.Cloud, `not json`},
	} {
		_, err := site.CountIssues(t.Context(), &stubDoer{body: tc.body},
			site.Info{Kind: tc.kind}, "updated >= -7d")
		if code := errs.Coerce(err).Code; code != "MALFORMED_COUNT" {
			t.Errorf("%s answering %s: code = %q, want MALFORMED_COUNT",
				tc.kind, tc.body, code)
		}
	}
}

// TestAZeroCountIsAnAnswer is the converse: an empty window is a count, and
// refusing it would refuse every quiet week.
func TestAZeroCountIsAnAnswer(t *testing.T) {
	for kind, body := range map[site.Kind]string{
		site.DataCenter: `{"startAt":0,"maxResults":0,"total":0,"issues":[]}`,
		site.Cloud:      `{"count":0}`,
	} {
		got, err := site.CountIssues(t.Context(), &stubDoer{body: body},
			site.Info{Kind: kind}, "updated >= -7d")
		if err != nil {
			t.Errorf("%s: a count of zero was refused: %v", kind, err)
		}
		if got.Issues != 0 {
			t.Errorf("%s: count = %d, want 0", kind, got.Issues)
		}
		if got.Approximate != (kind == site.Cloud) {
			t.Errorf("%s: approximate = %v", kind, got.Approximate)
		}
	}
}
