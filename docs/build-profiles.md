# Build profiles

Features are selected at **compile time** via build tags. An excluded feature
contributes zero bytes and zero attack surface.

This is stronger than a runtime check. The incumbent's `edit`, `move`, and
`delete` segfault on non-TTY stdin because a prompt was reachable in a context
with no human. Here that is not a bug to fix; it is a state that cannot be
constructed, because a headless binary has no prompt package linked. Compile-out
converts a class of runtime bugs into link errors.

## Tags

| Tag         | Intends to gate                                   | Gates today                 |
| ----------- | ------------------------------------------------- | --------------------------- |
| `write`     | All mutating commands                             | the 21 mutating verbs       |
| `mcp`       | `jr mcp serve`                                    | `jr mcp serve`              |
| `prompt`    | Interactive prompts, the setup wizard, completion | `jr completion`             |
| `admin`     | Project, board, and sprint administration         | `jr sprint close`           |
| `tui`       | `jr ui`, interactive tables                       | **nothing**, no `jr ui` yet |
| `render`    | Human-readable rendering for a terminal           | `--format markdown`         |
| `browser`   | `jr open`, the OAuth browser flow                 | **nothing**, no OAuth yet   |
| `clipboard` | Copying keys and URLs                             | **nothing**, nothing copies |

The right-hand column is not documentation that can drift. Two tests hold it,
because it makes two different claims.

`internal/lint/tags_test.go` asserts that a tag gates _code at all_: one gating
nothing must be listed in `notYetGating` with a reason, and one that starts
gating something fails the test until it is taken off that list. A file that
only records the tag is set, or a package that is nothing but a doc comment,
does not count as gating, because that would report exactly the reassurance the audit
exists to withhold.

`internal/lint/profiles_test.go` asserts what each cell actually says, by
building the full profile without each tag in turn and comparing what
disappears against the cell. That test exists because this one said 18 mutating
verbs while the binary held 19: the first check was passing the whole time,
because "does `write` gate anything" and "does `write` gate what this row
claims" are not the same question. A cell may name commands, count them, or say
**nothing**; anything else fails as unreadable rather than passing unchecked.

### Tags that combine

`jr sprint close` is behind `write && admin`, and it is the only thing so far
that needs two. Write, because it changes Jira; admin, because of what it
changes: closing a sprint ends an iteration for a whole board and returns
every unfinished issue to the backlog. The agent profile has `write` and not
`admin`, so it can edit an issue and cannot end an iteration.

That combination broke the audit before it worked. `filesPerTag` turned each tag
on by itself and compared against a build with none, so a file needing two tags
was added by neither and both looked emptier than they were. It now compares a
full build against a full build minus one tag, which attributes such a file to
every tag its constraint names. There is a second assertion alongside it:
anything `admin` gates must also be gated by `write`, because administering a
board is a mutation and a build without `write` must not contain one.

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

`make size` asserts the reader build stays under 12 MB. It guards against a
terminal, display-server, or `os/exec` dependency creeping in. It is **not** a
measure of what a profile excludes, and it should not be read as one.

Binary size is a poor proxy for compile-out. `full` and `agent` are near enough
the same number of bytes despite differing by two commands, because the excluded
code is smaller than the linker's section alignment absorbs. The guarantee that
matters is the command surface, and that does differ.

The counts below are asserted, not maintained. `internal/lint/profiles_test.go`
builds each profile, runs `jr schema` against it, and fails if this table
disagrees; it reads the tag sets from the Makefile so it has no second copy of
them to go stale. It exists because these numbers were four releases out of date
26, 25, 18, 17 against a real 54, 52, 35, 34, which is what a number in a
document that nothing checks eventually becomes.

| Profile  | Commands | Not present                           |
| -------- | -------- | ------------------------------------- |
| `full`   | 63       | none                                  |
| `agent`  | 61       | `completion`, `sprint close`          |
| `reader` | 41       | the above, plus the 20 mutating verbs |
| `ci`     | 40       | the above, plus `mcp serve`           |

`make test-profiles` runs the whole suite under every shipped tag set, and the
contract tests inside it assert the surface directly: no mutating command
survives in a build without `write`.

`jr skill` is in all four on purpose, and is worth stating because the reflex
would be to gate it behind `mcp`. It prints the agent skill: the instructions a
caller needs that the declarations cannot carry, with a command inventory
generated from the registry of the binary that printed it. The build that most
needs to explain itself is the smallest one, and a skill that shipped only with
`mcp` would be absent from `ci`, which is the profile an unattended job runs.
Because it is generated per build, a reader binary's skill lists no mutating
commands rather than describing commands it does not contain.

## Enforcement

The capability set is a compile-time constant, and these things follow:

**`jr schema` tells the truth.** A reader build does not list `issue create`. An
agent introspecting the binary sees what is there, not a list of commands that
will refuse.

**A tag gates real code.** `jr mcp serve` exists only under the `mcp` tag: the
`ci` profile's binary is 65KB smaller and `jr schema` does not list the command,
because it is not there.

**`jr version` prints the tag set.**

```
$ jr version --format tsv | grep display
display	jr 1.2.0 (reader; tags=mcp)
```

**A profile carries no code it cannot reach.** `make lint-untagged` runs
`staticcheck -checks=U1000 ./...` with no build tags, which is the build a `ci`
or `reader` user actually gets. `.golangci.yml` turns all eight tags on
deliberately: code behind a tag is still shipped code, and the cost is that
`unused` then analyses one build in which every file is present, so a symbol
reachable only from a `//go:build write` file looks used. `echoMode` sat in an
untagged file and was called from two write-tagged ones, so it compiled into
the reader and the ci binary and could never run there. Nothing in the tree
could see it: the linter that would have said so was the one configured not to.

**A command declares the tags it needs.** `internal/cli/contract_test.go`
iterates every registered command and asserts:

- Every tag it names is documented in `KnownTags`.
- Every tag it names is present in this build; a command registered in a build
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

import "github.com/kmoneil/jr/internal/registry"

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
- Which pass proves a profile contains no code it cannot reach, or the tool it
  runs. `internal/lint/vuln_test.go` asserts that `make lint-untagged` is
  wired into both `make lint` and the workflow, and that it uses staticcheck:
  a second `golangci-lint run` inherits the config's build tags and reports
  a clean answer it was never able to fail.
