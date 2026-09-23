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

	e.Unresolved = []UnresolvedFilter{{Flag: "assignee", Value: "Ada Lovelace"}}
	if err := render.Record(KindExplain, VersionExplain,
		e.Node()).Validate(); err != nil {
		t.Fatalf("an explanation with unresolved does not validate: %v", err)
	}
}
