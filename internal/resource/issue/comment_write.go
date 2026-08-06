//go:build write

package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/kmoneil/jira-cli/internal/adf"
	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds the comment write verbs emit.
const (
	KindCommentAdd       = "issue.comment.add"
	VersionCommentAdd    = 2
	KindCommentEdit      = "issue.comment.edit"
	VersionCommentEdit   = 1
	KindCommentDelete    = "issue.comment.delete"
	VersionCommentDelete = 1
)

func init() {
	registry.Register(commentAddCommand())
	registry.Register(commentEditCommand())
	registry.Register(commentDeleteCommand())
}

// bodyValue encodes comment or description text the way this deployment takes
// it.
//
// Data Center takes a string of wiki markup. Cloud takes an ADF document, so
// the text is *contained* in one — which is exact — rather than interpreted,
// which would not be. Nothing here reads `**bold**` as anything but six
// characters, and that is also what Data Center does with the same input: the
// text reaches the server as typed, and the server decides what it means.
//
// Turning markdown into real ADF marks is a different job with its own failure
// modes. It belongs in internal/adf's write-side subset, where an
// unrepresentable construct can be refused by name.
func bodyValue(kind site.Kind, text string) (any, error) {
	if kind != site.Cloud {
		return text, nil
	}
	doc, err := adf.FromText(text)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func commentAddCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "comment", "add"},
		Summary: "Add a comment to an issue",
		Description: strings.TrimSpace(`
Adds one comment.

The text is sent as plain text. No markup is interpreted: **bold** reaches Jira
as six characters and Jira decides what to do with them, which is also what
happens on Data Center. On Cloud the text is wrapped in the document structure
the API requires — a blank line starts a paragraph and a single newline is a
line break — because containing text is exact where interpreting it is not.

--dry-run prints the exact request, body included, and sends nothing.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue comment add ENG-101 'Reproduced on 9.12.7'",
			buildinfo.App + " issue comment add ENG-101 \"$(cat notes.txt)\"",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{Name: "body", Usage: "the comment text", Required: true},
		},
		Flags: []registry.Flag{
			{
				Name: "visibility", Type: registry.TypeString,
				Usage: "restrict to a role, e.g. 'role:Administrators'",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindCommentAdd, Version: VersionCommentAdd},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateCommentBody,
		Run:       runCommentAdd,
	}
}

func validateCommentBody(_ context.Context, inv *registry.Invocation) error {
	if err := requireIssueKey(inv); err != nil {
		return err
	}
	body := inv.Args[len(inv.Args)-1]
	if strings.TrimSpace(body) == "" {
		return errs.Usage("EMPTY_COMMENT", "a comment cannot be blank").
			WithRemedy("pass the text as the last argument")
	}
	if v := inv.Flags.String("visibility"); v != "" && !strings.Contains(v, ":") {
		return errs.Usage("INVALID_VISIBILITY",
			"%q does not name a visibility restriction", v).
			WithDetail("the form is <type>:<value>, e.g. role:Administrators").
			WithRemedy("pass role:<name> or group:<name>")
	}
	return nil
}

// commentBody builds the request body shared by add and edit.
func (c *Client) commentBody(text, visibility string) ([]byte, error) {
	value, err := bodyValue(c.Site.Kind, text)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{"body": value}
	if visibility != "" {
		kind, name, _ := strings.Cut(visibility, ":")
		payload["visibility"] = map[string]any{"type": kind, "value": name}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errs.Runtime("ENCODE_FAILED", "cannot encode the comment").Wrap(err)
	}
	return body, nil
}

// CommentAddRequest builds the request without sending it.
func (c *Client) CommentAddRequest(key, text, visibility string) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	body, err := c.commentBody(text, visibility)
	if err != nil {
		return transport.Request{}, err
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/comment",
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runCommentAdd(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue comment add")
	if err != nil {
		return nil, err
	}

	req, err := client.CommentAddRequest(
		inv.Args[0], inv.Args[1], inv.Flags.String("visibility"),
	)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.comment.add", req), nil
	}

	resp, err := client.Transport.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	// The created comment comes back in full, so its id is reported rather than
	// left for the caller to find by listing again.
	var raw rawComment
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.ID == "" {
		return nil, errs.Remote("MALFORMED_COMMENT_RESPONSE",
			"%s did not report the comment it created", req.Path).
			WithRequestID(resp.RequestID).
			WithDetail("the comment may exist; list them before retrying").
			Wrap(err)
	}
	comment, err := raw.convert(client.Body)
	if err != nil {
		return nil, err
	}

	return render.Record(KindCommentAdd, VersionCommentAdd,
		comment.Node().Attr("issue", inv.Args[0])), nil
}

func commentEditCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "comment", "edit"},
		Summary: "Replace the text of a comment",
		Description: strings.TrimSpace(`
Replaces a comment's body. The comment is named by its id, which
` + "`" + buildinfo.App + ` issue comment list` + "`" + ` reports — not by its position, which changes as
comments are added.

The text is handled exactly as on add: sent as plain text, with no markup
interpreted.`),
		Example: buildinfo.App + " issue comment edit ENG-101 10042 'Corrected: it was 9.12.7'",
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{Name: "id", Usage: "comment id, from `comment list`", Required: true},
			{Name: "body", Usage: "the replacement text", Required: true},
		},
		Flags: []registry.Flag{
			{
				Name: "visibility", Type: registry.TypeString,
				Usage: "restrict to a role, e.g. 'role:Administrators'",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindCommentEdit, Version: VersionCommentEdit},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateCommentBody,
		Run:       runCommentEdit,
	}
}

// CommentEditRequest builds the replacement without sending it.
func (c *Client) CommentEditRequest(
	key, id, text, visibility string,
) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	if err := validCommentID(id); err != nil {
		return transport.Request{}, err
	}
	body, err := c.commentBody(text, visibility)
	if err != nil {
		return transport.Request{}, err
	}
	return transport.Request{
		Method: transport.MethodPut,
		Path: c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) +
			"/comment/" + url.PathEscape(id),
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runCommentEdit(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue comment edit")
	if err != nil {
		return nil, err
	}

	req, err := client.CommentEditRequest(
		inv.Args[0], inv.Args[1], inv.Args[2], inv.Flags.String("visibility"),
	)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.comment.edit", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindCommentEdit, VersionCommentEdit, render.El("comment").
		Attr("id", inv.Args[1]).
		Attr("issue", inv.Args[0]).
		Attr("action", "edited")), nil
}

func commentDeleteCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "comment", "delete"},
		Summary: "Delete a comment",
		Description: strings.TrimSpace(`
Deletes one comment permanently. Jira has no undo for this, which is why --yes
is required: a confirmation this tool cannot ask for is a refusal rather than a
question nobody can answer.

--dry-run shows the request without needing --yes, because you look at what
would happen in order to decide whether to allow it.`),
		Example: buildinfo.App + " issue comment delete ENG-101 10042 --yes",
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{Name: "id", Usage: "comment id, from `comment list`", Required: true},
		},
		Flags: []registry.Flag{
			{Name: "yes", Type: registry.TypeBool, Usage: "confirm the deletion"},
			dryRunFlag(),
		},
		Mutating:     true,
		Destructive:  true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindCommentDelete, Version: VersionCommentDelete},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if err := requireIssueKey(inv); err != nil {
				return err
			}
			if len(inv.Args) < 2 {
				return errs.Usage("MISSING_COMMENT_ID", "a comment id is required")
			}
			return validCommentID(inv.Args[1])
		},
		Run: runCommentDelete,
	}
}

// CommentDeleteRequest builds the deletion without sending it.
func (c *Client) CommentDeleteRequest(key, id string) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	if err := validCommentID(id); err != nil {
		return transport.Request{}, err
	}
	return transport.Request{
		Method: transport.MethodDelete,
		Path: c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) +
			"/comment/" + url.PathEscape(id),
	}, nil
}

func runCommentDelete(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue comment delete")
	if err != nil {
		return nil, err
	}

	req, err := client.CommentDeleteRequest(inv.Args[0], inv.Args[1])
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.comment.delete", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindCommentDelete, VersionCommentDelete, render.El("comment").
		Attr("id", inv.Args[1]).
		Attr("issue", inv.Args[0]).
		Attr("action", "deleted")), nil
}

// validCommentID rejects an id this tool cannot address.
//
// Jira comment ids are numeric strings. Checking locally means a typo costs no
// round trip, and it keeps a caller's argument from reaching a URL path as
// anything but digits.
func validCommentID(id string) error {
	if id == "" {
		return errs.Usage("INVALID_COMMENT_ID", "a comment id is required")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return errs.Usage("INVALID_COMMENT_ID", "%q is not a comment id", id).
				WithDetail("a comment id is digits, e.g. 10042").
				WithRemedy("take it from `" + buildinfo.App + " issue comment list`")
		}
	}
	return nil
}
