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
	e, err := Explain("labels = retry", []string{"ENG"}, "", "")
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

// TestExplanationNodeNamesASetOfProjectsApartFromOne pins the v3 element both
// ways. Several projects are a `projects` list with no `project` leaf beside
// it, and one is the leaf with no list. A consumer written when `project`
// could only hold a key then reads a set as an absent leaf, which it already
// handles, rather than as one key with spaces in it.
func TestExplanationNodeNamesASetOfProjectsApartFromOne(t *testing.T) {
	several, err := Explain("labels = retry", []string{"OPS", "NET"}, "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if err := render.Record(KindExplain, VersionExplain,
		several.Node()).Validate(); err != nil {
		t.Fatalf("an explanation scoped to a set does not validate: %v", err)
	}
	if _, ok := several.Node().ChildNamed("project"); ok {
		t.Error("a set of projects was also written as the one-project leaf")
	}
	list, ok := several.Node().ChildNamed("projects")
	if !ok {
		t.Fatal("a set of projects did not reach the explanation")
	}
	if len(list.Children) != 2 || list.Children[0].Text != "OPS" || list.Children[1].Text != "NET" {
		t.Errorf("projects holds %d member(s), want OPS then NET", len(list.Children))
	}

	one, err := Explain("labels = retry", []string{"ENG"}, "", "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if _, ok := one.Node().ChildNamed("projects"); ok {
		t.Error("one project was written as a list of one")
	}
	if p, ok := one.Node().ChildNamed("project"); !ok || p.Text != "ENG" {
		t.Error("one project did not reach the explanation as the leaf")
	}
}
