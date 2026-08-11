//go:build write

package issue_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestLinkAddPutsTheIssuesAtTheRightEnds is the whole risk in this verb.
// Swapping them creates the opposite relationship between the same pair, and
// nothing about the response would say so.
func TestLinkAddPutsTheIssuesAtTheRightEnds(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	blocks := issue.LinkType{Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}

	// "ENG-1 blocks ENG-2": Jira writes a link as
	// "inwardIssue <inward> outwardIssue", so ENG-1 is the outward issue.
	req, err := client.LinkAddRequest("ENG-1", "ENG-2", blocks, issue.Outward)
	if err != nil {
		t.Fatalf("outward: %v", err)
	}
	decoded := body(t, req.Body)
	if got := nestedAny(decoded, "outwardIssue", "key"); got != "ENG-1" {
		t.Errorf("outwardIssue = %v, want ENG-1 — the sentence's subject", got)
	}
	if got := nestedAny(decoded, "inwardIssue", "key"); got != "ENG-2" {
		t.Errorf("inwardIssue = %v, want ENG-2", got)
	}

	// "ENG-1 is blocked by ENG-2" is the same pair the other way round.
	req, err = client.LinkAddRequest("ENG-1", "ENG-2", blocks, issue.Inward)
	if err != nil {
		t.Fatalf("inward: %v", err)
	}
	decoded = body(t, req.Body)
	if got := nestedAny(decoded, "inwardIssue", "key"); got != "ENG-1" {
		t.Errorf("inwardIssue = %v, want ENG-1", got)
	}
	if got := nestedAny(decoded, "outwardIssue", "key"); got != "ENG-2" {
		t.Errorf("outwardIssue = %v, want ENG-2", got)
	}

	// Both carry the type by name; only the ends move.
	if got := nestedAny(decoded, "type", "name"); got != "Blocks" {
		t.Errorf("type = %v", got)
	}
}

// TestLinkAddRunsAsARegisteredCommand resolves a phrase and sends the link, on
// both deployments. The recorded body is the outward reading, so a direction
// bug is a fixture miss rather than a silent inversion.
func TestLinkAddRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.link.add")
			if !ok {
				t.Fatal("issue link add is not registered")
			}

			conn, replayer := replayConn(t, "link-add."+string(kind)+".json")
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
				},
				Args: []string{"ENG-1", "blocks", "ENG-2"}, Flags: registry.NewFlags(),
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the link sent was not the one recorded: %v", unplayed)
			}
			// The sentence is echoed back, so the caller can see the direction
			// that was applied rather than infer it.
			from, _ := doc.Record.ChildNamed("from")
			rel, _ := doc.Record.ChildNamed("relationship")
			if from.Text != "ENG-1" || rel.Text != "blocks" {
				t.Errorf("the result does not echo the sentence: %+v", doc.Record)
			}
		})
	}
}

// TestSelfLinksAreRefused covers a mistake Jira accepts and nobody wants.
func TestSelfLinksAreRefused(t *testing.T) {
	cmd, _ := registry.Lookup("issue.link.add")
	inv := &registry.Invocation{
		Args: []string{"ENG-1", "blocks", "ENG-1"}, Flags: registry.NewFlags(),
	}
	err := cmd.Validate(t.Context(), inv)
	if err == nil {
		t.Fatal("an issue was linked to itself")
	}
	if code := errs.Coerce(err).Code; code != "SELF_LINK" {
		t.Errorf("code = %q, want SELF_LINK", code)
	}
}

// TestWorklogStartedUsesJirasOwnFormat covers a conversion that has to happen
// here. Jira takes neither RFC 3339 nor anything close: the offset has no colon
// and the fraction is exactly three digits, and sending RFC 3339 is a 400 that
// does not say which field was wrong.
func TestWorklogStartedUsesJirasOwnFormat(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	req, err := client.WorklogAddRequest(
		"ENG-101", "3h", "2026-08-05T09:00:00Z", "", time.Time{},
	)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	decoded := body(t, req.Body)
	if got := decoded["started"]; got != "2026-08-05T09:00:00.000+0000" {
		t.Errorf("started = %v, want Jira's own layout", got)
	}
	if got := decoded["timeSpent"]; got != "3h" {
		t.Errorf("timeSpent = %v, want it unconverted", got)
	}
	if _, present := decoded["comment"]; present {
		t.Error("an unmentioned comment reached the body")
	}
}

// TestWorklogDefaultsToNow covers the flag being optional: work logged without
// --started happened now, not at the zero time.
func TestWorklogDefaultsToNow(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	now := time.Date(2026, 8, 6, 14, 30, 0, 0, time.UTC)

	req, err := client.WorklogAddRequest("ENG-101", "30m", "", "", now)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := body(t, req.Body)["started"]; got != "2026-08-06T14:30:00.000+0000" {
		t.Errorf("started = %v, want the supplied clock", got)
	}
}

// TestWorklogCommentFollowsTheBodyRule covers the same containment as a comment:
// wiki text on Data Center, a document on Cloud, nothing interpreted.
func TestWorklogCommentFollowsTheBodyRule(t *testing.T) {
	dc, _ := writeClient(site.DataCenter)
	req, err := dc.WorklogAddRequest("ENG-101", "3h", "", "pairing on **retry**", time.Time{})
	if err != nil {
		t.Fatalf("data center: %v", err)
	}
	if got := body(t, req.Body)["comment"]; got != "pairing on **retry**" {
		t.Errorf("comment = %v, want the text unchanged", got)
	}

	cloud, _ := writeClient(site.Cloud)
	req, err = cloud.WorklogAddRequest("ENG-101", "3h", "", "pairing on **retry**", time.Time{})
	if err != nil {
		t.Fatalf("cloud: %v", err)
	}
	doc, ok := body(t, req.Body)["comment"].(map[string]any)
	if !ok || doc["type"] != "doc" {
		t.Fatalf("cloud comment is not a document: %s", req.Body)
	}
	if strings.Contains(string(req.Body), `"strong"`) {
		t.Errorf("markdown was interpreted: %s", req.Body)
	}
}

// TestWorklogAddRunsAsARegisteredCommand exercises the wrapper on both
// deployments and checks the created entry's id comes back.
func TestWorklogAddRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.worklog.add")
			if !ok {
				t.Fatal("issue worklog add is not registered")
			}

			conn, replayer := replayConn(t, "worklog-add."+string(kind)+".json")
			flags := registry.NewFlags()
			flags.SetString("started", "2026-08-05T09:00:00Z")
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
				},
				Args: []string{"ENG-101", "3h"}, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the body sent was not the one recorded: %v", unplayed)
			}
			if id, _ := doc.Record.AttrValue("id"); id != "10099" {
				t.Errorf("id = %q, want the created entry's", id)
			}
		})
	}
}

// TestRemovalsAreDestructiveAndCheckTheirIds covers both delete verbs.
func TestRemovalsAreDestructiveAndCheckTheirIds(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		code    string
	}{
		{"issue.link.remove", []string{"abc"}, "INVALID_LINK_ID"},
		{"issue.worklog.delete", []string{"ENG-101", "abc"}, "INVALID_WORKLOG_ID"},
	} {
		cmd, ok := registry.Lookup(tc.command)
		if !ok {
			t.Fatalf("%s is not registered", tc.command)
		}
		if !cmd.Destructive {
			t.Errorf("%s is not destructive, so --yes would not be required", tc.command)
		}
		if _, has := cmd.Flag("yes"); !has {
			t.Errorf("%s does not declare --yes", tc.command)
		}

		inv := &registry.Invocation{Args: tc.args, Flags: registry.NewFlags()}
		err := cmd.Validate(t.Context(), inv)
		if err == nil {
			t.Errorf("%s accepted a non-numeric id", tc.command)
			continue
		}
		if code := errs.Coerce(err).Code; code != tc.code {
			t.Errorf("%s code = %q, want %q", tc.command, code, tc.code)
		}
	}
}

// TestLinkRemoveTargetsTheLinkNotThePair is why it takes an id: two issues can
// be linked more than once, and "the link between A and B" would be ambiguous
// exactly when it mattered.
func TestLinkRemoveTargetsTheLinkNotThePair(t *testing.T) {
	cmd, _ := registry.Lookup("issue.link.remove")
	conn, _ := replayConn(t, "link-add.datacenter.json")

	flags := registry.NewFlags()
	flags.SetBool("dry-run", true)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"10042"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc.Kind != registry.KindDryRun {
		t.Fatalf("kind = %q", doc.Kind)
	}
	if len(doc.Record.Children) != 1 {
		t.Fatalf("got %d requests, want the one this sends", len(doc.Record.Children))
	}
	request := doc.Record.Children[0]
	method, _ := request.AttrValue("method")
	path, _ := request.AttrValue("path")
	if method != transport.MethodDelete || path != "/rest/api/2/issueLink/10042" {
		t.Errorf("request = %s %s", method, path)
	}
}

// TestWorklogDeleteRunsAsARegisteredCommand covers the last wrapper, on both
// deployments.
func TestWorklogDeleteRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.worklog.delete")
			if !ok {
				t.Fatal("issue worklog delete is not registered")
			}

			conn, replayer := replayConn(t, "worklog-delete."+string(kind)+".json")
			flags := registry.NewFlags()
			flags.SetBool("yes", true)
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
				},
				Args: []string{"ENG-101", "10001"}, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if action, _ := doc.Record.AttrValue("action"); action != "deleted" {
				t.Errorf("action = %q", action)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the delete was never sent: %v", unplayed)
			}
		})
	}
}

// TestWorklogValidationRefusesBeforeSending covers the checks that cost nothing.
func TestWorklogValidationRefusesBeforeSending(t *testing.T) {
	cmd, _ := registry.Lookup("issue.worklog.add")
	for _, tc := range []struct {
		name string
		args []string
		flag string
		code string
	}{
		{name: "bad key", args: []string{"nonsense", "3h"}, code: "INVALID_KEY"},
		{name: "no time", args: []string{"ENG-1"}, code: "INVALID_DURATION"},
		{name: "bad time", args: []string{"ENG-1", "3 hours"}, code: "INVALID_DURATION"},
		{
			name: "bad start", args: []string{"ENG-1", "3h"},
			flag: "yesterday", code: "INVALID_DATE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := registry.NewFlags()
			if tc.flag != "" {
				flags.SetString("started", tc.flag)
			}
			inv := &registry.Invocation{Args: tc.args, Flags: flags}

			err := cmd.Validate(t.Context(), inv)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
		})
	}

	// And --started explains what it is for, because "how long" and "when" are
	// easy to confuse in a command that takes both.
	flags := registry.NewFlags()
	flags.SetString("started", "3h")
	err := cmd.Validate(t.Context(),
		&registry.Invocation{Args: []string{"ENG-1", "3h"}, Flags: flags})
	if remedy := errs.Coerce(err).Remedy; !strings.Contains(remedy, "when the work happened") {
		t.Errorf("the remedy does not disambiguate: %q", remedy)
	}
}
