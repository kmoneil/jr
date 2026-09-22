package issue

import "testing"

// The dump has no escaping, so the interesting cases are all about what happens
// when a value contains a separator. They are pinned here rather than through
// the CLI because each one is a property of the parse and nothing else.
func TestParseSprintDump(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  sprintRef
		ok    bool
	}{
		{
			// Measured against Jira 10.4.0 Data Center, 2026-09-22.
			name: "a real dump",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1f581600[` +
				`activatedDate=<null>,autoStartStop=false,completeDate=<null>,` +
				`endDate=<null>,goal=<null>,id=1,incompleteIssuesDestinationId=<null>,` +
				`name=ENG Sprint 1,rapidViewId=1,sequence=1,startDate=<null>,` +
				`state=FUTURE,synced=false]`,
			want: sprintRef{ID: "1", Name: "ENG Sprint 1", State: "future"},
			ok:   true,
		},
		{
			// A pipe in a sprint name is ordinary and must survive: the
			// reporter's own sprints were named "Team | Sprint 3".
			name: "a name holding a pipe",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1[` +
				`id=7,name=Team | Sprint 3,state=ACTIVE]`,
			want: sprintRef{ID: "7", Name: "Team | Sprint 3", State: "active"},
			ok:   true,
		},
		{
			// The format cannot carry this and nothing here can fix it. What
			// it must not do is corrupt the other fields: the tail after the
			// comma is dropped, and id and state still arrive.
			name: "a name holding a comma loses its tail",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1[` +
				`id=7,name=Q3, week 2,state=ACTIVE]`,
			want: sprintRef{ID: "7", Name: "Q3", State: "active"},
			ok:   true,
		},
		{
			// And the reason that is only a loss rather than a takeover: the
			// real dump writes no space after a comma, so a key is compared
			// untrimmed and " state" is not "state". A name that embeds what
			// looks like a later key cannot overwrite one.
			name: "a name cannot forge a later key",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1[` +
				`id=7,name=Q3, state=CLOSED,state=ACTIVE]`,
			want: sprintRef{ID: "7", Name: "Q3", State: "active"},
			ok:   true,
		},
		{
			// The schema declares state as a closed set and every document is
			// checked against its schema before it reaches stdout, so passing
			// an unfamiliar word through would fail the whole command. The
			// sprint still arrives; only the state it could not vouch for is
			// dropped.
			name: "an unrecognised state is dropped, not passed through",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1[` +
				`id=7,name=Q3,state=PAUSED]`,
			want: sprintRef{ID: "7", Name: "Q3"},
			ok:   true,
		},
		{
			name:  "prose that mentions the class",
			input: `see com.atlassian.greenhopper.service.sprint.Sprint for the format`,
			ok:    false,
		},
		{
			name:  "the class name with no body",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1f581600`,
			ok:    false,
		},
		{
			// Neither an id nor a name is nothing a caller can act on, and
			// replacing a bad value with an empty one is worse than leaving it.
			name: "a dump naming neither id nor name",
			input: `com.atlassian.greenhopper.service.sprint.Sprint@1[` +
				`autoStartStop=false,synced=false]`,
			ok: false,
		},
		{name: "empty", input: "", ok: false},
		{name: "an ordinary string", input: "not a sprint at all", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSprintDump(tc.input)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSprintRefsIsAllOrNothing is the fallback rule. A field holding one
// parseable dump and one unparseable string is not a sprint field this code
// understands, and rendering half of it would report a sprint list with a
// member silently missing, which is the failure mode the whole project is
// built to refuse.
func TestSprintRefsIsAllOrNothing(t *testing.T) {
	good := `com.atlassian.greenhopper.service.sprint.Sprint@1[id=1,name=One,state=CLOSED]`

	if _, ok := sprintRefs([]string{good, "something else entirely"}); ok {
		t.Error("a mixed array parsed as sprints")
	}
	if _, ok := sprintRefs(nil); ok {
		t.Error("an empty array parsed as sprints")
	}
	refs, ok := sprintRefs([]string{good})
	if !ok || len(refs) != 1 || refs[0].Name != "One" {
		t.Errorf("a one-sprint array did not parse: %v %+v", ok, refs)
	}
}
