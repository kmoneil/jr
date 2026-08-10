//go:build write

package issue_test

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// parentEdit builds the request `issue edit <key> --parent <value>` would send,
// without sending it. Going through the registered command rather than through
// Client.EditRequest is deliberate: the flag has to survive the wiring, and the
// defect this flag exists to avoid — a flag that is declared and then discarded
// — lives in exactly that gap.
func parentEdit(
	t *testing.T, kind site.Kind, key, parent string,
) (map[string]any, error) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	flags := registry.NewFlags()
	flags.SetString("parent", parent)
	flags.SetBool("dry-run", true)
	inv := &registry.Invocation{
		Jira: &stubSession{kind: kind}, Args: []string{key}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		return nil, err
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		return nil, err
	}
	return dryRunBody(t, doc), nil
}

// dryRunBody pulls the single previewed request's body out of a dry-run
// document. The kind wraps every preview in a `requests` list, including a
// preview of one, so an edit's body is one level further down than the
// unwrapped v1 shape put it.
func dryRunBody(t *testing.T, doc *render.Doc) map[string]any {
	t.Helper()

	if len(doc.Record.Children) != 1 {
		t.Fatalf("got %d requests, want the one an edit sends",
			len(doc.Record.Children))
	}
	body, ok := doc.Record.Children[0].ChildNamed("body")
	if !ok {
		t.Fatal("the dry run printed no body")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body.Text), &payload); err != nil {
		t.Fatalf("the request body is not JSON: %v\n%s", err, body.Text)
	}
	return payload
}

// TestParentReachesTheRequest is the invariant a flag has to earn before it
// ships: --field was declared, fetched a field, and threw it away, and the
// gate that would have caught it is this one asked of every new flag.
//
// It is asserted on the *body* rather than on an exit code, because an edit
// answers 204 with no content — a --parent that reached nothing would report
// exactly the same success as one that worked.
func TestParentReachesTheRequest(t *testing.T) {
	for _, kind := range []site.Kind{site.Cloud, site.DataCenter} {
		payload, err := parentEdit(t, kind, "ENG-101", "ENG-42")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		fields, ok := payload["fields"].(map[string]any)
		if !ok {
			t.Fatalf("%s: the request named no fields: %v", kind, payload)
		}
		parent, ok := fields["parent"].(map[string]any)
		if !ok {
			t.Fatalf("%s: parent = %v, want an object naming a key", kind, fields["parent"])
		}
		if parent["key"] != "ENG-42" {
			t.Errorf("%s: parent key = %v, want ENG-42", kind, parent["key"])
		}
	}
}

// TestTheSentinelClearsTheParentWithAnExplicitNull covers the half that cannot
// be spelled by any key.
//
// A JSON null and an absent field are different instructions: absent leaves the
// issue under whatever it is under, which is the opposite of what `--parent
// none` asks for. The assertion is on the key being *present* and nil, because
// a map lookup returning nil cannot tell those apart.
func TestTheSentinelClearsTheParentWithAnExplicitNull(t *testing.T) {
	for _, word := range []string{"none", "None", "NONE"} {
		payload, err := parentEdit(t, site.Cloud, "ENG-101", word)
		if err != nil {
			t.Fatalf("--parent %s: %v", word, err)
		}
		fields, ok := payload["fields"].(map[string]any)
		if !ok {
			t.Fatalf("--parent %s: the request named no fields: %v", word, payload)
		}
		value, present := fields["parent"]
		if !present {
			t.Fatalf("--parent %s omitted the field, which leaves the parent "+
				"in place rather than clearing it", word)
		}
		if value != nil {
			t.Errorf("--parent %s sent %v, want an explicit null", word, value)
		}
	}
}

// TestAParentOnlyEditIsNotNothingToEdit is the regression this flag is one line
// away from at all times.
//
// `issue edit ENG-1 --parent ENG-42` names exactly one field. editTouchesAnything
// enumerates the fields by name, so a --parent left out of that list turns the
// only spelling of "move this into that epic" into NOTHING_TO_EDIT — a refusal
// with a remedy that does not apply, for a command that was given something to
// change.
func TestAParentOnlyEditIsNotNothingToEdit(t *testing.T) {
	if _, err := parentEdit(t, site.Cloud, "ENG-101", "ENG-42"); err != nil {
		t.Fatalf("an edit naming only --parent was refused: %v", err)
	}
	if _, err := parentEdit(t, site.Cloud, "ENG-101", "none"); err != nil {
		t.Fatalf("an edit clearing the parent was refused: %v", err)
	}

	// The other side of the same list: with no field at all it still refuses,
	// so the check above is not passing because the guard stopped working.
	cmd, _ := registry.Lookup("issue.edit")
	err := cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{kind: site.Cloud}, Args: []string{"ENG-101"},
		Flags: registry.NewFlags(), Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("an edit naming no field at all was accepted")
	}
	if code := errs.Coerce(err).Code; code != "NOTHING_TO_EDIT" {
		t.Errorf("code = %q, want NOTHING_TO_EDIT", code)
	}
}

// TestAParentKeyIsCanonicalizedBeforeItIsSent covers the rule the agile verbs
// already keep. A lowercase key is what a caller types and not what Jira uses,
// and the create path passed it through unchanged until this landed — so the
// same parent named two ways produced two different requests.
func TestAParentKeyIsCanonicalizedBeforeItIsSent(t *testing.T) {
	payload, err := parentEdit(t, site.Cloud, "ENG-101", "eng-42")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	fields, _ := payload["fields"].(map[string]any)
	parent, _ := fields["parent"].(map[string]any)
	if parent["key"] != "ENG-42" {
		t.Errorf("edit sent %v, want ENG-42", parent["key"])
	}

	// The create path builds its own body, so it is asserted separately rather
	// than assumed to share this one.
	client := &issue.Client{Site: site.Info{Kind: site.Cloud, Version: "test"}}
	req, err := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Story", Summary: "probe", Parent: "eng-42",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(string(req.Body), `"key":"ENG-42"`) {
		t.Errorf("create sent %s, want a canonical ENG-42", req.Body)
	}
}

// TestABadParentIsRefusedBeforeAnythingIsSent covers what is settled locally,
// so a typo costs no round trip.
func TestABadParentIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for _, tc := range []struct{ key, parent, code string }{
		{"ENG-101", "not-a-key", "INVALID_KEY"},
		{"ENG-101", "ENG", "INVALID_KEY"},
		{"ENG-101", "42", "INVALID_KEY"},
		// The relationship cannot be reflexive, and Jira would reject the cycle
		// anyway. This is SELF_EPIC's argument at the other end.
		{"ENG-101", "ENG-101", "SELF_PARENT"},
		{"ENG-101", "eng-101", "SELF_PARENT"},
	} {
		_, err := parentEdit(t, site.Cloud, tc.key, tc.parent)
		if err == nil {
			t.Errorf("--parent %q was accepted", tc.parent)
			continue
		}
		if code := errs.Coerce(err).Code; code != tc.code {
			t.Errorf("--parent %q: code = %q, want %q", tc.parent, code, tc.code)
		}
	}
}

// TestTheSentinelIsNotAKeyAndAKeyIsNotTheSentinel pins the one collision worth
// checking before a bare word is given a meaning.
//
// A site may well have a project keyed NONE. Its issues are still NONE-1, and
// ParseKey requires the number, so nothing a caller could legitimately mean is
// swallowed by the sentinel.
func TestTheSentinelIsNotAKeyAndAKeyIsNotTheSentinel(t *testing.T) {
	if _, ok := issue.ParseKey(issue.ParentSentinel); ok {
		t.Fatalf("%q parses as an issue key, so it cannot be the clearing word",
			issue.ParentSentinel)
	}

	// A project actually named NONE still reaches Jira as a key.
	payload, err := parentEdit(t, site.Cloud, "ENG-101", "NONE-1")
	if err != nil {
		t.Fatalf("NONE-1: %v", err)
	}
	fields, _ := payload["fields"].(map[string]any)
	parent, ok := fields["parent"].(map[string]any)
	if !ok {
		t.Fatalf("NONE-1 was read as the sentinel and cleared the parent: %v",
			fields["parent"])
	}
	if parent["key"] != "NONE-1" {
		t.Errorf("parent key = %v, want NONE-1", parent["key"])
	}
}

// TestCreateRefusesTheSentinelRatherThanDroppingIt covers the word where it has
// no meaning. There is nothing to clear on an issue that does not exist, and
// dropping the flag would create the issue and report success for a request
// nobody made.
func TestCreateRefusesTheSentinelRatherThanDroppingIt(t *testing.T) {
	client := &issue.Client{Site: site.Info{Kind: site.Cloud, Version: "test"}}
	_, err := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Story", Summary: "probe",
		Parent: issue.ParentSentinel,
	})
	if err == nil {
		t.Fatal("create accepted the clearing word as a parent")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_KEY" {
		t.Errorf("code = %q, want INVALID_KEY", code)
	}

	// And the command layer refuses it too, so the guard is not the only thing
	// standing between a caller and a "parent": null on a create.
	cmd, _ := registry.Lookup("issue.create")
	flags := registry.NewFlags()
	flags.SetString("type", "Story")
	flags.SetString("summary", "probe")
	flags.SetString("parent", issue.ParentSentinel)
	err = cmd.Validate(t.Context(), &registry.Invocation{
		Jira: &stubSession{kind: site.Cloud}, Flags: flags,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("issue create validated the clearing word as a parent")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_KEY" {
		t.Errorf("validate code = %q, want INVALID_KEY", code)
	}
}

// TestParentCombinesWithTheOtherFields covers the "only what you name is sent"
// rule holding once a second field is in play. A parent that replaced the body
// rather than joining it would pass every test above.
func TestParentCombinesWithTheOtherFields(t *testing.T) {
	cmd, _ := registry.Lookup("issue.edit")
	flags := registry.NewFlags()
	flags.SetString("parent", "ENG-42")
	flags.SetString("summary", "a better summary")
	flags.SetBool("dry-run", true)
	inv := &registry.Invocation{
		Jira: &stubSession{kind: site.Cloud}, Args: []string{"ENG-101"},
		Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	payload := dryRunBody(t, doc)
	fields, _ := payload["fields"].(map[string]any)
	if len(fields) != 2 {
		t.Errorf("fields = %v, want exactly summary and parent", fields)
	}
	if fields["summary"] != "a better summary" {
		t.Errorf("summary = %v", fields["summary"])
	}
	if parent, _ := fields["parent"].(map[string]any); parent["key"] != "ENG-42" {
		t.Errorf("parent = %v", fields["parent"])
	}
}

// TestTheParentEditIsAConversationBothProjectStylesHad is the reason this flag
// exists, and the only test here that could have discovered it.
//
// Everything above asserts the request this code builds. None of it can say
// whether Jira accepts it, and that was the whole defect: `epic add` posted to
// `/rest/agile/1.0/epic/{ref}/issue` for two releases behind a constructed
// cassette, and Cloud refuses that endpoint for team-managed projects — the
// default for every project created on a Cloud site.
//
// The four recordings are the same two requests against both project styles.
// That symmetry is the finding: the parent field is accepted by company-managed
// and team-managed alike, so replacing the agile endpoint needs no branch on
// style, and a branch would cost a project fetch per invocation to choose
// between two paths one of which works everywhere.
func TestTheParentEditIsAConversationBothProjectStylesHad(t *testing.T) {
	for _, tc := range []struct {
		name, fixture, key, parent string
	}{
		{"set on company-managed", "parentset-classic.cloud.json", "AGL-3", "AGL-1"},
		{"clear on company-managed", "parentclear-classic.cloud.json", "AGL-3", "none"},
		{"set on team-managed", "parentset-nextgen.cloud.json", "OPS-10", "OPS-5"},
		{"clear on team-managed", "parentclear-nextgen.cloud.json", "OPS-10", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cassette, err := transport.LoadCassette(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if !cassette.Evidence() {
				t.Fatalf("%s is not a recording, so replaying it establishes "+
					"nothing about the API", tc.fixture)
			}
			conn, replayer := replayConn(t, tc.fixture)

			cmd, ok := registry.Lookup("issue.edit")
			if !ok {
				t.Fatal("issue edit is not registered")
			}
			flags := registry.NewFlags()
			flags.SetString("parent", tc.parent)
			inv := &registry.Invocation{
				Jira: &stubSession{conn: conn, kind: site.Cloud},
				Args: []string{tc.key}, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			}
			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			if _, err := cmd.Run(t.Context(), inv); err != nil {
				t.Fatalf("the request this code builds is not the one the "+
					"server answered: %v", err)
			}
			// The replayer matches on path and body, so reaching this line
			// means both were what a real Jira took a 204 on.
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the write was never sent: %v", unplayed)
			}
			if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
				t.Errorf("a request went somewhere the cassette does not "+
					"answer: %v", unmatched)
			}
		})
	}
}
