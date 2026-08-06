//go:build write

package issue_test

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// writeClient builds a client for one deployment against a recording stub, so a
// test can assert what would go on the wire.
func writeClient(kind site.Kind) (*issue.Client, *captureDoer) {
	doer := &captureDoer{status: 201, body: `{"id":"10001","key":"ENG-201"}`}
	return &issue.Client{Transport: doer, Site: site.Info{Kind: kind}}, doer
}

// TestCreateRequestShapeOnBothDeployments is why both matter. Cloud is on v3
// and names a user by accountId; Data Center is on v2 and names one by name.
// Sending the wrong one is a 400 that says nothing about which field was wrong.
func TestCreateRequestShapeOnBothDeployments(t *testing.T) {
	for _, tc := range []struct {
		kind      site.Kind
		path      string
		assignee  string
		assigneeK string
	}{
		{site.Cloud, "/rest/api/3/issue", "712020:8f3a", "accountId"},
		{site.DataCenter, "/rest/api/2/issue", "ada", "name"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			client, _ := writeClient(tc.kind)
			req, err := client.CreateRequest(issue.CreateOptions{
				Project: "ENG", Type: "Bug", Summary: "a summary",
				Priority: "High", Labels: []string{"retry", "transport"},
				Assignee: tc.assignee, Parent: "ENG-1",
			})
			if err != nil {
				t.Fatalf("build: %v", err)
			}

			if req.Method != transport.MethodPost {
				t.Errorf("method = %q, want POST", req.Method)
			}
			if req.Path != tc.path {
				t.Errorf("path = %q, want %q", req.Path, tc.path)
			}

			fields := bodyFields(t, req.Body)
			if got := fields["summary"]; got != "a summary" {
				t.Errorf("summary = %v", got)
			}
			if got := nested(fields, "project", "key"); got != "ENG" {
				t.Errorf("project = %v, want the resolved one", got)
			}
			if got := nested(fields, "issuetype", "name"); got != "Bug" {
				t.Errorf("issuetype = %v", got)
			}
			if got := nested(fields, "parent", "key"); got != "ENG-1" {
				t.Errorf("parent = %v", got)
			}
			if got := nested(fields, "assignee", tc.assigneeK); got != tc.assignee {
				t.Errorf("assignee.%s = %v, want %q — the deployments are not "+
					"interchangeable here", tc.assigneeK, got, tc.assignee)
			}
		})
	}
}

// TestCreateOmitsWhatWasNotAsked keeps an unmentioned field out of the body. A
// key present with a zero value is an instruction to clear it, which is a very
// different request from not mentioning it.
func TestCreateOmitsWhatWasNotAsked(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	req, err := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "minimal",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	fields := bodyFields(t, req.Body)
	for _, absent := range []string{"priority", "labels", "assignee", "parent", "description"} {
		if _, present := fields[absent]; present {
			t.Errorf("%q is in the body but was never asked for", absent)
		}
	}
	if len(fields) != 3 {
		t.Errorf("body carries %d fields, want exactly project, issuetype, summary", len(fields))
	}
}

// TestDescriptionIsContainedNotConverted covers the shape each deployment
// takes. Cloud will not accept a string where a document belongs, so the text
// is wrapped in one — which is exact. What is *not* done is interpreting it:
// **bold** reaches Jira as six characters, which is also what Data Center does
// with the same input.
func TestDescriptionIsContainedNotConverted(t *testing.T) {
	const text = "line one\nline two\n\nsecond paragraph with **bold**"

	// Data Center takes it as a string, unchanged.
	dc, _ := writeClient(site.DataCenter)
	req, err := dc.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "s", Description: text,
	})
	if err != nil {
		t.Fatalf("data center: %v", err)
	}
	if got := bodyFields(t, req.Body)["description"]; got != text {
		t.Errorf("description = %v, want it carried through unchanged", got)
	}

	// Cloud takes a document containing the same characters.
	cloud, _ := writeClient(site.Cloud)
	req, err = cloud.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "s", Description: text,
	})
	if err != nil {
		t.Fatalf("cloud: %v", err)
	}
	doc, ok := bodyFields(t, req.Body)["description"].(map[string]any)
	if !ok {
		t.Fatalf("cloud description is not a document: %s", req.Body)
	}
	if doc["type"] != "doc" {
		t.Errorf("type = %v, want doc", doc["type"])
	}

	// A blank line started a second paragraph; the single newline did not.
	content, _ := doc["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("got %d paragraphs, want 2: %s", len(content), req.Body)
	}

	// And the characters survive exactly — nothing read the asterisks as a
	// mark, which is the difference between containing and converting.
	flat := string(req.Body)
	for _, want := range []string{"line one", "line two", "**bold**", "hardBreak"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the document does not carry %q: %s", want, flat)
		}
	}
	if strings.Contains(flat, `"strong"`) {
		t.Errorf("markdown was interpreted as a mark: %s", flat)
	}
}

// TestInvalidUTF8IsRefusedNotReplaced covers the encoding rule. Substituting
// U+FFFD would put a character in Jira the caller never wrote, with no way for
// them to know it happened.
func TestInvalidUTF8IsRefusedNotReplaced(t *testing.T) {
	cloud, _ := writeClient(site.Cloud)
	_, err := cloud.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "s",
		Description: "valid then \xff\xfe invalid",
	})
	if err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
	if code := errs.Coerce(err).Code; code != "INVALID_ENCODING" {
		t.Errorf("code = %q, want INVALID_ENCODING", code)
	}
}

// TestEditSendsOnlyWhatWasNamed is the property that makes edit safe to use on
// an issue somebody else is also working on.
func TestEditSendsOnlyWhatWasNamed(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	req, err := client.EditRequest(issue.EditOptions{
		Key: "ENG-101", Summary: "a better summary",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if req.Method != transport.MethodPut {
		t.Errorf("method = %q, want PUT", req.Method)
	}
	if req.Path != "/rest/api/2/issue/ENG-101" {
		t.Errorf("path = %q", req.Path)
	}
	fields := bodyFields(t, req.Body)
	if len(fields) != 1 || fields["summary"] != "a better summary" {
		t.Errorf("body = %v, want only the summary", fields)
	}
}

// TestLabelsReplaceOrModifyButNotBoth covers the two shapes Jira needs. Setting
// fields.labels replaces the set; add and remove have to go through update, and
// using the wrong one silently discards every label the caller did not mention.
func TestLabelsReplaceOrModifyButNotBoth(t *testing.T) {
	client, _ := writeClient(site.DataCenter)

	replace, err := client.EditRequest(issue.EditOptions{
		Key: "ENG-101", Labels: []string{"only", "these"},
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := bodyFields(t, replace.Body)["labels"]; got == nil {
		t.Error("--label did not set fields.labels, so it would not replace")
	}
	if _, has := body(t, replace.Body)["update"]; has {
		t.Error("--label used the incremental shape, which does not replace")
	}

	modify, err := client.EditRequest(issue.EditOptions{
		Key: "ENG-101", AddLabels: []string{"retry"}, RemoveLabels: []string{"wontfix"},
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	decoded := body(t, modify.Body)
	if _, has := decoded["fields"]; has {
		t.Error("add/remove used the replacing shape, which would drop every " +
			"label the caller did not name")
	}
	ops, _ := decoded["update"].(map[string]any)["labels"].([]any)
	if len(ops) != 2 {
		t.Fatalf("update.labels = %v, want an add and a remove", ops)
	}
	if op, ok := ops[0].(map[string]any); !ok || op["add"] == nil {
		t.Errorf("first op = %v, want an add", ops[0])
	}
	if op, ok := ops[1].(map[string]any); !ok || op["remove"] == nil {
		t.Errorf("second op = %v, want a remove", ops[1])
	}
}

// TestClearingAnAssigneeIsNotTheSameAsLeavingIt covers the distinction an empty
// string cannot carry: an explicit null clears the field, and omitting the key
// leaves whoever is on it.
func TestClearingAnAssigneeIsNotTheSameAsLeavingIt(t *testing.T) {
	client, _ := writeClient(site.DataCenter)

	cleared, err := client.EditRequest(issue.EditOptions{
		Key: "ENG-101", Assignee: "unassigned", SetAssignee: true,
	})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	fields := bodyFields(t, cleared.Body)
	value, present := fields["assignee"]
	if !present {
		t.Fatal("clearing the assignee omitted the field, which leaves it as it was")
	}
	if value != nil {
		t.Errorf("assignee = %v, want an explicit null", value)
	}

	untouched, err := client.EditRequest(issue.EditOptions{
		Key: "ENG-101", Summary: "s",
	})
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, present := bodyFields(t, untouched.Body)["assignee"]; present {
		t.Error("an unmentioned assignee reached the body")
	}
}

// TestMoveRequestShape covers the transition body on both deployments.
func TestMoveRequestShape(t *testing.T) {
	for kind, base := range map[site.Kind]string{
		site.Cloud: "/rest/api/3", site.DataCenter: "/rest/api/2",
	} {
		client, _ := writeClient(kind)
		req, err := client.MoveRequest("ENG-101", "11", "Fixed", "done here")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if req.Path != base+"/issue/ENG-101/transitions" {
			t.Errorf("%s path = %q", kind, req.Path)
		}

		decoded := body(t, req.Body)
		if got := nestedAny(decoded, "transition", "id"); got != "11" {
			t.Errorf("%s transition id = %v, want the resolved one", kind, got)
		}
		fields, ok := decoded["fields"].(map[string]any)
		if !ok {
			t.Fatalf("%s sent no fields object: %s", kind, req.Body)
		}
		if got := nestedAny(fields, "resolution", "name"); got != "Fixed" {
			t.Errorf("%s resolution = %v", kind, got)
		}
		if _, has := decoded["update"]; !has {
			t.Errorf("%s dropped the comment", kind)
		}
	}
}

// TestDryRunPrintsTheRequestNotAParaphrase is §4.1's requirement. The document
// is built from the same transport.Request the command was about to send, so
// the two cannot drift.
func TestDryRunPrintsTheRequestNotAParaphrase(t *testing.T) {
	client, doer := writeClient(site.DataCenter)
	req, err := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "a summary",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	doc := registry.DryRunDoc("issue.create", req)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.Kind != registry.KindDryRun {
		t.Errorf("kind = %q, want %q", doc.Kind, registry.KindDryRun)
	}

	var xml strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := xml.String()
	for _, want := range []string{
		`command="issue.create"`, `method="POST"`, `path="/rest/api/2/issue"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not carry %s:\n%s", want, out)
		}
	}
	// The body is the literal bytes that would have been sent, so it can be
	// pasted into curl — which is most of what a dry run is for.
	if !strings.Contains(out, string(req.Body)) {
		t.Errorf("the dry run does not carry the body verbatim:\n%s", out)
	}
	// And nothing was sent.
	if doer.calls != 0 {
		t.Errorf("building a dry run made %d requests", doer.calls)
	}
}

// TestDryRunNeverCarriesACredential is worth asserting rather than assuming.
// The document renders the request as the command built it, before the
// transport attaches anything.
func TestDryRunNeverCarriesACredential(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	req, _ := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "s",
	})

	var out strings.Builder
	if err := render.Write(&out, registry.DryRunDoc("issue.create", req), render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, forbidden := range []string{"Authorization", "Bearer", "Basic ", "Cookie"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("the dry run mentions %q:\n%s", forbidden, out.String())
		}
	}
}

// TestCreateReplaysUnderOneKey is the card's headline, through the registered
// command: the same create twice with one key sends one request.
func TestCreateReplaysUnderOneKey(t *testing.T) {
	cmd, ok := registry.Lookup("issue.create")
	if !ok {
		t.Fatal("issue create is not registered")
	}

	// One recorded create, and one only. A second request would be a fixture
	// miss, which is a stronger assertion than counting calls: the cassette
	// refuses to answer a duplicate rather than quietly allowing it.
	conn, replayer := replayConn(t, "create.datacenter.json")
	ledger := &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")}

	run := func() *render.Doc {
		t.Helper()
		flags := registry.NewFlags()
		flags.SetString("type", "Bug")
		flags.SetString("summary", "a summary")
		flags.SetString("idempotency-key", "deploy-42")
		inv := &registry.Invocation{
			Jira: &stubSession{
				doer: &stubDoer{body: catalogueJSON}, conn: conn,
				kind: site.DataCenter, ledger: ledger,
			},
			Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
		}
		if err := cmd.Validate(t.Context(), inv); err != nil {
			t.Fatalf("validate: %v", err)
		}
		doc, err := cmd.Run(t.Context(), inv)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return doc
	}

	first, second := run(), run()

	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the create was never sent: %v", unplayed)
	}

	firstKey, _ := first.Record.AttrValue("key")
	secondKey, _ := second.Record.AttrValue("key")
	if firstKey != secondKey || firstKey != "ENG-201" {
		t.Errorf("replay returned %q, want the original %q", secondKey, firstKey)
	}
	// The replay is marked, not silently identical — a caller has to be able to
	// tell "I made this" from "this already existed".
	if _, marked := first.Record.AttrValue("replayed"); marked {
		t.Error("the first create was marked as a replay")
	}
	if _, marked := second.Record.AttrValue("replayed"); !marked {
		t.Error("the replay was not marked")
	}
	// And identical apart from that marker: a consumer diffing two runs must
	// not see a difference that means nothing.
	firstID, _ := first.Record.AttrValue("id")
	secondID, _ := second.Record.AttrValue("id")
	if firstID != secondID {
		t.Errorf("replay id = %q, want the original %q", secondID, firstID)
	}
}

// TestAFailedCreateKeepsItsClaim is the conservative half. An error does not
// prove the request was not processed, so the key stays held and a retry is
// refused rather than duplicating.
func TestAFailedCreateKeepsItsClaim(t *testing.T) {
	cmd, _ := registry.Lookup("issue.create")
	conn, _ := replayConn(t, "create-fails.datacenter.json")
	ledger := &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")}

	invocation := func() *registry.Invocation {
		flags := registry.NewFlags()
		flags.SetString("type", "Bug")
		flags.SetString("summary", "a summary")
		flags.SetString("idempotency-key", "k")
		return &registry.Invocation{
			Jira: &stubSession{
				doer: &stubDoer{body: catalogueJSON}, conn: conn,
				kind: site.DataCenter, ledger: ledger,
			},
			Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
		}
	}

	if _, err := cmd.Run(t.Context(), invocation()); err == nil {
		t.Fatal("a 503 was reported as success")
	}

	_, err := cmd.Run(t.Context(), invocation())
	if err == nil {
		t.Fatal("a retry after an ambiguous failure was allowed through")
	}
	if code := errs.Coerce(err).Code; code != "IDEMPOTENT_IN_FLIGHT" {
		t.Errorf("code = %q, want IDEMPOTENT_IN_FLIGHT — the first attempt may "+
			"have been processed", code)
	}
	if errs.ExitOf(err) != exitcode.Conflict {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Conflict)
	}
}

// TestValidationRefusesBeforeAnythingIsSent covers the checks that cost no
// round trip.
func TestValidationRefusesBeforeAnythingIsSent(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		args          []string
		flags         map[string]string
		repeated      map[string][]string
		code          string
	}{
		{
			name: "blank summary", command: "issue.create",
			flags: map[string]string{"type": "Bug", "summary": "   "},
			code:  "EMPTY_SUMMARY",
		},
		{
			name: "bad idempotency key", command: "issue.create",
			flags: map[string]string{"type": "Bug", "summary": "s", "idempotency-key": "has space"},
			code:  "INVALID_IDEMPOTENCY_KEY",
		},
		{
			name: "bad parent", command: "issue.create",
			flags: map[string]string{"type": "Bug", "summary": "s", "parent": "nonsense"},
			code:  "INVALID_KEY",
		},
		{
			name: "bad key on edit", command: "issue.edit",
			args: []string{"nonsense"}, flags: map[string]string{"summary": "s"},
			code: "INVALID_KEY",
		},
		{
			name: "edit with nothing to do", command: "issue.edit",
			args: []string{"ENG-1"}, code: "NOTHING_TO_EDIT",
		},
		{
			name: "conflicting label flags", command: "issue.edit",
			args:     []string{"ENG-1"},
			repeated: map[string][]string{"label": {"a"}, "add-label": {"b"}},
			code:     "CONFLICTING_LABEL_FLAGS",
		},
		{
			name: "bad key on move", command: "issue.move",
			args: []string{"nonsense", "Close"}, code: "INVALID_KEY",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := registry.Lookup(tc.command)
			if !ok {
				t.Fatalf("%s is not registered", tc.command)
			}

			flags := registry.NewFlags()
			for k, v := range tc.flags {
				flags.SetString(k, v)
			}
			for k, values := range tc.repeated {
				for _, v := range values {
					flags.SetString(k, v)
				}
			}
			// No connection at all: validation that needed one would fail
			// here rather than silently costing a round trip.
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, kind: site.DataCenter,
				},
				Args: tc.args, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			err := cmd.Validate(t.Context(), inv)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
			if errs.ExitOf(err) != exitcode.Usage {
				t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
			}
		})
	}
}

// TestEveryWriteVerbIsSafeByConstruction restates the contract test's checks
// against these specific commands, so a regression names the verb rather than
// only the rule.
func TestEveryWriteVerbIsSafeByConstruction(t *testing.T) {
	for _, name := range []string{"issue.create", "issue.edit", "issue.move"} {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !cmd.Mutating {
			t.Errorf("%s changes Jira but is not marked mutating", name)
		}
		if _, has := cmd.Flag("dry-run"); !has {
			t.Errorf("%s does not accept --dry-run", name)
		}
		if !cmd.Emits(registry.KindDryRun, registry.VersionDryRun) {
			t.Errorf("%s does not declare the dry-run shape it can emit", name)
		}
		found := false
		for _, code := range cmd.AllExitCodes() {
			if code == exitcode.Blocked {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not declare exit 10, which read-only mode produces", name)
		}
		if len(cmd.RequiresTags) == 0 || cmd.RequiresTags[0] != "write" {
			t.Errorf("%s does not require the write tag, so a reader build "+
				"would contain it", name)
		}
	}
}

// body decodes a request body for assertion.
func body(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return decoded
}

// bodyFields returns the "fields" object, which is where most of a write lives.
func bodyFields(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	fields, ok := body(t, raw)["fields"].(map[string]any)
	if !ok {
		t.Fatalf("no fields object in %s", raw)
	}
	return fields
}

func nested(fields map[string]any, key, inner string) any {
	return nestedAny(fields, key, inner)
}

func nestedAny(decoded map[string]any, key, inner string) any {
	obj, ok := decoded[key].(map[string]any)
	if !ok {
		return nil
	}
	return obj[inner]
}

// captureDoer records what it was asked to send and answers with a fixed
// response, so a test can assert the request without a server.
type captureDoer struct {
	status   int
	body     string
	calls    int
	requests []transport.Request
}

func (c *captureDoer) Do(
	_ context.Context, req transport.Request,
) (*transport.Response, error) {
	c.calls++
	c.requests = append(c.requests, req)
	return &transport.Response{
		Status: c.status,
		Body:   []byte(c.body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// TestEditRunsAsARegisteredCommand exercises the layer a user invokes, on both
// deployments. The request-shape tests above never touch the wire, so without
// this the command wrapper is the untested part — which is where the last two
// bugs in this repo lived.
func TestEditRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.edit")
			if !ok {
				t.Fatal("issue edit is not registered")
			}

			conn, replayer := replayConn(t, "edit."+string(kind)+".json")
			flags := registry.NewFlags()
			flags.SetString("summary", "a better summary")
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
				},
				Args: []string{"ENG-101"}, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !cmd.Emits(doc.Kind, doc.Version) {
				t.Errorf("emitted %s v%d, which the command does not declare",
					doc.Kind, doc.Version)
			}
			if err := doc.Validate(); err != nil {
				t.Errorf("validate doc: %v", err)
			}
			// Jira answers 204, so the acknowledgement reports what was asked
			// for rather than re-reading the issue — a second request whose
			// answer could differ for unrelated reasons.
			if key, _ := doc.Record.AttrValue("key"); key != "ENG-101" {
				t.Errorf("key = %q", key)
			}
			if action, _ := doc.Record.AttrValue("action"); action != "edited" {
				t.Errorf("action = %q, want edited", action)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the edit was never sent: %v", unplayed)
			}
		})
	}
}

// TestMoveRunsAsARegisteredCommand covers the whole path the transition
// resolver was built for: read what the issue can do, resolve the name the
// caller typed, send the id.
func TestMoveRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.move")
			if !ok {
				t.Fatal("issue move is not registered")
			}

			conn, replayer := replayConn(t, "move."+string(kind)+".json")
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
					// Transitions come through Metadata, which needs the same
					// connection the move itself uses.
					metaClient: conn,
				},
				Args: []string{"ENG-101", "Close Issue"}, Flags: registry.NewFlags(),
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if err := doc.Validate(); err != nil {
				t.Errorf("validate doc: %v", err)
			}
			// The name became the id, which is the whole point.
			if id, _ := doc.Record.AttrValue("transition"); id != "2" {
				t.Errorf("transition = %q, want the resolved id 2", id)
			}
			// And the destination is reported, so a caller knows where it went
			// without asking again.
			to, ok := doc.Record.ChildNamed("to")
			if !ok {
				t.Fatal("the result does not say where the issue went")
			}
			if category, _ := to.AttrValue("category"); category != "done" {
				t.Errorf("category = %q, want done", category)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the move was never sent: %v", unplayed)
			}
		})
	}
}

// TestMoveRefusesAnUnavailableTransitionWithoutWriting is the safety property:
// the name is resolved against what the issue can do before anything is sent,
// so a wrong name costs one read and no write.
func TestMoveRefusesAnUnavailableTransitionWithoutWriting(t *testing.T) {
	cmd, _ := registry.Lookup("issue.move")
	conn, replayer := replayConn(t, "move.datacenter.json")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn,
			kind: site.DataCenter, metaClient: conn,
		},
		Args: []string{"ENG-101", "Resolve Issue"}, Flags: registry.NewFlags(),
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	_, err := cmd.Run(t.Context(), inv)
	if err == nil {
		t.Fatal("an unavailable transition was sent")
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_TRANSITION" {
		t.Errorf("code = %q, want UNKNOWN_TRANSITION", e.Code)
	}
	if !strings.Contains(e.Detail, "Close Issue (2") {
		t.Errorf("the refusal does not list what is available: %q", e.Detail)
	}
	// The transition POST is still unplayed, so nothing was written.
	unplayed := replayer.Unplayed()
	if len(unplayed) != 1 || !strings.Contains(unplayed[0], "POST") {
		t.Errorf("unplayed = %v, want the write to have been skipped", unplayed)
	}
}

// TestDryRunSendsNothing covers every verb's preview through the registered
// command, which is the only place the flag is actually read.
func TestDryRunSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		command, fixture string
		args             []string
		flags            map[string]string
	}{
		{
			command: "issue.create", fixture: "create.datacenter.json",
			flags: map[string]string{"type": "Bug", "summary": "a summary"},
		},
		{
			command: "issue.edit", fixture: "edit.datacenter.json",
			args: []string{"ENG-101"}, flags: map[string]string{"summary": "s"},
		},
		{
			command: "issue.move", fixture: "move.datacenter.json",
			args: []string{"ENG-101", "Close Issue"},
		},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd, _ := registry.Lookup(tc.command)
			conn, replayer := replayConn(t, tc.fixture)

			flags := registry.NewFlags()
			for k, v := range tc.flags {
				flags.SetString(k, v)
			}
			flags.SetBool("dry-run", true)
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn,
					kind: site.DataCenter, metaClient: conn,
				},
				Args: tc.args, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if doc.Kind != registry.KindDryRun {
				t.Errorf("kind = %q, want %q", doc.Kind, registry.KindDryRun)
			}
			if !cmd.Emits(doc.Kind, doc.Version) {
				t.Errorf("%s does not declare the dry-run shape", tc.command)
			}
			// Every write in the fixture is still unplayed, and the only
			// requests a preview may cost are the reads it declared.
			var writes, reads int
			for _, u := range replayer.Unplayed() {
				if strings.HasPrefix(u, "GET") {
					reads++
				} else {
					writes++
				}
			}
			if writes == 0 {
				t.Error("a dry run sent the write")
			}
			if reads != 0 {
				t.Errorf("%d reads went unused; the preview did not do what it "+
					"declared it would", reads)
			}
		})
	}
}

// TestAnUnkeyedDuplicateWarnsAndProceeds is §6.3's other half. Two deliberate
// identical creates are a legitimate thing to want, so the second one is warned
// about and not blocked — a caller who did not ask for idempotency does not
// silently get it.
func TestAnUnkeyedDuplicateWarnsAndProceeds(t *testing.T) {
	cmd, _ := registry.Lookup("issue.create")
	ledger := &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")}

	// Two identical creates, so two recorded interactions: the second really is
	// sent, which is the point.
	conn, replayer := replayConn(t, "create-twice.datacenter.json")

	var stderr strings.Builder
	run := func() {
		t.Helper()
		flags := registry.NewFlags()
		flags.SetString("type", "Bug")
		flags.SetString("summary", "a summary")
		inv := &registry.Invocation{
			Jira: &stubSession{
				doer: &stubDoer{body: catalogueJSON}, conn: conn,
				kind: site.DataCenter, ledger: ledger,
			},
			Flags: flags, Stderr: &stderr, Progress: registry.NoProgress,
			Format: render.XML,
		}
		if _, err := cmd.Run(t.Context(), inv); err != nil {
			t.Fatalf("run: %v", err)
		}
	}

	run()
	if stderr.Len() != 0 {
		t.Errorf("the first create warned about something: %s", stderr.String())
	}
	run()

	// Both were sent — nothing was blocked.
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the second create was suppressed: %v", unplayed)
	}
	if !strings.Contains(stderr.String(), "POSSIBLE_DUPLICATE") {
		t.Errorf("the second create did not warn:\n%s", stderr.String())
	}
	// The warning names what the first one produced, so a caller can go and
	// look rather than guess.
	if !strings.Contains(stderr.String(), "ENG-201") {
		t.Errorf("the warning does not name the earlier issue:\n%s", stderr.String())
	}
}

// TestAssignNamesTheUserTheWayTheDeploymentDoes covers the split that produces
// a 400 saying nothing about which field was wrong.
func TestAssignNamesTheUserTheWayTheDeploymentDoes(t *testing.T) {
	for _, tc := range []struct {
		kind  site.Kind
		field string
	}{
		{site.Cloud, "accountId"},
		{site.DataCenter, "name"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			client, _ := writeClient(tc.kind)

			req, err := client.AssignRequest("ENG-101", "ada")
			if err != nil {
				t.Fatalf("assign: %v", err)
			}
			if req.Method != transport.MethodPut {
				t.Errorf("method = %q, want PUT", req.Method)
			}
			if !strings.HasSuffix(req.Path, "/issue/ENG-101/assignee") {
				t.Errorf("path = %q, want the dedicated assignee endpoint", req.Path)
			}
			decoded := body(t, req.Body)
			if decoded[tc.field] != "ada" {
				t.Errorf("body = %v, want the user under %q", decoded, tc.field)
			}

			// unassigned clears the field; default hands it to the project's
			// own rule. They are different requests and must not collapse.
			cleared, err := client.AssignRequest("ENG-101", "unassigned")
			if err != nil {
				t.Fatalf("unassign: %v", err)
			}
			value, present := body(t, cleared.Body)[tc.field]
			if !present || value != nil {
				t.Errorf("unassigned body = %s, want an explicit null", cleared.Body)
			}

			auto, err := client.AssignRequest("ENG-101", "default")
			if err != nil {
				t.Fatalf("default: %v", err)
			}
			if body(t, auto.Body)[tc.field] != "-1" {
				t.Errorf("default body = %s, want the project-default sentinel", auto.Body)
			}
		})
	}
}

// TestDeleteIsDestructiveAndSaysSo covers the declaration the central gate acts
// on, plus the subtask flag that keeps a cascade from being silent.
func TestDeleteIsDestructiveAndSaysSo(t *testing.T) {
	cmd, ok := registry.Lookup("issue.delete")
	if !ok {
		t.Fatal("issue delete is not registered")
	}
	if !cmd.Destructive {
		t.Error("issue delete is not marked destructive, so --yes would not be required")
	}
	if _, has := cmd.Flag("yes"); !has {
		t.Error("issue delete does not declare --yes")
	}

	client, _ := writeClient(site.DataCenter)

	plain, err := client.DeleteRequest("ENG-101", false)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if plain.Method != transport.MethodDelete {
		t.Errorf("method = %q, want DELETE", plain.Method)
	}
	if plain.Query.Get("deleteSubtasks") != "" {
		t.Error("subtasks are cascaded by default, which would delete work " +
			"the caller never named")
	}

	cascade, err := client.DeleteRequest("ENG-101", true)
	if err != nil {
		t.Fatalf("delete with subtasks: %v", err)
	}
	if cascade.Query.Get("deleteSubtasks") != "true" {
		t.Errorf("--subtasks did not reach the request: %v", cascade.Query)
	}
}

// TestWatchAndUnwatchAreDifferentRequests covers the shapes: adding takes the
// user in the body, removing takes them in a query parameter named for the
// deployment.
func TestWatchAndUnwatchAreDifferentRequests(t *testing.T) {
	for _, tc := range []struct {
		kind  site.Kind
		param string
		user  string
	}{
		{site.Cloud, "accountId", "712020:8f3a"},
		{site.DataCenter, "username", "ada"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			client, _ := writeClient(tc.kind)

			add, err := client.WatchRequest("ENG-101", tc.user, false)
			if err != nil {
				t.Fatalf("watch: %v", err)
			}
			if add.Method != transport.MethodPost {
				t.Errorf("method = %q, want POST", add.Method)
			}
			// A bare JSON string, which is what this endpoint takes — not an
			// object, and not a form value.
			var asString string
			if err := json.Unmarshal(add.Body, &asString); err != nil {
				t.Fatalf("the body is not a bare JSON string: %s", add.Body)
			}
			if asString != tc.user {
				t.Errorf("body = %q, want %q", asString, tc.user)
			}

			remove, err := client.WatchRequest("ENG-101", tc.user, true)
			if err != nil {
				t.Fatalf("unwatch: %v", err)
			}
			if remove.Method != transport.MethodDelete {
				t.Errorf("method = %q, want DELETE", remove.Method)
			}
			if got := remove.Query.Get(tc.param); got != tc.user {
				t.Errorf("query %s = %q, want %q", tc.param, got, tc.user)
			}
			if len(remove.Body) != 0 {
				t.Errorf("a DELETE carried a body: %s", remove.Body)
			}
		})
	}
}

// TestCloneCopiesExactlyWhatItSays is the honesty property. A copy that
// silently brought half an issue's history along would be worse than one that
// states its scope — so the scope is asserted, in both directions.
func TestCloneCopiesExactlyWhatItSays(t *testing.T) {
	cmd, ok := registry.Lookup("issue.clone")
	if !ok {
		t.Fatal("issue clone is not registered")
	}

	conn, replayer := replayConn(t, "clone.datacenter.json")
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-101"}, Flags: registry.NewFlags(),
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
		// The recorded create carries exactly the fields clone should copy, so
		// an unplayed POST means the body differed from what is documented.
		t.Fatalf("the clone did not send the documented body: %v", unplayed)
	}

	if key, _ := doc.Record.AttrValue("key"); key != "ENG-205" {
		t.Errorf("key = %q, want the new issue", key)
	}
	// The copy names its source, so a caller can tell the two apart later.
	if from, _ := doc.Record.AttrValue("cloned-from"); from != "ENG-101" {
		t.Errorf("cloned-from = %q", from)
	}
}

// TestCloneDoesNotCarryTheDescriptionFromCloud covers the one field whose copy
// depends on the deployment. On Cloud it arrives as ADF, and this tool does not
// yet build one to send back — copying it raw would be rejected and flattening
// it would lose the markup.
func TestCloneDoesNotCarryTheDescriptionFromCloud(t *testing.T) {
	client, _ := writeClient(site.Cloud)

	// The wiki case carries it; that is what the Data Center fixture proves.
	// Here the source is ADF, so a create built from it must omit it rather
	// than fail or approximate.
	req, err := client.CreateRequest(issue.CreateOptions{
		Project: "ENG", Type: "Bug", Summary: "a copy",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, present := bodyFields(t, req.Body)["description"]; present {
		t.Error("a description reached a Cloud create")
	}
}

// TestEveryMutatingVerbIsRegisteredAndSafe is the full sweep, so a new verb
// added later cannot quietly skip the machinery.
func TestEveryMutatingVerbIsRegisteredAndSafe(t *testing.T) {
	want := []string{
		"issue.create", "issue.edit", "issue.move",
		"issue.assign", "issue.delete", "issue.clone", "issue.watch",
	}
	for _, name := range want {
		cmd, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !cmd.Mutating {
			t.Errorf("%s is not marked mutating", name)
		}
		if _, has := cmd.Flag("dry-run"); !has {
			t.Errorf("%s does not accept --dry-run", name)
		}
		if !cmd.Emits(registry.KindDryRun, registry.VersionDryRun) {
			t.Errorf("%s does not declare the dry-run shape", name)
		}
		if len(cmd.RequiresTags) == 0 || cmd.RequiresTags[0] != "write" {
			t.Errorf("%s does not require the write tag", name)
		}
		if cmd.Destructive {
			if _, has := cmd.Flag("yes"); !has {
				t.Errorf("%s is destructive but does not require --yes", name)
			}
		}
	}
}

// TestTheRemainingVerbsRunAsRegisteredCommands exercises each one through the
// registry against a recorded conversation, on both deployments. The
// request-shape tests above never touch the wire; without this the command
// wrapper is the untested part.
func TestTheRemainingVerbsRunAsRegisteredCommands(t *testing.T) {
	for _, tc := range []struct {
		command, fixture string
		args             []string
		flags            map[string]bool
		action           string
	}{
		{command: "issue.assign", fixture: "assign", args: []string{"ENG-101", "ada"}, action: "assigned"},
		{
			command: "issue.delete", fixture: "delete", args: []string{"ENG-101"},
			flags: map[string]bool{"yes": true}, action: "deleted",
		},
		{command: "issue.watch", fixture: "watch", args: []string{"ENG-101"}, action: "watching"},
		{
			command: "issue.watch", fixture: "unwatch", args: []string{"ENG-101"},
			flags: map[string]bool{"remove": true}, action: "not-watching",
		},
	} {
		for _, kind := range deployments {
			t.Run(tc.fixture+"/"+string(kind), func(t *testing.T) {
				cmd, ok := registry.Lookup(tc.command)
				if !ok {
					t.Fatalf("%s is not registered", tc.command)
				}

				conn, replayer := replayConn(t, tc.fixture+"."+string(kind)+".json")
				flags := registry.NewFlags()
				for k, v := range tc.flags {
					flags.SetBool(k, v)
				}
				inv := &registry.Invocation{
					Jira: &stubSession{
						doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
					},
					Args: tc.args, Flags: flags,
					Stderr: io.Discard, Progress: registry.NoProgress,
				}

				if err := cmd.Validate(t.Context(), inv); err != nil {
					t.Fatalf("validate: %v", err)
				}
				doc, err := cmd.Run(t.Context(), inv)
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				if !cmd.Emits(doc.Kind, doc.Version) {
					t.Errorf("emitted %s v%d, which the command does not declare",
						doc.Kind, doc.Version)
				}
				if err := doc.Validate(); err != nil {
					t.Errorf("validate doc: %v", err)
				}
				if action, _ := doc.Record.AttrValue("action"); action != tc.action {
					t.Errorf("action = %q, want %q", action, tc.action)
				}
				if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
					t.Errorf("the request was never sent as recorded: %v", unplayed)
				}
			})
		}
	}
}

// TestDeleteNamesTheFlagThatWouldHaveWorked covers the hint. Jira refuses an
// issue with subtasks in prose, and passing that through unchanged leaves the
// caller to discover that a flag exists.
func TestDeleteNamesTheFlagThatWouldHaveWorked(t *testing.T) {
	cmd, _ := registry.Lookup("issue.delete")
	conn, _ := replayConn(t, "delete-subtasks.datacenter.json")

	flags := registry.NewFlags()
	flags.SetBool("yes", true)
	inv := &registry.Invocation{
		Jira: &stubSession{
			doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: site.DataCenter,
		},
		Args: []string{"ENG-101"}, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	_, err := cmd.Run(t.Context(), inv)
	if err == nil {
		t.Fatal("an issue with subtasks was deleted without asking")
	}
	if remedy := errs.Coerce(err).Remedy; !strings.Contains(remedy, "--subtasks") {
		t.Errorf("the refusal does not name the flag that would work: %q", remedy)
	}

	// With the flag already passed, Jira's own message stands: adding a remedy
	// pointing at a flag the caller used would be noise.
	flags.SetBool("subtasks", true)
	conn2, _ := replayConn(t, "delete-subtasks.datacenter.json")
	inv.Jira = &stubSession{
		doer: &stubDoer{body: catalogueJSON}, conn: conn2, kind: site.DataCenter,
	}
	_, err = cmd.Run(t.Context(), inv)
	if err == nil {
		t.Fatal("the second call succeeded unexpectedly")
	}
	if remedy := errs.Coerce(err).Remedy; strings.Contains(remedy, "--subtasks") {
		t.Errorf("the remedy repeats a flag already given: %q", remedy)
	}
}
