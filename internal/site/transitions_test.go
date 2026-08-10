package site_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/site"
)

// transitionsJSON is what /issue/{key}/transitions returns on both
// deployments. The ids are numeric strings, which is the trap: "11" sorts below
// "2" as text.
const transitionsJSON = `{"expand":"transitions","transitions":[
	{"id":"11","name":"Start Progress","hasScreen":false,
	 "to":{"id":"3","name":"In Progress",
	       "statusCategory":{"key":"indeterminate","name":"In Progress"}}},
	{"id":"2","name":"Close Issue","hasScreen":true,
	 "to":{"id":"6","name":"Closed","statusCategory":{"key":"done","name":"Done"}},
	 "fields":{"resolution":{"required":true,"name":"Resolution",
	                         "schema":{"type":"resolution"},
	                         "allowedValues":[{"id":"1","name":"Fixed"},
	                                          {"id":"2","name":"Won't Do"}]},
	           "comment":{"required":false,"name":"Comment",
	                      "schema":{"type":"comment"}}}},
	{"id":"31","name":"Reopen","hasScreen":false,
	 "to":{"id":"1","name":"Open","statusCategory":{"key":"new","name":"To Do"}}}
]}`

// TestFetchTransitionsUsesTheDeploymentsAPIVersion covers the one branch this
// function has between the two deployments.
//
// Every other test here drives it as Cloud, so a mutation of Info.APIBase's
// Data Center arm went unnoticed in this file while createmeta, fields, the
// probe, and user search all caught it. The response shape is genuinely shared
// — rawTransitions says so — which is exactly why the path is the only thing
// left to get wrong, and why nothing else would notice if it were.
func TestFetchTransitionsUsesTheDeploymentsAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		want string
	}{
		{site.Cloud, "/rest/api/3/issue/ENG-101/transitions"},
		{site.DataCenter, "/rest/api/2/issue/ENG-101/transitions"},
	} {
		doer := &pathRecordingDoer{stubDoer: stubDoer{body: transitionsJSON}}
		if _, err := site.FetchTransitions(
			t.Context(), doer, site.Info{Kind: tc.kind}, "ENG-101",
		); err != nil {
			t.Fatalf("%s: fetch: %v", tc.kind, err)
		}
		if doer.path != tc.want {
			t.Errorf("%s asked for %q, want %q", tc.kind, doer.path, tc.want)
		}
	}
}

func TestFetchTransitionsNormalizes(t *testing.T) {
	doer := &stubDoer{body: transitionsJSON}
	got, err := site.FetchTransitions(t.Context(), doer, site.Info{Kind: site.Cloud}, "ENG-101")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if got.IssueKey != "ENG-101" {
		t.Errorf("IssueKey = %q, want the issue it was read for", got.IssueKey)
	}
	if len(got.Items) != 3 {
		t.Fatalf("got %d transitions, want 3", len(got.Items))
	}

	// Ordered numerically: 2, 11, 31. As text it would be 11, 2, 31 — the same
	// trap issue keys have, in a place nobody would think to look.
	var ids []string
	for _, item := range got.Items {
		ids = append(ids, item.ID)
	}
	if strings.Join(ids, ",") != "2,11,31" {
		t.Errorf("ids = %v, want them ordered numerically", ids)
	}

	closeIssue := got.Items[0]
	if closeIssue.To.Category != site.CategoryDone {
		t.Errorf("category = %q, want %q", closeIssue.To.Category, site.CategoryDone)
	}
	if !closeIssue.HasScreen {
		t.Error("a transition with a screen was reported without one")
	}
	// Required fields come first, so "what must I supply" is answerable from
	// the top of the list.
	if len(closeIssue.Fields) != 2 || closeIssue.Fields[0].ID != "resolution" {
		t.Fatalf("fields = %+v, want the required one first", closeIssue.Fields)
	}
	if got := closeIssue.Fields[0].AllowedValues; strings.Join(got, ",") != "Fixed,Won't Do" {
		t.Errorf("allowed values = %v, want the names", got)
	}
}

// TestTransitionsAreNeverCached is the deliberate difference from the field
// catalogue. Transitions depend on the issue's current status, so a stored copy
// answers the question as it stood when it was stored.
func TestTransitionsAreNeverCached(t *testing.T) {
	dir := t.TempDir()
	doer := &stubDoer{body: transitionsJSON}
	meta := metadataAt(doer, dir, testNow)

	for i := range 3 {
		if _, err := meta.Transitions(t.Context(), "ENG-101"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if doer.calls != 3 {
		t.Errorf("transitions were fetched %d times, want 3 — a cached list "+
			"would answer a stale question", doer.calls)
	}
}

// TestResolveTransitionByNameAndId is the card's headline: a name resolves to
// the id `issue move` has to send.
func TestResolveTransitionByNameAndId(t *testing.T) {
	transitions := loadTransitions(t)

	for input, want := range map[string]string{
		"Start Progress": "11",
		"start progress": "11",
		"Close Issue":    "2",
		"31":             "31",
	} {
		got, err := transitions.Resolve(input)
		if err != nil {
			t.Errorf("Resolve(%q) = %v", input, err)
			continue
		}
		if got.ID != want {
			t.Errorf("Resolve(%q) = %q, want %q", input, got.ID, want)
		}
	}
}

// TestUnknownTransitionListsWhatIsAvailable covers the difference from a field
// typo: a transition missing from the list is usually blocked from the current
// status, so the whole available set is more useful than near matches.
func TestUnknownTransitionListsWhatIsAvailable(t *testing.T) {
	transitions := loadTransitions(t)

	_, err := transitions.Resolve("Resolve Issue")
	if err == nil {
		t.Fatal("an unavailable transition was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_TRANSITION" {
		t.Errorf("code = %q, want UNKNOWN_TRANSITION", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Usage)
	}
	if !strings.Contains(e.Message, "ENG-101") {
		t.Errorf("the message does not name the issue: %q", e.Message)
	}
	for _, want := range []string{"Start Progress (11", "Close Issue (2", "Reopen (31"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("the detail does not list %s: %q", want, e.Detail)
		}
	}
	// The destination matters: two transitions can share a name and go
	// somewhere different.
	if !strings.Contains(e.Detail, "In Progress") {
		t.Errorf("the detail does not say where a transition leads: %q", e.Detail)
	}
}

// TestAmbiguousTransitionIsRefused covers a workflow offering two moves with
// one name. Picking either would move the issue somewhere unasked for.
func TestAmbiguousTransitionIsRefused(t *testing.T) {
	transitions := &site.Transitions{IssueKey: "ENG-1", Items: []site.Transition{
		{ID: "21", Name: "Done", To: site.Status{ID: "6", Name: "Closed"}},
		{ID: "22", Name: "Done", To: site.Status{ID: "7", Name: "Resolved"}},
	}}

	_, err := transitions.Resolve("Done")
	if err == nil {
		t.Fatal("an ambiguous transition resolved to one of the candidates")
	}
	e := errs.Coerce(err)
	if e.Code != "AMBIGUOUS_TRANSITION" {
		t.Errorf("code = %q, want AMBIGUOUS_TRANSITION", e.Code)
	}
	for _, id := range []string{"21", "22"} {
		if !strings.Contains(e.Detail, id) {
			t.Errorf("the detail does not list %s: %q", id, e.Detail)
		}
	}
}

// TestAnIssueWithNoTransitionsSaysSo separates "you named the wrong one" from
// "you cannot move this at all", which are different problems with different
// fixes.
func TestAnIssueWithNoTransitionsSaysSo(t *testing.T) {
	transitions := &site.Transitions{IssueKey: "ENG-1"}

	_, err := transitions.Resolve("Done")
	if err == nil {
		t.Fatal("a transition resolved against an empty workflow")
	}
	e := errs.Coerce(err)
	if !strings.Contains(e.Detail, "no transitions at all") {
		t.Errorf("the detail does not distinguish an empty workflow: %q", e.Detail)
	}
	if !strings.Contains(e.Remedy, "credential") {
		t.Errorf("the remedy does not point at permissions: %q", e.Remedy)
	}
}

func TestFetchTransitionsRefusesAnUnusableBody(t *testing.T) {
	doer := &stubDoer{body: `<html>not jira</html>`}
	_, err := site.FetchTransitions(t.Context(), doer, site.Info{Kind: site.Cloud}, "ENG-1")
	if err == nil {
		t.Fatal("an HTML body was accepted as a transition list")
	}
	if code := errs.Coerce(err).Code; code != "MALFORMED_TRANSITIONS" {
		t.Errorf("code = %q, want MALFORMED_TRANSITIONS", code)
	}
}

func loadTransitions(t *testing.T) *site.Transitions {
	t.Helper()
	got, err := site.FetchTransitions(t.Context(),
		&stubDoer{body: transitionsJSON}, site.Info{Kind: site.Cloud}, "ENG-101")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return got
}

// TestTransitionIdsSurviveOddServers covers the ordering fallbacks. Jira sends
// numeric ids, but a plugin can send anything, and the order still has to be
// total — an unstable order means a paged or diffed result changes between runs
// for no reason a caller can see.
func TestTransitionIdsSurviveOddServers(t *testing.T) {
	body := `{"transitions":[
		{"id":"31","name":"c","to":{"id":"1","name":"Open"}},
		{"id":"2","name":"a","to":{"id":"1","name":"Open"}},
		{"id":"custom-b","name":"e","to":{"id":"1","name":"Open"}},
		{"id":"99999999999999999999","name":"f","to":{"id":"1","name":"Open"}},
		{"id":"custom-a","name":"d","to":{"id":"1","name":"Open"}},
		{"id":"11","name":"b","to":{"id":"1","name":"Open"}}
	]}`
	got, err := site.FetchTransitions(t.Context(),
		&stubDoer{body: body}, site.Info{Kind: site.Cloud}, "ENG-1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	var ids []string
	for _, item := range got.Items {
		ids = append(ids, item.ID)
	}
	// Numeric ids first and in numeric order; everything else after, in text
	// order. An id long enough to overflow falls back to text rather than
	// wrapping to a small number and sorting first.
	want := "2,11,31,99999999999999999999,custom-a,custom-b"
	if strings.Join(ids, ",") != want {
		t.Errorf("ids = %v\nwant %s", ids, want)
	}
}

// TestStatusCategoryFallsBackToTheName covers the Data Center versions that
// send a category name and no key. Without the fallback every status on those
// servers reports as unknown, which is the field an automated caller branches
// on.
func TestStatusCategoryFallsBackToTheName(t *testing.T) {
	for _, tc := range []struct{ key, name, want string }{
		{"new", "", site.CategoryToDo},
		{"undefined", "", site.CategoryToDo},
		{"indeterminate", "", site.CategoryInProgress},
		{"done", "", site.CategoryDone},
		{"", "To Do", site.CategoryToDo},
		{"", "New", site.CategoryToDo},
		{"", "In Progress", site.CategoryInProgress},
		{"", "Done", site.CategoryDone},
		{"", "Complete", site.CategoryDone},
		{"", "", site.CategoryUnknown},
		{"", "Something A Plugin Invented", site.CategoryUnknown},
	} {
		if got := site.NormalizeCategory(tc.key, tc.name); got != tc.want {
			t.Errorf("NormalizeCategory(%q, %q) = %q, want %q",
				tc.key, tc.name, got, tc.want)
		}
	}
}

// TestResolveRefusesEmptyInput covers the guard that would otherwise let an
// empty --transition match a transition whose name is also empty.
func TestResolveRefusesEmptyInput(t *testing.T) {
	transitions := loadTransitions(t)
	for _, input := range []string{"", "   "} {
		if _, err := transitions.Resolve(input); err == nil {
			t.Errorf("Resolve(%q) was accepted", input)
		} else if code := errs.Coerce(err).Code; code != "INVALID_TRANSITION" {
			t.Errorf("Resolve(%q) code = %q, want INVALID_TRANSITION", input, code)
		}
	}
}

// TestTransitionsEscapeTheIssueKey covers a caller's argument reaching a URL
// path. JoinPath cleans ".." rather than refusing it, so an unescaped key
// holding separators would resolve to a different endpoint on the same host.
func TestTransitionsEscapeTheIssueKey(t *testing.T) {
	doer := &pathRecordingDoer{stubDoer: stubDoer{body: `{"transitions":[]}`}}
	if _, err := site.FetchTransitions(t.Context(), doer,
		site.Info{Kind: site.Cloud}, "../../admin"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// The key contributes exactly one path segment however it is spelled.
	// "..%2F..%2Fadmin" cannot traverse; "../../admin" would.
	if got, want := strings.Count(doer.path, "/"), 6; got != want {
		t.Errorf("path %q has %d segments, want %d — the key escaped its own",
			doer.path, got, want)
	}
	if !strings.HasSuffix(doer.path, "/transitions") {
		t.Errorf("path = %q, want it to still end at the transitions endpoint", doer.path)
	}
}
