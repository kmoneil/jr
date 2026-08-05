// Package issue is the issue resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
package issue

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/render"
)

// Kinds and schema versions this resource emits. Bump a version in the same
// commit that changes the corresponding golden file.
const (
	KindList    = "issue.list"
	VersionList = 1
)

// Issue is one issue, in the shape this tool reports rather than the shape
// Jira returns.
type Issue struct {
	Key      string
	ID       string
	Summary  string
	Status   Status
	Assignee User
	Reporter User
	Priority string
	Type     string
	Project  string
	Created  string
	Updated  string
	Labels   []string
}

// Status is an issue's workflow state, plus the category the state belongs to.
//
// The category matters more than the name for anything automated: a project can
// rename "In Progress" to anything, but the category stays one of three values.
type Status struct {
	Name     string
	Category string
}

// User is whoever an issue is assigned to or reported by.
//
// ID is an accountId on Cloud and a username on Data Center. The two are not
// interchangeable, which is why the field is named for what it is rather than
// for either one.
type User struct {
	ID      string
	Display string
}

// Status categories, normalized. Jira's own keys are "new", "indeterminate",
// and "done", which are not words anyone would guess.
const (
	CategoryToDo       = "to-do"
	CategoryInProgress = "in-progress"
	CategoryDone       = "done"
	CategoryUnknown    = "unknown"
)

// rawIssue is the JSON Jira returns. It is separate from Issue so that a change
// in Jira's shape is one struct to adjust rather than a change to the output
// contract.
type rawIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"statusCategory"`
		} `json:"status"`
		Assignee *rawUser `json:"assignee"`
		Reporter *rawUser `json:"reporter"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Project *struct {
			Key string `json:"key"`
		} `json:"project"`
		Created string   `json:"created"`
		Updated string   `json:"updated"`
		Labels  []string `json:"labels"`
	} `json:"fields"`
}

// rawUser covers both deployments: Cloud identifies a user by accountId, Data
// Center by name.
type rawUser struct {
	AccountID   string `json:"accountId"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
}

func (u *rawUser) convert() User {
	if u == nil {
		return User{}
	}
	id := u.AccountID
	if id == "" {
		id = u.Name
	}
	if id == "" {
		id = u.Key
	}
	return User{ID: id, Display: u.DisplayName}
}

// jiraTimeLayouts are the timestamp formats Jira serves. The first is what both
// deployments actually send; the rest are there because a proxy or a plugin
// occasionally normalizes them.
var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05.999-0700",
	"2006-01-02T15:04:05-0700",
	time.RFC3339Nano,
	time.RFC3339,
}

// normalizeTime converts a Jira timestamp to RFC 3339 in UTC.
//
// A timestamp that cannot be parsed is an error rather than a value passed
// through. Emitting whatever arrived would mean the output format depends on
// the server, and a consumer that parsed the documented shape would break on a
// row rather than on a request — which is much harder to notice.
func normalizeTime(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, layout := range jiraTimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errs.Remote("MALFORMED_TIMESTAMP",
		"Jira returned a %s timestamp this tool cannot parse", field).
		WithDetail("%q", value).
		WithRemedy("report this: the timestamp format changed")
}

// normalizeCategory maps Jira's status category key onto a stable name.
func normalizeCategory(key, name string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "new", "undefined":
		return CategoryToDo
	case "indeterminate":
		return CategoryInProgress
	case "done":
		return CategoryDone
	}
	// Some Data Center versions omit the key and send only a name.
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "to do", "new":
		return CategoryToDo
	case "in progress":
		return CategoryInProgress
	case "done", "complete":
		return CategoryDone
	}
	return CategoryUnknown
}

func (r rawIssue) convert() (Issue, error) {
	created, err := normalizeTime("created", r.Fields.Created)
	if err != nil {
		return Issue{}, err
	}
	updated, err := normalizeTime("updated", r.Fields.Updated)
	if err != nil {
		return Issue{}, err
	}

	out := Issue{
		Key:     r.Key,
		ID:      r.ID,
		Summary: r.Fields.Summary,
		Status: Status{
			Name: r.Fields.Status.Name,
			Category: normalizeCategory(
				r.Fields.Status.StatusCategory.Key,
				r.Fields.Status.StatusCategory.Name,
			),
		},
		Assignee: r.Fields.Assignee.convert(),
		Reporter: r.Fields.Reporter.convert(),
		Created:  created,
		Updated:  updated,
		Labels:   r.Fields.Labels,
	}
	if r.Fields.Priority != nil {
		out.Priority = r.Fields.Priority.Name
	}
	if r.Fields.IssueType != nil {
		out.Type = r.Fields.IssueType.Name
	}
	if r.Fields.Project != nil {
		out.Project = r.Fields.Project.Key
	}
	return out, nil
}

// decodeIssues converts a page of raw issues.
func decodeIssues(raw []json.RawMessage) ([]Issue, error) {
	out := make([]Issue, 0, len(raw))
	for i, data := range raw {
		var r rawIssue
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, errs.Remote("MALFORMED_ISSUE",
				"Jira returned an issue this tool cannot read").
				WithDetail("issue %d of the page", i+1).
				Wrap(err)
		}
		issue, err := r.convert()
		if err != nil {
			return nil, err
		}
		out = append(out, issue)
	}
	return out, nil
}

// Node renders one issue.
//
// Long text is a child element and short scalars are attributes, so a
// description containing newlines never has to be escaped into an attribute.
func (i Issue) Node() *render.Node {
	n := render.El("issue").
		Attr("key", i.Key).
		Attr("id", i.ID).
		Leaf("summary", i.Summary).
		Child(render.El("status").
			Attr("category", i.Status.Category).
			SetText(i.Status.Name))

	assignee := render.El("assignee").
		Attr("id", i.Assignee.ID).
		Attr("display", i.Assignee.Display)
	n.Child(assignee)

	n.AttrIf("type", i.Type)
	n.AttrIf("priority", i.Priority)
	n.AttrIf("project", i.Project)
	n.LeafIf("created", i.Created)
	n.LeafIf("updated", i.Updated)

	labels := make([]*render.Node, 0, len(i.Labels))
	for _, l := range i.Labels {
		labels = append(labels, render.El("label").SetText(l))
	}
	n.Child(render.ListEl("labels", "label", labels...))
	return n
}

// ListColumns is the default TSV column set for `issue list`.
//
// Adding a column here is a major version bump: agents diff output, and a new
// column shifts every field after it.
func ListColumns() []render.Column {
	return []render.Column{
		{Header: "key", Path: "@key"},
		{Header: "status", Path: "status"},
		{Header: "assignee", Path: "assignee@display"},
		{Header: "updated", Path: "updated"},
		{Header: "summary", Path: "summary"},
	}
}
