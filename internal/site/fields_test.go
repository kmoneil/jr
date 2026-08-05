package site_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// testNow is the fixed clock the cache tests share, so an entry can be expired
// without waiting a day for it.
var testNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// fieldCatalogue is what both deployments return from /field: ids that are
// nothing like the names people use, which is why resolution exists at all.
const fieldCatalogue = `[
	{"id":"summary","key":"summary","name":"Summary","custom":false,
	 "searchable":true,"orderable":true,"navigable":true,
	 "clauseNames":["summary"],"schema":{"type":"string"}},
	{"id":"duedate","key":"duedate","name":"Due Date","custom":false,
	 "searchable":true,"orderable":true,"navigable":true,
	 "clauseNames":["due","duedate"],"schema":{"type":"date"}},
	{"id":"customfield_10042","key":"customfield_10042","name":"Story Points",
	 "custom":true,"searchable":true,"orderable":true,"navigable":true,
	 "clauseNames":["cf[10042]","Story Points"],
	 "schema":{"type":"number","customId":10042}},
	{"id":"customfield_10099","key":"customfield_10099","name":"Sprint",
	 "custom":true,"searchable":true,"orderable":false,"navigable":true,
	 "clauseNames":["cf[10099]","Sprint"],
	 "schema":{"type":"array","items":"json","customId":10099}}
]`

func TestFetchFieldsUsesTheDeploymentsAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		want string
	}{
		{site.Cloud, "/rest/api/3/field"},
		{site.DataCenter, "/rest/api/2/field"},
	} {
		doer := &pathRecordingDoer{stubDoer: stubDoer{body: fieldCatalogue}}
		if _, err := site.FetchFields(t.Context(), doer, site.Info{Kind: tc.kind}); err != nil {
			t.Fatalf("%s: fetch: %v", tc.kind, err)
		}
		if doer.path != tc.want {
			t.Errorf("%s asked for %q, want %q", tc.kind, doer.path, tc.want)
		}
	}
}

func TestFetchFieldsNormalizesTheCatalogue(t *testing.T) {
	doer := &stubDoer{body: fieldCatalogue}
	catalogue, err := site.FetchFields(t.Context(), doer, site.Info{Kind: site.Cloud})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(catalogue.Fields) != 4 {
		t.Fatalf("got %d fields, want 4", len(catalogue.Fields))
	}

	// Sorted by id, so two runs against one site produce the same rows in the
	// same order. The server promises no order.
	for i := 1; i < len(catalogue.Fields); i++ {
		if catalogue.Fields[i-1].ID > catalogue.Fields[i].ID {
			t.Fatalf("the catalogue is not sorted by id: %v", ids(catalogue))
		}
	}

	got, err := catalogue.Resolve("Sprint")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Type != "array" || got.Items != "json" || !got.Custom {
		t.Errorf("the schema was not carried through: %+v", got)
	}
}

// TestFetchFieldsFallsBackToKey covers the Data Center versions that populate
// key and leave id empty. Dropping the field would make its name unresolvable
// for a reason nobody could see.
func TestFetchFieldsFallsBackToKey(t *testing.T) {
	doer := &stubDoer{body: `[{"key":"customfield_10001","name":"Team","custom":true}]`}
	catalogue, err := site.FetchFields(t.Context(), doer, site.Info{Kind: site.DataCenter})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(catalogue.Fields) != 1 || catalogue.Fields[0].ID != "customfield_10001" {
		t.Fatalf("got %+v, want the key used as the id", catalogue.Fields)
	}
}

func TestFetchFieldsRefusesAnUnusableBody(t *testing.T) {
	doer := &stubDoer{body: `<html>not jira</html>`}
	_, err := site.FetchFields(t.Context(), doer, site.Info{Kind: site.Cloud})
	if err == nil {
		t.Fatal("an HTML body was accepted as a field catalogue")
	}
	if code := errs.Coerce(err).Code; code != "MALFORMED_FIELD_LIST" {
		t.Errorf("code = %q, want MALFORMED_FIELD_LIST", code)
	}
}

func TestResolveAcceptsIdNameAndClauseName(t *testing.T) {
	catalogue := load(t)

	for input, want := range map[string]string{
		"customfield_10042": "customfield_10042",
		"Story Points":      "customfield_10042",
		"STORY POINTS":      "customfield_10042",
		"cf[10042]":         "customfield_10042",
		"  duedate  ":       "duedate",
		"Due Date":          "duedate",
		"due":               "duedate",
	} {
		got, err := catalogue.Resolve(input)
		if err != nil {
			t.Errorf("Resolve(%q) = %v", input, err)
			continue
		}
		if got.ID != want {
			t.Errorf("Resolve(%q) = %q, want %q", input, got.ID, want)
		}
	}
}

// TestResolvePrefersAnExactIdMatch covers a site where one field's name is
// another field's id. Without checking ids first, an exact id lookup would fail
// as ambiguous against a name that merely looked like it.
func TestResolvePrefersAnExactIdMatch(t *testing.T) {
	catalogue := &site.Catalogue{Fields: []site.Field{
		{ID: "duedate", Name: "Due Date"},
		{ID: "customfield_10077", Name: "duedate", Custom: true},
	}}

	got, err := catalogue.Resolve("duedate")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != "duedate" {
		t.Errorf("Resolve = %q, want the field whose id it is", got.ID)
	}
}

func TestResolveRefusesAnUnknownField(t *testing.T) {
	catalogue := load(t)

	_, err := catalogue.Resolve("Storey Points")
	if err == nil {
		t.Fatal("an unknown field was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_FIELD" {
		t.Errorf("code = %q, want UNKNOWN_FIELD", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
	}
	// The near miss has to carry the id, because the id is what the caller
	// needs to type next.
	if !strings.Contains(e.Detail, "Story Points (customfield_10042)") {
		t.Errorf("the detail does not name the near match with its id: %q", e.Detail)
	}
}

// TestResolveRefusesAnAmbiguousName covers two custom fields sharing a name,
// which Jira permits. They hold different values, so picking one would report
// the wrong column for every issue.
func TestResolveRefusesAnAmbiguousName(t *testing.T) {
	catalogue := &site.Catalogue{Fields: []site.Field{
		{ID: "customfield_10001", Name: "Team", Custom: true},
		{ID: "customfield_10002", Name: "Team", Custom: true},
	}}

	_, err := catalogue.Resolve("Team")
	if err == nil {
		t.Fatal("an ambiguous name resolved to one of the candidates")
	}
	e := errs.Coerce(err)
	if e.Code != "AMBIGUOUS_FIELD" {
		t.Errorf("code = %q, want AMBIGUOUS_FIELD", e.Code)
	}
	for _, id := range []string{"customfield_10001", "customfield_10002"} {
		if !strings.Contains(e.Detail, id) {
			t.Errorf("the detail does not list %s: %q", id, e.Detail)
		}
	}
}

func TestNearMatchesAreOrderedAndBounded(t *testing.T) {
	catalogue := load(t)

	near := catalogue.NearMatches("Story Pointe", 5)
	if len(near) == 0 || near[0].ID != "customfield_10042" {
		t.Fatalf("NearMatches put %v first, want the closest", ids(&site.Catalogue{Fields: near}))
	}

	// Something with nothing near it suggests nothing, rather than the whole
	// catalogue ranked by how little it matches.
	if got := catalogue.NearMatches("zzzzzzzzzzzz", 5); len(got) != 0 {
		t.Errorf("NearMatches on nonsense = %v, want none", ids(&site.Catalogue{Fields: got}))
	}

	if got := catalogue.NearMatches("Sprin", 1); len(got) != 1 {
		t.Errorf("NearMatches returned %d, want at most 1", len(got))
	}
}

// TestFieldsAreCachedAcrossProcesses is the point of the cache: resolving a
// name must not cost a round trip on every invocation. Two accessors sharing a
// directory stand in for two runs of the binary.
func TestFieldsAreCachedAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	now := testNow
	doer := &stubDoer{body: fieldCatalogue}

	first := metadataAt(doer, dir, now)
	if _, err := first.Fields(t.Context()); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := metadataAt(doer, dir, now.Add(time.Hour))
	catalogue, err := second.Fields(t.Context())
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if doer.calls != 1 {
		t.Errorf("the catalogue was fetched %d times, want 1", doer.calls)
	}
	if len(catalogue.Fields) != 4 {
		t.Errorf("the cached catalogue has %d fields, want 4", len(catalogue.Fields))
	}
	if _, err := os.Stat(filepath.Join(dir, "fields.json")); err != nil {
		t.Errorf("the catalogue is not at <site>/fields.json: %v", err)
	}
}

// TestFieldsAreMemoizedWithinOneRun covers a command that resolves a name while
// validating its flags and again while running: one request, not two.
func TestFieldsAreMemoizedWithinOneRun(t *testing.T) {
	doer := &stubDoer{body: fieldCatalogue}
	meta := &site.Metadata{Client: doer, Info: site.Info{Kind: site.Cloud}}

	for i := range 3 {
		if _, err := meta.Fields(t.Context()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if doer.calls != 1 {
		t.Errorf("the catalogue was fetched %d times, want 1", doer.calls)
	}
}

func TestStaleFieldsAreRefetched(t *testing.T) {
	dir := t.TempDir()
	now := testNow
	doer := &stubDoer{body: fieldCatalogue}

	if _, err := metadataAt(doer, dir, now).Fields(t.Context()); err != nil {
		t.Fatalf("first: %v", err)
	}
	stale := metadataAt(doer, dir, now.Add(site.DefaultTTL+time.Minute))
	if _, err := stale.Fields(t.Context()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if doer.calls != 2 {
		t.Errorf("an expired catalogue was reused: %d calls, want 2", doer.calls)
	}
}

// TestRefreshBustsTheFieldCache covers --refresh, which exists so a caller who
// just added a custom field does not have to wait out the TTL.
func TestRefreshBustsTheFieldCache(t *testing.T) {
	dir := t.TempDir()
	now := testNow
	doer := &stubDoer{body: fieldCatalogue}

	if _, err := metadataAt(doer, dir, now).Fields(t.Context()); err != nil {
		t.Fatalf("first: %v", err)
	}
	forced := metadataAt(doer, dir, now.Add(time.Minute))
	forced.Refresh = true
	if _, err := forced.Fields(t.Context()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if doer.calls != 2 {
		t.Errorf("--refresh reused the cache: %d calls, want 2", doer.calls)
	}
}

// metadataAt builds an accessor with a fixed clock, so a test can expire an
// entry without waiting a day for it.
func metadataAt(doer site.Doer, dir string, now time.Time) *site.Metadata {
	return &site.Metadata{
		Client: doer,
		Info:   site.Info{Kind: site.Cloud},
		Cache:  &site.Cache{Dir: dir},
		Now:    func() time.Time { return now },
	}
}

func load(t *testing.T) *site.Catalogue {
	t.Helper()
	catalogue, err := site.FetchFields(t.Context(),
		&stubDoer{body: fieldCatalogue}, site.Info{Kind: site.Cloud})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return catalogue
}

func ids(c *site.Catalogue) []string {
	out := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		out = append(out, f.ID)
	}
	return out
}

// pathRecordingDoer remembers what was asked for, which is how the API version
// assertion sees which endpoint a deployment got.
type pathRecordingDoer struct {
	stubDoer
	path string
}

func (p *pathRecordingDoer) Do(
	ctx context.Context, r transport.Request,
) (*transport.Response, error) {
	p.path = r.Path
	return p.stubDoer.Do(ctx, r)
}
