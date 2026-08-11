//go:build write

package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// Kinds the remaining mutating verbs emit.
const (
	KindAssign    = "issue.assign"
	VersionAssign = 2
	KindDelete    = "issue.delete"
	VersionDelete = 1
	KindClone     = "issue.clone"
	VersionClone  = 1
	KindWatch     = "issue.watch"
	VersionWatch  = 1
)

func init() {
	registry.Register(assignCommand())
	registry.Register(deleteCommand())
	registry.Register(cloneCommand())
	registry.Register(watchCommand())
}

// requireKey is the local check every verb taking an issue key shares. A 404
// for a malformed key reads like a missing issue rather than a typo, and the
// value reaches a URL path.
func requireKey(inv *registry.Invocation) error {
	if len(inv.Args) == 0 {
		return errs.Usage("INVALID_KEY", "an issue key is required").
			WithDetail("an issue key looks like ENG-123")
	}
	if _, ok := ParseKey(inv.Args[0]); !ok {
		return errs.Usage("INVALID_KEY", "%q is not an issue key", inv.Args[0]).
			WithDetail("an issue key looks like ENG-123").
			WithRemedy("pass a key, not an id or a summary")
	}
	return nil
}

func assignCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "assign"},
		Summary: "Set or clear an issue's assignee",
		Description: strings.TrimSpace(`
Assigns an issue, through the endpoint Jira provides for it rather than through
a general edit — the two differ in what permission they need, and a caller who
may assign is not always a caller who may edit.

The user is named by their display name, their email address, or the id this
deployment identifies them by — an accountId on Cloud, a username on Data
Center. A name is resolved against the directory before anything is sent, so a
name matching nobody, or two people, is refused here rather than by a 400 that
says nothing about which field was wrong.

A name has to match exactly. A partial one is refused with what it nearly
matched, because a short name that means one person today and a different one
after somebody joins is worse than being asked to be specific.

The word unassigned clears the assignee. The word default hands the issue to
whatever the project's default assignee is, which is not the same thing. The
word currentUser means whoever holds the credential, as it does on issue list.

--if-unchanged refuses the assignment if the issue changed since you read it,
exactly as on issue edit. Reassigning work somebody else has just picked up is
the case it is for.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue assign ENG-101 'Ada Lovelace'",
			buildinfo.App + " issue assign ENG-101 unassigned",
			buildinfo.App + " issue assign ENG-101 default --dry-run",
			buildinfo.App + " issue assign ENG-101 'Ada Lovelace' --if-unchanged eyJkIjo",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{
				Name: "assignee", Required: true,
				Usage: "the user, or the word unassigned or default",
			},
		},
		Flags:        []registry.Flag{ifUnchangedFlagDecl(), dryRunFlag()},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindAssign, Version: VersionAssign},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateAssign,
		Run:       runAssign,
	}
}

// AssignRequest builds the assignment without sending it.
func (c *Client) AssignRequest(key, assignee string) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	// The field name is the deployment's, and so is the sentinel. "-1" is how
	// Jira is told to use the project default; an explicit null clears it.
	field := "name"
	if c.Site.Kind == site.Cloud {
		field = "accountId"
	}

	var value any
	switch {
	case strings.EqualFold(assignee, "unassigned"), strings.EqualFold(assignee, "empty"):
		value = nil
	case strings.EqualFold(assignee, "default"), strings.EqualFold(assignee, "automatic"):
		value = "-1"
	default:
		value = assignee
	}

	body, err := json.Marshal(map[string]any{field: value})
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the assignment").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPut,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/assignee",
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

// validateAssign resolves the user before anything is sent, so a name that
// names nobody — or two people — is refused here rather than by a 400 that
// says which field was wrong and nothing else.
func validateAssign(ctx context.Context, inv *registry.Invocation) error {
	if err := requireKey(inv); err != nil {
		return err
	}
	if len(inv.Args) < 2 {
		return errs.Usage("INVALID_USER", "a user is required").
			WithRemedy("pass a name, an id, or the word unassigned")
	}
	if err := validatePrecondition(inv); err != nil {
		return err
	}
	return validateAssignee(ctx, inv, inv.Args[1])
}

func runAssign(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue assign")
	if err != nil {
		return nil, err
	}

	assignee := resolvedAssignee(inv, inv.Args[1])
	req, err := client.AssignRequest(inv.Args[0], assignee)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.assign", req), nil
	}
	checked, err := checkUnchanged(ctx, inv, client, inv.Args[0])
	if err != nil {
		return nil, err
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	// The id, not the name typed: `issue assign ENG-1 "Ada Lovelace"` and the
	// same command with her accountId produce byte-identical output, which is
	// the rule --field settled. How a request was spelled is not part of the
	// contract.
	return stampPrecondition(render.Record(KindAssign, VersionAssign, render.El("issue").
		Attr("key", inv.Args[0]).
		Attr("action", "assigned").
		Leaf("assignee", assignee)), checked), nil
}

func deleteCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "delete"},
		Summary: "Delete an issue",
		Description: strings.TrimSpace(`
Deletes an issue permanently. Jira has no undo for this.

--yes is required, because nothing here ever blocks on input: a confirmation
this tool cannot ask for is a refusal rather than a question nobody can answer.

An issue with subtasks is refused unless --subtasks says to take them too.
Cascading silently would delete work the caller never named.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue delete ENG-101 --yes",
			buildinfo.App + " issue delete ENG-101 --subtasks --yes",
			buildinfo.App + " issue delete ENG-101 --dry-run",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{
			{Name: "yes", Type: registry.TypeBool, Usage: "confirm the deletion"},
			{
				Name: "subtasks", Type: registry.TypeBool,
				Usage: "delete the issue's subtasks along with it",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		Destructive:  true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindDelete, Version: VersionDelete},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  func(_ context.Context, inv *registry.Invocation) error { return requireKey(inv) },
		Run:       runDelete,
	}
}

// DeleteRequest builds the deletion without sending it.
func (c *Client) DeleteRequest(key string, subtasks bool) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}

	query := url.Values{}
	if subtasks {
		query.Set("deleteSubtasks", "true")
	}
	return transport.Request{
		Method: transport.MethodDelete,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()),
		Query:  query,
	}, nil
}

func runDelete(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue delete")
	if err != nil {
		return nil, err
	}

	req, err := client.DeleteRequest(inv.Args[0], inv.Flags.Bool("subtasks"))
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.delete", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, explainSubtaskRefusal(err, inv.Flags.Bool("subtasks"))
	}

	return render.Record(KindDelete, VersionDelete, render.El("issue").
		Attr("key", inv.Args[0]).
		Attr("action", "deleted")), nil
}

// explainSubtaskRefusal names the flag that would have allowed the deletion.
//
// Jira refuses an issue with subtasks and says so in prose. Passing that
// through unchanged leaves the caller to work out that a flag exists.
func explainSubtaskRefusal(err error, alreadyAsked bool) error {
	if alreadyAsked {
		return err
	}
	e := errs.Coerce(err)
	if !strings.Contains(strings.ToLower(e.Detail), "subtask") {
		return err
	}
	return e.WithRemedy("pass --subtasks to delete them too, or move them first")
}

func cloneCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "clone"},
		Summary: "Create a copy of an issue",
		Description: strings.TrimSpace(`
Reads an issue and creates a new one from it. Jira has no clone endpoint, so
this is a read followed by a create, and it is worth being exact about what
crosses over.

Copied: summary, issue type, priority, labels, description, and parent.

Not copied: status, resolution, assignee, reporter, comments, attachments,
links, worklogs, watchers, and every custom field. A copy that silently brought
half an issue's history along would be worse than one that says what it is.

A Cloud description is also not copied. It arrives as a document, and
re-encoding it as plain text would flatten every mark and link in it.

--summary replaces the copied summary. --idempotency-key makes a retry safe, the
same as on issue create.

--dry-run prints the create it would send, after doing the read it needs to
build one.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue clone ENG-101",
			buildinfo.App + " issue clone ENG-101 --summary 'Retry, second attempt'",
			buildinfo.App + " issue clone ENG-101 --idempotency-key retry-1",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key to copy, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{
			{
				Name: "summary", Type: registry.TypeString,
				Usage: "summary for the copy; defaults to the original's",
			},
			{
				Name: "idempotency-key", Type: registry.TypeString,
				Usage: "make a retry safe: the same key returns the original copy",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindClone, Version: VersionClone},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateClone,
		Run:       runClone,
	}
}

func validateClone(_ context.Context, inv *registry.Invocation) error {
	if err := requireKey(inv); err != nil {
		return err
	}
	if key := inv.Flags.String("idempotency-key"); key != "" {
		return idem.ValidateKey(key)
	}
	return nil
}

func runClone(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue clone")
	if err != nil {
		return nil, err
	}

	source, err := client.Get(ctx, inv.Args[0], DetailFields())
	if err != nil {
		return nil, err
	}

	summary := inv.Flags.String("summary")
	if summary == "" {
		summary = source.Summary
	}
	opt := CreateOptions{
		Project:  source.Project,
		Type:     source.Type,
		Summary:  summary,
		Priority: source.Priority,
		Labels:   source.Labels,
		Parent:   source.Parent,
	}
	// A wiki description crosses over as it is. An ADF one does not: it arrives
	// as a document, and re-encoding it as plain text would flatten every mark
	// and link in it into characters. Dropping it is the honest half-copy;
	// silently flattening would be the dishonest one.
	if source.BodyFormat == BodyWiki {
		opt.Description = source.Description
	}

	req, err := client.CreateRequest(opt)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.clone", req), nil
	}

	made, err := createWithLedger(ctx, inv, client, req)
	if err != nil {
		return nil, err
	}

	n := render.El("issue").
		Attr("key", made.Key).
		Attr("id", made.ID).
		Attr("cloned-from", source.Key)
	if made.Replayed {
		n.Attr("replayed", "true")
	}
	return render.Record(KindClone, VersionClone, n), nil
}

func watchCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "watch"},
		Summary: "Start or stop watching an issue",
		Description: strings.TrimSpace(`
Adds the authenticated user to an issue's watchers, or removes them with
--remove.

It costs one extra request: to watch as yourself the tool has to know who you
are, and who you are is an accountId on Cloud and a username on Data Center.
Guessing either would send a watcher Jira does not recognize.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue watch ENG-101",
			buildinfo.App + " issue watch ENG-101 --remove",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "key", Usage: "issue key, e.g. ENG-101", Required: true,
		}},
		Flags: []registry.Flag{
			{
				Name: "remove", Type: registry.TypeBool,
				Usage: "stop watching instead of starting",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindWatch, Version: VersionWatch},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  func(_ context.Context, inv *registry.Invocation) error { return requireKey(inv) },
		Run:       runWatch,
	}
}

// WatchRequest builds the watcher change without sending it.
//
// The two directions are different requests, not one with a flag: adding takes
// the user in the body, removing takes them in the query, and the parameter is
// named for the deployment.
func (c *Client) WatchRequest(key, user string, remove bool) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	path := c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/watchers"

	if remove {
		param := "username"
		if c.Site.Kind == site.Cloud {
			param = "accountId"
		}
		return transport.Request{
			Method: transport.MethodDelete,
			Path:   path,
			Query:  url.Values{param: {user}},
		}, nil
	}

	// A bare JSON string, which is the shape this endpoint takes — not an
	// object, and not a form value.
	body, err := json.Marshal(user)
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the watcher").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   path,
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runWatch(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue watch")
	if err != nil {
		return nil, err
	}

	account, err := site.Whoami(ctx, client.Transport, client.Site)
	if err != nil {
		return nil, err
	}
	if account.ID == "" {
		return nil, errs.Remote("UNKNOWN_ACCOUNT",
			"this site did not say who the credential belongs to").
			WithRemedy("a watcher has to be named, and the name comes from /myself")
	}

	remove := inv.Flags.Bool("remove")
	req, err := client.WatchRequest(inv.Args[0], account.ID, remove)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.watch", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	action := "watching"
	if remove {
		action = "not-watching"
	}
	return render.Record(KindWatch, VersionWatch, render.El("issue").
		Attr("key", inv.Args[0]).
		Attr("action", action).
		Leaf("watcher", account.ID)), nil
}

// writeClientFor is the opening every mutating verb shares: refuse without a
// session, then connect.
func writeClientFor(
	ctx context.Context, inv *registry.Invocation, command string,
) (*Client, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "%s has no connection to Jira", command)
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{
		Transport: conn, Site: info,
		Body:       bodyMode(inv),
		BodyFormat: inv.Flags.String("body-format"),
	}, nil
}
