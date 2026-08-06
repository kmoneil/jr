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
	KindSprintAdd     = "sprint.add"
	VersionSprintAdd  = 1
	KindEpicAdd       = "epic.add"
	VersionEpicAdd    = 1
	KindEpicRemove    = "epic.remove"
	VersionEpicRemove = 1
)

func init() {
	registry.Register(sprintAddCommand())
	registry.Register(epicAddCommand())
	registry.Register(epicRemoveCommand())
}

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
		client.site.AgileBase()+"/sprint/"+inv.Args[0]+"/issue", keys,
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

	req, err := moveRequest(
		client.site.AgileBase()+"/epic/"+inv.Args[0]+"/issue", keys,
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
		movedNode("epic", inv.Args[0], "added", keys)), nil
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
		movedNode("epic", noEpic, "removed", keys)), nil
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

// validEpicRef accepts what the agile API addresses an epic by: a key or an id.
func validEpicRef(ref string) error {
	if _, ok := issue.ParseKey(ref); ok {
		return nil
	}
	if _, err := strconv.Atoi(ref); err == nil {
		return nil
	}
	return errs.Usage("INVALID_EPIC", "%q is not an epic key or id", ref).
		WithDetail("an epic is addressed as ENG-42 or as 10101").
		WithRemedy("take it from `%s epic list`", buildinfo.App)
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
