//go:build write

// Moving issues between containers is the operation this package was described
// for. `sprint add` and `epic add` each touch two resources — the issues being
// moved and the thing they move into — and resources never import each other,
// so neither could live in internal/resource/sprint or internal/resource/epic.
//
// What they actually need from the issue resource is issue.ParseKey. A local
// copy would be a fourth reimplementation of the one function this project has
// an invariant about, and the first one nobody would think to keep in step.

package workflow

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kinds and schema versions these verbs emit.
const (
	KindSprintAdd    = "sprint.add"
	VersionSprintAdd = 1
	// v2 added a status per issue and the applied/requested counts. On Cloud an
	// epic move is one request per issue, so "all of them moved" stopped being
	// the only outcome and the document had to be able to say so.
	KindEpicAdd       = "epic.add"
	VersionEpicAdd    = 2
	KindEpicRemove    = "epic.remove"
	VersionEpicRemove = 2
)

func init() {
	registry.Register(sprintAddCommand())
	registry.Register(epicAddCommand())
	registry.Register(epicRemoveCommand())

	render.RegisterSchema(KindSprintAdd, MovedSchema("sprint", "added"))
	render.RegisterSchema(KindEpicAdd, AppliedSchema("epic", "added"))
	render.RegisterSchema(KindEpicRemove, AppliedSchema("epic", "removed"))
}

// MovedSchema is the shape of a move Jira applies in one request: the
// container, what happened, and every issue it happened to.
//
// Jira answers with 204 and no body, so this reports what was asked for. The
// issues are listed rather than counted because a caller has to be able to
// check that the set it named is the set that moved.
func MovedSchema(container, action string) *render.Schema {
	return &render.Schema{
		Element: container,
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString},
			{Name: "action", Type: render.TypeString, Enum: []string{action}},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("issues", "issue", &render.Schema{
				Element: "issue",
				Attrs:   []render.Field{{Name: "key", Type: render.TypeString}},
			})},
		},
	}
}

// AppliedSchema is MovedSchema for a move that may take more than one request.
//
// On Cloud an epic move is a PUT of the parent field per issue, because the
// batched agile endpoint serves company-managed projects only and the parent
// field is the spelling that works on both styles. So a run can stop in the
// middle, and a document that could only say "these moved" would have to either
// lie about the ones that did not or omit them.
//
// requested and applied are the two numbers a caller compares. They are counts
// rather than a complete="false", which means a truncated *result set* and
// carries exit 3 with it — a write has no result set to truncate, and one code
// meaning two things is the defect this avoids.
func AppliedSchema(container, action string) *render.Schema {
	return &render.Schema{
		Element: container,
		Attrs: []render.Field{
			// For epic remove this is the literal "none": leaving an epic is
			// spelled as joining the absence of one, and there is no DELETE.
			{Name: "id", Type: render.TypeString},
			{Name: "action", Type: render.TypeString, Enum: []string{action}},
			{Name: "requested", Type: render.TypeInt},
			{Name: "applied", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("issues", "issue", &render.Schema{
				Element: "issue",
				Attrs: []render.Field{
					{Name: "key", Type: render.TypeString},
					{
						Name: "status", Type: render.TypeString,
						Enum: []string{StatusMoved, StatusFailed, StatusNotAttempted},
					},
				},
			})},
		},
	}
}

// What one issue's move came to. A run stops at the first failure, so what
// follows it was never sent — reported as such rather than as failed, because
// "Jira refused this" and "nobody asked Jira" are different facts and the
// caller's retry depends on which.
const (
	StatusMoved        = "moved"
	StatusFailed       = "failed"
	StatusNotAttempted = "not-attempted"
)

// MaxIssuesPerRequest is the agile API's cap on one move.
//
// More than this is refused rather than split across requests. Two requests can
// half-succeed, and the result would be neither "moved" nor "not moved" — a
// state this tool has no honest way to report. Splitting is the caller's
// decision to make, because they are the ones who know what a partial move
// means for what they are doing.
const MaxIssuesPerRequest = 50

// noEpic is the epic the agile API moves an issue to in order to remove it from
// the one it is in. There is no DELETE: leaving an epic is spelled as joining
// the absence of one.
const noEpic = "none"

// writeExits are the statuses these verbs can end with. Blocked is what
// read-only mode produces; Conflict is a precondition that failed, such as a
// sprint that is already closed.
func writeExits() []exitcode.Code {
	return []exitcode.Code{
		exitcode.Blocked, exitcode.Auth, exitcode.NotFound, exitcode.Permission,
		exitcode.Conflict, exitcode.RateLimit, exitcode.Remote,
	}
}

// dryRunFlag is declared rather than added implicitly, because the contract test
// reads the declaration — and a flag that exists only at runtime is one
// `jr schema` cannot describe.
func dryRunFlag() registry.Flag {
	return registry.Flag{
		Name: "dry-run", Type: registry.TypeBool,
		Usage: "print the request that would be sent, and send nothing",
	}
}

func sprintAddCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"sprint", "add"},
		Summary: "Move issues into a sprint",
		Description: strings.TrimSpace(`
Moves one or more issues into a sprint.

An issue belongs to at most one sprint, so this is a move and not an addition:
an issue already in another sprint leaves it. Nothing is removed from the issue
otherwise — its status, its epic, and its assignee are untouched.

At most ` + strconv.Itoa(MaxIssuesPerRequest) + ` issues at a time, which is the API's own cap. More than that is
refused rather than split across requests, because two requests can half-succeed
and the result would be neither moved nor not moved.

--dry-run prints the exact request, body included, and sends nothing.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint add 128 ENG-1 ENG-2",
			buildinfo.App + " sprint add 128 ENG-1 --dry-run",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "sprint", Usage: "sprint id, from `jr sprint list`", Required: true},
			{Name: "issue", Usage: "issue key, e.g. ENG-101", Required: true, Variadic: true},
		},
		Flags:        []registry.Flag{dryRunFlag()},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindSprintAdd, Version: VersionSprintAdd},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateSprintAdd,
		Run:       runSprintAdd,
	}
}

func validateSprintAdd(_ context.Context, inv *registry.Invocation) error {
	if len(inv.Args) == 0 {
		return errs.Usage("INVALID_SPRINT_ID", "a sprint id is required")
	}
	if err := validNumericID("sprint", inv.Args[0]); err != nil {
		return err
	}
	_, err := parseIssueKeys(inv.Args[1:])
	return err
}

func runSprintAdd(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "sprint add")
	if err != nil {
		return nil, err
	}
	keys, err := parseIssueKeys(inv.Args[1:])
	if err != nil {
		return nil, err
	}

	req, err := moveRequest(
		client.site.AgileBase()+"/sprint/"+url.PathEscape(inv.Args[0])+"/issue", keys,
	)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc(KindSprintAdd, req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindSprintAdd, VersionSprintAdd,
		movedNode("sprint", inv.Args[0], "added", keys)), nil
}

func epicAddCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"epic", "add"},
		Summary: "Move issues into an epic",
		Description: strings.TrimSpace(`
Moves one or more issues under an epic.

An issue belongs to at most one epic, so this is a move and not an addition: an
issue already under another epic leaves it. Use ` + "`" + buildinfo.App + ` epic remove` + "`" + ` to take
an issue out of an epic without putting it in another.

The epic is a key or an id, exactly as on ` + "`" + buildinfo.App + ` epic get` + "`" + `.

At most ` + strconv.Itoa(MaxIssuesPerRequest) + ` issues at a time, which is the API's own cap. More than that is
refused rather than split across requests, because two requests can half-succeed
and the result would be neither moved nor not moved.

--dry-run prints the exact request, body included, and sends nothing.`),
		Example: strings.Join([]string{
			buildinfo.App + " epic add ENG-42 ENG-101 ENG-102",
			buildinfo.App + " epic add 10101 ENG-101 --dry-run",
		}, "\n"),
		Args: []registry.Arg{
			{Name: "epic", Usage: "epic key or id, e.g. ENG-42", Required: true},
			{Name: "issue", Usage: "issue key, e.g. ENG-101", Required: true, Variadic: true},
		},
		Flags:        []registry.Flag{dryRunFlag()},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindEpicAdd, Version: VersionEpicAdd},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate:  validateEpicAdd,
		Run:       runEpicAdd,
	}
}

func validateEpicAdd(_ context.Context, inv *registry.Invocation) error {
	if len(inv.Args) == 0 {
		return errs.Usage("INVALID_EPIC", "an epic key or id is required")
	}
	if err := validEpicRef(inv.Args[0]); err != nil {
		return err
	}
	keys, err := parseIssueKeys(inv.Args[1:])
	if err != nil {
		return err
	}
	// An epic cannot contain itself, and the API answers the attempt with a 400
	// that does not say which of the issues was the problem.
	for _, key := range keys {
		if strings.EqualFold(key, inv.Args[0]) {
			return errs.Usage("SELF_EPIC", "an epic cannot be moved into itself").
				WithDetail("both ends are %s", key)
		}
	}
	return nil
}

func runEpicAdd(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "epic add")
	if err != nil {
		return nil, err
	}
	keys, err := parseIssueKeys(inv.Args[1:])
	if err != nil {
		return nil, err
	}
	ref, err := epicRef(inv.Args[0])
	if err != nil {
		return nil, err
	}

	if client.site.Kind == site.Cloud {
		return client.applyParent(ctx, inv, parentMove{
			kind: KindEpicAdd, version: VersionEpicAdd,
			id: ref, action: "added", parent: ref, keys: keys,
		})
	}

	req, err := moveRequest(
		client.site.AgileBase()+"/epic/"+url.PathEscape(ref)+"/issue", keys,
	)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc(KindEpicAdd, req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindEpicAdd, VersionEpicAdd,
		appliedNode("epic", inv.Args[0], "added", allMoved(keys))), nil
}

func epicRemoveCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"epic", "remove"},
		Summary: "Take issues out of their epic",
		Description: strings.TrimSpace(`
Removes one or more issues from whichever epic they are in.

The epic is not named, because it does not need to be: an issue belongs to at
most one, and this takes it out of that one. An issue that is in no epic is
unaffected.

Nothing is deleted. The issues stay exactly as they were apart from no longer
having a parent epic.

At most ` + strconv.Itoa(MaxIssuesPerRequest) + ` issues at a time, which is the API's own cap.

--dry-run prints the exact request, body included, and sends nothing.`),
		Example: strings.Join([]string{
			buildinfo.App + " epic remove ENG-101 ENG-102",
			buildinfo.App + " epic remove ENG-101 --dry-run",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "issue", Usage: "issue key, e.g. ENG-101", Required: true, Variadic: true,
		}},
		Flags:        []registry.Flag{dryRunFlag()},
		Mutating:     true,
		NeedsJira:    true,
		RequiresTags: []string{"write"},
		Outputs: []registry.Output{
			{Kind: KindEpicRemove, Version: VersionEpicRemove},
			registry.DryRunOutput(),
		},
		ExitCodes: writeExits(),
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			_, err := parseIssueKeys(inv.Args)
			return err
		},
		Run: runEpicRemove,
	}
}

func runEpicRemove(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "epic remove")
	if err != nil {
		return nil, err
	}
	keys, err := parseIssueKeys(inv.Args)
	if err != nil {
		return nil, err
	}

	if client.site.Kind == site.Cloud {
		return client.applyParent(ctx, inv, parentMove{
			kind: KindEpicRemove, version: VersionEpicRemove,
			id: noEpic, action: "removed", parent: issue.ParentSentinel, keys: keys,
		})
	}

	req, err := moveRequest(client.site.AgileBase()+"/epic/"+noEpic+"/issue", keys)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc(KindEpicRemove, req), nil
	}
	if err := client.send(ctx, req); err != nil {
		return nil, err
	}

	return render.Record(KindEpicRemove, VersionEpicRemove,
		appliedNode("epic", noEpic, "removed", allMoved(keys))), nil
}

// parentMove is one `epic add` or `epic remove` on Cloud, where the move is a
// PUT of the parent field per issue.
type parentMove struct {
	kind    string
	version int
	// id and action are what the result reports; parent is what goes in the
	// body, which for a removal is the clearing word rather than a key.
	id     string
	action string
	parent string
	keys   []string
}

// applyParent moves each issue by setting or clearing its parent.
//
// This is the Cloud path for both epic verbs, and it is one request per issue
// because the parent field offers no batched spelling. The batched agile
// endpoint does, and Cloud refuses it for team-managed projects — the default
// for every project created on a Cloud site — so the choice is between a path
// that works for some callers and a path that works for all of them.
//
// It stops at the first failure rather than pressing on. Continuing would turn
// one refusal into a list of them, and a caller who cannot do the first move is
// overwhelmingly unlikely to want the rest attempted; what they need is to know
// exactly where it got to, which the document says.
func (c *client) applyParent(
	ctx context.Context, inv *registry.Invocation, move parentMove,
) (*render.Doc, error) {
	client := &issue.Client{Transport: c.conn, Site: c.site}

	requests := make([]transport.Request, 0, len(move.keys))
	for _, key := range move.keys {
		req, err := client.EditRequest(issue.EditOptions{
			Key: key, Parent: move.parent, SetParent: true,
		})
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	// Every request is built before any is sent, so a body this tool would have
	// refused to build costs no half-applied move.
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc(move.kind, requests...), nil
	}

	statuses := make([]issueStatus, 0, len(move.keys))
	var failure error
	for i, req := range requests {
		if failure != nil {
			statuses = append(statuses, issueStatus{move.keys[i], StatusNotAttempted})
			continue
		}
		if err := c.send(ctx, req); err != nil {
			failure = err
			statuses = append(statuses, issueStatus{move.keys[i], StatusFailed})
			continue
		}
		statuses = append(statuses, issueStatus{move.keys[i], StatusMoved})
	}

	doc := render.Record(move.kind, move.version,
		appliedNode("epic", move.id, move.action, statuses))
	if failure != nil {
		return nil, &registry.PartiallyApplied{Doc: doc, Cause: failure}
	}
	return doc, nil
}

// issueStatus is one issue and what its move came to.
type issueStatus struct {
	key    string
	status string
}

// allMoved is the whole set, applied. It is what a single-request move reports:
// Jira answered 204 for the batch, so either all of them moved or none did and
// the command already failed.
func allMoved(keys []string) []issueStatus {
	out := make([]issueStatus, 0, len(keys))
	for _, key := range keys {
		out = append(out, issueStatus{key, StatusMoved})
	}
	return out
}

// parseIssueKeys checks every key and returns them canonicalized.
//
// It is called from Validate and again from the command body, so a bad key is
// refused before anything is sent and the body never has to trust its argument.
// Running it twice costs nothing and means neither path can be the one that
// forgot.
func parseIssueKeys(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errs.Usage("NO_ISSUES", "at least one issue key is required").
			WithRemedy("e.g. `%s epic remove ENG-101`", buildinfo.App)
	}
	if len(args) > MaxIssuesPerRequest {
		return nil, errs.Usage("TOO_MANY_ISSUES",
			"the agile API moves at most %d issues at a time, and %d were given",
			MaxIssuesPerRequest, len(args)).
			WithRemedy("split the run; this tool will not, because a half-applied " +
				"move cannot be reported as either done or not done")
	}

	out := make([]string, 0, len(args))
	for _, arg := range args {
		key, ok := issue.ParseKey(arg)
		if !ok {
			return nil, errs.Usage("INVALID_KEY", "%q is not an issue key", arg).
				WithDetail("an issue key looks like ENG-123").
				WithRemedy("pass a key, not an id or a summary")
		}
		out = append(out, key.String())
	}
	return out, nil
}

// epicRef canonicalizes what the agile API addresses an epic by: a key or an
// id. The returned value is what goes in the path, so it is the parser's output
// and never the caller's argument.
func epicRef(ref string) (string, error) {
	if key, ok := issue.ParseKey(ref); ok {
		return key.String(), nil
	}
	// Digits, not strconv.Atoi, which accepts a leading sign — "-42" is not an
	// id, and "/rest/agile/1.0/epic/-42/issue" is not an endpoint.
	if err := validNumericID("epic", ref); err == nil {
		return ref, nil
	}
	return "", errs.Usage("INVALID_EPIC", "%q is not an epic key or id", ref).
		WithDetail("an epic is addressed as ENG-42 or as 10101").
		WithRemedy("take it from `%s epic list`", buildinfo.App)
}

// validEpicRef is the validation half of epicRef, for Command.Validate.
func validEpicRef(ref string) error {
	_, err := epicRef(ref)
	return err
}

// validNumericID rejects an id this tool cannot address, so a typo costs no
// round trip and a caller's argument never reaches a URL path as anything but
// digits.
func validNumericID(what, id string) error {
	code := "INVALID_" + strings.ToUpper(what) + "_ID"
	if id == "" {
		return errs.Usage(code, "a %s id is required", what)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return errs.Usage(code, "%q is not a %s id", id, what).
				WithDetail("a %s id is digits, e.g. 42", what).
				WithRemedy("take it from `%s %s list`", buildinfo.App, what)
		}
	}
	return nil
}

// moveRequest builds the request without sending it, so --dry-run prints the
// very bytes that would have gone out. A dry run that rendered a description of
// the request instead would be a second implementation of it, and the two would
// drift.
func moveRequest(path string, keys []string) (transport.Request, error) {
	body, err := json.Marshal(map[string]any{"issues": keys})
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the move").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		Path:   path,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   body,
	}, nil
}

// movedNode is the acknowledgement these verbs share.
//
// Jira answers all three with 204 and no body, so this reports what was asked
// for rather than re-reading the issues. Fetching them again would be a second
// request whose answer could differ for reasons unrelated to this command.
func movedNode(container, id, action string, keys []string) *render.Node {
	items := make([]*render.Node, 0, len(keys))
	for _, key := range keys {
		items = append(items, render.El("issue").Attr("key", key))
	}
	return render.El(container).
		Attr("id", id).
		Attr("action", action).
		Child(render.ListEl("issues", "issue", items...))
}

// appliedNode is movedNode for a move that may take more than one request, and
// so may stop in the middle. The counts are what a caller compares; the
// per-issue status is what tells them where to resume.
func appliedNode(container, id, action string, statuses []issueStatus) *render.Node {
	items := make([]*render.Node, 0, len(statuses))
	applied := 0
	for _, s := range statuses {
		if s.status == StatusMoved {
			applied++
		}
		items = append(items, render.El("issue").
			Attr("key", s.key).
			Attr("status", s.status))
	}
	return render.El(container).
		Attr("id", id).
		Attr("action", action).
		Attr("requested", strconv.Itoa(len(statuses))).
		Attr("applied", strconv.Itoa(applied)).
		Child(render.ListEl("issues", "issue", items...))
}

// client is a connection plus the deployment it was probed as.
type client struct {
	conn *transport.Client
	site site.Info
}

// send performs a request whose success carries no body worth parsing. Jira
// answers all three of these with 204.
func (c *client) send(ctx context.Context, req transport.Request) error {
	resp, err := c.conn.Do(ctx, req)
	if err != nil {
		return err
	}
	return transport.Err(resp)
}

// clientFor is the opening every command here shares.
func clientFor(
	ctx context.Context, inv *registry.Invocation, command string,
) (*client, error) {
	if inv.Jira == nil {
		return nil, errs.Runtime("NO_SESSION", "%s has no connection to Jira", command)
	}
	conn, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &client{conn: conn, site: info}, nil
}
