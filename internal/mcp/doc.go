//go:build mcp

// Package mcp serves the command registry over the Model Context Protocol.
//
// Every command becomes a tool whose schema is derived from the same metadata
// that builds the command tree and `jr schema`, so adding a command adds a tool
// for free and the two cannot drift.
package mcp
