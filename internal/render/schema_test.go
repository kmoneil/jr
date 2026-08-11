package render_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/render"
)

// widget is a shape with one of everything the schema language can express, so
// the tests below can violate one rule at a time.
func widget() *render.Schema {
	return &render.Schema{
		Element: "widget",
		Attrs: []render.Field{
			{Name: "id", Type: render.TypeInt},
			{Name: "kind", Type: render.TypeString, Enum: []string{"big", "small"}},
			{Name: "ready", Type: render.TypeBool, Optional: true},
			{Name: "seen", Type: render.TypeTimestamp, Optional: true},
			{Name: "due", Type: render.TypeDate, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("name", render.TypeString)},
			{Schema: render.Leaf("note", render.TypeString), Optional: true},
			{Schema: render.Leaf("tag", render.TypeString), Optional: true, Repeated: true},
		},
	}
}

func validWidget() *render.Node {
	return render.El("widget").
		Attr("id", "7").
		Attr("kind", "big").
		Leaf("name", "a widget")
}

func TestAConformingNodePasses(t *testing.T) {
	full := validWidget().
		Attr("ready", "true").
		Attr("seen", "2026-08-06T09:00:00Z").
		Attr("due", "2026-08-31")
	full.Child(render.El("note").SetText("a note"))
	full.Child(render.El("tag").SetText("one"))
	full.Child(render.El("tag").SetText("two"))

	if err := widget().Conform(full, "test"); err != nil {
		t.Fatalf("a conforming node was refused: %v", err)
	}
}

// TestEveryWayToViolateAShape is the table the enforcement rests on. Each row
// is a shape a consumer could not parse against the published contract, so each
// has to be refused rather than written.
func TestEveryWayToViolateAShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func() *render.Node
		detail string
	}{
		{
			name:   "wrong element",
			build:  func() *render.Node { n := validWidget(); n.Name = "gadget"; return n },
			detail: "expected <widget> and found <gadget>",
		},
		{
			name:   "undeclared attribute",
			build:  func() *render.Node { return validWidget().Attr("colour", "red") },
			detail: `undeclared attribute "colour"`,
		},
		{
			name: "missing required attribute",
			build: func() *render.Node {
				return render.El("widget").Attr("kind", "big").Leaf("name", "x")
			},
			detail: `required attribute "id" is missing`,
		},
		{
			name:   "attribute of the wrong type",
			build:  func() *render.Node { n := validWidget(); n.Attrs[0].Value = "seven"; return n },
			detail: `is "seven", which is declared as an integer`,
		},
		{
			name:   "value outside its enum",
			build:  func() *render.Node { n := validWidget(); n.Attrs[1].Value = "medium"; return n },
			detail: "which is not one of big, small",
		},
		{
			name:   "a bool that is not true or false",
			build:  func() *render.Node { return validWidget().Attr("ready", "yes") },
			detail: "a bool is exactly true or false",
		},
		{
			// The whole reason timestamps are normalized: a consumer told the
			// value is RFC 3339 must not meet Data Center's own spelling.
			name:   "a timestamp in Jira's own format",
			build:  func() *render.Node { return validWidget().Attr("seen", "2026-08-06T09:00:00.000+0000") },
			detail: "which is not RFC 3339",
		},
		{
			name:   "a timestamp that is not UTC",
			build:  func() *render.Node { return validWidget().Attr("seen", "2026-08-06T09:00:00+02:00") },
			detail: "which is not UTC",
		},
		{
			name:   "a date carrying a time",
			build:  func() *render.Node { return validWidget().Attr("due", "2026-08-31T00:00:00Z") },
			detail: "which is not a YYYY-MM-DD date",
		},
		{
			name: "undeclared element",
			build: func() *render.Node {
				return validWidget().Child(render.El("sprocket").SetText("x"))
			},
			detail: "undeclared element <sprocket>",
		},
		{
			name:   "missing required element",
			build:  func() *render.Node { return render.El("widget").Attr("id", "7").Attr("kind", "big") },
			detail: "required element <name> is missing",
		},
		{
			name: "an element repeated that may not be",
			build: func() *render.Node {
				return validWidget().Child(render.El("name").SetText("again"))
			},
			detail: "appears more than once and is not repeated",
		},
		{
			name:   "text where none is declared",
			build:  func() *render.Node { return validWidget().SetText("loose text") },
			detail: "carries text, and none is declared",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := widget().Conform(tc.build(), "test")
			if err == nil {
				t.Fatal("a non-conforming node was accepted")
			}
			e := errs.Coerce(err)
			if e.Code != "SCHEMA_VIOLATION" {
				t.Errorf("code = %q, want SCHEMA_VIOLATION", e.Code)
			}
			if !strings.Contains(e.Detail, tc.detail) {
				t.Errorf("detail = %q, want it to contain %q", e.Detail, tc.detail)
			}
			// The message names where in the document it went wrong, so a
			// report is actionable without reproducing it.
			if !strings.Contains(e.Message, "test") {
				t.Errorf("message = %q, want it to name the position", e.Message)
			}
		})
	}
}

// TestAnEmptyValuePassesEveryTypeCheck covers the one deliberate hole. This
// tool emits "present but empty" on purpose — an unassigned issue, a context
// with no default project — and type-checking those would force every such
// field to be declared as a string, which would say less than the truth.
func TestAnEmptyValuePassesEveryTypeCheck(t *testing.T) {
	n := validWidget().Attr("ready", "").Attr("seen", "").Attr("due", "")
	if err := widget().Conform(n, "test"); err != nil {
		t.Fatalf("an empty value was refused: %v", err)
	}
}

// TestExtraPermitsCallerNamedElements covers the shape that depends on flags.
// `issue list --field "Story Points"` adds a <customfield_10042>, and no fixed
// list can name it — so the contract says where the names come from instead of
// pretending the shape is closed.
func TestExtraPermitsCallerNamedElements(t *testing.T) {
	open := widget()
	open.Extra = &render.Extra{Named: "a requested field id", Type: render.TypeString}

	n := validWidget().Leaf("customfield_10042", "3")
	if err := open.Conform(n, "test"); err != nil {
		t.Fatalf("an extra element was refused by an open schema: %v", err)
	}
	// Closed by default: the same node against the same schema without Extra.
	if err := widget().Conform(n, "test"); err == nil {
		t.Error("a closed schema accepted an undeclared element")
	}
}

// TestWriteRefusesANonConformingDocument is the enforcement that makes the
// published schema worth trusting. render.Write validates before it emits a
// byte, so a document that does not match its contract never reaches stdout.
func TestWriteRefusesANonConformingDocument(t *testing.T) {
	const kind = "test.widget"
	render.RegisterSchema(kind, widget())

	good := render.Record(kind, 1, validWidget())
	if err := render.Write(io.Discard, good, render.XML); err != nil {
		t.Fatalf("a conforming document was refused: %v", err)
	}

	bad := render.Record(kind, 1, validWidget().Attr("colour", "red"))
	var buf strings.Builder
	if err := render.Write(&buf, bad, render.XML); err == nil {
		t.Fatal("a non-conforming document was written")
	}
	if buf.Len() != 0 {
		t.Errorf("bytes reached the writer before the refusal:\n%s", buf.String())
	}
}

// TestAnUnregisteredKindIsNotChecked documents the deliberate gap. A test
// building an ad-hoc document should not have to invent a schema to render it;
// internal/cli/contract_test.go is what makes sure no shipped kind is in that
// position.
func TestAnUnregisteredKindIsNotChecked(t *testing.T) {
	doc := render.Record("test.unregistered", 1, render.El("anything").Attr("x", "y"))
	if err := render.Write(io.Discard, doc, render.XML); err != nil {
		t.Fatalf("an unregistered kind was checked against something: %v", err)
	}
}

// TestTheSchemaRendersItself covers what `jr contract` publishes.
func TestTheSchemaRendersItself(t *testing.T) {
	n := widget().Node()
	if name, _ := n.AttrValue("name"); name != "widget" {
		t.Errorf("name = %q", name)
	}

	attrs, ok := n.ChildNamed("attributes")
	if !ok || len(attrs.Children) != 5 {
		t.Fatalf("attributes = %+v, want all five", attrs)
	}
	first := attrs.Children[0]
	if got, _ := first.AttrValue("type"); got != string(render.TypeInt) {
		t.Errorf("first attribute type = %q", got)
	}

	elements, ok := n.ChildNamed("elements")
	if !ok || len(elements.Children) != 3 {
		t.Fatalf("elements = %+v, want all three", elements)
	}
	// Cardinality is published, because "may this be absent" is the question a
	// hand-written parser gets wrong.
	repeated := elements.Children[2]
	if got, _ := repeated.AttrValue("repeated"); got != "true" {
		t.Errorf("tag repeated = %q, want true", got)
	}
}
