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

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// Kinds and schema versions this resource emits. Bump a version in the same
// commit that changes the corresponding golden file.
const (
	KindList    = "issue.list"
	VersionList = 5
	KindGet     = "issue.get"
	VersionGet  = 7
)

// Body formats a description can arrive in.
//
// The attribute is always present and always accurate. A caller that wants to
// render the text has to know which it got, and guessing — or converting
// silently — is how markup ends up mangled with nothing to indicate it.
const (
	// BodyWiki is Data Center's wiki markup, carried through unchanged.
	BodyWiki = "wiki"
	// BodyADF is Cloud's Atlassian Document Format, exactly as Jira sent it.
	// Only --raw-body produces this.
	BodyADF = "adf"
	// BodyMarkdown is a Cloud document converted by internal/adf. The
	// conversion is lossless or it fails, so a body reported as markdown holds
	// everything the document did.
	BodyMarkdown = "markdown"
)

// BodyMode says how a Cloud body is reported.
//
// The zero value converts, because that is the useful answer and the one a
// caller who did not think about it should get. Asking for the document itself
// is the deliberate act.
type BodyMode int

// Body modes.
const (
	// ModeMarkdown converts an ADF document to markdown, or fails naming the
	// construct that stopped it.
	ModeMarkdown BodyMode = iota
	// ModeRaw emits the document exactly as Jira sent it.
	ModeRaw
	// ModeEcho converts, and reports the document itself rather than failing.
	//
	// Only a write's echo uses it, and only because the write already
	// happened. `issue comment add --body-format adf` with a document holding
	// a coloured span created the comment and then exited 2 rendering it, and
	// a caller told their comment failed adds it again. Reporting the exact
	// document is not an approximation — the format attribute says which one
	// came back — and it is the only answer that does not invent a second
	// comment.
	ModeEcho
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
	// BodyFormat names the markup Description is in: wiki, markdown, or adf.
	BodyFormat  string
	Resolution  string
	Parent      string
	Components  []string
	FixVersions []string
	// Extra holds fields the caller asked for with --field that this package
	// has no native shape for. They are carried through rather than dropped:
	// a field that was fetched and then discarded is a flag that did nothing.
	Extra []ExtraField

	// URL is where a person opens this issue, set by --url and empty
	// otherwise.
	//
	// It is not a field Jira sends. Jira's `self` is the REST endpoint, which
	// is the wrong link for a human: it returns JSON. This is built from the
	// deployment's own baseUrl, so it is one string per row that nobody asked
	// for unless they did.
	URL string
	// Age is how long ago the issue was last updated, in words, and is set
	// only when --age asked for it. Like URL it is not a field Jira sends: it
	// is derived here, from `updated` and the clock, and it never replaces the
	// timestamp it is derived from.
	Age string

	// Precondition is the opaque token a caller passes to `--if-unchanged`,
	// set by `issue get` and empty on a listed row. See precondition.go for
	// why a row does not carry one.
	Precondition string

	// updatedRaw is Jira's own `updated`, kept exactly as it arrived.
	//
	// Updated above is normalized to the second, which is what this tool
	// publishes and all a reader needs. A precondition needs the millisecond
	// the server actually recorded, so the raw value is carried here rather
	// than the published one being widened — widening it would move every
	// timestamp in every golden to answer a question only one command asks.
	// Unexported because it is an input to EncodePrecondition and not a field
	// of the output contract.
	updatedRaw string
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

func (r rawIssue) convert(mode BodyMode) (Issue, error) {
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
		Assignee:   r.Fields.Assignee.convert(),
		Reporter:   r.Fields.Reporter.convert(),
		Created:    created,
		Updated:    updated,
		updatedRaw: r.Fields.Updated,
		Labels:     r.Fields.Labels,
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
	out.Description, out.BodyFormat, err = decodeDescription(r.Fields.Description, mode)
	if err != nil {
		return Issue{}, err
	}
	return out, nil
}

// decodeDescription reads a description, which is a different type on each
// deployment.
//
// Data Center sends a string of wiki markup, which is carried through
// unchanged: it is markup this tool does not read and has nothing to convert
// it to. Cloud sends an Atlassian Document Format document, which becomes
// markdown unless the caller asked for the document itself.
//
// The format attribute names what the caller got either way, so nothing has to
// be guessed from the shape of the text.
func decodeDescription(raw json.RawMessage, mode BodyMode) (text, format string, err error) {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "" || trimmed == "null":
		return "", "", nil
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			// Unreachable through a decoded issue, because encoding/json
			// validates a RawMessage as part of its parent. It is here because
			// "cannot happen" is a claim, and returning the undecoded bytes as
			// if they were the markup would make a broken body look like a
			// body with quotes in it.
			return "", "", errs.Remote("MALFORMED_BODY",
				"Jira returned a body this tool cannot read").
				Wrap(err)
		}
		return s, BodyWiki, nil
	case mode == ModeRaw:
		return trimmed, BodyADF, nil
	}

	doc, err := adf.Parse(raw)
	if err != nil {
		if mode == ModeEcho {
			return trimmed, BodyADF, nil
		}
		return "", "", err
	}
	markdown, err := adf.ToMarkdown(doc)
	if err != nil {
		if mode == ModeEcho {
			return trimmed, BodyADF, nil
		}
		return "", "", err
	}
	// A document that held nothing but empty paragraphs converts to no text,
	// and no text is no description — the same answer an absent field gives.
	if markdown == "" {
		return "", "", nil
	}
	return markdown, BodyMarkdown, nil
}

// decodeIssues converts a page of raw issues. extras names the fields the
// caller asked for beyond the default set.
func decodeIssues(raw []json.RawMessage, extras []string, mode BodyMode) ([]Issue, error) {
	out := make([]Issue, 0, len(raw))
	for i, data := range raw {
		var r rawIssue
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, errs.Remote("MALFORMED_ISSUE",
				"Jira returned an issue this tool cannot read").
				WithDetail("issue %d of the page", i+1).
				Wrap(err)
		}
		issue, err := r.convert(mode)
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

	// The reporter, on the same terms as the assignee. It was asked for on
	// every request from the beginning — it is in DefaultFields — parsed into
	// this struct, and then rendered nowhere at all, so `--reporter ada` could
	// filter on a value no output could show. Found by asking why
	// `--field reporter` did nothing, which it also did.
	n.Child(render.El("reporter").
		Attr("id", i.Reporter.ID).
		Attr("display", i.Reporter.Display))

	n.AttrIf("type", i.Type)
	n.AttrIf("priority", i.Priority)
	n.AttrIf("project", i.Project)
	n.AttrIf("resolution", i.Resolution)
	n.AttrIf("parent", i.Parent)
	// Only `issue get` mints one, so this is absent on a listed row and the
	// shared Schema does not declare it — GetSchema does. AttrIf rather than a
	// branch on the command, because a row that ever did carry one would then
	// fail validation loudly instead of quietly widening issue.list's shape.
	n.AttrIf("precondition", i.Precondition)
	n.LeafIf("created", i.Created)
	n.LeafIf("updated", i.Updated)
	n.LeafIf("url", i.URL)
	n.LeafIf("age", i.Age)

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

// fieldID is what an id may look like once resolved. It is narrow because the
// id becomes an XML element name, and a field whose id is not a valid name
// cannot be rendered at all.
var fieldID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// ResolveFields turns what a caller typed after --field into field ids.
//
// Every value goes through the catalogue, including one that already looks like
// an id: a mistyped customfield_1004 is otherwise sent for Jira to reject with
// a 400 that names nothing, which is the exact row §5.2 exists to fix. The
// catalogue is cached for a day, so this costs one request on a cold cache and
// none after it.
//
// The returned ids are in the order given, so the columns a caller asked for
// appear in the order they asked for them.
func ResolveFields(catalogue *site.Catalogue, requested []string) ([]string, error) {
	out := make([]string, 0, len(requested))
	for _, name := range requested {
		field, err := catalogue.Resolve(name)
		if err != nil {
			return nil, err
		}
		if err := checkRenderable(name, field); err != nil {
			return nil, err
		}
		out = append(out, field.ID)
	}
	return out, nil
}

// subresourceTypes are the schema types whose value is a structure with no
// scalar form, mapped to the command that reads one properly. An empty string
// means there is no such command and the field simply has no column.
//
// `scalarize` reduces a structure by looking for `value`, `name`, `displayName`,
// or `key`, and falls back to the raw JSON when a map holds none of them. Every
// type here holds none of them, so every one of them used to print its whole
// serialized object into a cell: `--field comment` on five issues came to 196 KB
// with the gravatar URLs in it.
var subresourceTypes = map[string]string{
	"comments-page": "jr issue get <key> --with-comments",
	"timetracking":  "",
	"progress":      "",
	"votes":         "",
	"watches":       "",
}

// subresourceItems are the array element types with the same problem. An array
// scalarizes each element and joins them, so `labels` (items `string`),
// `components` (items `component`) and `fixVersions` (items `version`) all
// produce something a column can hold. These do not.
var subresourceItems = map[string]string{
	"attachment": "jr issue attachment list <key>",
	"worklog":    "jr issue worklog list <key>",
}

// subresourceIDs are the fields the catalogue cannot distinguish by schema.
//
// `issuelinks` and `subtasks` are both `array` of `issuelinks` and are not the
// same shape: a subtask element carries `key` and scalarizes to `ENG-6, ENG-7`,
// which is a useful column, and a link element carries a nested `type` and
// `outwardIssue` and scalarizes to JSON. Refusing by element type would take
// `--field subtasks` away from every caller who has it working today, so this
// one is named by id, and the reason is here rather than in a card.
var subresourceIDs = map[string]string{
	"issuelinks": "jr issue link list <key>",
}

// checkRenderable refuses a field this tool cannot turn into output.
//
// Two of the three cases are about the id rather than what the caller typed:
// the id is what becomes an element name, so it is the id that has to be a
// legal name and the id that can collide with one an issue already uses. The
// third is about the field's schema, and it is a deny list on purpose. A type
// nobody here has seen goes through `scalarize` like any other and prints
// badly at worst; an allow list missing a type refuses a field that works
// today, on every site whose custom types were not in front of whoever wrote
// the list. The cost of that choice is that an unfamiliar structure still
// reaches the JSON fallback, which is narrowed here and not closed. Closing it
// needs a renderer per subresource.
func checkRenderable(input string, f site.Field) error {
	switch {
	case !fieldID.MatchString(f.ID):
		return errs.Usage("INVALID_FIELD",
			"%q resolves to %q, which cannot be an element name", input, f.ID).
			WithDetail("an id must start with a letter and hold only letters, " +
				"digits, and underscores").
			WithRemedy("this field cannot be rendered; report it")
	case reservedNames[f.ID] && !nativeFields[f.ID]:
		return errs.Usage("INVALID_FIELD",
			"%q collides with a field this tool already reports", f.ID)
	}
	if verb, ok := subresource(f); ok {
		err := errs.Usage("UNRENDERABLE_FIELD",
			"%q is a %s, which has no value a column can hold", input, describe(f))
		if verb == "" {
			return err.WithRemedy("this field has no single value; drop it from --field")
		}
		return err.WithRemedy("read it with `%s` instead", verb)
	}
	return nil
}

// subresource reports whether a field is one of the structures above, and the
// command that reads it.
func subresource(f site.Field) (string, bool) {
	if verb, ok := subresourceIDs[f.ID]; ok {
		return verb, true
	}
	if verb, ok := subresourceTypes[f.Type]; ok {
		return verb, true
	}
	if f.Type == "array" {
		if verb, ok := subresourceItems[f.Items]; ok {
			return verb, true
		}
	}
	return "", false
}

// describe names a field's shape the way the catalogue does, so the refusal
// says what was wrong with the field rather than what was wrong with the flag.
func describe(f site.Field) string {
	if f.Type == "array" && f.Items != "" {
		return "list of " + f.Items + " objects"
	}
	if f.Type == "" {
		return "structure"
	}
	return f.Type + " structure"
}

// ExtraFieldNames returns the resolved ids that are not already reported
// natively, deduplicated and in the order given.
//
// Two spellings of the same field resolve to one id and collapse here, so
// `--field "Story Points" --field customfield_10042` produces one column rather
// than two identical ones.
func ExtraFieldNames(resolved []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(resolved))
	for _, id := range resolved {
		if nativeFields[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// nativeColumns is the TSV column a native field produces when --field names
// one, keyed by the id ResolveFields returns.
//
// It exists because "already in the output" was true of the XML and false of
// TSV, which is the default format. `--field created` resolved, was fetched,
// was rendered as an element, and added no column — so on the format almost
// everybody sees, the flag did nothing at all and said nothing about it. That
// is the third time a --field bug has had this shape, and the invariant written
// after the first two is that a flag either affects the output or does not
// exist.
//
// The four fields ListColumns already projects are deliberately absent: naming
// one is not a no-op, it asks for something already there, and adding a second
// `updated` column would be a stranger answer than doing nothing. Every other
// native field is here, and the paths are the node's own shape rather than the
// field's name — issuetype renders as the `type` attribute, and fixVersions as
// the `fix-versions` list.
var nativeColumns = map[string]render.Column{
	"reporter":    {Header: "reporter", Path: "reporter@display"},
	"priority":    {Header: "priority", Path: "@priority"},
	"issuetype":   {Header: "type", Path: "@type"},
	"project":     {Header: "project", Path: "@project"},
	"created":     {Header: "created", Path: "created"},
	"resolution":  {Header: "resolution", Path: "@resolution"},
	"parent":      {Header: "parent", Path: "@parent"},
	"description": {Header: "description", Path: "description"},
	"labels":      {Header: "labels", Path: "labels"},
	"components":  {Header: "components", Path: "components"},
	"fixversions": {Header: "fix-versions", Path: "fix-versions"},
	"fixVersions": {Header: "fix-versions", Path: "fix-versions"},
}

// ExtraColumns returns the TSV columns for the resolved ids, appended after the
// defaults.
//
// The header is the id rather than the name the caller typed, so the output of
// `--field "Story Points"` is byte-identical to that of
// `--field customfield_10042`. How a request was spelled is not part of the
// contract; what came back is.
//
// Without these, --field would change the XML and leave TSV — the default
// format — looking exactly as it did before, which is the same as doing
// nothing.
func ExtraColumns(resolved []string) []render.Column {
	out := make([]render.Column, 0, len(resolved))
	seen := map[string]bool{}
	for _, id := range resolved {
		if seen[id] {
			continue
		}
		seen[id] = true

		// A native field points at where the node already puts it; anything
		// else is an element named for its own id.
		col, native := nativeColumns[id]
		switch {
		case native:
			out = append(out, col)
		case nativeFields[id]:
			// Already one of the default columns. Asking for it is honoured by
			// it being there, and a second copy would be the surprise.
		default:
			out = append(out, render.Column{Header: id, Path: id})
		}
	}
	return dedupeColumns(out)
}

// dedupeColumns collapses two spellings of one field to a single column.
// `--field fixVersions --field fixversions` resolves to two ids pointing at one
// place in the node, and a caller who names a field twice wants it once.
func dedupeColumns(cols []render.Column) []render.Column {
	out := make([]render.Column, 0, len(cols))
	seen := map[string]bool{}
	for _, c := range cols {
		if seen[c.Header] {
			continue
		}
		seen[c.Header] = true
		out = append(out, c)
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
