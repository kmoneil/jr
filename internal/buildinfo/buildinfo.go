// Package buildinfo reports what this binary is and what it can do.
//
// The capability set is a compile-time constant. A feature excluded at build
// time contributes zero bytes and zero attack surface, and `jr schema` in a
// build without it does not list it — an agent introspecting the binary sees
// the truth, not a list of commands that will refuse. See docs/build-profiles.md.
package buildinfo

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
)

// Release, Commit, and Built are stamped by the linker. They are variables so
// tests can pin them and compare golden output.
//
// Release is always a semantic version — scripts/version.sh guarantees that in
// every case, including an untagged tree and a source tree with no git at all.
// The default here is what an unstamped build reports, and it says 0.0.0
// rather than a plausible number: a binary built outside the Makefile should
// not claim a release somebody might act on. `go build ./cmd/jr` produces one.
var (
	Release = "0.0.0-unknown"
	Commit  = "unknown"
	Built   = "unknown"
)

// GoVersion and Platform are variables for the same reason.
var (
	GoVersion = runtime.Version()
	Platform  = runtime.GOOS + "/" + runtime.GOARCH
)

// App is the binary name, used in output and in usage strings.
const App = "jr"

// KnownTags lists every build tag this project defines, in the order they are
// documented. A tag not in this list is not a capability.
var KnownTags = []string{
	"tui",
	"prompt",
	"render",
	"browser",
	"clipboard",
	"mcp",
	"write",
	"admin",
}

// TagDescriptions explains each tag for `jr schema` and `jr version`.
var TagDescriptions = map[string]string{
	"tui":       "Interactive terminal UI (jr ui)",
	"prompt":    "Interactive prompts and the setup wizard",
	"render":    "ADF to terminal markdown rendering",
	"browser":   "Opening URLs and the OAuth browser flow",
	"clipboard": "Copying keys and URLs to the system clipboard",
	"mcp":       "MCP server (jr mcp serve)",
	"write":     "All mutating commands",
	"admin":     "Project, board, and sprint administration",
}

// enabled is populated by the tag-gated files in this package, one per tag.
var enabled = map[string]bool{}

func enable(tag string) { enabled[tag] = true }

// A build with no tags compiles none of the tag_*.go files, so nothing would
// reference enable. This keeps it live in every configuration.
var _ = enable

// HasTag reports whether the capability was compiled into this binary.
func HasTag(tag string) bool { return enabled[tag] }

// Tags returns the enabled tags in KnownTags order.
func Tags() []string {
	out := make([]string, 0, len(enabled))
	for _, t := range KnownTags {
		if enabled[t] {
			out = append(out, t)
		}
	}
	return out
}

// TagList returns the enabled tags as a comma-separated string, or "none".
func TagList() string {
	t := Tags()
	if len(t) == 0 {
		return "none"
	}
	return strings.Join(t, ",")
}

// MissingTags returns the members of want that this binary was not built with.
func MissingTags(want []string) []string {
	var missing []string
	for _, t := range want {
		if !enabled[t] {
			missing = append(missing, t)
		}
	}
	return missing
}

// Profile names the shipped build profile this tag set corresponds to, or
// "custom" for any other combination.
func Profile() string {
	switch strings.Join(Tags(), ",") {
	case "tui,prompt,render,browser,clipboard,mcp,write,admin":
		return "full"
	case "mcp,write":
		return "agent"
	case "mcp":
		return "reader"
	case "":
		return "ci"
	default:
		return "custom"
	}
}

// CanWrite reports whether this binary contains any mutating command. A reader
// build cannot mutate Jira, which is a stronger guarantee than a runtime check.
func CanWrite() bool { return HasTag("write") }

// CanPrompt reports whether this binary can block on human input. A headless
// build has no code path that can, because the prompt package is not linked.
func CanPrompt() bool { return HasTag("prompt") }

// Display is the one-line version banner, e.g.
// "jr 1.2.0 (reader; tags=mcp)".
func Display() string {
	return fmt.Sprintf("%s %s (%s; tags=%s)", App, Release, Profile(), TagList())
}

// UnknownTags returns any enabled tag that is not in KnownTags. It exists so a
// test can catch a tag file that was added without documenting the tag.
func UnknownTags() []string {
	var out []string
	for t := range enabled {
		if !slices.Contains(KnownTags, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}
