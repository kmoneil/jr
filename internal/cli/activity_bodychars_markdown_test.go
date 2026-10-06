//go:build render

package cli_test

import (
	"strings"
	"testing"
)

// TestBodyCharsKeepsMarkdownOneTable is the layout half of issue 219. A body
// makes the markdown writer give every event a section of its own, because a
// document's length is unknown; fifty events became several hundred lines of
// repeated table headers. A bounded body is short by construction, so the feed
// stays one table and the body sits in its cell.
func TestBodyCharsKeepsMarkdownOneTable(t *testing.T) {
	url := activityJira(t)
	env := credentialed(t)
	mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

	full := mustRun(t, env, "issue", "activity", "--since", fixtureSince, "--format", "markdown")
	if !strings.Contains(full.stdout, "### body") {
		t.Fatalf("the unbounded feed is no longer sections, so this case shows nothing:\n%s", full.stdout)
	}

	got := mustRun(t, env, "issue", "activity", "--since", fixtureSince,
		"--body-chars", "40", "--format", "markdown")
	if strings.Contains(got.stdout, "### body") || strings.Contains(got.stdout, "## event") {
		t.Errorf("a bounded feed still rendered a section per event:\n%s", got.stdout)
	}
	for _, want := range []string{"| body | body-length |", "Here is the whole investigation.<br><br>"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the table is missing %q:\n%s", want, got.stdout)
		}
	}
}
