package site_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedCreateMetaIsAConversationAServerHad closes the last of the
// three bugs this project keeps citing.
//
// `meta createmeta` called /rest/api/2/issue/createmeta, an endpoint removed in
// Jira 9.0, and the hand-written fixture answered it happily for months
// because its author wrote both halves. The replacement is two requests — the
// issue types for a project, then the fields for one type — and this is a real
// Data Center 10.4.0 answering exactly the pair this code builds.
//
// The removed route does not even 404 as a missing endpoint: it falls through
// to /issue/{idOrKey} and reports "Issue Does Not Exist", so the failure it
// produced looked like a missing issue rather than a missing endpoint.
func TestTheRecordedCreateMetaIsAConversationAServerHad(t *testing.T) {
	client, replayer := recordedClientAt(t,
		"createmeta-recorded.datacenter.json", "https://recorded.invalid")

	meta := &site.Metadata{
		Client: client,
		Info:   site.Info{Kind: site.DataCenter},
		Now:    func() time.Time { return time.Unix(0, 0) },
	}

	created, err := meta.CreateMeta(t.Context(), "ENG", "Task")
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}

	// Every field a create must carry, from the instance rather than from a
	// list somebody typed.
	var required []string
	for _, f := range created.Fields {
		if f.Required {
			required = append(required, f.ID)
		}
	}
	for _, want := range []string{"issuetype", "project", "summary"} {
		if !contains(required, want) {
			t.Errorf("%s is not required by the recorded metadata: %v", want, required)
		}
	}
	// The cassette also holds the deployment probe, because the invocation that
	// produced it passed --refresh and that busts the deployment cache too.
	// This test drives the metadata directly, so the probe is the one exchange
	// it is not expected to make — and naming it is better than dropping the
	// check, which would stop noticing a createmeta request that never went.
	for _, unplayed := range replayer.Unplayed() {
		if !strings.Contains(unplayed, "/serverInfo") {
			t.Errorf("a recorded exchange was never requested: %s", unplayed)
		}
	}
}

// TestTheRecordedTransitionsAreAConversationAServerHad covers the read that is
// deliberately never cached, and the resolution built on it.
//
// Transitions depend on the issue's current status, so a stored copy answers
// the question as it stood when it was stored. What a recording adds is that
// the shape Jira really sends resolves: the id this tool posts back comes from
// the server's own list, matched by the name a caller typed.
func TestTheRecordedTransitionsAreAConversationAServerHad(t *testing.T) {
	client, replayer := recordedClientAt(t,
		"transitions-recorded.datacenter.json", "https://recorded.invalid")

	meta := &site.Metadata{
		Client: client,
		Info:   site.Info{Kind: site.DataCenter},
	}

	transitions, err := meta.Transitions(t.Context(), "ENG-8")
	if err != nil {
		t.Fatalf("the request this code builds is not the one the server "+
			"answered: %v", err)
	}

	move, err := transitions.Resolve("In Progress")
	if err != nil {
		t.Fatalf("a transition the server offered did not resolve: %v", err)
	}
	if move.ID != "21" {
		t.Errorf("id = %q, want 21 — the id the server gave for this move", move.ID)
	}
	if move.To.Category != site.CategoryInProgress {
		t.Errorf("category = %q, want %q", move.To.Category, site.CategoryInProgress)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("a recorded exchange was never requested: %v", unplayed)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
