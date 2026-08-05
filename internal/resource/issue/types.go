// Package issue is the issue resource.
//
// It knows nothing about any other resource, and nothing outside cmd, tui,
// mcp, workflow, and internal/commands may import it — which is what keeps it
// independently compilable and what makes compile-out work.
package issue

import (
	"encoding/json"
	"regexp"
	"strconv"
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
	KindGet     = "issue.get"
	VersionGet  = 1
)

// Body formats a description can arrive in.
//
// The attribute is always present and always accurate. A caller that wants to
// render the text has to know which it got, and guessing — or converting
// silently — is how markup ends up mangled with nothing to indicate it.
const (
	// BodyWiki is Data Center's wiki markup, carried through unchanged.
	BodyWiki = "wiki"
	// BodyADF is Cloud's Atlassian Document Format, carried through as JSON
	// until a converter exists. Emitting it raw is honest; emitting a
	// half-conversion would not be.
	BodyADF = "adf"
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

	// The fields below are populated by `issue get` and left empty by
	// `issue list`, which does not request them. The node shape is the same
	// either way, so a caller parses a listed issue and a fetched one
	// identically.
	Description string
	// BodyFormat names the markup Description is in: wiki or adf.
	BodyFormat  string
	Resolution  string
	Parent      string
	Components  []string
	FixVersions []string
	// Extra holds fields the caller asked for with --field that this package
	// has no native shape for. They are carried through rather than dropped:
	// a field that was fetched and then discarded is a flag that did nothing.
	Extra []ExtraField
}

// ExtraField is one field requested by id and reduced to a scalar.
type ExtraField struct {
	ID    string
	Value string
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
		Created     string          `json:"created"`
		Updated     string          `json:"updated"`
		Labels      []string        `json:"labels"`
		Description json.RawMessage `json:"description"`
		Resolution  *struct {
			Name string `json:"name"`
		} `json:"resolution"`
		Parent *struct {
			Key string `json:"key"`
		} `json:"parent"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		FixVersions []struct {
			Name string `json:"name"`
		} `json:"fixVersions"`
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
	if r.Fields.Resolution != nil {
		out.Resolution = r.Fields.Resolution.Name
	}
	if r.Fields.Parent != nil {
		out.Parent = r.Fields.Parent.Key
	}
	for _, c := range r.Fields.Components {
		out.Components = append(out.Components, c.Name)
	}
	for _, v := range r.Fields.FixVersions {
		out.FixVersions = append(out.FixVersions, v.Name)
	}
	out.Description, out.BodyFormat = decodeDescription(r.Fields.Description)
	return out, nil
}

// decodeDescription reads a description, which is a different type on each
// deployment.
//
// Data Center sends a string of wiki markup. Cloud sends an ADF document, which
// is an object. Both are carried through unchanged with the format named, so a
// caller knows what it has. Converting ADF to markdown here would mean shipping
// a half-converter and calling its output markdown.
func decodeDescription(raw json.RawMessage) (text, format string) {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "" || trimmed == "null":
		return "", ""
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return trimmed, BodyWiki
		}
		return s, BodyWiki
	default:
		return trimmed, BodyADF
	}
}

// decodeIssues converts a page of raw issues. extras names the fields the
// caller asked for beyond the default set.
func decodeIssues(raw []json.RawMessage, extras []string) ([]Issue, error) {
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
		issue.Extra = extraFields(data, extras)
		out = append(out, issue)
	}
	return out, nil
}

// extraFields pulls the requested non-default fields out of the raw payload.
//
// It decodes the issue a second time rather than capturing the field object
// alongside the typed one: two struct fields sharing a JSON tag makes
// encoding/json ignore both, which silently empties every typed field.
//
// A field the server did not return is reported as present-and-empty rather
// than omitted, so a caller can tell "this issue has no value for it" from "I
// asked for something that does not exist" — the latter shows up as empty on
// every row.
func extraFields(data json.RawMessage, names []string) []ExtraField {
	if len(names) == 0 {
		return nil
	}
	var envelope struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	fields := envelope.Fields

	out := make([]ExtraField, 0, len(names))
	for _, name := range names {
		out = append(out, ExtraField{ID: name, Value: scalarize(fields[name])})
	}
	return out
}

// scalarize reduces a field value to one cell.
//
// Jira custom fields arrive in several shapes: a bare string or number, a
// {"value": …} for a select, a {"displayName": …} for a user, or an array of
// those for a multi-select. Anything that does not reduce is emitted as compact
// JSON rather than dropped — an unreadable value in the output is still better
// than a silently missing one.
func scalarize(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return scalarizeValue(v, raw)
}

func scalarizeValue(v any, raw json.RawMessage) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, scalarizeValue(item, nil))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		// The keys Jira uses to name the human-readable side of a structured
		// value, most specific first.
		for _, key := range []string{"value", "name", "displayName", "key"} {
			if inner, ok := t[key].(string); ok && inner != "" {
				return inner
			}
		}
	}
	if len(raw) > 0 {
		return strings.Join(strings.Fields(string(raw)), " ")
	}
	compact, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(compact)
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
	n.AttrIf("resolution", i.Resolution)
	n.AttrIf("parent", i.Parent)
	n.LeafIf("created", i.Created)
	n.LeafIf("updated", i.Updated)

	// Long text is a child element with its markup named, and mixed content
	// goes in a CDATA section so newlines and fenced code survive untouched.
	if i.Description != "" {
		n.Child(render.El("description").
			Attr("format", i.BodyFormat).
			SetCDATA(i.Description))
	}

	labels := make([]*render.Node, 0, len(i.Labels))
	for _, l := range i.Labels {
		labels = append(labels, render.El("label").SetText(l))
	}
	n.Child(render.ListEl("labels", "label", labels...))

	if len(i.Components) > 0 {
		n.Child(render.ListEl("components", "component", textNodes("component", i.Components)...))
	}
	if len(i.FixVersions) > 0 {
		n.Child(render.ListEl("fix-versions", "fix-version",
			textNodes("fix-version", i.FixVersions)...))
	}

	// A requested field becomes an element named for its id, so it addresses
	// like any other: `--format json` gives "customfield_10042": "3", and a
	// TSV column path is just the id.
	for _, f := range i.Extra {
		n.Leaf(f.ID, f.Value)
	}
	return n
}

// textNodes builds a list of text-only elements.
func textNodes(name string, values []string) []*render.Node {
	out := make([]*render.Node, 0, len(values))
	for _, v := range values {
		out = append(out, render.El(name).SetText(v))
	}
	return out
}

// DetailFields is what `issue get` asks Jira for: everything `issue list`
// needs, plus the fields only worth fetching one issue at a time.
func DetailFields() []string {
	return append(DefaultFields(),
		"description", "resolution", "parent", "components", "fixVersions")
}

// nativeFields are the fields this package models itself. A caller naming one
// of these with --field changes nothing, because it is already in the output.
var nativeFields = map[string]bool{
	"summary": true, "status": true, "assignee": true, "reporter": true,
	"priority": true, "issuetype": true, "project": true,
	"created": true, "updated": true, "labels": true,
	"description": true, "resolution": true, "parent": true,
	"components": true, "fixversions": true, "fixVersions": true,
}

// reservedNames are the element and attribute names an issue already uses. A
// requested field cannot take one of them without colliding.
var reservedNames = map[string]bool{
	"key": true, "id": true, "summary": true, "status": true, "assignee": true,
	"reporter": true, "type": true, "priority": true, "project": true,
	"created": true, "updated": true, "labels": true, "description": true,
	"resolution": true, "parent": true, "components": true,
}

// fieldID is what --field accepts: a field id, e.g. customfield_10042 or
// duedate.
var fieldID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// ValidateFieldNames rejects a --field value this tool cannot render.
//
// Field *names* — "Story Points" — are not accepted yet. Resolving one to its
// id needs the field catalogue, and guessing would either send Jira something
// it rejects opaquely or produce an element name that is not valid XML. Saying
// so is better than either.
func ValidateFieldNames(requested []string) error {
	for _, name := range requested {
		switch {
		case !fieldID.MatchString(name):
			return errs.Usage("INVALID_FIELD", "%q is not a field id", name).
				WithDetail("a field id looks like customfield_10042 or duedate").
				WithRemedy("field names are not resolved yet; pass the id, " +
					"which the Jira admin field screen shows")
		case reservedNames[name] && !nativeFields[name]:
			return errs.Usage("INVALID_FIELD",
				"%q collides with a field this tool already reports", name)
		}
	}
	return nil
}

// ExtraFieldNames returns the requested fields that are not already reported
// natively, deduplicated and in the order given.
func ExtraFieldNames(requested []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(requested))
	for _, name := range requested {
		if nativeFields[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ExtraColumns returns the TSV columns for requested fields, appended after the
// defaults.
//
// Without these, --field would change the XML and leave TSV — the default
// format — looking exactly as it did before, which is the same as doing
// nothing.
func ExtraColumns(requested []string) []render.Column {
	extras := ExtraFieldNames(requested)
	out := make([]render.Column, 0, len(extras))
	for _, name := range extras {
		out = append(out, render.Column{Header: name, Path: name})
	}
	return out
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
