package site_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// stubDoer answers a probe without a network, and counts how often it was
// asked — which is what the cache tests assert on.
type stubDoer struct {
	body   string
	status int
	calls  int
	err    error
	// header adds response headers beyond Content-Type, for the one caller
	// that reads any: the rate-limit disclosure a site makes on every response
	// and a diagnostic reports.
	header map[string][]string
}

func (s *stubDoer) Do(context.Context, transport.Request) (*transport.Response, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	status := s.status
	if status == 0 {
		status = 200
	}
	header := map[string][]string{"Content-Type": {"application/json"}}
	for name, values := range s.header {
		header[name] = values
	}
	return &transport.Response{
		Status: status,
		Body:   []byte(s.body),
		Header: header,
	}, nil
}

const cloudInfo = `{"baseUrl":"https://acme.atlassian.invalid","version":"1001.0.0",
	"versionNumbers":[1001,0,0],"deploymentType":"Cloud"}`

const dcInfo = `{"baseUrl":"https://jira.acme.invalid","version":"9.12.7",
	"versionNumbers":[9,12,7],"deploymentType":"Server"}`

func TestProbeIdentifiesBothDeployments(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		kind     site.Kind
		apiBase  string
		byCursor bool
	}{
		{"cloud", cloudInfo, site.Cloud, "/rest/api/3", true},
		{"data center", dcInfo, site.DataCenter, "/rest/api/2", false},
		{
			"data center reporting DataCenter",
			`{"version":"9.12.7","deploymentType":"DataCenter"}`,
			site.DataCenter, "/rest/api/2", false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := site.Probe(t.Context(), &stubDoer{body: tc.body}, time.Now())
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if info.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", info.Kind, tc.kind)
			}
			// The API version follows from the deployment, which is the whole
			// reason the probe exists.
			if info.APIBase() != tc.apiBase {
				t.Errorf("APIBase = %q, want %q", info.APIBase(), tc.apiBase)
			}
			if info.CursorPaginated() != tc.byCursor {
				t.Errorf("CursorPaginated = %v, want %v", info.CursorPaginated(), tc.byCursor)
			}
		})
	}
}

// TestAgileBaseIsTheSameOnBothDeployments records why AgileBase exists at all.
//
// Boards, sprints, and epics are not on either platform REST version. Jira
// Software has served them from /rest/agile/1.0 since it shipped and has never
// versioned that alongside the platform API, so a Cloud site answering v3 for
// issues still answers 1.0 for boards. Building an agile path out of APIBase
// produces a 404 that reads like a board that does not exist.
//
// If a deployment ever does move, this test is what fails first.
func TestAgileBaseIsTheSameOnBothDeployments(t *testing.T) {
	for _, kind := range []site.Kind{site.Cloud, site.DataCenter} {
		info := site.Info{Kind: kind}
		if got := info.AgileBase(); got != "/rest/agile/1.0" {
			t.Errorf("%s AgileBase = %q, want /rest/agile/1.0", kind, got)
		}
		if info.AgileBase() == info.APIBase() {
			t.Errorf("%s: the agile API is not the platform API", kind)
		}
	}
}

// TestUnknownDeploymentIsRefused is the "nothing is guessed" rule at its most
// consequential. Guessing Cloud would send v3 requests to a v2 server and
// produce a 404 that reads like a missing issue; guessing Data Center would use
// offset pagination against a cursor API, which is the incumbent's exact bug.
func TestUnknownDeploymentIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"version":"1.0","deploymentType":"Quantum"}`,
		`{"version":"1.0"}`,
		`{"version":"1.0","deploymentType":""}`,
	} {
		_, err := site.Probe(t.Context(), &stubDoer{body: body}, time.Now())
		if err == nil {
			t.Errorf("an unrecognized deployment was accepted: %s", body)
			continue
		}
		if errs.ExitOf(err) != exitcode.Remote {
			t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Remote)
		}
	}
}

func TestProbeRejectsNonJSON(t *testing.T) {
	_, err := site.Probe(t.Context(),
		&stubDoer{body: "<html><body>Sign in</body></html>"}, time.Now())
	if err == nil {
		t.Fatal("an HTML login page was accepted as server info")
	}
	// The remedy has to name the likely cause, because a login page is what a
	// misconfigured SSO proxy returns.
	if !strings.Contains(errs.Coerce(err).Remedy, "proxy") {
		t.Errorf("the remedy does not mention the likely cause: %q", errs.Coerce(err).Remedy)
	}
}

func TestProbePropagatesHTTPErrors(t *testing.T) {
	_, err := site.Probe(t.Context(), &stubDoer{status: 401, body: `{}`}, time.Now())
	if err == nil {
		t.Fatal("a 401 probe succeeded")
	}
	if errs.ExitOf(err) != exitcode.Auth {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Auth)
	}
}

func newResolver(t *testing.T, doer *stubDoer, now time.Time) *site.Resolver {
	t.Helper()
	return &site.Resolver{
		Client: doer,
		Cache:  &site.Cache{Dir: filepath.Join(t.TempDir(), "acme.atlassian.invalid")},
		Now:    func() time.Time { return now },
	}
}

// TestResolveCachesTheProbe is why metadata caching is a feature rather than an
// optimization: without it every invocation pays a round trip before it can
// decide which endpoint to call.
func TestResolveCachesTheProbe(t *testing.T) {
	doer := &stubDoer{body: cloudInfo}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	resolver := newResolver(t, doer, now)

	first, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if first.Cached {
		t.Error("the first resolve reported itself cached")
	}

	second, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve again: %v", err)
	}
	if !second.Cached {
		t.Error("the second resolve did not come from cache")
	}
	if doer.calls != 1 {
		t.Errorf("probed %d times, want 1", doer.calls)
	}
	if second.Kind != first.Kind {
		t.Errorf("the cached answer differs: %q vs %q", second.Kind, first.Kind)
	}
}

func TestResolveReprobesAfterTheTTL(t *testing.T) {
	doer := &stubDoer{body: cloudInfo}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	resolver := newResolver(t, doer, now)

	if _, err := resolver.Resolve(t.Context()); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// A day later the answer is no longer trusted, so an upgraded server is
	// picked up without anyone clearing a cache.
	resolver.Now = func() time.Time { return now.Add(site.DefaultTTL + time.Minute) }
	if _, err := resolver.Resolve(t.Context()); err != nil {
		t.Fatalf("resolve after ttl: %v", err)
	}
	if doer.calls != 2 {
		t.Errorf("probed %d times, want 2", doer.calls)
	}
}

func TestRefreshBustsTheCache(t *testing.T) {
	doer := &stubDoer{body: cloudInfo}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	resolver := newResolver(t, doer, now)

	if _, err := resolver.Resolve(t.Context()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	resolver.Refresh = true
	if _, err := resolver.Resolve(t.Context()); err != nil {
		t.Fatalf("resolve with refresh: %v", err)
	}
	if doer.calls != 2 {
		t.Errorf("probed %d times with --refresh, want 2", doer.calls)
	}
}

// TestResolveWorksWithoutACache covers a machine with no writable cache
// directory: slower, but never broken.
func TestResolveWorksWithoutACache(t *testing.T) {
	doer := &stubDoer{body: cloudInfo}
	resolver := &site.Resolver{Client: doer, Now: time.Now}

	for range 3 {
		if _, err := resolver.Resolve(t.Context()); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if doer.calls != 3 {
		t.Errorf("probed %d times without a cache, want 3", doer.calls)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	c := &site.Cache{Dir: filepath.Join(t.TempDir(), "acme")}

	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	if err := c.Put("fields", payload{Name: "story-points", Count: 3}); err != nil {
		t.Fatalf("put: %v", err)
	}

	var got payload
	ok, err := c.Get("fields", time.Hour, &got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("a value just written was not found")
	}
	if got.Name != "story-points" || got.Count != 3 {
		t.Errorf("got %+v", got)
	}
}

// TestCorruptCacheIsIgnoredNotFatal matters because everything here can be
// re-fetched: a broken cache must cost time, never correctness.
func TestCorruptCacheIsIgnoredNotFatal(t *testing.T) {
	dir := t.TempDir()
	c := &site.Cache{Dir: dir}
	if err := writeFile(filepath.Join(dir, "deployment.json"), "not json at all"); err != nil {
		t.Fatalf("write: %v", err)
	}

	var into site.Info
	ok, err := c.Get("deployment", time.Hour, &into)
	if err != nil {
		t.Fatalf("a corrupt entry produced an error: %v", err)
	}
	if ok {
		t.Error("a corrupt entry was reported usable")
	}
}

// TestCacheKeyCannotEscapeTheDirectory is why the key format is narrow: a key
// becomes a filename.
func TestCacheKeyCannotEscapeTheDirectory(t *testing.T) {
	c := &site.Cache{Dir: t.TempDir()}
	for _, key := range []string{"../escape", "a/b", "..", "", "Upper", ".hidden"} {
		if err := c.Put(key, "x"); err == nil {
			t.Errorf("Put accepted key %q", key)
		}
		if _, err := c.Get(key, time.Hour, new(string)); err == nil {
			t.Errorf("Get accepted key %q", key)
		}
	}
}

// TestClockGoingBackwardsExpiresTheEntry covers a machine whose clock was
// corrected: an entry stored in the future is not trustworthy.
func TestClockGoingBackwardsExpiresTheEntry(t *testing.T) {
	c := &site.Cache{Dir: t.TempDir()}
	if err := c.Put("deployment", site.Info{Kind: site.Cloud}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A reader whose clock has moved backwards must not trust an entry that
	// now appears to be from the future.
	c.Now = func() time.Time { return time.Now().Add(-2 * time.Hour) }

	var into site.Info
	ok, err := c.Get("deployment", time.Hour, &into)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Error("an entry stored in the future was treated as fresh")
	}
}

func TestCacheClear(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	c := &site.Cache{Dir: dir}
	if err := c.Put("deployment", site.Info{Kind: site.Cloud}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}

	var into site.Info
	ok, err := c.Get("deployment", time.Hour, &into)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Error("an entry survived Clear")
	}
}

func TestNilCacheIsSafe(t *testing.T) {
	var c *site.Cache
	if err := c.Clear(); err != nil {
		t.Errorf("Clear on a nil cache: %v", err)
	}
	if ok, err := c.Get("deployment", time.Hour, new(site.Info)); ok || err != nil {
		t.Errorf("Get on a nil cache = (%v, %v)", ok, err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
