//go:build write

package workflow_test

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
	"github.com/kmoneil/jr/internal/workflow"
)

// TestRequestsOfReadsBackWhatDryRunDocWrote holds the two directions of a
// dry run to each other. A sequence step records the requests its command's
// dry run rendered and sends them later, so anything DryRunDoc renders and
// RequestsOf drops would be a step that applies something other than what was
// read.
func TestRequestsOfReadsBackWhatDryRunDocWrote(t *testing.T) {
	json := map[string][]string{"Content-Type": {"application/json"}}
	want := []transport.Request{
		{
			Method: transport.MethodPut, Path: "/rest/api/2/issue/ENG-1",
			Header: json, Body: []byte(`{"fields":{"summary":"a \"quoted\" <title> & more"}}`),
		},
		{
			Method: transport.MethodPost, Path: "/rest/api/2/issue/ENG-1/transitions",
			Query:  url.Values{"expand": {"transitions.fields"}, "b": {"2", "1"}},
			Header: json, Body: []byte(`{"transition":{"id":"31"}}`),
		},
		{Method: "DELETE", Path: "/rest/api/2/issueLink/10000"},
	}
	got, err := workflow.RequestsOf(registry.DryRunDoc("issue.edit", want...))
	if err != nil {
		t.Fatalf("RequestsOf: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back\n%+v\nwant\n%+v", got, want)
	}
}

// TestRequestsOfRefusesWhatIsNotADryRun keeps a command that answered with
// its result, because the dry-run flag never reached it, from being read as
// though it had planned nothing, or something.
func TestRequestsOfRefusesWhatIsNotADryRun(t *testing.T) {
	for name, doc := range map[string]*render.Doc{
		"nil":              nil,
		"another kind":     render.Record("issue.edit", 1, render.El("edited")),
		"no requests list": render.Record(registry.KindDryRun, registry.VersionDryRun, render.El("request")),
		"an empty list": render.Record(registry.KindDryRun, registry.VersionDryRun,
			render.ListEl("requests", "request")),
	} {
		if _, err := workflow.RequestsOf(doc); err == nil {
			t.Errorf("%s: read as requests", name)
		}
	}
}
