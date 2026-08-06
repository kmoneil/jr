// Package commands links every resource into the binary.
//
// A resource registers itself from an init function in a tag-gated file, so it
// is present only in a build that has the tags it needs. Something has to
// import it for that init to run, and this is that something — one place that
// names every resource, rather than a list scattered through cmd/.
//
// It is also what lets the contract tests see the full command surface: they
// import this package, and the assertions in internal/cli then run against
// every registered command rather than only the built-ins.
//
// Importing a resource is otherwise forbidden outside cmd, tui, mcp, and
// workflow. This package is the fifth exception, and internal/lint knows it.
package commands

import (
	// Each resource registers its commands from init.
	_ "github.com/kmoneil/jira-cli/internal/resource/field"
	_ "github.com/kmoneil/jira-cli/internal/resource/issue"
	_ "github.com/kmoneil/jira-cli/internal/resource/meta"
	_ "github.com/kmoneil/jira-cli/internal/resource/project"
	_ "github.com/kmoneil/jira-cli/internal/resource/user"
)
