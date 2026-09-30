package site_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/site"
)

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
