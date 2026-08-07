//go:build !render

package render_test

import (
	"slices"
	"testing"

	"github.com/kmoneil/jira-cli/internal/render"
)

// TestMarkdownIsAbsentWithoutTheTag is the half of the pair that runs in every
// build that does not carry `render`, which is agent, reader, and ci.
//
// A build tag that gates a capability has to be checked from *both* sides. The
// tagged test proves the format works where it exists; without this one,
// nothing would notice if it leaked into a profile that is supposed not to have
// it — and the whole argument for tagging an unversioned format is that three
// of the four shipped profiles cannot emit it.
func TestMarkdownIsAbsentWithoutTheTag(t *testing.T) {
	if _, err := render.ParseFormat("markdown"); err == nil {
		t.Error("a build without the render tag accepted --format markdown")
	}
	if names := render.FormatNames(); slices.Contains(names, "markdown") {
		t.Errorf("markdown is advertised in a build that cannot write it: %v", names)
	}
}
