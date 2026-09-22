package render_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/render"
)

// openShape is a schema whose element names are open and whose extra elements
// are fully declared: an optional `name`, and text.
func openShape() *render.Schema {
	return &render.Schema{
		Element: "issue",
		Attrs:   []render.Field{{Name: "key", Type: render.TypeString}},
		Extra: &render.Extra{
			Named: "a requested field id",
			Type:  render.TypeString,
			Attrs: []render.Field{
				{Name: "name", Type: render.TypeString, Optional: true},
			},
		},
	}
}

// TestAnOpenShapeEnforcesItsDeclaredAttributes is the fix this file exists for.
//
// Until Recursive existed, this check could not be turned on. Three attempts
// were made and all three were hedged back out, because the schema document's
// own meta-schema used an Extra to mean "an element, recursively" and every one
// of those carries a name, so enforcing an Extra's attributes refused every
// schema jr publishes. The two statements are now different types and this one
// can be checked.
func TestAnOpenShapeEnforcesItsDeclaredAttributes(t *testing.T) {
	ok := render.El("issue").Attr("key", "ENG-1").
		Child(render.El("customfield_10042").Attr("name", "Story Points").SetText("5"))
	if err := openShape().Conform(ok, ""); err != nil {
		t.Errorf("a declared attribute was refused: %v", err)
	}

	bad := render.El("issue").Attr("key", "ENG-1").
		Child(render.El("customfield_10042").Attr("undeclared", "Story Points").SetText("5"))
	err := openShape().Conform(bad, "")
	if err == nil {
		t.Fatal("an undeclared attribute on an open shape was accepted")
	}
	if !strings.Contains(err.Error(), "undeclared") {
		t.Errorf("the violation does not name the attribute: %v", err)
	}
}

// TestARecursiveShapeChecksNothingBelowIt is the other half, and it is what
// makes the check above affordable.
//
// A schema that declines to describe its children cannot check them, and
// saying so is the declaration. Anything at all is allowed through, including
// the attributes that used to make this indistinguishable from an open shape.
func TestARecursiveShapeChecksNothingBelowIt(t *testing.T) {
	s := &render.Schema{
		Element:   "elements",
		ListOf:    "element",
		Attrs:     []render.Field{{Name: "count", Type: render.TypeInt}},
		Recursive: &render.Recursive{Named: "element, because this schema contains itself"},
	}

	n := render.ListEl("elements", "element",
		render.El("element").Attr("name", "summary").Attr("anything", "at all").
			Child(render.El("whatever")),
	)
	if err := s.Conform(n, ""); err != nil {
		t.Errorf("a recursive shape checked what it said it would not: %v", err)
	}
}

// TestRecursiveWinsOverExtra pins the precedence. A schema carrying both is
// claiming to describe what it has just said it will not, and the honest half
// is the one that declines.
func TestRecursiveWinsOverExtra(t *testing.T) {
	s := openShape()
	s.Recursive = &render.Recursive{Named: "anything"}

	n := render.El("issue").Attr("key", "ENG-1").
		Child(render.El("customfield_10042").Attr("undeclared", "x"))
	if err := s.Conform(n, ""); err != nil {
		t.Errorf("Recursive did not take precedence over Extra: %v", err)
	}
}

// TestARecursiveShapeIsPublished is the contract half. A consumer pins the
// shape with `jr contract`, so a statement the schema cannot print is a
// statement the contract does not carry.
func TestARecursiveShapeIsPublished(t *testing.T) {
	s := &render.Schema{
		Element:   "elements",
		Recursive: &render.Recursive{Named: "element, because this schema contains itself"},
	}

	var b strings.Builder
	if err := render.Write(&b, render.Record("probe", 1, s.Node()), render.Format("xml")); err != nil {
		t.Fatalf("writing the schema: %v", err)
	}
	if !strings.Contains(b.String(), "<recursive>element, because this schema contains itself</recursive>") {
		t.Errorf("a recursive shape is not published:\n%s", b.String())
	}
}
