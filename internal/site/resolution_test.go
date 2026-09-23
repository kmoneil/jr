package site_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/site"
)

// resolveIssueJSON is two transitions of the system `jira` workflow as Data
// Center 10.4.0 answered them on 2026-09-23, with the host replaced and the
// Resolve Issue screen trimmed to its resolution field. Start Progress has no
// screen, and Resolve Issue's screen offers every resolution the site has,
// each with its id. It is the only screened transition anybody has measured:
// none of the default workflows measured on either deployment has one.
const resolveIssueJSON = `{"expand":"transitions","transitions":[
	{"id":"4","name":"Start Progress","opsbarSequence":20,
	 "to":{"self":"https://recorded.invalid/rest/api/2/status/3","name":"In Progress","id":"3",
	       "statusCategory":{"id":4,"key":"indeterminate","colorName":"inprogress","name":"In Progress"}},
	 "fields":{}},
	{"id":"5","name":"Resolve Issue","opsbarSequence":40,
	 "to":{"self":"https://recorded.invalid/rest/api/2/status/5","name":"Resolved","id":"5",
	       "statusCategory":{"id":3,"key":"done","colorName":"success","name":"Done"}},
	 "fields":{"resolution":{"required":true,
	     "schema":{"type":"resolution","system":"resolution"},
	     "name":"Resolution","fieldId":"resolution","operations":["set"],
	     "allowedValues":[
	       {"self":"https://recorded.invalid/rest/api/2/resolution/10000","name":"Done","id":"10000"},
	       {"self":"https://recorded.invalid/rest/api/2/resolution/10001","name":"Won't Do","id":"10001"},
	       {"self":"https://recorded.invalid/rest/api/2/resolution/10002","name":"Duplicate","id":"10002"},
	       {"self":"https://recorded.invalid/rest/api/2/resolution/10003","name":"Cannot Reproduce","id":"10003"}]}}}
]}`

// transitionIn fetches a body and resolves one transition out of it, so each
// test reads the decoder's output rather than a struct somebody typed.
func transitionIn(t *testing.T, body, name string) site.Transition {
	t.Helper()
	got, err := site.FetchTransitions(t.Context(), &stubDoer{body: body},
		site.Info{Kind: site.DataCenter}, "ENG-101")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	transition, err := got.Resolve(name)
	if err != nil {
		t.Fatalf("resolve %q: %v", name, err)
	}
	return transition
}

// TestAResolutionIsSpelledTheWayTheSiteSpellsIt: by id or by name in any case,
// and the answer is the screen's own value.
//
// Jira matches the name case-sensitively, so the spelling is the whole point:
// 10.4.0 refused `won't do` for `Won't Do`, and `10001` sent as a name.
func TestAResolutionIsSpelledTheWayTheSiteSpellsIt(t *testing.T) {
	resolve := transitionIn(t, resolveIssueJSON, "Resolve Issue")
	for _, tc := range []struct {
		typed    string
		id, name string
	}{
		{"Won't Do", "10001", "Won't Do"},
		{"won't do", "10001", "Won't Do"},
		{"  WON'T DO  ", "10001", "Won't Do"},
		{"10002", "10002", "Duplicate"},
		{"cannot reproduce", "10003", "Cannot Reproduce"},
	} {
		got, err := resolve.Resolution(tc.typed)
		if err != nil {
			t.Errorf("%q: %v", tc.typed, err)
			continue
		}
		if got.ID != tc.id || got.Name != tc.name {
			t.Errorf("%q resolved to %+v, want %s (%s)", tc.typed, got, tc.name, tc.id)
		}
	}
}

// TestAResolutionTheScreenDoesNotOfferIsRefusedWithEveryOneItDoes is issue 180:
// the set arrived with the transitions read, and nothing read it.
func TestAResolutionTheScreenDoesNotOfferIsRefusedWithEveryOneItDoes(t *testing.T) {
	resolve := transitionIn(t, resolveIssueJSON, "Resolve Issue")
	_, err := resolve.Resolution("wont do")
	if err == nil {
		t.Fatal("a resolution the screen does not offer was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_RESOLUTION" {
		t.Errorf("code = %q, want UNKNOWN_RESOLUTION", e.Code)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
	}
	if !strings.Contains(e.Message, "Resolve Issue") || !strings.Contains(e.Message, `"wont do"`) {
		t.Errorf("message does not say which transition and which input: %q", e.Message)
	}
	want := "available: Done (10000), Won't Do (10001), Duplicate (10002), " +
		"Cannot Reproduce (10003)"
	if e.Detail != want {
		t.Errorf("detail = %q, want %q", e.Detail, want)
	}
	if e.Remedy == "" {
		t.Error("no remedy")
	}
}

// TestAResolutionIsRefusedByATransitionWithNoFieldForOne is the common case:
// none of the default workflows measured on either deployment puts a screen on
// any transition, and Jira refuses a resolution there on both. It checks the
// screen before the value, and so does this, so a misspelling gets the reason
// that would survive correcting it.
func TestAResolutionIsRefusedByATransitionWithNoFieldForOne(t *testing.T) {
	start := transitionIn(t, resolveIssueJSON, "Start Progress")
	for _, typed := range []string{"Done", "wont do"} {
		_, err := start.Resolution(typed)
		if err == nil {
			t.Fatalf("%q was accepted by a transition with no resolution field", typed)
		}
		e := errs.Coerce(err)
		if e.Code != "TRANSITION_TAKES_NO_RESOLUTION" {
			t.Errorf("%q: code = %q, want TRANSITION_TAKES_NO_RESOLUTION", typed, e.Code)
		}
		if e.Exit != exitcode.Usage {
			t.Errorf("%q: exit = %d, want %d", typed, e.Exit, exitcode.Usage)
		}
		if !strings.Contains(e.Remedy, "meta transitions") {
			t.Errorf("%q: the remedy does not say where to look: %q", typed, e.Remedy)
		}
	}
}

// TestAResolutionIsTheSystemFieldNotAnythingCalledResolution: the screen check
// reads the id, because `fields.resolution` sets the system field and a custom
// field can be named anything.
func TestAResolutionIsTheSystemFieldNotAnythingCalledResolution(t *testing.T) {
	body := `{"transitions":[{"id":"7","name":"Close",
	  "to":{"id":"6","name":"Closed","statusCategory":{"key":"done"}},
	  "fields":{"customfield_10100":{"required":false,"name":"Resolution",
	    "schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"},
	    "allowedValues":[{"id":"1","value":"Done"}]}}}]}`
	_, err := transitionIn(t, body, "Close").Resolution("Done")
	if code := errs.Coerce(err).Code; code != "TRANSITION_TAKES_NO_RESOLUTION" {
		t.Errorf("code = %q, want TRANSITION_TAKES_NO_RESOLUTION for a custom "+
			"field that is only called Resolution", code)
	}
}

// TestNoResolutionAsksForNothing: an empty flag is not a request, whatever the
// screen says, and must not refuse a transition that has no resolution field.
func TestNoResolutionAsksForNothing(t *testing.T) {
	start := transitionIn(t, resolveIssueJSON, "Start Progress")
	for _, typed := range []string{"", "   "} {
		got, err := start.Resolution(typed)
		if err != nil || got != (site.Resolution{}) {
			t.Errorf("%q = %+v, %v, want nothing and no error", typed, got, err)
		}
	}
}

// TestTwoResolutionsSpelledAlikeAreRefused: a name that folds onto two values
// is refused with both, and an id still picks one, because ids are unique.
func TestTwoResolutionsSpelledAlikeAreRefused(t *testing.T) {
	body := `{"transitions":[{"id":"2","name":"Close Issue",
	  "to":{"id":"6","name":"Closed","statusCategory":{"key":"done"}},
	  "fields":{"resolution":{"required":true,"name":"Resolution",
	    "schema":{"type":"resolution","system":"resolution"},
	    "allowedValues":[{"id":"1","name":"Done"},{"id":"2","name":"DONE"}]}}}]}`
	closeIssue := transitionIn(t, body, "Close Issue")

	_, err := closeIssue.Resolution("done")
	e := errs.Coerce(err)
	if e.Code != "AMBIGUOUS_RESOLUTION" {
		t.Fatalf("code = %q, want AMBIGUOUS_RESOLUTION", e.Code)
	}
	if !strings.Contains(e.Detail, "Done (1)") || !strings.Contains(e.Detail, "DONE (2)") {
		t.Errorf("detail does not list both: %q", e.Detail)
	}
	if got, err := closeIssue.Resolution("2"); err != nil || got.Name != "DONE" {
		t.Errorf("an id did not pick one: %+v, %v", got, err)
	}
}

// TestAnIDStaysWithItsValue: a value that reduces to nothing is dropped, and its
// id must go with it, or every id after it names the wrong resolution.
func TestAnIDStaysWithItsValue(t *testing.T) {
	body := `{"transitions":[{"id":"2","name":"Close Issue",
	  "to":{"id":"6","name":"Closed","statusCategory":{"key":"done"}},
	  "fields":{"resolution":{"required":true,"name":"Resolution",
	    "schema":{"type":"resolution","system":"resolution"},
	    "allowedValues":[{"id":"1","name":"Done"},{},{"id":"3","name":"Duplicate"}]}}}]}`
	got, err := transitionIn(t, body, "Close Issue").Resolution("3")
	if err != nil || got.Name != "Duplicate" {
		t.Errorf("id 3 = %+v, %v, want Duplicate", got, err)
	}
}

// TestAResolutionFieldThatListsNothingConstrainsNothing is the one shape nobody
// has seen. An empty AllowedValues means unconstrained everywhere else in this
// package, so the input goes as typed and Jira, which validates the name, has
// the last word. Refusing here would invent a limit.
func TestAResolutionFieldThatListsNothingConstrainsNothing(t *testing.T) {
	body := `{"transitions":[{"id":"2","name":"Close Issue",
	  "to":{"id":"6","name":"Closed","statusCategory":{"key":"done"}},
	  "fields":{"resolution":{"required":true,"name":"Resolution",
	    "schema":{"type":"resolution","system":"resolution"}}}}]}`
	got, err := transitionIn(t, body, "Close Issue").Resolution(" Fixed ")
	if err != nil {
		t.Fatalf("an unconstrained field refused a value: %v", err)
	}
	if got.Name != "Fixed" || got.ID != "" {
		t.Errorf("got %+v, want the input as typed and no id", got)
	}
}
