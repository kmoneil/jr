//go:build write

package issue_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// TestCommentBodyIsContainedNotConverted is the same rule as a description,
// applied where it matters most: a comment is the most common write an agent
// makes, so refusing one on Cloud would have made the tool useless there.
func TestCommentBodyIsContainedNotConverted(t *testing.T) {
	const text = "line one\nline two\n\nsecond with **bold**"

	dc, _ := writeClient(site.DataCenter)
	req, err := dc.CommentAddRequest("ENG-101", text, "")
	if err != nil {
		t.Fatalf("data center: %v", err)
	}
	if got := body(t, req.Body)["body"]; got != text {
		t.Errorf("body = %v, want the text unchanged", got)
	}

	cloud, _ := writeClient(site.Cloud)
	req, err = cloud.CommentAddRequest("ENG-101", text, "")
	if err != nil {
		t.Fatalf("cloud: %v", err)
	}
	doc, ok := body(t, req.Body)["body"].(map[string]any)
	if !ok {
		t.Fatalf("cloud body is not a document: %s", req.Body)
	}
	if doc["type"] != "doc" {
		t.Errorf("type = %v, want doc", doc["type"])
	}
	if content, _ := doc["content"].([]any); len(content) != 2 {
		t.Errorf("got %d paragraphs, want 2: %s", len(content), req.Body)
	}
	if !strings.Contains(string(req.Body), "**bold**") {
		t.Errorf("the text was altered: %s", req.Body)
	}
	if strings.Contains(string(req.Body), `"strong"`) {
		t.Errorf("markdown was interpreted: %s", req.Body)
	}
}

// TestARestrictedCommentIsSentAsOne covers the visibility flag, which is the
// difference between a note to the team and a note to everyone.
func TestARestrictedCommentIsSentAsOne(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	req, err := client.CommentAddRequest("ENG-101", "internal", "role:Administrators")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	visibility, ok := body(t, req.Body)["visibility"].(map[string]any)
	if !ok {
		t.Fatalf("no visibility in %s", req.Body)
	}
	if visibility["type"] != "role" || visibility["value"] != "Administrators" {
		t.Errorf("visibility = %v", visibility)
	}

	// And an unrestricted comment carries no visibility key at all, rather than
	// an empty one, which Jira would reject.
	plain, err := client.CommentAddRequest("ENG-101", "public", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, present := body(t, plain.Body)["visibility"]; present {
		t.Errorf("an unrestricted comment carried a visibility: %s", plain.Body)
	}
}

// TestCommentIdsAreCheckedLocally keeps a typo from costing a round trip, and
// keeps a caller's argument from reaching a URL path as anything but digits.
func TestCommentIdsAreCheckedLocally(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	for _, bad := range []string{"", "abc", "10042x", "../../admin", "10 042"} {
		if _, err := client.CommentDeleteRequest("ENG-101", bad); err == nil {
			t.Errorf("comment id %q was accepted", bad)
		} else if code := errs.Coerce(err).Code; code != "INVALID_COMMENT_ID" {
			t.Errorf("%q code = %q, want INVALID_COMMENT_ID", bad, code)
		}
	}
	if _, err := client.CommentDeleteRequest("ENG-101", "10042"); err != nil {
		t.Errorf("a real id was refused: %v", err)
	}
}

// TestCommentWriteVerbsValidateBeforeSending covers the checks that cost
// nothing, including the one that stops a blank comment reaching Jira.
func TestCommentWriteVerbsValidateBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		args          []string
		flags         map[string]string
		code          string
	}{
		{
			name: "blank body", command: "issue.comment.add",
			args: []string{"ENG-101", "   "}, code: "EMPTY_COMMENT",
		},
		{
			name: "bad key", command: "issue.comment.add",
			args: []string{"nonsense", "text"}, code: "INVALID_KEY",
		},
		{
			name: "visibility with no type", command: "issue.comment.add",
			args:  []string{"ENG-101", "text"},
			flags: map[string]string{"visibility": "Administrators"},
			code:  "INVALID_VISIBILITY",
		},
		{
			name: "bad id on delete", command: "issue.comment.delete",
			args: []string{"ENG-101", "abc"}, code: "INVALID_COMMENT_ID",
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
			inv := &registry.Invocation{Args: tc.args, Flags: flags}

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

// TestCommentAddRunsAsARegisteredCommand exercises the command wrapper on both
// deployments, and asserts the created comment's id comes back rather than
// leaving the caller to list again to find it.
func TestCommentAddRunsAsARegisteredCommand(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			cmd, ok := registry.Lookup("issue.comment.add")
			if !ok {
				t.Fatal("issue comment add is not registered")
			}

			conn, replayer := replayConn(t, "comment-add."+string(kind)+".json")
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn, kind: kind,
				},
				Args: []string{"ENG-101", "a new comment"}, Flags: registry.NewFlags(),
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
			if !cmd.Emits(doc.Kind, doc.Version) {
				t.Errorf("emitted %s v%d, undeclared", doc.Kind, doc.Version)
			}
			if id, _ := doc.Record.AttrValue("id"); id != "10099" {
				t.Errorf("id = %q, want the created comment's", id)
			}
			if key, _ := doc.Record.AttrValue("issue"); key != "ENG-101" {
				t.Errorf("issue = %q", key)
			}
		})
	}
}

// TestCommentDeleteIsDestructive covers the declaration the central gate acts
// on, and the request it would send.
func TestCommentDeleteIsDestructive(t *testing.T) {
	cmd, ok := registry.Lookup("issue.comment.delete")
	if !ok {
		t.Fatal("issue comment delete is not registered")
	}
	if !cmd.Destructive {
		t.Error("comment delete is not destructive, so --yes would not be required")
	}
	if _, has := cmd.Flag("yes"); !has {
		t.Error("comment delete does not declare --yes")
	}

	for _, kind := range deployments {
		conn, replayer := replayConn(t, "comment-delete."+string(kind)+".json")
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
			t.Fatalf("%s validate: %v", kind, err)
		}
		doc, err := cmd.Run(t.Context(), inv)
		if err != nil {
			t.Fatalf("%s run: %v", kind, err)
		}
		if action, _ := doc.Record.AttrValue("action"); action != "deleted" {
			t.Errorf("%s action = %q", kind, action)
		}
		if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
			t.Errorf("%s: the delete was never sent: %v", kind, unplayed)
		}
	}
}

// TestCommentDryRunSendsNothing covers the preview on each write verb.
func TestCommentDryRunSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"issue.comment.add", []string{"ENG-101", "text"}},
		{"issue.comment.edit", []string{"ENG-101", "10001", "text"}},
		{"issue.comment.delete", []string{"ENG-101", "10001"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd, _ := registry.Lookup(tc.command)
			conn, replayer := replayConn(t, "comment-delete.datacenter.json")

			flags := registry.NewFlags()
			flags.SetBool("dry-run", true)
			inv := &registry.Invocation{
				Jira: &stubSession{
					doer: &stubDoer{body: catalogueJSON}, conn: conn,
					kind: site.DataCenter,
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
			// The fixture's one interaction is untouched, so nothing was sent.
			if len(replayer.Unplayed()) != 1 {
				t.Error("a dry run reached the wire")
			}
		})
	}
}

// TestCommentPathsEscapeTheirArguments keeps a key or an id from resolving to
// another endpoint on the same host.
func TestCommentPathsEscapeTheirArguments(t *testing.T) {
	client, _ := writeClient(site.DataCenter)
	req, err := client.CommentEditRequest("ENG-101", "10042", "text", "")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if req.Method != transport.MethodPut {
		t.Errorf("method = %q, want PUT", req.Method)
	}
	if req.Path != "/rest/api/2/issue/ENG-101/comment/10042" {
		t.Errorf("path = %q", req.Path)
	}

	var out strings.Builder
	if err := render.Write(&out, registry.DryRunDoc("issue.comment.edit", req),
		render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out.String(), "Authorization") {
		t.Errorf("the preview mentions a credential:\n%s", out.String())
	}
}
