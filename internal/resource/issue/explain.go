package issue

import (
	"strings"

	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// explainList answers --explain for issue list.
//
// It composes through listQuery and BuildQuery, the same funnel the command
// sends through, so the report cannot drift from the request. The user
// filters Validate would resolve against the server go out as typed and are
// named under unresolved instead: resolution is a request, and an explanation
// that made one would send the query it exists to show.
func explainList(inv *registry.Invocation) (*render.Doc, error) {
	return explainQuery(listQuery(inv), unresolvedUserFilters(inv))
}

// explainActivity answers --explain for issue activity, whose query carries
// --since exactly as typed.
func explainActivity(inv *registry.Invocation) (*render.Doc, error) {
	return explainQuery(QueryOptions{
		Project:      activityProject(inv),
		JQL:          inv.Flags.String("jql"),
		UpdatedAfter: inv.Flags.String(sinceFlag),
	}, nil)
}

// explainChanges answers --explain for issue changes. The bound it sends is a
// floor computed from --since in the account's timezone, or decoded from a
// cursor, and both need the server, so the query carries the raw value and
// names it unresolved.
func explainChanges(inv *registry.Invocation) (*render.Doc, error) {
	since := inv.Flags.String(sinceFlag)
	var unresolved []jql.UnresolvedFilter
	if since != "" {
		unresolved = append(unresolved,
			jql.UnresolvedFilter{Flag: sinceFlag, Value: since})
	}
	return explainQuery(QueryOptions{
		Project:      feedProject(inv),
		JQL:          inv.Flags.String("jql"),
		UpdatedAfter: since,
	}, unresolved)
}

// unresolvedUserFilters names the user-valued filters whose values are
// resolved to ids just before sending. currentUser and the assignee
// sentinels are complete as typed: JQL has a form for each, and nothing is
// substituted for them.
func unresolvedUserFilters(inv *registry.Invocation) []jql.UnresolvedFilter {
	var out []jql.UnresolvedFilter
	for _, name := range userFilterFlags {
		value := strings.TrimSpace(inv.Flags.String(name))
		if value == "" || isCurrentUser(value) {
			continue
		}
		if IsAssigneeSentinel(value) && sentinelFilterFlags[name] {
			continue
		}
		out = append(out, jql.UnresolvedFilter{Flag: name, Value: value})
	}
	return out
}

// explainQuery renders what BuildQuery would send for these options.
//
// The fragment is validated locally first, the way jql explain validates its
// own, because a fragment that does not lex has no query to show and the
// refusal has to name the position. Nothing here reaches the network.
func explainQuery(
	opt QueryOptions, unresolved []jql.UnresolvedFilter,
) (*render.Doc, error) {
	var fields []string
	if opt.JQL != "" {
		var err error
		if fields, err = jql.Fields(opt.JQL); err != nil {
			return nil, err
		}
		if err := jql.ValidateFragment(opt.JQL); err != nil {
			return nil, err
		}
	}
	query, err := BuildQuery(opt)
	if err != nil {
		return nil, err
	}
	e := jql.Explanation{
		Fragment: opt.JQL, Query: query, Project: opt.Project,
		Fields: fields, Parenthesized: true, Unresolved: unresolved,
	}
	return render.Record(jql.KindExplain, jql.VersionExplain, e.Node()), nil
}
