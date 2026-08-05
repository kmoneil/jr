// Package resource is the parent of the per-resource packages.
//
// Each Jira resource is an isolated directory beneath this one, holding its
// types, its client, its command declarations, and its recorded fixtures.
// Resources share transport, auth, jql, and adf, and know nothing about each
// other. Adding a resource means adding a directory and registering it — no
// edits to existing resources.
//
// Nothing may import a resource package except cmd, tui, mcp, and workflow.
// The rule is enforced by the import-graph test in internal/lint.
package resource
