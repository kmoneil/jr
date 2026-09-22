package issue

import (
	"strings"
	"testing"
)

// FuzzParseSprintDumpHoldsItsDeclaredShape is the regression for a defect that
// was introduced and caught before it shipped.
//
// `state` is declared as a closed set of future, active, and closed, and every
// document is checked against its schema before it reaches stdout. The first
// version of the parse lower-cased whatever the server sent and passed it
// through, so a Jira reporting any other word would have turned `issue get`
// into a SCHEMA_VIOLATION that failed the whole command rather than reporting a
// sprint without its state.
//
// Two properties, and the first is the one a fuzzer is for: whatever comes off
// a server, this returns or it does not, and it never panics doing it.
func FuzzParseSprintDumpHoldsItsDeclaredShape(f *testing.F) {
	for _, seed := range []string{
		// The measured shape, from Jira 10.4.0 Data Center.
		`com.atlassian.greenhopper.service.sprint.Sprint@1f581600[activatedDate=<null>,` +
			`autoStartStop=false,completeDate=<null>,endDate=<null>,goal=<null>,id=1,` +
			`incompleteIssuesDestinationId=<null>,name=ENG Sprint 1,rapidViewId=1,` +
			`sequence=1,startDate=<null>,state=FUTURE,synced=false]`,
		// The defect this test exists for.
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=1,name=A,state=PAUSED]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=1,name=A,state=]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=1,name=A,state=<null>]`,
		// Separators inside values, which the format cannot escape.
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=7,name=Q3, week 2,state=ACTIVE]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=7,name=a=b,state=CLOSED]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=7,name=]]>,state=CLOSED]`,
		// Boundaries of the anchor and the brackets.
		`com.atlassian.greenhopper.service.sprint.Sprint@`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[id=1]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[name=only]`,
		`com.atlassian.greenhopper.service.sprint.Sprint@1[=,=,=]`,
		// Not sprints.
		"", "not a sprint", "see com.atlassian.greenhopper.service.sprint.Sprint",
		"\x00", "É", strings.Repeat("a", 1024),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, ok := parseSprintDump(s)
		if !ok {
			return
		}
		// The attribute is declared as a closed set, and an empty value is
		// omitted rather than emitted, so those are the only four answers that
		// can survive schema conformance.
		if got.State != "" && !sprintStates[got.State] {
			t.Errorf("state = %q, which the schema declares impossible", got.State)
		}
		// Reporting a sprint that names neither of the two things a caller can
		// act on is worse than reporting the raw value.
		if got.ID == "" && got.Name == "" {
			t.Errorf("parsed %q into a sprint with no id and no name", s)
		}
	})
}
