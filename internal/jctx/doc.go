// Package jctx implements named contexts: one credential store, many sites.
//
// A context is {site, credential ref, default project, default board, default
// fields}. Project is never mandatory — it defaults from the context and can
// always be overridden or omitted. A command that genuinely needs a project and
// has none fails with exit 2 and names the flag.
//
// The package is jctx rather than context so that resource packages can import
// it alongside the standard library's context without aliasing either.
package jctx
