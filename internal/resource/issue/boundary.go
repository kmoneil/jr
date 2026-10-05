package issue

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/kmoneil/jr/internal/jql"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// sortFieldKey is where validation leaves the field id a --sort resolved to.
const sortFieldKey = "issue.sortfield"

// resolveSortField finds the field a --sort orders by, so the walk can fetch
// it and a cut list's warning can say how far down the order it reached.
//
// --sort is a JQL clause name, and a clause name is not a field id: `due`,
// `type`, `cf[10042]` and `'Story Points'` each order by a field whose id is
// something else. The key and the default set need no request, which covers
// the sorts people reach for; anything else is looked up in the field
// catalogue, the same cached request --field makes.
//
// Nothing here refuses. Jira decides whether a sort is valid, and a sort this
// cannot resolve is one whose boundary is unknown, which the warning says by
// writing none.
func resolveSortField(ctx context.Context, inv *registry.Invocation) {
	inv.SetValue(sortFieldKey, sortFieldID(ctx, inv))
}

func sortFieldID(ctx context.Context, inv *registry.Invocation) string {
	name := strings.TrimSpace(inv.Flags.String("sort"))
	if name == "" {
		return ""
	}
	if strings.EqualFold(name, "key") || strings.EqualFold(name, SortKey) {
		return SortKey
	}
	for _, id := range DefaultFields() {
		if strings.EqualFold(name, id) {
			return id
		}
	}
	if inv.Jira == nil {
		return ""
	}
	meta, err := inv.Jira.Metadata(ctx)
	if err != nil {
		return ""
	}
	catalogue, err := meta.Fields(ctx)
	if err != nil {
		return ""
	}
	field, err := catalogue.Resolve(name)
	if err != nil {
		return ""
	}
	return field.ID
}

// sortField returns the id validation resolved, empty when there is none.
func sortField(inv *registry.Invocation) string {
	id, _ := inv.Value(sortFieldKey).(string)
	return id
}

// listBoundary is the warning's boundary for a walk ordered by field that
// last wrote a row carrying reached, or the zero value when either is unknown.
func listBoundary(inv *registry.Invocation, field, reached string) render.Boundary {
	if field == "" || reached == "" {
		return render.Boundary{}
	}
	dir, err := jql.SortDirection(inv.Flags.String("sort"), inv.Flags.String("order"))
	if err != nil {
		return render.Boundary{}
	}
	return render.Boundary{
		Field: field, Order: strings.ToLower(string(dir)), Reached: reached,
	}
}

// boundaryValue is the value a row carries for the field id a walk is ordered
// by, or empty when it carries no single one.
//
// It is the row's own rendering, so the warning's `reached` compares with the
// rows already written: `updated` normalized to the second in UTC, `project`
// as its key, a person as the display name the default column shows, and any
// other field as the text its element holds under --field.
//
// A list-valued field has no single value to report. Labels, components,
// versions, a multi-select and a sprint field are ordered by Jira in ways a
// joined list does not show, so they report nothing rather than a string that
// reads like a bound and is not one. Description is long text, which JQL does
// not order by.
func boundaryValue(issue Issue, data json.RawMessage, id string) string {
	switch id {
	case SortKey:
		return issue.Key
	case "summary":
		return issue.Summary
	case "status":
		return issue.Status.Name
	case "assignee":
		return issue.Assignee.Display
	case "reporter":
		return issue.Reporter.Display
	case "priority":
		return issue.Priority
	case "issuetype":
		return issue.Type
	case "project":
		return issue.Project
	case "created":
		return issue.Created
	case "updated":
		return issue.Updated
	case "resolution":
		return issue.Resolution
	case "parent":
		return issue.Parent
	}
	if nativeFields[id] {
		return ""
	}
	var envelope struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ""
	}
	raw := bytes.TrimSpace(envelope.Fields[id])
	if len(raw) > 0 && raw[0] == '[' {
		return ""
	}
	return scalarize(raw)
}
