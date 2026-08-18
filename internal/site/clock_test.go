package site_test

import (
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/site"
)

// These moved here from internal/resource/issue with the function they cover.
// It read the site's clock for the change feed, and a diagnostic in internal/cli
// needs the same answer — which it cannot get from a resource, because only the
// edges may import one. The tests come along because a moved function whose
// tests stayed behind is a function nobody is testing where it now lives.

// TestTheSiteClockIsTheSitesAndNotThisMachines is the reason a request is spent
// on /serverInfo at all. Every timestamp it will be compared against was written
// by the server.
func TestTheSiteClockIsTheSitesAndNotThisMachines(t *testing.T) {
	doer := &stubDoer{body: `{"deploymentType":"Cloud","version":"1001.0.0",` +
		`"serverTime":"2019-03-04T05:06:07.891-0600"}`}

	got, err := site.Now(t.Context(), doer, site.Info{Kind: site.Cloud})
	if err != nil {
		t.Fatalf("site clock: %v", err)
	}
	// A date no machine running this test has on its own clock, and an offset
	// that is not UTC, so an implementation reading either would fail here.
	want := time.Date(2019, 3, 4, 11, 6, 7, 891_000_000, time.UTC)
	if !got.Equal(want) {
		t.Errorf("site clock = %s, want %s", got, want)
	}
}

func TestASiteThatReportsNoClockIsRefused(t *testing.T) {
	doer := &stubDoer{body: `{"deploymentType":"Cloud","version":"1001.0.0"}`}

	_, err := site.Now(t.Context(), doer, site.Info{Kind: site.Cloud})
	e := requireCode(t, err, "NO_SERVER_TIME")
	if e.Detail == "" {
		t.Error("refusal does not say why the clock is needed")
	}
}

func TestAClockThisToolCannotParseIsRefused(t *testing.T) {
	doer := &stubDoer{body: `{"deploymentType":"Cloud","serverTime":"last Tuesday"}`}

	_, err := site.Now(t.Context(), doer, site.Info{Kind: site.Cloud})
	_ = requireCode(t, err, "MALFORMED_TIMESTAMP")
}

// TestEveryLayoutJiraSendsIsRead covers the list itself, which is the reason
// this moved: two packages parsing Jira timestamps from two copies of one list
// is the defect this tree has paid for twice in internal/adf.
func TestEveryLayoutJiraSendsIsRead(t *testing.T) {
	want := time.Date(2026, 8, 17, 14, 30, 0, 0, time.UTC)
	for _, value := range []string{
		"2026-08-17T14:30:00.000+0000",
		"2026-08-17T09:30:00.000-0500",
		"2026-08-17T14:30:00+0000",
		"2026-08-17T14:30:00Z",
		"2026-08-17T14:30:00.000000000Z",
	} {
		got, ok := site.ParseTime(value)
		if !ok {
			t.Errorf("ParseTime(%q) refused a form Jira sends", value)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("ParseTime(%q) = %s, want %s", value, got, want)
		}
	}

	if _, ok := site.ParseTime("last Tuesday"); ok {
		t.Error("ParseTime accepted something that is not a timestamp")
	}
}

// requireCode fails unless err is this tool's structured error with this code.
func requireCode(t *testing.T, err error, code string) *errs.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	structured, ok := errs.AsError(err)
	if !ok {
		t.Fatalf("error is not structured: %v", err)
	}
	if structured.Code != code {
		t.Fatalf("code = %q, want %s (%v)", structured.Code, code, err)
	}
	return structured
}
