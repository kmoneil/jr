package site_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// labelDoer answers the suggestion route the way both deployments do: it
// filters case-insensitively on a prefix and truncates at the cap.
//
// Answering from a set rather than from a canned body is what lets one harness
// cover the cases that differ only in how many labels share a prefix, which is
// where the cap and the ordering live.
type labelDoer struct {
	labels []string
	// dead answers every request with an empty result set, which is what the
	// route does for a field it does not know.
	dead    bool
	asked   []string
	paths   []string
	queries []url.Values
	calls   int
	broken  string
}

func (d *labelDoer) Do(
	_ context.Context, r transport.Request,
) (*transport.Response, error) {
	d.calls++
	d.paths = append(d.paths, r.Path)
	d.queries = append(d.queries, r.Query)
	prefix := r.Query.Get("fieldValue")
	d.asked = append(d.asked, prefix)

	var matched []string
	if !d.dead {
		for _, l := range d.labels {
			if strings.HasPrefix(strings.ToLower(l), strings.ToLower(prefix)) {
				matched = append(matched, l)
			}
			if len(matched) == site.SuggestionCap {
				break
			}
		}
	}

	type result struct {
		Value       string `json:"value"`
		DisplayName string `json:"displayName"`
	}
	body := struct {
		Results []result `json:"results"`
	}{Results: make([]result, 0, len(matched))}
	for _, l := range matched {
		body.Results = append(body.Results, result{Value: renderLabel(l), DisplayName: l})
	}
	if d.broken != "" {
		body.Results = append(body.Results, result{Value: d.broken})
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &transport.Response{
		Status: 200,
		Body:   encoded,
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// renderLabel is how the server spells a label back: bare when it can be, and
// quoted with JQL's escaping when it cannot. Recorded from Jira Software Data
// Center 9.12.38, from 10.4.1, and from Cloud on 2026-08-13, which agree.
func renderLabel(l string) string {
	if !strings.ContainsAny(l, `,"\[]() `) {
		return l
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(l) + `"`
}

func TestSuggestLabelsUsesTheDeploymentsAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		want string
	}{
		{site.Cloud, "/rest/api/3/jql/autocompletedata/suggestions"},
		{site.DataCenter, "/rest/api/2/jql/autocompletedata/suggestions"},
	} {
		doer := &labelDoer{labels: []string{"retry"}}
		if _, err := site.SuggestLabels(t.Context(), doer, site.Info{Kind: tc.kind}, "re"); err != nil {
			t.Fatalf("%s: suggest: %v", tc.kind, err)
		}
		if doer.paths[0] != tc.want {
			t.Errorf("%s asked for %q, want %q", tc.kind, doer.paths[0], tc.want)
		}
	}
}

// TestSuggestLabelsAsksForLabelsAndNothingElse pins the two parameters and the
// one that must stay absent: both Data Center lines answer a request
// carrying predicateName with a 500.
func TestSuggestLabelsAsksForLabelsAndNothingElse(t *testing.T) {
	doer := &labelDoer{labels: []string{"retry"}}
	if _, err := site.SuggestLabels(t.Context(), doer, site.Info{Kind: site.Cloud}, "re"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	query := doer.queries[0]
	if got := query.Get("fieldName"); got != "labels" {
		t.Errorf("fieldName is %q, want labels", got)
	}
	if got := query.Get("fieldValue"); got != "re" {
		t.Errorf("fieldValue is %q, want re", got)
	}
	if _, ok := query["predicateName"]; ok {
		t.Error("predicateName was sent; Data Center answers that with a 500")
	}
}

// TestSuggestLabelsReadsTheServersQuoting is the case that separates comparing
// values from comparing spellings. Every one of these labels is a value Jira
// stores and hands back quoted.
func TestSuggestLabelsReadsTheServersQuoting(t *testing.T) {
	labels := []string{`a,b`, `back\slash`, `q"uote`, `brac[ket]`, `uni-café`}
	doer := &labelDoer{labels: labels}

	got, err := site.SuggestLabels(t.Context(), doer, site.Info{Kind: site.Cloud}, "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(got) != len(labels) {
		t.Fatalf("got %d labels, want %d", len(got), len(labels))
	}
	for i, want := range labels {
		if got[i] != want {
			t.Errorf("label %d decoded to %q, want %q", i, got[i], want)
		}
	}
}

func TestSuggestLabelsRefusesAValueItCannotRead(t *testing.T) {
	doer := &labelDoer{labels: []string{"retry"}, broken: `"unclosed`}

	_, err := site.SuggestLabels(t.Context(), doer, site.Info{Kind: site.Cloud}, "re")
	if err == nil {
		t.Fatal("an unreadable value was accepted")
	}
	if !strings.Contains(err.Error(), "MALFORMED_LABEL_SUGGESTIONS") {
		t.Errorf("error is %v, want MALFORMED_LABEL_SUGGESTIONS", err)
	}
}

func TestUnknownLabelsReportsOnlyWhatNothingCarries(t *testing.T) {
	doer := &labelDoer{labels: []string{"retry", "transport", `a,b`}}

	unknown, err := site.UnknownLabels(
		t.Context(), doer, site.Info{Kind: site.Cloud},
		[]string{"retry", "retyr", `a,b`, "RETRY"},
	)
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "retyr" {
		t.Fatalf("reported %v, want [retyr]", unknown)
	}

	// retry, retyr, a,b, with RETRY folding onto retry, plus the one that
	// checks the route is answering at all.
	if doer.calls != 4 {
		t.Errorf("spent %d requests, want 4: %v", doer.calls, doer.asked)
	}
}

func TestUnknownLabelsCostsNothingWhenEveryLabelIsKnown(t *testing.T) {
	doer := &labelDoer{labels: []string{"retry", "transport"}}

	unknown, err := site.UnknownLabels(
		t.Context(), doer, site.Info{Kind: site.Cloud}, []string{"retry", "transport"},
	)
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if unknown != nil {
		t.Errorf("reported %v, want nothing", unknown)
	}
	if doer.calls != 2 {
		t.Errorf("spent %d requests, want 2: the liveness check is only for a warning", doer.calls)
	}
}

// TestUnknownLabelsSaysNothingWhenTheAnswerIsFull is the guard against a
// warning that rests on an ordering the server does not document.
func TestUnknownLabelsSaysNothingWhenTheAnswerIsFull(t *testing.T) {
	crowd := make([]string, 0, site.SuggestionCap)
	for i := range site.SuggestionCap {
		crowd = append(crowd, "zz-"+string(rune('a'+i)))
	}
	doer := &labelDoer{labels: crowd}

	unknown, err := site.UnknownLabels(
		t.Context(), doer, site.Info{Kind: site.Cloud}, []string{"zz"},
	)
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if unknown != nil {
		t.Errorf("reported %v; the exact match could have been past the cap", unknown)
	}
}

// TestUnknownLabelsSaysNothingWhenTheRouteSaysNothing is the difference
// between a site with no labels and a deployment that does not answer this
// question. Both are 200 with an empty list, and warning about the second
// would put an invented warning on every correct query.
func TestUnknownLabelsSaysNothingWhenTheRouteSaysNothing(t *testing.T) {
	doer := &labelDoer{dead: true}

	unknown, err := site.UnknownLabels(
		t.Context(), doer, site.Info{Kind: site.DataCenter}, []string{"retry"},
	)
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if unknown != nil {
		t.Errorf("reported %v against a route that answers nothing", unknown)
	}
	if doer.calls != 2 {
		t.Errorf("spent %d requests, want 2", doer.calls)
	}
}

func TestUnknownLabelsIgnoresBlanks(t *testing.T) {
	doer := &labelDoer{labels: []string{"retry"}}

	unknown, err := site.UnknownLabels(
		t.Context(), doer, site.Info{Kind: site.Cloud}, []string{"", "   "},
	)
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if unknown != nil || doer.calls != 0 {
		t.Errorf("reported %v after %d requests, want nothing and none", unknown, doer.calls)
	}
}

func TestUnknownLabelsPropagatesAFailure(t *testing.T) {
	doer := &stubDoer{status: 500, body: `{"errorMessages":["boom"]}`}

	if _, err := site.UnknownLabels(
		t.Context(), doer, site.Info{Kind: site.Cloud}, []string{"retry"},
	); err == nil {
		t.Fatal("a 500 was swallowed here; the caller decides what to do with it")
	}
}

func TestMetadataUnknownLabelsResolvesAgainstItsOwnSite(t *testing.T) {
	doer := &labelDoer{labels: []string{"retry"}}
	meta := &site.Metadata{Client: doer, Info: site.Info{Kind: site.DataCenter}}

	unknown, err := meta.UnknownLabels(t.Context(), []string{"retyr"})
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "retyr" {
		t.Fatalf("reported %v, want [retyr]", unknown)
	}
	if !strings.HasPrefix(doer.paths[0], "/rest/api/2/") {
		t.Errorf("asked %q, want the Data Center base", doer.paths[0])
	}
}
