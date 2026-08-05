package site_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// The two deployments answer createmeta from different endpoints in different
// shapes. Both fixtures describe the same screen, which is what lets the test
// assert the shapes converge.
const (
	cloudIssueTypesJSON = `{"maxResults":100,"startAt":0,"total":2,"isLast":true,
		"values":[{"id":"10001","name":"Bug","subtask":false},
		          {"id":"10002","name":"Story","subtask":false}]}`

	cloudCreateMetaJSON = `{"maxResults":100,"startAt":0,"total":3,"isLast":true,
		"values":[
			{"fieldId":"summary","name":"Summary","required":true,
			 "schema":{"type":"string"},"hasDefaultValue":false},
			{"fieldId":"priority","name":"Priority","required":false,
			 "schema":{"type":"priority"},"hasDefaultValue":true,
			 "allowedValues":[{"id":"1","name":"High"},{"id":"2","name":"Low"}]},
			{"fieldId":"labels","name":"Labels","required":false,
			 "schema":{"type":"array","items":"string"},"hasDefaultValue":false}]}`

	dcCreateMetaJSON = `{"projects":[{"key":"ENG","issuetypes":[{"id":"10001","name":"Bug",
		"fields":{
			"summary":{"required":true,"name":"Summary","schema":{"type":"string"},
			           "hasDefaultValue":false},
			"priority":{"required":false,"name":"Priority","schema":{"type":"priority"},
			            "hasDefaultValue":true,
			            "allowedValues":[{"id":"1","name":"High"},{"id":"2","name":"Low"}]},
			"labels":{"required":false,"name":"Labels",
			          "schema":{"type":"array","items":"string"},"hasDefaultValue":false}}}]}]}`
)

// TestCreateMetaConvergesAcrossDeployments is why both fixtures exist. Cloud
// pages the fields and wants an issue type id; Data Center serves the lot from
// one request keyed by name. A caller must not be able to tell.
func TestCreateMetaConvergesAcrossDeployments(t *testing.T) {
	cloud := &routingDoer{routes: map[string]string{
		"/rest/api/3/issue/createmeta/ENG/issuetypes":       cloudIssueTypesJSON,
		"/rest/api/3/issue/createmeta/ENG/issuetypes/10001": cloudCreateMetaJSON,
	}}
	dc := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta": dcCreateMetaJSON,
	}}

	got := map[site.Kind]*site.CreateMeta{}
	for kind, doer := range map[site.Kind]*routingDoer{
		site.Cloud: cloud, site.DataCenter: dc,
	} {
		meta, err := site.FetchCreateMeta(t.Context(), doer, site.Info{Kind: kind}, "ENG", "Bug")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		got[kind] = meta
	}

	c, d := got[site.Cloud], got[site.DataCenter]
	if c.Project != d.Project || c.IssueType != d.IssueType {
		t.Errorf("pairing differs: %+v vs %+v", c, d)
	}
	if len(c.Fields) != len(d.Fields) {
		t.Fatalf("cloud has %d fields, dc has %d", len(c.Fields), len(d.Fields))
	}
	for i := range c.Fields {
		if !reflect.DeepEqual(c.Fields[i], d.Fields[i]) {
			t.Errorf("field %d differs:\n cloud %+v\n dc    %+v", i, c.Fields[i], d.Fields[i])
		}
	}

	// Required first, then by id. "What must I supply" is the question this
	// command is asked, so it is answerable from the top of the list.
	if c.Fields[0].ID != "summary" || !c.Fields[0].Required {
		t.Errorf("first field = %+v, want the required one", c.Fields[0])
	}
	if c.Fields[1].ID != "labels" || c.Fields[2].ID != "priority" {
		t.Errorf("optional fields are not ordered by id: %+v", c.Fields[1:])
	}

	// Cloud costs one more request, because it has to map the type name to an
	// id before it can ask for the fields.
	if cloud.calls != 2 {
		t.Errorf("cloud made %d requests, want 2", cloud.calls)
	}
	if dc.calls != 1 {
		t.Errorf("data center made %d requests, want 1", dc.calls)
	}
}

func TestCreateMetaCarriesAllowedValues(t *testing.T) {
	doer := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta": dcCreateMetaJSON,
	}}
	meta, err := site.FetchCreateMeta(t.Context(), doer,
		site.Info{Kind: site.DataCenter}, "ENG", "Bug")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	for _, f := range meta.Fields {
		if f.ID != "priority" {
			continue
		}
		if strings.Join(f.AllowedValues, ",") != "High,Low" {
			t.Errorf("allowed values = %v, want the names", f.AllowedValues)
		}
		// A required field with a default is not something the caller must
		// provide, so the two are reported separately.
		if !f.HasDefault {
			t.Error("a field with a default was reported without one")
		}
		return
	}
	t.Error("priority is missing from the result")
}

// TestDataCenterEmptyResultIsNotFound covers the shape that would otherwise
// read as success: a filter matching nothing comes back as an empty array with
// a 200, not a 404.
//
// The two causes are separated, because "the type is wrong" and "the project is
// wrong" have different fixes and reporting them as one leaves a caller
// guessing which of three things to check.
func TestDataCenterEmptyResultIsNotFound(t *testing.T) {
	t.Run("unknown type lists what the project offers", func(t *testing.T) {
		doer := &sequencingDoer{bodies: []string{
			`{"projects":[]}`,
			`{"projects":[{"key":"ENG","issuetypes":[
				{"id":"10001","name":"Bug"},{"id":"10002","name":"Story"}]}]}`,
		}}
		_, err := site.FetchCreateMeta(t.Context(), doer,
			site.Info{Kind: site.DataCenter}, "ENG", "Buug")
		if err == nil {
			t.Fatal("an empty createmeta result was reported as success")
		}
		e := errs.Coerce(err)
		if e.Code != "UNKNOWN_ISSUE_TYPE" {
			t.Errorf("code = %q, want UNKNOWN_ISSUE_TYPE", e.Code)
		}
		if e.Exit != exitcode.NotFound {
			t.Errorf("exit = %v, want %v", e.Exit, exitcode.NotFound)
		}
		// The same help Cloud gives, from a deployment whose endpoint does not
		// resolve the name for us.
		if !strings.Contains(e.Detail, "Bug (10001)") ||
			!strings.Contains(e.Detail, "Story (10002)") {
			t.Errorf("the detail does not list the available types: %q", e.Detail)
		}
	})

	t.Run("unknown project says so", func(t *testing.T) {
		doer := &sequencingDoer{bodies: []string{
			`{"projects":[]}`,
			`{"projects":[]}`,
		}}
		_, err := site.FetchCreateMeta(t.Context(), doer,
			site.Info{Kind: site.DataCenter}, "NOPE", "Bug")
		if err == nil {
			t.Fatal("an unknown project was reported as success")
		}
		if code := errs.Coerce(err).Code; code != "UNKNOWN_PROJECT" {
			t.Errorf("code = %q, want UNKNOWN_PROJECT", code)
		}
	})

	t.Run("a failed diagnostic lookup does not replace the answer", func(t *testing.T) {
		doer := &sequencingDoer{bodies: []string{`{"projects":[]}`}, failAfter: 1}
		_, err := site.FetchCreateMeta(t.Context(), doer,
			site.Info{Kind: site.DataCenter}, "ENG", "Bug")
		if err == nil {
			t.Fatal("an empty createmeta result was reported as success")
		}
		// The first request established that nothing matched. A second request
		// failing must not turn that into an unrelated error.
		if code := errs.Coerce(err).Code; code != "UNKNOWN_PROJECT" {
			t.Errorf("code = %q, want UNKNOWN_PROJECT", code)
		}
	})
}

// sequencingDoer answers each call with the next body, which is how a
// two-request conversation to the same path is replayed.
type sequencingDoer struct {
	bodies []string
	calls  int
	// failAfter makes every call past this index return a server error, so a
	// diagnostic lookup can be made to fail.
	failAfter int
}

func (s *sequencingDoer) Do(
	context.Context, transport.Request,
) (*transport.Response, error) {
	s.calls++
	if s.failAfter > 0 && s.calls > s.failAfter {
		return &transport.Response{
			Status: 500,
			Body:   []byte(`{"errorMessages":["boom"]}`),
			Header: map[string][]string{"Content-Type": {"application/json"}},
		}, nil
	}
	body := `{"projects":[]}`
	if s.calls <= len(s.bodies) {
		body = s.bodies[s.calls-1]
	}
	return &transport.Response{
		Status: 200,
		Body:   []byte(body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestUnknownIssueTypeListsWhatTheProjectOffers is the Cloud path's version of
// "nothing is guessed": the type name is resolved before the fields are asked
// for, so a typo is refused with the alternatives rather than 404ing on an id
// that was never looked up.
func TestUnknownIssueTypeListsWhatTheProjectOffers(t *testing.T) {
	doer := &routingDoer{routes: map[string]string{
		"/rest/api/3/issue/createmeta/ENG/issuetypes": cloudIssueTypesJSON,
	}}
	_, err := site.FetchCreateMeta(t.Context(), doer, site.Info{Kind: site.Cloud}, "ENG", "Buug")
	if err == nil {
		t.Fatal("an unknown issue type was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_ISSUE_TYPE" {
		t.Errorf("code = %q, want UNKNOWN_ISSUE_TYPE", e.Code)
	}
	if !strings.Contains(e.Detail, "Bug (10001)") || !strings.Contains(e.Detail, "Story (10002)") {
		t.Errorf("the detail does not list the available types: %q", e.Detail)
	}
	// It never reached the fields endpoint, so the refusal cost one request.
	if doer.calls != 1 {
		t.Errorf("made %d requests before refusing, want 1", doer.calls)
	}
}

func TestResolveIssueTypeRefusesAmbiguity(t *testing.T) {
	types := []site.IssueType{
		{ID: "10001", Name: "Task"},
		{ID: "10009", Name: "task"},
	}
	_, err := site.ResolveIssueType(types, "Task")
	if err == nil {
		t.Fatal("an ambiguous issue type resolved to one of the candidates")
	}
	if code := errs.Coerce(err).Code; code != "AMBIGUOUS_ISSUE_TYPE" {
		t.Errorf("code = %q, want AMBIGUOUS_ISSUE_TYPE", code)
	}

	// An exact id match is never ambiguous, even when a name collides.
	got, err := site.ResolveIssueType(types, "10009")
	if err != nil || got.ID != "10009" {
		t.Errorf("ResolveIssueType by id = %+v, %v", got, err)
	}
}

// TestCreateMetaIsCached is the difference from transitions: a create screen
// changes when an administrator edits it, so a day-old answer is still the
// answer.
func TestCreateMetaIsCached(t *testing.T) {
	dir := t.TempDir()
	doer := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta": dcCreateMetaJSON,
	}}

	first := dcMetadataAt(doer, dir, testNow)
	cold, err := first.CreateMeta(t.Context(), "ENG", "Bug")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second := dcMetadataAt(doer, dir, testNow.Add(time.Hour))
	warm, err := second.CreateMeta(t.Context(), "ENG", "Bug")
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if doer.calls != 1 {
		t.Errorf("createmeta was fetched %d times, want 1", doer.calls)
	}
	// A warm cache returns what a cold one did. The server echoes the resolved
	// pairing, and returning the requested one on a hit would make the output
	// depend on whether the cache was warm.
	if cold.Project != warm.Project || cold.IssueType != warm.IssueType {
		t.Errorf("cold %+v and warm %+v disagree on the pairing", cold, warm)
	}
	if len(cold.Fields) != len(warm.Fields) {
		t.Errorf("cold has %d fields, warm has %d", len(cold.Fields), len(warm.Fields))
	}
}

// TestCreateMetaCacheIsPerPairing stops one issue type's required fields being
// served as another's.
func TestCreateMetaCacheIsPerPairing(t *testing.T) {
	dir := t.TempDir()
	doer := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta": dcCreateMetaJSON,
	}}
	meta := dcMetadataAt(doer, dir, testNow)

	if _, err := meta.CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("bug: %v", err)
	}
	if _, err := meta.CreateMeta(t.Context(), "ENG", "Story"); err != nil {
		t.Fatalf("story: %v", err)
	}
	if _, err := meta.CreateMeta(t.Context(), "OPS", "Bug"); err != nil {
		t.Fatalf("other project: %v", err)
	}
	if doer.calls != 3 {
		t.Errorf("three pairings cost %d requests, want 3", doer.calls)
	}
}

func dcMetadataAt(doer site.Doer, dir string, now time.Time) *site.Metadata {
	return &site.Metadata{
		Client: doer,
		Info:   site.Info{Kind: site.DataCenter},
		Cache:  &site.Cache{Dir: dir},
		Now:    func() time.Time { return now },
	}
}

// routingDoer answers by path, so a two-request conversation can be replayed
// without a server. An unrouted path is a failure rather than an empty body:
// a silent 200 with nothing in it is how a test passes for the wrong reason.
type routingDoer struct {
	routes map[string]string
	calls  int
	paths  []string
}

func (r *routingDoer) Do(
	_ context.Context, req transport.Request,
) (*transport.Response, error) {
	r.calls++
	r.paths = append(r.paths, req.Path)

	body, ok := r.routes[req.Path]
	if !ok {
		return &transport.Response{
			Status: 404,
			Body:   []byte(`{"errorMessages":["no route"]}`),
			Header: map[string][]string{"Content-Type": {"application/json"}},
		}, nil
	}
	return &transport.Response{
		Status: 200,
		Body:   []byte(body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestAllowedValuesCoverEveryShape covers the several ways Jira describes a
// constrained value. A shape that reduces to nothing is dropped rather than
// rendered as an empty string, because an empty cell in a list of choices reads
// as a choice.
func TestAllowedValuesCoverEveryShape(t *testing.T) {
	body := `{"projects":[{"key":"ENG","issuetypes":[{"id":"1","name":"Bug","fields":{
		"named":{"required":false,"name":"Named","schema":{"type":"option"},
		         "allowedValues":[{"id":"1","name":"By Name"}]},
		"valued":{"required":false,"name":"Valued","schema":{"type":"option"},
		          "allowedValues":[{"id":"2","value":"By Value"}]},
		"idonly":{"required":false,"name":"Id Only","schema":{"type":"option"},
		          "allowedValues":[{"id":"3"}]},
		"bare":{"required":false,"name":"Bare","schema":{"type":"option"},
		        "allowedValues":["a bare string"]},
		"empty":{"required":false,"name":"Empty","schema":{"type":"option"},
		         "allowedValues":[{}]}
	}}]}]}`

	meta, err := site.FetchCreateMeta(t.Context(),
		&routingDoer{routes: map[string]string{"/rest/api/2/issue/createmeta": body}},
		site.Info{Kind: site.DataCenter}, "ENG", "Bug")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	want := map[string][]string{
		"named":  {"By Name"},
		"valued": {"By Value"},
		"idonly": {"3"},
		"bare":   {"a bare string"},
		"empty":  {},
	}
	for _, f := range meta.Fields {
		expected, ok := want[f.ID]
		if !ok {
			t.Errorf("unexpected field %q", f.ID)
			continue
		}
		if strings.Join(f.AllowedValues, ",") != strings.Join(expected, ",") {
			t.Errorf("%s allowed values = %v, want %v", f.ID, f.AllowedValues, expected)
		}
	}
}

// TestCloudPagesEveryField is the one that would fail silently. A project with
// more fields than a page holds would otherwise report a short list — and a
// short list of *required* fields is a create that fails on a field the caller
// was never told about.
func TestCloudPagesEveryField(t *testing.T) {
	doer := &sequencingDoer{bodies: []string{
		cloudIssueTypesJSON,
		`{"total":4,"isLast":false,"values":[
			{"fieldId":"a","name":"A","required":true,"schema":{"type":"string"}},
			{"fieldId":"b","name":"B","required":false,"schema":{"type":"string"}}]}`,
		`{"total":4,"isLast":true,"values":[
			{"fieldId":"c","name":"C","required":true,"schema":{"type":"string"}},
			{"fieldId":"d","name":"D","required":false,"schema":{"type":"string"}}]}`,
	}}

	meta, err := site.FetchCreateMeta(t.Context(), doer, site.Info{Kind: site.Cloud}, "ENG", "Bug")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(meta.Fields) != 4 {
		t.Fatalf("got %d fields across pages, want 4: %+v", len(meta.Fields), meta.Fields)
	}
	// Required first, so the sort spans pages rather than being applied per
	// page and leaving an optional field above a required one.
	if !meta.Fields[0].Required || !meta.Fields[1].Required {
		t.Errorf("required fields did not sort to the top across pages: %+v", meta.Fields)
	}
}

// TestCloudPagesIssueTypes covers the same trap one endpoint earlier: a project
// with more types than a page holds must not lose the one being resolved.
func TestCloudPagesIssueTypes(t *testing.T) {
	doer := &sequencingDoer{bodies: []string{
		`{"total":3,"isLast":false,"values":[{"id":"1","name":"Bug"}]}`,
		`{"total":3,"isLast":false,"values":[{"id":"2","name":"Story"}]}`,
		`{"total":3,"isLast":true,"values":[{"id":"3","name":"Epic"}]}`,
	}}

	types, err := site.FetchIssueTypes(t.Context(), doer, site.Info{Kind: site.Cloud}, "ENG")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(types) != 3 {
		t.Fatalf("got %d types across pages, want 3: %+v", len(types), types)
	}
	if _, err := site.ResolveIssueType(types, "Epic"); err != nil {
		t.Errorf("a type on the last page did not resolve: %v", err)
	}
}

// TestPagingStopsOnAnEmptyPage stops a server that never sets isLast from
// spinning forever. A loop bounded only by what the server claims is a loop the
// server controls.
func TestPagingStopsOnAnEmptyPage(t *testing.T) {
	doer := &sequencingDoer{bodies: []string{
		`{"total":99,"isLast":false,"values":[{"id":"1","name":"Bug"}]}`,
		`{"total":99,"isLast":false,"values":[]}`,
	}}

	types, err := site.FetchIssueTypes(t.Context(), doer, site.Info{Kind: site.Cloud}, "ENG")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(types) != 1 {
		t.Errorf("got %d types, want the one that arrived", len(types))
	}
	if doer.calls != 2 {
		t.Errorf("made %d requests, want 2 — a page that added nothing ends the loop", doer.calls)
	}
}

// TestResolveIssueTypeRefusesEmpty covers the guard, and the shape of the
// refusal when a project offers nothing to suggest.
func TestResolveIssueTypeRefusesEmpty(t *testing.T) {
	if _, err := site.ResolveIssueType(nil, "  "); err == nil {
		t.Error("an empty issue type was accepted")
	} else if code := errs.Coerce(err).Code; code != "INVALID_ISSUE_TYPE" {
		t.Errorf("code = %q, want INVALID_ISSUE_TYPE", code)
	}

	_, err := site.ResolveIssueType(nil, "Bug")
	if err == nil {
		t.Fatal("a type resolved against an empty project")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_ISSUE_TYPE" || e.Detail != "" {
		t.Errorf("got %q / %q, want UNKNOWN_ISSUE_TYPE with nothing to suggest",
			e.Code, e.Detail)
	}
}

// TestMalformedFieldMetaIsRefused covers a field described in a shape this tool
// cannot read. Skipping it would produce a create screen missing a required
// field, which fails later and somewhere else.
func TestMalformedFieldMetaIsRefused(t *testing.T) {
	body := `{"projects":[{"key":"ENG","issuetypes":[{"id":"1","name":"Bug",
		"fields":{"summary":"not an object"}}]}]}`
	_, err := site.FetchCreateMeta(t.Context(),
		&routingDoer{routes: map[string]string{"/rest/api/2/issue/createmeta": body}},
		site.Info{Kind: site.DataCenter}, "ENG", "Bug")
	if err == nil {
		t.Fatal("a field described as a string was accepted")
	}
	if code := errs.Coerce(err).Code; code != "MALFORMED_FIELD_META" {
		t.Errorf("code = %q, want MALFORMED_FIELD_META", code)
	}
}

// TestCreateMetaRefusesAnUnusableBody covers both deployments' decode guards.
func TestCreateMetaRefusesAnUnusableBody(t *testing.T) {
	for _, tc := range []struct {
		kind  site.Kind
		route string
	}{
		{site.DataCenter, "/rest/api/2/issue/createmeta"},
		{site.Cloud, "/rest/api/3/issue/createmeta/ENG/issuetypes"},
	} {
		doer := &routingDoer{routes: map[string]string{tc.route: `<html>not jira</html>`}}
		_, err := site.FetchCreateMeta(t.Context(), doer, site.Info{Kind: tc.kind}, "ENG", "Bug")
		if err == nil {
			t.Fatalf("%s accepted an HTML body", tc.kind)
		}
		if code := errs.Coerce(err).Code; code != "MALFORMED_CREATEMETA" {
			t.Errorf("%s code = %q, want MALFORMED_CREATEMETA", tc.kind, code)
		}
	}
}

// TestRefreshBustsTheCreateMetaCache covers --refresh, so somebody who just
// changed a screen does not have to wait out the TTL.
func TestRefreshBustsTheCreateMetaCache(t *testing.T) {
	dir := t.TempDir()
	doer := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta": dcCreateMetaJSON,
	}}

	if _, err := dcMetadataAt(doer, dir, testNow).CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("first: %v", err)
	}
	forced := dcMetadataAt(doer, dir, testNow.Add(time.Minute))
	forced.Refresh = true
	if _, err := forced.CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if doer.calls != 2 {
		t.Errorf("--refresh reused the cache: %d calls, want 2", doer.calls)
	}

	// Past the TTL measured from the entry --refresh wrote, not from the first
	// one: an entry exactly at the TTL is still fresh, because the check is
	// strictly greater.
	stale := dcMetadataAt(doer, dir, testNow.Add(2*site.DefaultTTL))
	if _, err := stale.CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("third: %v", err)
	}
	if doer.calls != 3 {
		t.Errorf("an expired entry was reused: %d calls, want 3", doer.calls)
	}
}
