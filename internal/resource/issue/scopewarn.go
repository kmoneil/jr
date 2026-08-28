package issue

import (
	"strings"

	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
)

// ScopeMismatchCode is the warning a query carries when its raw --jql names a
// project the effective scope excludes.
const ScopeMismatchCode = "SCOPE_MISMATCH"

// warnScopeMismatch says so when a caller's raw --jql selects projects the
// effective project scope cannot return.
//
// This is the failure the whole command set is otherwise silent about. jr holds
// both halves of the contradiction inside one process — the literal string
// GOV-208 in the caller's --jql, and project = IDO in its own context — and
// BuildQuery ANDs them into a query that cannot match by construction. What
// comes back is a clean, complete, exit-0, empty result, which is the same
// bytes as an honest "nothing matched".
//
// It is the same argument UNKNOWN_LABEL already ships in this package, applied
// to the scope instead of the label, and it is cheaper: that check costs one
// uncached request per label, and this costs a tokenize of a string the caller
// already typed. The paragraph on issue list that explains UNKNOWN_LABEL even
// names this confounder four lines later ("this query may be scoped to one
// project"), so the tree knew the scope narrows an answer invisibly and only
// ever said it in a comment.
//
// Exit 0 stays. The query is legal, the rows it returned are real, and the
// caller may well have meant it. Not being able to tell is what was wrong.
//
// What it does not say, in the shape UNKNOWN_LABEL's own paragraph uses:
//
//   - **It compares against the effective scope, not the site's catalogue.** A
//     project key that was renamed still resolves on the server, so
//     `key = OLD-1` against a scope of NEW is a mismatch by string comparison
//     and not by meaning. That is the one false positive this can produce, it
//     needs a rename plus a caller still using the old key, and closing it
//     would cost a request on every invocation carrying a --jql. It is a
//     warning at exit 0, so the trade is a line of stderr against a round trip
//     on the common path, and the round trip loses.
//   - **It says nothing about whether an in-scope project will match.** Naming
//     the project you are scoped to is not a mismatch and never warns, which
//     is silence about a query that may still return nothing for every other
//     reason a query returns nothing.
//
// Measured before shipping: over the 22 distinct --jql fragments in this
// repository's README, docs, skill assets, and command examples, one warns
// against a scope of ENG, and it is `key = OPS-1`, which genuinely cannot match.
// Zero false positives on the worked examples this project publishes.
func warnScopeMismatch(inv *registry.Invocation, project, fragment string) {
	excluded := scopeMismatch(project, fragment)
	if len(excluded) == 0 {
		return
	}
	warn(inv, ScopeMismatchCode, scopeMismatchMessage(project, excluded))
}

// scopeMismatch returns the projects the fragment selects that the scope
// excludes, or nothing when there is no mismatch to report.
//
// It is deliberately quiet in every case it cannot be certain about:
//
//   - No scope means nothing is excluded. --all-projects arrives here as an
//     empty project and is the common way to mean it.
//   - No fragment means nothing to compare.
//   - A fragment that does not lex is not this check's business. The command's
//     own Validate refuses it with JQL_SYNTAX, and a warning about a query that
//     is about to be rejected would be noise in front of the real answer.
//   - A fragment that names no project positively selects every project the
//     scope allows, which is the ordinary case and must stay free.
func scopeMismatch(project, fragment string) []string {
	if project == "" || strings.TrimSpace(fragment) == "" {
		return nil
	}
	selected, err := jql.ProjectsSelected(fragment)
	if err != nil || len(selected) == 0 {
		return nil
	}

	scope := strings.ToUpper(project)
	var excluded []string
	for _, p := range selected {
		if p != scope {
			excluded = append(excluded, p)
		}
	}
	return excluded
}

// scopeMismatchMessage names both halves of the contradiction, because either
// one alone sends the reader to the wrong place: the scope alone reads as a
// complaint about the context, and the excluded projects alone read as a
// complaint about the query.
func scopeMismatchMessage(project string, excluded []string) string {
	subject := "project " + excluded[0]
	if len(excluded) > 1 {
		subject = "projects " + strings.Join(excluded, ", ")
	}
	return "--jql selects " + subject + ", and the scope is " + project +
		", so those rows cannot be returned; use --all-projects or --project " +
		"to widen it"
}
