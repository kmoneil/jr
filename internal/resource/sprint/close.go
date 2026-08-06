//go:build write && admin

// Closing a sprint is board administration, not ordinary work: it ends an
// iteration for everyone on the board and moves whatever did not get finished.
// So it needs both tags — write, because it changes Jira, and admin, because of
// what it changes. An agent build has write and not admin, and therefore cannot
// close a sprint even though it can edit issues.

package sprint

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kind and schema version the close verb emits.
const (
	KindClose    = "sprint.close"
	VersionClose = 1
)

func init() {
	registry.Register(closeCommand())

	render.RegisterSchema(KindClose, CloseSchema())
}

// CloseSchema is the shape of a sprint that was ended. The name comes from the
// read that precedes the write, so the acknowledgement says which sprint ended
// rather than echoing back the id the caller already had.
func CloseSchema() *render.Schema {
	return &render.Schema{
		Element: "sprint",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeInt},
			{Name: "state", Type: render.TypeString, Enum: []string{StateClosed}},
			{Name: "action", Type: render.TypeString, Enum: []string{"closed"}},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
		},
	}
}

func closeCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"sprint", "close"},
		Summary: "Close an active sprint",
		Description: strings.TrimSpace(`
Ends a running sprint.

This is not only a state change. Every issue in the sprint that is not complete
leaves it and returns to the backlog, and Jira offers no undo — reopening the
sprint is not something the API can do. That is why --yes is required and why
this needs the admin tag as well as write: an agent build can edit an issue and
cannot end an iteration.

The sprint is read first, so a sprint that is not active is refused before
anything is sent. A future sprint has not started and a closed one is already
finished; both are a precondition failure rather than a request worth making.

--dry-run prints the exact request, body included, and sends nothing. The read
still happens, so a dry run tells you whether the close would be allowed.`),
		Example: strings.Join([]string{
			buildinfo.App + " sprint close 128 --yes",
			buildinfo.App + " sprint close 128 --dry-run",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "id", Usage: "sprint id, from `jr sprint list`", Required: true,
		}},
		Flags: []registry.Flag{
			{Name: "yes", Type: registry.TypeBool, Usage: "confirm ending the sprint"},
			{
				Name: "dry-run", Type: registry.TypeBool,
				Usage: "print the request that would be sent, and send nothing",
			},
		},
		Mutating:     true,
		Destructive:  true,
		NeedsJira:    true,
		RequiresTags: []string{"write", "admin"},
		Outputs: []registry.Output{
			{Kind: KindClose, Version: VersionClose},
			registry.DryRunOutput(),
		},
		ExitCodes: []exitcode.Code{
			exitcode.Blocked, exitcode.Auth, exitcode.NotFound, exitcode.Permission,
			exitcode.Conflict, exitcode.RateLimit, exitcode.Remote,
		},
		Validate: func(_ context.Context, inv *registry.Invocation) error {
			if len(inv.Args) == 0 {
				return errs.Usage("INVALID_SPRINT_ID", "a sprint id is required")
			}
			return ValidateID(inv.Args[0])
		},
		Run: runClose,
	}
}

// CloseRequest builds the close without sending it.
//
// The agile API spells a partial update as a POST to the sprint itself, with
// only the fields that change. A PUT would be a full replacement, and every
// field left out of it — the name, the goal, the dates — would be cleared.
func (c *Client) CloseRequest(id string) (transport.Request, error) {
	if err := ValidateID(id); err != nil {
		return transport.Request{}, err
	}
	body, err := json.Marshal(map[string]any{"state": StateClosed})
	if err != nil {
		return transport.Request{}, errs.Runtime("ENCODE_FAILED",
			"cannot encode the close request").Wrap(err)
	}
	return transport.Request{
		Method: transport.MethodPost,
		// Escaped even though ValidateID has already restricted it to digits.
		// Every other path in this package is built the same way, and a guard
		// that is only present where somebody judged it necessary is one that
		// gets left out the next time somebody judges.
		Path:   c.Site.AgileBase() + "/sprint/" + url.PathEscape(id),
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   body,
	}, nil
}

func runClose(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	client, err := clientFor(ctx, inv, "sprint close")
	if err != nil {
		return nil, err
	}

	// Read before write, so a sprint that cannot be closed costs one read and
	// no mutation. It also means the acknowledgement can name the sprint rather
	// than echoing back the id the caller already had.
	current, err := client.Get(ctx, inv.Args[0])
	if err != nil {
		return nil, err
	}
	if current.State != StateActive {
		return nil, errs.New(exitcode.Conflict, "SPRINT_NOT_ACTIVE",
			"sprint %s is %s, and only an active sprint can be closed",
			current.ID, current.State).
			WithDetail("%q", current.Name).
			WithRemedy("%s", remedyFor(current.State))
	}

	req, err := client.CloseRequest(current.ID)
	if err != nil {
		return nil, err
	}
	if inv.Flags.Bool("dry-run") {
		return registry.DryRunDoc("sprint.close", req), nil
	}

	resp, err := client.Transport.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := transport.Err(resp); err != nil {
		return nil, err
	}

	return render.Record(KindClose, VersionClose, render.El("sprint").
		Attr("id", current.ID).
		Attr("state", StateClosed).
		Attr("action", "closed").
		Leaf("name", current.Name)), nil
}

// remedyFor says what to do about a sprint in the wrong state, which differs:
// one has not happened yet and the other already has.
func remedyFor(state string) string {
	if state == StateFuture {
		return "a sprint has to be started before it can be closed"
	}
	return "it is already closed; nothing was changed"
}
