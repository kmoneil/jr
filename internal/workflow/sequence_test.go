//go:build write

package workflow_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/workflow"
)

func parseSteps(t *testing.T, raw, key string) ([]workflow.SequenceStep, error) {
	t.Helper()
	return workflow.ParseSequenceSteps(registry.Default, []byte(raw), key)
}

// TestTheReportersCloseIsThreeSteps is issue 157's own example: a comment, a
// field and a transition, which is how a team closes a ticket, as one
// sequence. Each step is the command line it would have been, read by that
// command's declaration.
func TestTheReportersCloseIsThreeSteps(t *testing.T) {
	steps, err := parseSteps(t, `[
		["issue", "comment", "add", "ENG-101", "Shipped in 1.4."],
		["issue", "edit", "ENG-101", "--field", "Story Points=1"],
		["issue", "move", "ENG-101", "Done", "--resolution", "Done"]
	]`, "ENG-101")
	if err != nil {
		t.Fatalf("the reporter's close was refused: %v", err)
	}
	for i, want := range []string{"issue.comment.add", "issue.edit", "issue.move"} {
		if got := steps[i].Command.Name(); got != want || steps[i].Number != i+1 {
			t.Errorf("step %d is %s (number %d), want %s", i+1, got, steps[i].Number, want)
		}
	}
	if got := steps[0].Args; len(got) != 2 || got[1] != "Shipped in 1.4." {
		t.Errorf("the comment step's arguments are %q", got)
	}
	if got := steps[1].Flags.StringSlice("field"); len(got) != 1 || got[0] != "Story Points=1" {
		t.Errorf("the edit step's --field is %q", got)
	}
	if got := steps[2].Flags.String("resolution"); got != "Done" {
		t.Errorf("the move step's --resolution is %q", got)
	}
}

// TestEveryStepCommandIsAllowed is the v1 set, each in its simplest form, so a
// command dropped from the set fails here by name.
func TestEveryStepCommandIsAllowed(t *testing.T) {
	for _, step := range []string{
		`["issue", "edit", "ENG-1", "--summary", "New title"]`,
		`["issue", "assign", "ENG-1", "ada"]`,
		`["issue", "comment", "add", "ENG-1", "Hello"]`,
		`["issue", "link", "add", "ENG-1", "blocks", "ENG-2"]`,
		`["issue", "link", "add", "ENG-2", "blocks", "ENG-1"]`,
		`["sprint", "add", "7", "ENG-1"]`,
		`["issue", "move", "ENG-1", "Done"]`,
	} {
		if _, err := parseSteps(t, "["+step+"]", "ENG-1"); err != nil {
			t.Errorf("%s was refused: %v", step, err)
		}
	}
	if got := workflow.StepCommands(); len(got) != 6 {
		t.Errorf("StepCommands = %q, want the six v1 commands", got)
	}
}

// TestASequenceThatCouldNeverRunIsRefusedFromTheText is every refusal decided
// from the steps and the declarations alone, before anything reads the issue.
// Each names the step it is about.
func TestASequenceThatCouldNeverRunIsRefusedFromTheText(t *testing.T) {
	for _, tc := range []struct {
		name, raw, code, says string
	}{
		{"not JSON", `issue edit ENG-1`, "INVALID_STEPS", ""},
		{"a step that is not a list of strings", `[["issue", "edit", 1]]`, "INVALID_STEPS", ""},
		{"no steps", `[]`, "INVALID_STEPS", ""},
		{"too many steps", `[` + strings.Repeat(`["issue","comment","add","ENG-1","x"],`, 20) +
			`["issue","comment","add","ENG-1","x"]]`, "TOO_MANY_STEPS", "21"},
		{"words that are no command", `[["issue", "frobnicate", "ENG-1"]]`, "STEP_NOT_ALLOWED", "step 1"},
		{"the binary's name left on", `[["jr", "issue", "edit", "ENG-1"]]`, "STEP_NOT_ALLOWED", "step 1"},
		{"a read", `[["issue", "get", "ENG-1"]]`, "STEP_NOT_ALLOWED", "issue get"},
		{"a destructive command", `[["issue", "delete", "ENG-1"]]`, "STEP_NOT_ALLOWED", "issue delete"},
		{
			"a global flag", `[["issue", "edit", "ENG-1", "--summary", "x"], ["issue", "edit", "ENG-1", "--project", "OPS"]]`,
			registry.GlobalFlagInStep, "step 2",
		},
		{"a dry run", `[["issue", "edit", "ENG-1", "--summary", "x", "--dry-run"]]`, "STEP_TAKES_NO_PLAN_FLAG", "--dry-run"},
		{
			"a baseline", `[["issue", "edit", "ENG-1", "--summary", "x", "--if-unchanged", "2026-01-01T00:00:00Z"]]`,
			"STEP_TAKES_NO_PLAN_FLAG", "--if-unchanged",
		},
		{
			"a plan of its own", `[["issue", "move", "ENG-1", "Done", "--plan-out", "p.xml"]]`,
			"STEP_TAKES_NO_PLAN_FLAG", "--plan-out",
		},
		{
			"a flag the command does not have", `[["issue", "comment", "add", "ENG-1", "x", "--field", "a=b"]]`,
			"INVALID_USAGE", "step 1",
		},
		{"too few arguments", `[["issue", "comment", "add", "ENG-1"]]`, "INVALID_USAGE", "step 1"},
		{"another issue", `[["issue", "edit", "ENG-2", "--summary", "x"]]`, "STEP_NAMES_ANOTHER_ISSUE", "ENG-2"},
		{"two issues", `[["issue", "edit", "ENG-1", "ENG-2", "--summary", "x"]]`, "STEP_NAMES_ANOTHER_ISSUE", "ENG-2"},
		{"a comment elsewhere", `[["issue", "comment", "add", "ENG-2", "x"]]`, "STEP_NAMES_ANOTHER_ISSUE", "ENG-2"},
		{
			"a link between two others", `[["issue", "link", "add", "ENG-2", "blocks", "ENG-3"]]`,
			"STEP_NAMES_ANOTHER_ISSUE", "neither",
		},
		{"a sprint for another", `[["sprint", "add", "7", "ENG-1", "ENG-2"]]`, "STEP_NAMES_ANOTHER_ISSUE", "ENG-2"},
		{"an assignee and no key", `[["issue", "assign", "ada"]]`, "NO_ISSUES", "step 1"},
		{
			"a move first", `[["issue", "move", "ENG-1", "Done"], ["issue", "comment", "add", "ENG-1", "x"]]`,
			"TRANSITION_NOT_LAST", "step 1 of 2",
		},
		{
			"two moves", `[["issue", "move", "ENG-1", "In Progress"], ["issue", "move", "ENG-1", "Done"]]`,
			"TRANSITION_NOT_LAST", "1 and 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSteps(t, tc.raw, "ENG-1")
			if err == nil {
				t.Fatal("accepted")
			}
			e := errs.Coerce(err)
			if e.Code != tc.code {
				t.Errorf("refused as %s (%s), want %s", e.Code, e.Message, tc.code)
			}
			if !strings.Contains(e.Message, tc.says) {
				t.Errorf("message %q does not say %q", e.Message, tc.says)
			}
		})
	}
}

// TestTheSequencesIssueIsReadAsAKey keeps a lower-case spelling of the
// sequence's own issue from reading as a stranger, as everywhere else a key is
// typed.
func TestTheSequencesIssueIsReadAsAKey(t *testing.T) {
	if _, err := parseSteps(t, `[["issue", "edit", "eng-1", "--summary", "x"]]`, "ENG-1"); err != nil {
		t.Errorf("the sequence's own issue in lower case was refused: %v", err)
	}
}

// TestAStepsOwnRefusalKeepsItsCode is the command's refusal, unchanged but for
// the step it happened at, so a caller branching on the code reads the one
// the command line would have given.
func TestAStepsOwnRefusalKeepsItsCode(t *testing.T) {
	_, err := parseSteps(t, `[["issue", "comment", "add", "ENG-1", "x"], ["issue", "edit", "ENG-1", "--nope"]]`, "ENG-1")
	e := errs.Coerce(err)
	if e.Code != "INVALID_USAGE" || !strings.HasPrefix(e.Message, "step 2: ") ||
		!strings.Contains(e.Remedy, "issue edit --help") {
		t.Errorf("got %s %q, remedy %q", e.Code, e.Message, e.Remedy)
	}
}
