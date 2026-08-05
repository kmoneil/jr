// Package adf converts between Atlassian Document Format and markdown.
//
// Read-side conversion is golden-tested against a corpus of real documents.
// Write-side conversion covers a documented subset and rejects the rest loudly:
// a loud rejection beats silent mangling, and this is where the incumbent's
// known issues live.
package adf
