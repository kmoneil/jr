package issue

import (
	"strings"

	"github.com/kmoneil/jr/internal/render"
)

// sprintDumpPrefix is what Data Center puts in an issue's sprint field: the
// Greenhopper object's Java toString, one string per sprint.
//
// Measured against Jira 10.4.0 Data Center on 2026-09-22, an issue in one
// sprint carries exactly this:
//
//	com.atlassian.greenhopper.service.sprint.Sprint@1f581600[activatedDate=<null>,
//	autoStartStop=false,completeDate=<null>,endDate=<null>,goal=<null>,id=1,
//	incompleteIssuesDestinationId=<null>,name=ENG Sprint 1,rapidViewId=1,
//	sequence=1,startDate=<null>,state=FUTURE,synced=false]
//
// Cloud sends an array of objects for the same field and never had this
// problem: `scalarizeValue`'s map branch already reduces one to its `name`.
//
// The match is anchored on the fully qualified class name rather than on the
// shape of the brackets, because the alternative is reinterpreting a text field
// that happens to look structured. A value that genuinely is a serialized
// Greenhopper sprint is a sprint, whatever field it arrived in; a value that
// merely mentions the class is prose, and prose is carried through untouched.
const sprintDumpPrefix = "com.atlassian.greenhopper.service.sprint.Sprint@"

// javaNull is what the dump writes for an absent value. It is four characters
// of text, not an empty one, so it has to be recognised rather than trusted.
const javaNull = "<null>"

// sprintStates is the vocabulary the `state` attribute is declared with.
//
// Spelled out here rather than imported from `resource/sprint`, which holds the
// same three, because resources never import each other. The duplication is the
// architecture rule's price and is cheap: this is Jira's own closed set for the
// agile API, and `sprint list --state` takes exactly these words.
var sprintStates = map[string]bool{"future": true, "active": true, "closed": true}

// sprintRef is one sprint, as much of it as the dump names.
type sprintRef struct {
	ID    string
	Name  string
	State string
}

// parseSprintDump reads one Greenhopper toString. It reports false for anything
// that is not one, which is every other value in Jira.
//
// The format has no escaping: keys and values are separated by `=`, pairs by
// `,`, and a sprint named `Q3, week 2` produces a dump nothing can take apart
// correctly. That is Greenhopper's problem and it cannot be solved here, so the
// parse is deliberately narrow: it reads the keys it wants and tolerates
// anything it does not recognise. A name containing a comma loses its tail
// rather than corrupting the next field, and the caller still gets the id and
// the state.
func parseSprintDump(s string) (sprintRef, bool) {
	rest, ok := cutSprintPrefix(s)
	if !ok {
		return sprintRef{}, false
	}
	open := strings.IndexByte(rest, '[')
	if open < 0 || !strings.HasSuffix(rest, "]") {
		return sprintRef{}, false
	}

	out := sprintRef{}
	body := rest[open+1 : len(rest)-1]
	for pair := range strings.SplitSeq(body, ",") {
		key, value, found := strings.Cut(pair, "=")
		if !found || value == javaNull {
			continue
		}
		switch key {
		case "id":
			out.ID = value
		case "name":
			out.Name = value
		case "state":
			// Jira spells it ACTIVE, CLOSED, FUTURE; jr spells sprint state in
			// lower case everywhere else, and `sprint list --state active` is
			// the flag a caller already knows.
			//
			// An unrecognised state is dropped rather than passed through,
			// because the schema declares this attribute as a closed set and
			// output is checked against the schema before it reaches stdout.
			// Passing one through would turn an unfamiliar server into a
			// SCHEMA_VIOLATION that fails the whole command, which is a far
			// worse answer than a sprint reported without its state.
			if lower := strings.ToLower(value); sprintStates[lower] {
				out.State = lower
			}
		}
	}
	// A dump naming neither an id nor a name says nothing a caller can use, and
	// treating it as a sprint would replace a bad value with an empty one.
	if out.ID == "" && out.Name == "" {
		return sprintRef{}, false
	}
	return out, true
}

// cutSprintPrefix takes the class name off the front, and is where the anchor
// lives.
func cutSprintPrefix(s string) (string, bool) {
	return strings.CutPrefix(strings.TrimSpace(s), sprintDumpPrefix)
}

// sprintRefs reads a whole field value as sprints, reporting false unless every
// element is one.
//
// All or nothing, deliberately. A field holding one parseable dump and one
// unparseable string is not a sprint field this code understands, and rendering
// half of it structurally would report a sprint list with a member silently
// missing. Falling back leaves every byte in the caller's hands.
func sprintRefs(values []string) ([]sprintRef, bool) {
	if len(values) == 0 {
		return nil, false
	}
	out := make([]sprintRef, 0, len(values))
	for _, v := range values {
		ref, ok := parseSprintDump(v)
		if !ok {
			return nil, false
		}
		out = append(out, ref)
	}
	return out, true
}

// sprintNode renders the sprints of one field as the list container the field
// id names.
//
// The item carries its name as text rather than as an attribute so that a TSV
// column addressing the field flattens to the names: `listValues` reads each
// child's text, which is what makes `--field Sprint` a column holding
// "ENG Sprint 1, ENG Sprint 2" instead of a blank cell.
func sprintNode(id string, refs []sprintRef) *render.Node {
	items := make([]*render.Node, 0, len(refs))
	for _, r := range refs {
		items = append(items, render.El("sprint").
			AttrIf("id", r.ID).
			AttrIf("state", r.State).
			SetText(r.Name))
	}
	return render.ListEl(id, "sprint", items...)
}

// sprintFieldSchema is the shape a requested field takes when it holds sprints
// rather than a scalar.
//
// Declared on the open shape rather than on a named element because the
// container's name is the field's own id, which differs per site: the sprint
// field is customfield_10109 on one Data Center and customfield_10020 on
// another, and no fixed list can name it. That is the same reason `--field`
// needs an open shape at all.
//
// The name is the item's text rather than an attribute so a TSV column over the
// field flattens to the names. The two dates Greenhopper also reports are
// deliberately absent: a sprint's window is `sprint get`'s answer, and copying
// it onto every issue row would be the same field twice with two chances to
// disagree.
func sprintFieldSchema() *render.Schema {
	return render.ListSchema("field", "sprint", &render.Schema{
		Element: "sprint",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeString, Optional: true},
			{
				Name: "state", Type: render.TypeString, Optional: true,
				Enum: []string{"future", "active", "closed"},
			},
		},
		Text: &render.Field{Type: render.TypeString},
	})
}
