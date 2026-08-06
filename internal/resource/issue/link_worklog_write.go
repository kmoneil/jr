//go:build write

package issue

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds the link and worklog write verbs emit.
const (
	KindLinkAdd          = "issue.link.add"
	VersionLinkAdd       = 1
	KindLinkRemove       = "issue.link.remove"
	VersionLinkRemove    = 1
	KindWorklogAdd       = "issue.worklog.add"
	VersionWorklogAdd    = 1
	KindWorklogDelete    = "issue.worklog.delete"
	VersionWorklogDelete = 1
)

func init() {
	registry.Register(linkAddCommand())
	registry.Register(linkRemoveCommand())
	registry.Register(worklogAddCommand())
	registry.Register(worklogDeleteCommand())
}

func linkAddCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "link", "add"},
		Summary: "Link two issues",
		Description: strings.TrimSpace(`
Creates a link, written as a sentence: <from> <relationship> <to>. So

    ` + buildinfo.App + ` issue link add ENG-1 blocks ENG-2

reads "ENG-1 blocks ENG-2", and reversing the relationship reverses the link.

The relationship is a phrase, not a type name, and that is deliberate. "Blocks"
names a relationship without saying which way it runs, so it is refused with
both readings rather than resolved to one. Guessing would be wrong half the
time, and the issue that ends up blocked is the one nobody was watching.

An unknown phrase is refused with every phrase the site offers. Link wording is
customizable, so showing what exists beats guessing at a near match.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue link add ENG-1 blocks ENG-2",
			buildinfo.App + ` issue link add ENG-1 "is blocked by" ENG-2`,
			buildinfo.App + " issue link add ENG-1 relates to ENG-2 --dry-run",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "from", Usage: "the issue the sentence starts with", Required: true},
			{
				Name: "relationship", Required: true,
				Usage: `the phrase, e.g. blocks or "is blocked by"`,
			},
			{Name: "to", Usage: "the issue at the other end", Required: true},
		},
		Flags:        []registry.Flag{dryRunFlag()},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindLinkAdd, Version: VersionLinkAdd},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateLinkAdd,
		Run:       runLinkAdd,
	}
}

func validateLinkAdd(_ context.Context, inv *registry.Invocation) error {
	if len(inv.Args) < 3 {
		return errs.Usage("INVALID_LINK",
			"a link needs two issues and a relationship between them").
			WithRemedy("e.g. `issue link add ENG-1 blocks ENG-2`")
	}
	for _, i := range []int{0, 2} {
		if _, ok := ParseKey(inv.Args[i]); !ok {
			return errs.Usage("INVALID_KEY", "%q is not an issue key", inv.Args[i]).
				WithDetail("an issue key looks like ENG-123")
		}
	}
	if inv.Args[0] == inv.Args[2] {
		return errs.Usage("SELF_LINK", "an issue cannot be linked to itself").
			WithDetail("both ends are %s", inv.Args[0])
	}
	// The relationship is resolved in the command body, because resolving it
	// needs the site's link types and a dry run should not pay for that twice.
	return nil
}

// LinkAddRequest builds the link without sending it.
//
// Jira writes a link as "inwardIssue <inward> outwardIssue", so the direction
// the phrase resolved to decides which of the caller's two issues goes where.
// Swapping them creates the opposite relationship, which is why this takes a
// Direction rather than inferring one.
func (c *Client) LinkAddRequest(
	from, to string, linkType LinkType, dir Direction,
) (transport.Request, error) {
	fromKey, ok := ParseKey(from)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", from)
	}
	toKey, ok := ParseKey(to)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", to)
	}

	inward, outward := fromKey.String(), toKey.String()
	if dir == Outward {
		// "from blocks to": from is the outward issue.
		inward, outward = toKey.String(), fromKey.String()
	}

	body, err := json.Marshal(map[string]any{
		"type":         map[string]any{"name": linkType.Name},
		"inwardIssue":  map[string]any{"key": inward},
		"outwardIssue": map[string]any{"key": outward},
	})
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the link").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   c.Site.APIBase() + "/issueLink",
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runLinkAdd(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue link add")
	if err != nil {
		return nil, err
	}

	types, err := client.FetchLinkTypes(ctx)
	if err != nil {
		return nil, err
	}
	// Resolved before anything is sent, so a phrase this site does not have
	// costs one read and no write.
	linkType, dir, err := ResolveLinkType(types, inv.Args[1])
	if err != nil {
		return nil, err
	}

	req, err := client.LinkAddRequest(inv.Args[0], inv.Args[2], linkType, dir)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.link.add", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	// The sentence is echoed back as the caller wrote it, so they can see the
	// direction that was applied rather than infer it.
	return render.Record(KindLinkAdd, VersionLinkAdd, render.El("link").
		Attr("type", linkType.Name).
		Attr("action", "linked").
		Leaf("from", inv.Args[0]).
		Leaf("relationship", inv.Args[1]).
		Leaf("to", inv.Args[2])), nil
}

func linkRemoveCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "link", "remove"},
		Summary: "Remove a link between two issues",
		Description: strings.TrimSpace(`
Removes one link by its id, which ` + "`" + buildinfo.App + ` issue link list` + "`" + ` reports.

The id rather than the pair of issues, because two issues can be linked more
than once with different relationships, and removing "the link between A and B"
would be ambiguous exactly when it mattered.`),
		Example: buildinfo.App + " issue link remove 10042 --yes",
		Args: []registry.Arg{{
			Name: "id", Usage: "link id, from `issue link list`", Required: true,
		}},
		Flags: []registry.Flag{
			{Name: "yes", Type: registry.TypeBool, Usage: "confirm the removal"},
			dryRunFlag(),
		},
		Mutating:     true,
		Destructive:  true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindLinkRemove, Version: VersionLinkRemove},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if len(inv.Args) == 0 {
				return errs.Usage("INVALID_LINK_ID", "a link id is required")
			}
			return validNumericID("link", inv.Args[0])
		},
		Run: runLinkRemove,
	}
}

func runLinkRemove(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue link remove")
	if err != nil {
		return nil, err
	}

	req := transport.Request{
		Method: transport.MethodDelete,
		Path:   client.Site.APIBase() + "/issueLink/" + url.PathEscape(inv.Args[0]),
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.link.remove", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindLinkRemove, VersionLinkRemove, render.El("link").
		Attr("id", inv.Args[0]).
		Attr("action", "removed")), nil
}

func worklogAddCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "worklog", "add"},
		Summary: "Log work against an issue",
		Description: strings.TrimSpace(`
Records time spent on an issue.

The time is Jira's own format — "3h", "1d 4h", "2w 3d" — and is checked before
it is sent, because a mistyped duration is the kind that logs 3 minutes where 3
hours was meant. It is never converted: a working week is a site setting, and
this tool does not decide how long anyone's day is.

--started says when the work happened, which is not when it is being logged.
It defaults to now, and takes an RFC 3339 timestamp otherwise.

--comment is plain text, exactly as on issue comment add.`),
		Example: strings.Join([]string{
			buildinfo.App + " issue worklog add ENG-101 3h",
			buildinfo.App + ` issue worklog add ENG-101 "1d 4h" --comment "pairing on the retry path"`,
			buildinfo.App + " issue worklog add ENG-101 30m --started 2026-08-05T09:00:00Z",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{Name: "time", Usage: `time spent, e.g. "3h" or "1d 4h"`, Required: true},
		},
		Flags: []registry.Flag{
			{
				Name: "started", Type: registry.TypeString,
				Usage: "when the work happened, RFC 3339; defaults to now",
			},
			{
				Name: "comment", Type: registry.TypeString,
				Usage: "a note about the work, as plain text",
			},
			dryRunFlag(),
		},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindWorklogAdd, Version: VersionWorklogAdd},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateWorklogAdd,
		Run:       runWorklogAdd,
	}
}

func validateWorklogAdd(_ context.Context, inv *registry.Invocation) error {
	if err := requireIssueKey(inv); err != nil {
		return err
	}
	if len(inv.Args) < 2 {
		return errs.Usage("INVALID_DURATION", "a time is required").
			WithDetail(`Jira's format, e.g. "3h", "1d 4h", "2w"`)
	}
	if err := ValidateDuration(inv.Args[1]); err != nil {
		return err
	}
	if started := inv.Flags.String("started"); started != "" {
		if _, err := time.Parse(time.RFC3339, started); err != nil {
			return errs.Usage("INVALID_DATE",
				"%q is not an RFC 3339 timestamp", started).
				WithDetail("e.g. 2026-08-05T09:00:00Z").
				WithRemedy("--started is when the work happened, not how long it took")
		}
	}
	return nil
}

// jiraTimeLayout is the one format Jira accepts for a worklog's start.
//
// It is not RFC 3339: the offset has no colon and the fraction is exactly three
// digits. Sending RFC 3339 is a 400 that does not say which field was wrong,
// which is why the conversion happens here rather than being left to the caller.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// WorklogAddRequest builds the entry without sending it.
func (c *Client) WorklogAddRequest(
	key, spent, started, comment string, now time.Time,
) (transport.Request, error) {
	parsed, ok := ParseKey(key)
	if !ok {
		return transport.Request{}, errs.Usage("INVALID_KEY", "%q is not an issue key", key)
	}
	if err := ValidateDuration(spent); err != nil {
		return transport.Request{}, err
	}

	at := now
	if started != "" {
		t, err := time.Parse(time.RFC3339, started)
		if err != nil {
			return transport.Request{}, errs.Usage("INVALID_DATE",
				"%q is not an RFC 3339 timestamp", started)
		}
		at = t
	}

	payload := map[string]any{
		"timeSpent": strings.TrimSpace(spent),
		"started":   at.Format(jiraTimeLayout),
	}
	if comment != "" {
		value, err := bodyValue(c.Site.Kind, comment)
		if err != nil {
			return transport.Request{}, err
		}
		payload["comment"] = value
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the worklog").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   c.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) + "/worklog",
		Header: jsonHeader(),
		Body:   body,
	}, nil
}

func runWorklogAdd(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue worklog add")
	if err != nil {
		return nil, err
	}

	req, err := client.WorklogAddRequest(inv.Args[0], inv.Args[1],
		inv.Flags.String("started"), inv.Flags.String("comment"), time.Now())
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.worklog.add", req), nil
	}

	resp, err := client.Transport.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	var raw rawWorklog
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.ID == "" {
		return nil, errs.Remote("MALFORMED_WORKLOG_RESPONSE",
			"%s did not report the worklog it created", req.Path).
			WithRequestID(resp.RequestID).
			WithDetail("the entry may exist; list them before retrying").
			Wrap(err)
	}
	logged, err := raw.convert()
	if err != nil {
		return nil, err
	}

	return render.Record(KindWorklogAdd, VersionWorklogAdd,
		logged.Node().Attr("issue", inv.Args[0])), nil
}

func worklogDeleteCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"issue", "worklog", "delete"},
		Summary: "Delete a worklog entry",
		Description: strings.TrimSpace(`
Deletes one worklog entry permanently, by the id ` + "`" + buildinfo.App +
			` issue worklog list` + "`" + ` reports.

Jira has no undo for this, which is why --yes is required.`),
		Example: buildinfo.App + " issue worklog delete ENG-101 10042 --yes",
		Args: []registry.Arg{
			{Name: "key", Usage: "issue key, e.g. ENG-101", Required: true},
			{Name: "id", Usage: "worklog id, from `issue worklog list`", Required: true},
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
			{Kind: KindWorklogDelete, Version: VersionWorklogDelete},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if err := requireIssueKey(inv); err != nil {
				return err
			}
			if len(inv.Args) < 2 {
				return errs.Usage("INVALID_WORKLOG_ID", "a worklog id is required")
			}
			return validNumericID("worklog", inv.Args[1])
		},
		Run: runWorklogDelete,
	}
}

func runWorklogDelete(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := writeClientFor(ctx, inv, "issue worklog delete")
	if err != nil {
		return nil, err
	}
	parsed, _ := ParseKey(inv.Args[0])

	req := transport.Request{
		Method: transport.MethodDelete,
		Path: client.Site.APIBase() + "/issue/" + url.PathEscape(parsed.String()) +
			"/worklog/" + url.PathEscape(inv.Args[1]),
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("issue.worklog.delete", req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindWorklogDelete, VersionWorklogDelete, render.El("worklog").
		Attr("id", inv.Args[1]).
		Attr("issue", inv.Args[0]).
		Attr("action", "deleted")), nil
}
