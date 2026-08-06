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

// The two deployments answer createmeta from the same route in different
// envelopes. Both fixtures describe the same screen, which is what lets the
// test assert the results converge — but they must not describe it in the same
// JSON, because the difference between the two envelopes is the whole point.
//
// An earlier version served one body to both. It passed, and `meta createmeta`
// was broken on Cloud the entire time: the parser read "values", Cloud returns
// "issueTypes", so a project with seven issue types reported having none by any
// requested name.
const (
	issueTypesJSON = `{"maxResults":100,"startAt":0,"total":2,"isLast":true,
		"values":[{"id":"10001","name":"Bug","subtask":false},
		          {"id":"10002","name":"Story","subtask":false}]}`

	// Cloud names the arrays "issueTypes" and "fields" and sends no isLast;
	// the loop ends on the total instead. Both are as recorded.
	issueTypesCloudJSON = `{"maxResults":100,"startAt":0,"total":2,
		"issueTypes":[{"id":"10001","name":"Bug","subtask":false},
		              {"id":"10002","name":"Story","subtask":false}]}`

	createMetaFieldsCloudJSON = `{"maxResults":100,"startAt":0,"total":3,
		"fields":[
			{"fieldId":"summary","name":"Summary","required":true,
			 "schema":{"type":"string"},"hasDefaultValue":false},
			{"fieldId":"priority","name":"Priority","required":false,
			 "schema":{"type":"priority"},"hasDefaultValue":true,
			 "allowedValues":[{"id":"1","name":"High"},{"id":"2","name":"Low"}]},
			{"fieldId":"labels","name":"Labels","required":false,
			 "schema":{"type":"array","items":"string"},"hasDefaultValue":false}]}`

	createMetaFieldsJSON = `{"maxResults":100,"startAt":0,"total":3,"isLast":true,
		"values":[
			{"fieldId":"summary","name":"Summary","required":true,
			 "schema":{"type":"string"},"hasDefaultValue":false},
			{"fieldId":"priority","name":"Priority","required":false,
			 "schema":{"type":"priority"},"hasDefaultValue":true,
			 "allowedValues":[{"id":"1","name":"High"},{"id":"2","name":"Low"}]},
			{"fieldId":"labels","name":"Labels","required":false,
			 "schema":{"type":"array","items":"string"},"hasDefaultValue":false}]}`
)

// TestCreateMetaConvergesAcrossDeployments keeps both deployments on the same
// two requests.
//
// They used to diverge here more than anywhere else: Data Center served the lot
// from one `createmeta` call filtered by projectKeys and issuetypeNames. That
// endpoint was removed in Jira 9.0, so the only route left is the pair — one to
// map a type name to an id, one to page the fields — and it is the same on both.
// What is left to assert is that the base path is the only difference.
func TestCreateMetaConvergesAcrossDeployments(t *testing.T) {
	cloud := &routingDoer{routes: map[string]string{
		"/rest/api/3/issue/createmeta/ENG/issuetypes":       issueTypesCloudJSON,
		"/rest/api/3/issue/createmeta/ENG/issuetypes/10001": createMetaFieldsCloudJSON,
	}}
	dc := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta/ENG/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10001": createMetaFieldsJSON,
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
	// Two requests on both, now that both take the same route: one to map the
	// type name to an id, one to page that type's fields.
	if dc.calls != 2 {
		t.Errorf("data center made %d requests, want 2", dc.calls)
	}
}

func TestCreateMetaCarriesAllowedValues(t *testing.T) {
	doer := &routingDoer{routes: map[string]string{
		"/rest/api/2/issue/createmeta/ENG/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10002": createMetaFieldsJSON,
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

// TestTheTwoCausesStaySeparate keeps "no such project" and "no such type"
// apart. They have different fixes, and reporting them as one leaves a caller
// guessing which of three things to check.
//
// This used to need a second probe: Data Center answered an unmatched filter
// with an empty array and a 200, so the only way to tell was to ask again
// without the type. The modern route makes it structural — the project is in
// the path, so a missing one is a 404, and a missing type is a name absent from
// what that request returned.
func TestTheTwoCausesStaySeparate(t *testing.T) {
	t.Run("unknown type lists what the project offers", func(t *testing.T) {
		doer := &routingDoer{routes: map[string]string{
			"/rest/api/2/issue/createmeta/ENG/issuetypes": issueTypesJSON,
		}}
		_, err := site.FetchCreateMeta(t.Context(), doer,
			site.Info{Kind: site.DataCenter}, "ENG", "Buug")
		if err == nil {
			t.Fatal("an unknown issue type was accepted")
		}
		e := errs.Coerce(err)
		if e.Code != "UNKNOWN_ISSUE_TYPE" {
			t.Fatalf("code = %q, want UNKNOWN_ISSUE_TYPE", e.Code)
		}
		// The types the project does offer, so a typo is one command to fix
		// rather than two.
		for _, want := range []string{"Bug", "Story"} {
			if !strings.Contains(e.Detail, want) {
				t.Errorf("the detail does not offer %q: %q", want, e.Detail)
			}
		}
	})

	t.Run("unknown project says so", func(t *testing.T) {
		doer := &statusDoer{status: 404, body: `{"errorMessages":["not found"]}`}
		_, err := site.FetchCreateMeta(t.Context(), doer,
			site.Info{Kind: site.DataCenter}, "NOPE", "Bug")
		if err == nil {
			t.Fatal("a missing project was accepted")
		}
		e := errs.Coerce(err)
		if e.Code != "UNKNOWN_PROJECT" {
			t.Errorf("code = %q, want UNKNOWN_PROJECT", e.Code)
		}
		if e.Exit != exitcode.NotFound {
			t.Errorf("exit = %v, want %v", e.Exit, exitcode.NotFound)
		}
		if !strings.Contains(e.Message, "NOPE") {
			t.Errorf("the refusal does not name the project: %q", e.Message)
		}
	})
}

// statusDoer answers everything with one status and body.
type statusDoer struct {
	status int
	body   string
	calls  int
}

func (s *statusDoer) Do(context.Context, transport.Request) (*transport.Response, error) {
	s.calls++
	return &transport.Response{
		Status: s.status,
		Body:   []byte(s.body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
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
		"/rest/api/3/issue/createmeta/ENG/issuetypes": issueTypesJSON,
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
		"/rest/api/2/issue/createmeta/ENG/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10002": createMetaFieldsJSON,
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

	if doer.calls != 2 {
		t.Errorf("createmeta cost %d requests, want the 2 one lookup takes", doer.calls)
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
		"/rest/api/2/issue/createmeta/ENG/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10002": createMetaFieldsJSON,
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
	if doer.calls != 6 {
		t.Errorf("three pairings cost %d requests, want 6 — two each", doer.calls)
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
	body := `{"maxResults":100,"startAt":0,"total":5,"isLast":true,"values":[
		{"fieldId":"named","name":"Named","required":false,"schema":{"type":"option"},
		 "allowedValues":[{"id":"1","name":"By Name"}]},
		{"fieldId":"valued","name":"Valued","required":false,"schema":{"type":"option"},
		 "allowedValues":[{"id":"2","value":"By Value"}]},
		{"fieldId":"idonly","name":"Id Only","required":false,"schema":{"type":"option"},
		 "allowedValues":[{"id":"3"}]},
		{"fieldId":"bare","name":"Bare","required":false,"schema":{"type":"option"},
		 "allowedValues":["a bare string"]},
		{"fieldId":"empty","name":"Empty","required":false,"schema":{"type":"option"},
		 "allowedValues":[{}]}]}`

	meta, err := site.FetchCreateMeta(t.Context(),
		&routingDoer{routes: map[string]string{
			"/rest/api/2/issue/createmeta/ENG/issuetypes":       issueTypesJSON,
			"/rest/api/2/issue/createmeta/ENG/issuetypes/10001": body,
		}},
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
		issueTypesJSON,
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
	// The map-of-field-id shape now reaches this only through transitions:
	// createmeta's modern route returns a list. The decoder is shared, so the
	// coverage follows it rather than being deleted with the old route.
	body := `{"transitions":[{"id":"11","name":"Start","to":{"id":"3","name":"In Progress",
		"statusCategory":{"key":"indeterminate"}},
		"fields":{"summary":"not an object"}}]}`
	_, err := site.FetchTransitions(t.Context(),
		&stubDoer{body: body}, site.Info{Kind: site.DataCenter}, "ENG-101")
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
		{site.DataCenter, "/rest/api/2/issue/createmeta/ENG/issuetypes"},
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
		"/rest/api/2/issue/createmeta/ENG/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes":       issueTypesJSON,
		"/rest/api/2/issue/createmeta/OPS/issuetypes/10001": createMetaFieldsJSON,
		"/rest/api/2/issue/createmeta/ENG/issuetypes/10002": createMetaFieldsJSON,
	}}

	if _, err := dcMetadataAt(doer, dir, testNow).CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("first: %v", err)
	}
	forced := dcMetadataAt(doer, dir, testNow.Add(time.Minute))
	forced.Refresh = true
	if _, err := forced.CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("second: %v", err)
	}
	// Two requests per lookup, so two lookups are four.
	if doer.calls != 4 {
		t.Errorf("--refresh reused the cache: %d calls, want 4", doer.calls)
	}

	// Past the TTL measured from the entry --refresh wrote, not from the first
	// one: an entry exactly at the TTL is still fresh, because the check is
	// strictly greater.
	stale := dcMetadataAt(doer, dir, testNow.Add(2*site.DefaultTTL))
	if _, err := stale.CreateMeta(t.Context(), "ENG", "Bug"); err != nil {
		t.Fatalf("third: %v", err)
	}
	if doer.calls != 6 {
		t.Errorf("an expired entry was reused: %d calls, want 6", doer.calls)
	}
}
