# Build profiles

Features are selected at **compile time** via build tags. An excluded feature
contributes zero bytes and zero attack surface.

This is stronger than a runtime check. The incumbent's `edit`, `move`, and
`delete` segfault on non-TTY stdin because a prompt was reachable in a context
with no human. Here that is not a bug to fix — it is a state that cannot be
constructed, because a headless binary has no prompt package linked. Compile-out
converts a class of runtime bugs into link errors.

## Tags

| Tag         | Includes                                                | Pulls in                |
| ----------- | ------------------------------------------------------- | ----------------------- |
| `tui`       | `jr ui`, interactive tables                             | `tview`, `tcell`        |
| `prompt`    | Interactive prompts, the setup wizard, shell completion | `survey`                |
| `render`    | ADF→terminal markdown rendering                         | `glamour`, `chroma`     |
| `browser`   | `jr open`, the OAuth browser flow                       | `pkg/browser`           |
| `clipboard` | Copying keys and URLs                                   | `xclip`/`xsel` shellout |
| `mcp`       | `jr mcp serve`                                          | MCP SDK                 |
| `write`     | All mutating commands                                   | —                       |
| `admin`     | Project, board, and sprint administration               | —                       |

The list lives in `internal/buildinfo.KnownTags`, with one `tag_<name>.go` file
per tag. A tag enabled without an entry there fails
`TestBuildDeclaresOnlyDocumentedTags`.

## Shipped profiles

```
make build         # full     tui,prompt,render,browser,clipboard,mcp,write,admin
make build-agent   # agent    mcp,write
make build-reader  # reader   mcp
make build-ci      # ci       (none)
make build-all     # all four
```

| Profile  | Guarantee                                                                              |
| -------- | -------------------------------------------------------------------------------------- |
| `full`   | Everything. Assumes a human at a terminal.                                             |
| `agent`  | No TTY assumptions, no interactivity, no browser, no clipboard. Cannot block on input. |
| `reader` | **Physically cannot mutate Jira.** The mutating commands are not in the binary.        |
| `ci`     | Query only, smallest possible.                                                         |

`make size` asserts the reader build stays under 12 MB and is the gate that
keeps a terminal, display-server, or `os/exec` dependency from creeping in.

## Enforcement

The capability set is a compile-time constant, and three things follow:

**`jr schema` tells the truth.** A reader build does not list `issue create`. An
agent introspecting the binary sees what is there, not a list of commands that
will refuse.

**`jr version` prints the tag set.**

```
$ jr version --format tsv | grep display
display	jr 0.1.0-dev (reader; tags=mcp)
```

**A command declares the tags it needs.** `internal/cli/contract_test.go`
iterates every registered command and asserts:

- Every tag it names is documented in `KnownTags`.
- Every tag it names is present in this build — a command registered in a build
  missing its tags means the registration is not gated correctly.
- Every mutating command requires `write`.
- In a build without `write`, no registered command is mutating.

`make test-profiles` runs the whole suite under each shipped tag set, so a
command that only compiles under one profile fails the others loudly.

## Registering a tag-gated command

Put the registration in a file carrying the tag:

```go
//go:build write

package issue

import "github.com/kmoneil/jira-cli/internal/registry"

func init() {
	registry.Register(createCommand())
}
```

A build without `write` does not compile the file, so the command is absent
rather than refusing at runtime. That is the whole mechanism.

## Runtime read-only mode

Compile-out is the strong guarantee. `JIRA_READONLY=1`, `--readonly`, and a
context marked read-only are the weaker one, for a build that does contain
mutating commands: every mutating command exits 10 before any network call.

Use both. A reader build cannot mutate; read-only mode means a full build in an
automated context cannot either.

## Keeping this current

Update this document in the same change that alters any of:

- The set of build tags, or what a tag gates.
- A shipped profile's tag set, or the addition of a profile.
- The reader size budget in the Makefile.
- What a profile is guaranteed not to contain.
- The way tag-gated commands register themselves.
