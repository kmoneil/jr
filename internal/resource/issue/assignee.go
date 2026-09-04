package issue

import (
	"context"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/nearest"
	"github.com/kmoneil/jr/internal/registry"
)

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

// resolvedUsersKey is where `issue list` leaves the ids it resolved for its
// user-valued filters, keyed by flag name.
//
// Separate from resolvedAssigneeKey, which the write verbs use for the one
// assignee they are about to set. A filter and a value being written are
// different things that happen to share a spelling on one flag.
const resolvedUsersKey = "issue.users"

// userFilterFlags are the `issue list` filters whose value names a person.
//
// Every one resolves the same way --assignee always has. A tool that took a
// display name on one flag and required an opaque account id on the next would
// have two flags with one spelling, and the failure is silent: `watcher = "Ada
// Lovelace"` against Cloud matches nothing and comes back as a complete, empty,
// successful result — indistinguishable from "you are watching nothing".
var userFilterFlags = []string{
	"assignee", "reporter", "creator", "involving", "watcher", "voter",
	"worklog-author", "was-assignee", "changed-by",
}

// sentinelFilterFlags are the filters for which the words in assigneeSentinels
// mean something.
//
// Only --assignee. `unassigned` is a real state of the assignee field and is
// not a state of the others in any useful sense: `creator IS EMPTY` matches
// nothing, and a predicate has no empty form at all — `CHANGED BY EMPTY` is not
// JQL. Accepting the word everywhere would turn a nonsense filter into an empty
// result set and exit 0.
var sentinelFilterFlags = map[string]bool{"assignee": true}

// validateUserFilters resolves every user-valued filter on `issue list`.
//
// It happens in Validate for the usual reason — this command streams, so a
// refusal from the body would arrive after the header was on stdout — and it
// resolves all of them before returning, so a caller who mistyped two names is
// told about the first rather than about neither.
func validateUserFilters(ctx context.Context, inv *registry.Invocation) error {
	resolved := make(map[string]string, len(userFilterFlags))
	for _, name := range userFilterFlags {
		id, err := resolveUserFilter(ctx, inv, name)
		if err != nil {
			return err
		}
		if id != "" {
			resolved[name] = id
		}
	}
	inv.SetValue(resolvedUsersKey, resolved)
	return nil
}

// resolveUserFilter turns one flag's value into the id this deployment wants,
// or the empty string when there is nothing to resolve.
func resolveUserFilter(
	ctx context.Context, inv *registry.Invocation, name string,
) (string, error) {
	value := strings.TrimSpace(inv.Flags.String(name))
	switch {
	case value == "":
		return "", nil
	case isCurrentUser(value):
		// JQL has a function for it, so the query says "whoever is running
		// this" rather than naming an account. Asking the server who that is
		// would be a request for an answer the query already carries.
		return "", nil
	case IsAssigneeSentinel(value):
		if sentinelFilterFlags[name] {
			return "", nil
		}
		return "", errs.Usage("INVALID_USER",
			"--%s names a person, and %q names nobody", name, value).
			WithRemedy("use a display name, an email, an id, " +
				"or the word currentUser")
	}

	if inv.Jira == nil {
		return "", errs.Runtime("NO_SESSION",
			"--%s cannot be resolved without a connection to Jira", name)
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return "", err
	}
	user, err := meta.ResolveUser(ctx, value)
	if err != nil {
		return "", suggestCurrentUser(err, value)
	}
	return user.ID, nil
}

// resolvedUser returns the id validation resolved for a filter, or what the
// caller typed where there was nothing to resolve.
func resolvedUser(inv *registry.Invocation, name string) string {
	if ids, ok := inv.Value(resolvedUsersKey).(map[string]string); ok {
		if id := ids[name]; id != "" {
			return id
		}
	}
	return inv.Flags.String(name)
}

// currentUserAliases are the words somebody reaches for when they mean
// themselves and `currentUser` is not what they typed.
//
// They are not typos of the sentinel, which is why they are listed rather than
// measured: `me` is two characters and shares none of them with `currentUser`,
// so no edit distance will ever connect the two. What connects them is meaning,
// and meaning has to be written down.
var currentUserAliases = map[string]bool{
	"me": true, "@me": true, "self": true, "myself": true, "mine": true,
}

// meansTheCaller reports whether a value that did not resolve was probably an
// attempt to name the authenticated account.
//
// Two ways in, because there are two ways to miss. An alias above is a semantic
// miss. Anything close to the sentinel by edit distance is a spelling miss,
// which catches `current-user`, `current_user` and `currentusr` without listing
// them, and goes through internal/nearest because that package exists to stop
// this tool having four ideas of "close".
func meansTheCaller(input string) bool {
	v := strings.ToLower(strings.TrimSpace(input))
	if v == "" {
		return false
	}
	if currentUserAliases[v] {
		return true
	}
	return nearest.Distance(v, "currentuser") <= nearest.Threshold(v)
}

// suggestCurrentUser leads an UNKNOWN_USER remedy with the sentinel when the
// caller was probably naming themselves.
//
// The refusal itself is right and stays: no user on the site is called `me`.
// What was wrong is that the remedy pointed away from the fix. On a site with
// people whose display names contain "me" it offered four of them and said
// "pass one of these exactly", which reads as though one of those humans was
// meant; on a site with none it offered `user list`, which is a search for a
// person who does not exist. Neither branch named the word that works, and the
// word appears two functions away in this file, in the refusal `--reporter
// unassigned` already gets.
//
// It fires only on a value that was already going to be refused, so the happy
// path costs nothing.
//
// The hint leads and the original remedy follows rather than being replaced,
// because the guess can be wrong: somebody may genuinely have a colleague whose
// display name did not match, and taking the name list away from them to make
// room for a suggestion would trade one misdirection for another.
func suggestCurrentUser(err error, input string) error {
	if err == nil || !meansTheCaller(input) {
		return err
	}
	e := errs.Coerce(err)
	if e.Code != "UNKNOWN_USER" {
		return err
	}
	if e.Remedy == "" {
		return e.WithRemedy("did you mean currentUser? it resolves to the " +
			"account this credential belongs to")
	}
	return e.WithRemedy("did you mean currentUser? it resolves to the "+
		"account this credential belongs to. otherwise, %s", e.Remedy)
}
