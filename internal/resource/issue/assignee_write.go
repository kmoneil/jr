//go:build write

// Resolving an assignee for a *write* lives here, behind the tag, because
// nothing in a reader build reaches it. `issue list` resolves its user filters
// through validateUserFilters in assignee.go instead, and the two differ in
// more than the tag: a filter can say currentUser and let JQL answer it, while
// a write has to name an account, so this one asks /myself and that one does
// not.
//
// It moved here when list stopped calling it. A symbol reachable only under a
// tag belongs in a file that declares the tag, which is what `make
// lint-untagged` checks and what the build profiles are sold on.

package issue

import (
	"context"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
)

// resolvedAssigneeKey is where validation leaves the id it resolved.
const resolvedAssigneeKey = "issue.assignee"

// validateAssignee resolves a caller's assignee to the id this deployment
// wants, and leaves it on the invocation for the command body.
//
// It happens in Validate rather than in the body so that a name Jira does not
// know is refused before anything is sent.
func validateAssignee(ctx context.Context, inv *registry.Invocation, input string) error {
	if input == "" || IsAssigneeSentinel(input) {
		return nil
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION",
			"an assignee cannot be resolved without a connection to Jira")
	}

	if isCurrentUser(input) {
		conn, info, err := inv.Jira.Connect(ctx)
		if err != nil {
			return err
		}
		account, err := site.Whoami(ctx, conn, info)
		if err != nil {
			return err
		}
		inv.SetValue(resolvedAssigneeKey, account.ID)
		return nil
	}

	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return err
	}
	user, err := meta.ResolveUser(ctx, input)
	if err != nil {
		return suggestCurrentUser(err, input)
	}
	inv.SetValue(resolvedAssigneeKey, user.ID)
	return nil
}

// resolvedAssignee returns the id validation resolved, or what the caller
// typed where there was nothing to resolve.
func resolvedAssignee(inv *registry.Invocation, input string) string {
	if id, ok := inv.Value(resolvedAssigneeKey).(string); ok && id != "" {
		return id
	}
	return input
}
