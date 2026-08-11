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
	_ "github.com/kmoneil/jr/internal/resource/board"
	_ "github.com/kmoneil/jr/internal/resource/epic"
	_ "github.com/kmoneil/jr/internal/resource/field"
	_ "github.com/kmoneil/jr/internal/resource/issue"
	_ "github.com/kmoneil/jr/internal/resource/jql"
	_ "github.com/kmoneil/jr/internal/resource/meta"
	_ "github.com/kmoneil/jr/internal/resource/project"
	_ "github.com/kmoneil/jr/internal/resource/sprint"
	_ "github.com/kmoneil/jr/internal/resource/user"

	// The verbs that move issues between containers span two resources, so
	// they live in workflow rather than in either one.
	_ "github.com/kmoneil/jr/internal/workflow"
)
