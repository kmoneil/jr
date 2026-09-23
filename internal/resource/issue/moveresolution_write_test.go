//go:build write

package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The two cassettes here are constructed from what the servers answered on
// 2026-09-23, byte for byte apart from the host. The Data Center one is the
// system `jira` workflow on 10.4.0, whose Resolve Issue transition carries the
// Resolve Issue Screen; nothing the rig seeds has a screened transition, so it
// cannot be a manifest recording. Every refused POST in it is the refusal Jira
// sent for that exact body, which is what lets the unfixed code run to the end
// and be wrong rather than miss the fixture.

// moveWithResolution runs `issue move ENG-101 <transition> --resolution <r>`
// against a cassette.
func moveWithResolution(
	t *testing.T, kind site.Kind, fixture, transition, resolution string, dryRun bool,
) (*render.Doc, *transport.Replayer, error) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.move")
	if !ok {
		t.Fatal("issue move is not registered")
	}
	conn, replayer := replayConn(t, fixture)
	flags := registry.NewFlags()
	flags.SetString("resolution", resolution)
	flags.SetBool("dry-run", dryRun)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: kind, metaClient: conn,
		},
		Args: []string{"ENG-101", transition}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	doc, err := cmd.Run(t.Context(), inv)
	return doc, replayer, err
}

// sentWrites is the POSTs a run actually made, read off what it left unplayed.
func sentWrites(replayer *transport.Replayer, all int) int {
	unsent := 0
	for _, u := range replayer.Unplayed() {
		if strings.HasPrefix(u, "POST") {
			unsent++
		}
	}
	return all - unsent
}

// writesIn is how many POSTs the Data Center cassette answers.
const writesIn = 5

// TestAResolutionIsSentTheWayTheSiteSpellsIt is issue 180's other half, and the
// one the issue did not know about.
//
// Jira matches a resolution name case-sensitively: 10.4.0 refused `won't do`
// with "Resolution name 'won't do' is not valid" when the resolution is
// `Won't Do`. It also takes no id through the name: `10001` was refused the
// same way. The transition beside it has always been resolved by id or by any
// case and sent as the id, so a caller had every reason to expect the same.
// Whatever reaches Jira has to be the site's own spelling.
//
// Only spellings a server was actually sent are here, so each one the unfixed
// code sends meets the refusal Jira really gave it.
func TestAResolutionIsSentTheWayTheSiteSpellsIt(t *testing.T) {
	for _, typed := range []string{"Won't Do", "won't do", "10001"} {
		t.Run(typed, func(t *testing.T) {
			doc, replayer, err := moveWithResolution(t, site.DataCenter,
				"move-resolution.datacenter.json", "Resolve Issue", typed, false)
			if err != nil {
				t.Fatalf("--resolution %q was sent as typed and Jira refused it: %v",
					typed, err)
			}
			if id, _ := doc.Record.AttrValue("transition"); id != "5" {
				t.Errorf("transition = %q, want 5", id)
			}
			for _, u := range replayer.Unplayed() {
				if strings.Contains(u, `"name":"Won't Do"`) {
					t.Errorf("the site's spelling was never sent; unplayed: %v",
						replayer.Unplayed())
				}
			}
			if n := sentWrites(replayer, writesIn); n != 1 {
				t.Errorf("%d writes were sent, want exactly the one", n)
			}
		})
	}
}

// TestAResolutionTheScreenDoesNotOfferIsRefusedBeforeSending is the issue as
// filed. Jira refuses the name too, so nothing was ever lost, but it refused
// with a BAD_REQUEST and no candidates, after the tool had fetched the whole
// valid set to resolve the transition and read none of it.
func TestAResolutionTheScreenDoesNotOfferIsRefusedBeforeSending(t *testing.T) {
	_, replayer, err := moveWithResolution(t, site.DataCenter,
		"move-resolution.datacenter.json", "Resolve Issue", "wont do", false)
	if err == nil {
		t.Fatal("a resolution the screen does not offer was accepted")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_RESOLUTION" {
		t.Errorf("code = %q, want UNKNOWN_RESOLUTION: %v", e.Code, err)
	}
	if e.Exit != exitcode.Usage {
		t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
	}
	// Every value the screen offers, each with the id that can be passed
	// instead, because four candidates is a short list and a typo is not the
	// only way to get this wrong.
	for _, want := range []string{
		"Done (10000)", "Won't Do (10001)", "Duplicate (10002)", "Cannot Reproduce (10003)",
	} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("detail does not offer %s: %q", want, e.Detail)
		}
	}
	if n := sentWrites(replayer, writesIn); n != 0 {
		t.Errorf("%d writes were sent for a resolution that could not apply", n)
	}
}

// TestADryRunRefusesAResolutionTheScreenDoesNotOffer is the trap the issue was
// filed about: the preview is the thing somebody reads before an irreversible
// change, and it printed a well-formed request for a resolution that does not
// exist.
func TestADryRunRefusesAResolutionTheScreenDoesNotOffer(t *testing.T) {
	doc, _, err := moveWithResolution(t, site.DataCenter,
		"move-resolution.datacenter.json", "Resolve Issue", "wont do", true)
	if err == nil {
		t.Fatalf("the dry run blessed a resolution the screen does not offer, "+
			"and printed %s", doc.Kind)
	}
	if code := errs.Coerce(err).Code; code != "UNKNOWN_RESOLUTION" {
		t.Errorf("code = %q, want UNKNOWN_RESOLUTION", code)
	}
}

// TestAResolutionIsRefusedWhereTheScreenHasNone covers the common case, because
// none of the default workflows measured on either deployment puts a screen on
// any transition. Jira refuses a resolution there, on both, with "Field
// 'resolution' cannot be set. It is not on the appropriate screen, or unknown."
// It checks the screen before the value, so a bad name gets the screen
// message; so does this.
func TestAResolutionIsRefusedWhereTheScreenHasNone(t *testing.T) {
	for _, tc := range []struct {
		kind                   site.Kind
		fixture, transition, r string
		writes                 int
	}{
		{site.DataCenter, "move-resolution.datacenter.json", "Start Progress", "Done", writesIn},
		{site.Cloud, "move-resolution.cloud.json", "To Do", "wont do", 1},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			_, replayer, err := moveWithResolution(t, tc.kind, tc.fixture,
				tc.transition, tc.r, false)
			if err == nil {
				t.Fatal("a resolution was sent with a transition that has no field for one")
			}
			e := errs.Coerce(err)
			if e.Code != "TRANSITION_TAKES_NO_RESOLUTION" {
				t.Errorf("code = %q, want TRANSITION_TAKES_NO_RESOLUTION: %v", e.Code, err)
			}
			if e.Exit != exitcode.Usage {
				t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
			}
			if e.Remedy == "" {
				t.Error("no remedy: the caller needs to be told how to find one that does")
			}
			if n := sentWrites(replayer, tc.writes); n != 0 {
				t.Errorf("%d writes were sent", n)
			}
		})
	}
}
