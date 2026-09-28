package jql

import (
	"testing"

	"github.com/kmoneil/jr/internal/render"
)

// TestExplanationNodeSaysWhatWasNotResolved pins the v2 element both ways: a
// filter under unresolved validates against the schema, and an explanation
// with nothing unresolved emits no container for a consumer to misread as an
// empty claim.
func TestExplanationNodeSaysWhatWasNotResolved(t *testing.T) {
	e, err := Explain("labels = retry", "ENG", "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if err := render.Record(KindExplain, VersionExplain,
		e.Node()).Validate(); err != nil {
		t.Fatalf("a clean explanation does not validate: %v", err)
	}
	if _, ok := e.Node().ChildNamed("unresolved"); ok {
		t.Fatal("an explanation with nothing unresolved emitted an <unresolved> container")
	}

	e.Unresolved = []UnresolvedFilter{{Flag: "assignee", Value: "Ada Lovelace"}}
	if err := render.Record(KindExplain, VersionExplain,
		e.Node()).Validate(); err != nil {
		t.Fatalf("an explanation with unresolved does not validate: %v", err)
	}
	u, ok := e.Node().ChildNamed("unresolved")
	if !ok {
		t.Fatal("an unresolved filter did not reach the explanation")
	}
	if len(u.Children) != 1 {
		t.Fatalf("unresolved holds %d filter(s), want 1", len(u.Children))
	}
	f := u.Children[0]
	if flag, _ := f.AttrValue("flag"); flag != "assignee" {
		t.Errorf("flag = %q, want assignee", flag)
	}
	if f.Text != "Ada Lovelace" {
		t.Errorf("value = %q, want the input as typed", f.Text)
	}
}
