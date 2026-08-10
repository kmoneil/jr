//go:build !write

package cli

import "github.com/kmoneil/jira-cli/internal/registry"

// writeAppliedSoFar is the reader build's half of the pair. Nothing here can
// mutate Jira, so no command can apply half of anything, and the type the write
// build matches on does not exist in this one.
func (a *app) writeAppliedSoFar(
	*registry.Command, *registry.Invocation, error,
) (bool, error) {
	return false, nil
}
