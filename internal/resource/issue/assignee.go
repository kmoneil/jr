package issue

import (
	"context"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/site"
)

// resolvedAssigneeKey is where validation leaves the id it resolved.
const resolvedAssigneeKey = "issue.assignee"

// assigneeSentinels name no user.
//
// They are the words each verb already gives its own meaning — clear it, hand
// it to the project default — and resolving one would search the directory for
// somebody called "unassigned". A command that has no meaning for one of these
// passes it through as it always did.
var assigneeSentinels = map[string]bool{
	"unassigned": true, "empty": true,
	"default": true, "automatic": true,
}

// IsAssigneeSentinel reports a word that names no user.
func IsAssigneeSentinel(value string) bool {
	return assigneeSentinels[strings.ToLower(strings.TrimSpace(value))]
}

// isCurrentUser reports the word that means whoever holds the credential.
//
// `issue list` has always honoured it, because JQL has a function for it. The
// write verbs did not, so `issue assign ENG-1 currentUser` sent the literal
// word as an accountId and got a 400 naming nothing — a word that means one
// thing on one command and nothing on the next is worse than a word that does
// not exist.
func isCurrentUser(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return v == "currentuser" || v == "currentuser()"
}

// validateAssigneeFilter resolves the assignee `issue list` filters on.
//
// It differs from the write verbs in one word: JQL has a currentUser function,
// so the query says "whoever is running this" rather than naming an account,
// and asking the server who that is would be a request for an answer the query
// already carries.
func validateAssigneeFilter(ctx context.Context, inv *registry.Invocation, input string) error {
	if isCurrentUser(input) {
		return nil
	}
	return validateAssignee(ctx, inv, input)
}

// validateAssignee resolves a caller's assignee to the id this deployment
// wants, and leaves it on the invocation for the command body.
//
// It happens in Validate rather than in the body for the usual reason: `issue
// list` streams, so its header goes out before its body runs, and a refusal
// from inside the body would arrive after bytes were on stdout.
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
		return err
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
