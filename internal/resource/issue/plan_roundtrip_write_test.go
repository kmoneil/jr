//go:build write

package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
)

// planFixture is a plan with one of everything the change set can carry, so a
// round trip that drops a field fails rather than passing on the easy case.
func planFixture() *issue.Plan {
	return &issue.Plan{
		Verb: "issue.edit",
		Change: issue.EditOptions{
			Summary:      "a better summary",
			Description:  "a body\nwith a newline",
			Priority:     "High",
			Assignee:     "712020:8f3a",
			SetAssignee:  true,
			Parent:       "ENG-42",
			SetParent:    true,
			Labels:       []string{"kept", "also-kept"},
			AddLabels:    []string{"triaged"},
			RemoveLabels: []string{"wontfix"},
			Fields:       map[string]any{"customfield_10042": "5"},
		},
		Rows: []issue.PlanRow{
			{Key: "ENG-101", Precondition: "eyJrIjoiRU5HLTEwMSJ9", IdempotencyKey: "auto-aaaa"},
			{Key: "ENG-102", Precondition: "eyJrIjoiRU5HLTEwMiJ9", IdempotencyKey: "auto-bbbb"},
		},
	}
}

func renderPlan(t *testing.T, p *issue.Plan) string {
	t.Helper()
	var buf strings.Builder
	if err := render.Write(&buf, issue.PlanDoc(p), render.XML); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// TestAPlanSurvivesBeingWrittenAndReadBack is the property the whole surface
// rests on: apply rebuilds requests from what it parsed, so anything the
// round trip drops is a change the caller reviewed and did not get.
//
// It is asserted field by field rather than by comparing two structs, because
// a struct comparison that passes tells you nothing about which field was
// carried and a new field added to EditOptions would silently be untested
// either way. TestEveryChangeFieldSurvivesThePlan holds that gap.
func TestAPlanSurvivesBeingWrittenAndReadBack(t *testing.T) {
	want := planFixture()
	got, err := issue.ParsePlan(strings.NewReader(renderPlan(t, want)))
	if err != nil {
		t.Fatalf("a plan this tool wrote was refused by its own reader: %v", err)
	}

	if got.Verb != want.Verb {
		t.Errorf("verb = %q, want %q", got.Verb, want.Verb)
	}
	if len(got.Rows) != len(want.Rows) {
		t.Fatalf("got %d rows, want %d", len(got.Rows), len(want.Rows))
	}
	for i, row := range want.Rows {
		if got.Rows[i] != row {
			t.Errorf("row %d = %+v, want %+v", i, got.Rows[i], row)
		}
	}

	c, w := got.Change, want.Change
	for _, tc := range []struct{ name, got, want string }{
		{"summary", c.Summary, w.Summary},
		{"description", c.Description, w.Description},
		{"priority", c.Priority, w.Priority},
		{"assignee", c.Assignee, w.Assignee},
		{"parent", c.Parent, w.Parent},
		{"labels", strings.Join(c.Labels, ","), strings.Join(w.Labels, ",")},
		{"add-labels", strings.Join(c.AddLabels, ","), strings.Join(w.AddLabels, ",")},
		{"remove-labels", strings.Join(c.RemoveLabels, ","), strings.Join(w.RemoveLabels, ",")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if !c.SetAssignee || !c.SetParent {
		t.Errorf("set-assignee/set-parent = %t/%t, want both true", c.SetAssignee, c.SetParent)
	}
	if got, ok := c.Fields["customfield_10042"]; !ok || got != "5" {
		t.Errorf("customfield_10042 = %v (present %t), want 5", got, ok)
	}
}

// TestClearingLabelsSurvivesSeparatelyFromLeavingThemAlone is the case a
// struct comparison would pass over and a caller would lose an edit to.
//
// A nil slice and an empty one are different requests: nil leaves the labels
// alone, empty clears them. If the round trip turned "clear" into "leave
// alone", the reviewed plan would say the labels go and the run would keep
// them, with nothing failing.
func TestClearingLabelsSurvivesSeparatelyFromLeavingThemAlone(t *testing.T) {
	clearing := &issue.Plan{
		Verb:   "issue.edit",
		Change: issue.EditOptions{Labels: []string{}},
		Rows:   []issue.PlanRow{{Key: "ENG-1", IdempotencyKey: "auto-a"}},
	}
	got, err := issue.ParsePlan(strings.NewReader(renderPlan(t, clearing)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Change.Labels == nil {
		t.Error("a plan that clears the labels was read back as one that leaves them alone")
	}
	if len(got.Change.Labels) != 0 {
		t.Errorf("labels = %v, want empty", got.Change.Labels)
	}

	leaving := &issue.Plan{
		Verb:   "issue.edit",
		Change: issue.EditOptions{Summary: "x"},
		Rows:   []issue.PlanRow{{Key: "ENG-1", IdempotencyKey: "auto-a"}},
	}
	got, err = issue.ParsePlan(strings.NewReader(renderPlan(t, leaving)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Change.Labels != nil {
		t.Errorf("a plan that leaves the labels alone was read back as clearing them: %v",
			got.Change.Labels)
	}
}
