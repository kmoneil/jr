package cli

import (
	"strings"
	"testing"
)

// TestTheProgressLineCountsWhatItIsCounting is the regression for a line that
// said "issues" on every streaming command in the tool.
//
// `jr project list` reported "12 issues" on a terminal, `jr field list`
// reported "148 issues", and so did user, board, sprint, epic, transition,
// component, link, and attachment listings. Cosmetic, human-only, and never
// present on a pipe, which is how it lasted: the value was hardcoded two lines
// from where the right one was already available.
//
// Driven against the reporter rather than through the binary, because the
// reporter exists only when stderr is a terminal and standing one up needs a
// pty. What that leaves uncovered is one line in stream.go, `a.progress(
// rc.CollectionName)`, and the shape of a regression there is the whole tool
// reverting to a single noun again.
func TestTheProgressLineCountsWhatItIsCounting(t *testing.T) {
	for _, tc := range []struct {
		noun string
		want string
	}{
		{"projects", "12 / 40 projects,"},
		{"issues", "12 / 40 issues,"},
		{"worklogs", "12 / 40 worklogs,"},
	} {
		var out strings.Builder
		newTTYProgress(&out, tc.noun).Update(12, 40)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("noun %q produced %q, want it to contain %q",
				tc.noun, out.String(), tc.want)
		}
	}
}

// TestAnUnknownTotalIsNotARatio covers the other half of Update's contract.
//
// Nine listings passed out.Count() as both arguments, so the ratio read "12 /
// 12" from the first report onward — at the moment --limit had just cut the
// result short and exit 3 was about to say otherwise. A total is what the
// server had; zero is how a command says it does not know.
func TestAnUnknownTotalIsNotARatio(t *testing.T) {
	var out strings.Builder
	newTTYProgress(&out, "users").Update(12, 0)

	got := out.String()
	if !strings.Contains(got, "12 users,") {
		t.Errorf("an unknown total did not render a bare count: %q", got)
	}
	if strings.Contains(got, "/") {
		t.Errorf("an unknown total was rendered as a ratio: %q", got)
	}
}

// TestAProgressLineAlwaysNamesSomething keeps the fallback honest. A reporter
// built with no noun is a caller that did not go through stream(), and a line
// reading "12 , 3s" would be worse than a wrong noun.
func TestAProgressLineAlwaysNamesSomething(t *testing.T) {
	var out strings.Builder
	newTTYProgress(&out, "").Update(12, 40)

	if !strings.Contains(out.String(), "12 / 40 rows,") {
		t.Errorf("a reporter with no noun produced %q", out.String())
	}
}
