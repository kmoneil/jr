# Build profiles

Features are selected at **compile time** via build tags. An excluded feature
contributes zero bytes and zero attack surface.

This is stronger than a runtime check. The incumbent's `edit`, `move`, and
`delete` segfault on non-TTY stdin because a prompt was reachable in a context
with no human. Here that is not a bug to fix; it is a state that cannot be
constructed, because a headless binary has no prompt package linked. Compile-out
converts a class of runtime bugs into link errors.

## What decides authority, and what does not

**`jr` is a human's command-line tool, and its authority is exactly the
credential it holds.** There is no permission model above that: a personal
access token or a logged-in account can do whatever that account can do, and
every command in the binary can do it.

So if something other than you is going to run it, decide the authority at the
credential, and work down this list only as far as you have to.

### 1. Least privilege at the credential — always try this first

The only limit that survives everything, because it is enforced by Jira rather
than by anything on your machine. Nothing below is a substitute for it; each is
a weaker fallback for when it is genuinely unavailable.

- **Cloud:** an API token with scopes, or an OAuth app with only what it needs.
- **Data Center:** there are no scopes on a personal access token — it inherits
  every permission the account has — so least privilege means **a second Jira
  account** with the project permissions you intend, and a token minted from it.

If you find yourself deciding not to do this because it is inconvenient, the
rest of this section is what you are accepting instead, and it is weaker.

### 2. If you will not scope the credential: constrain the binary

A reader binary does not refuse to write; it does not contain the verbs. No
flag, context, or environment variable puts them back.

| You want                        | Use                                        |
| ------------------------------- | ------------------------------------------ |
| It cannot change Jira           | `jr-reader` or `jr-ci`                     |
| It cannot see your credential   | not the CLI — see 3                        |
| Different authority per project | not available; see `_plans` for the design |

**A profile subtracts Jira authority and no local authority.** All four builds
carry the same `context create/edit/delete/use` and `auth login/logout`, and all
four choose their paths from `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, and
`JIRA_CONFIG_FILE` — deliberately, because a binary that could not be configured
could not be used. So a `jr-ci` handed to an agent cannot change Jira, and can
still rewrite that agent's contexts, store a different credential, point itself
at another config file, and **print the credential with `auth token`**, which is
in every build and exists to reveal it. After that it does not need `jr` at all.

That `auth token` is in every build is a decision, taken 2026-08-13, and not an
oversight waiting on a tag. Gating it would close one door in a room with an
open wall: anything that can run `jr` as you can read the credential file
directly, so a build without the command is not a build that keeps the secret.
The deployment where it *does* matter is one where the caller cannot read that
file, and there the command is removed by how the binary is deployed rather than
by which tag it was compiled with.

The binary is therefore a ceiling on what reaches Jira *through that binary*. It
is not a ceiling on the credential, and the credential is what grants
everything — which is why step 3 below is not optional on a shared machine.

`--readonly`, `JIRA_READONLY`, and a context created `--readonly` are **not** in
that table on purpose. They are a latch within an invocation, not a boundary:
anything that can run `jr` twice can run `jr context edit <name> --unset
readonly` and then write, and that is by design, because a binary that could not
be configured could not be used. Use them against your own mistakes.

### 3. If the agent runs on the same machine, deny it explicitly

**An agent that can run `jr` holds your credential**, because the credential is
in the filesystem `jr` reads. Giving it a reader binary does not help if a full
one is on the path, and neither helps if it can read the store and use the token
directly. On a shared machine you have to deny it, in the agent's own sandbox or
permission rules:

- **Executing `jr`** — and any other Jira client, and `curl` or equivalent
  against your Jira host. Whatever mechanism your agent harness offers for
  denying a command, this is what it is for.
- **Writing `$XDG_CONFIG_HOME/jr/config.toml`** (`~/.config/jr/config.toml`).
  It is mode 0644 and is meant to be hand-edited and kept in a dotfiles
  repository — so an agent that can write it can repoint every later invocation
  at another site, or at another credential, without running anything.
- **Running `jr auth token`**, which prints the credential from any build. It is
  the same secret as the file below by a different door, so denying one without
  the other denies nothing.

**Allowlist, never blocklist.** A deny list of dangerous verbs is wrong the
moment a new one ships, and it is already wrong today: blocking `issue create`
leaves `issue clone --project X`, `issue move`, `issue link add`, and
`issue comment add`, all of which write. Permit what the agent needs by name and
refuse the rest. In Claude Code that is a permission rule in settings the agent
cannot edit, and the shape is:

```jsonc
// allow, by exact subcommand
"Bash(jr issue list:*)", "Bash(jr issue get:*)",
"Bash(jr issue history:*)", "Bash(jr issue activity:*)",
"Bash(jr issue comment list:*)",

// and refuse everything else that could reach Jira or the credential
"deny": ["Bash(jr:*)", "Bash(curl:*)", "Read(**/credentials.toml)"]
```

Add `Bash(jr issue comment add:*)` and you have an agent that may read and
comment and do nothing else — which is the "comments only" case, available
today, with no policy engine. The enforcement is the harness, evaluated before
the command runs, which is the property that matters: it is outside the
principal it constrains.

Two things this does not survive. An agent that writes a shell script and runs
*that* may slip a command matcher, depending on how the harness matches. And a
harness whose settings the agent can write is not an enforcement point at all —
check where those rules live before relying on them.
- **Reading `$XDG_STATE_HOME/jr/credentials.toml`**
  (`~/.local/state/jr/credentials.toml`). It is mode 0600 and refused on read if
  it is wider, which stops *other users* reading it and does nothing about a
  process running as you. Denying read is the control that matters; denying
  write only stops replacement.

If you cannot deny those, assume the agent has your full Jira authority,
because it does.

### 4. What the MCP server does and does not give you

`jr mcp serve` keeps the **administrative surface off the wire**: a peer cannot
call `auth_login`, `auth_logout`, `auth_token`, or any `context` writer, so it
cannot repoint the server, replace its credential, or read the credential out.
Each is refused by name with the reason. The reading siblings — `auth status`,
`context list`, `context show` — remain, because they report and reveal nothing.

That is a description of the interface, and it holds **only against a peer that
speaks nothing but MCP**. The server currently speaks stdio, which means its
client spawns it, with the same user and the same files — so today it is a
tidier interface rather than a boundary. A boundary needs a server the agent
connects to and does not start, running as a user whose credential it cannot
read. That work is carded and not built.

## Tags

| Tag      | Intends to gate                                   | Gates today                 |
| -------- | ------------------------------------------------- | --------------------------- |
| `write`  | All mutating commands                             | the 21 mutating verbs       |
| `mcp`    | `jr mcp serve`                                    | `jr mcp serve`              |
| `prompt` | Interactive prompts, the setup wizard, completion | `jr completion`. Also the no-echo token prompt inside `auth login`, which is in every build and only asks here |
| `admin`  | Project, board, and sprint administration         | `jr sprint close`           |
| `render` | Human-readable rendering for a terminal           | `--format markdown`         |

There were eight. `tui`, `browser`, and `clipboard` were dropped on 2026-08-13,
having gated nothing since they were declared: there was no `jr ui`, no OAuth
browser flow, and nothing that copies. They were honest about it in this table
and in `notYetGating`, and honesty in a footnote is not the same as accuracy in
the thing a reader sees. `jr version` named eight capabilities and the binary
had five, which is the failure this tool exists to refuse in the one place it
describes itself. Each one is a two-line file and a table row on the day
somebody builds the feature.

The right-hand column is not documentation that can drift. Two tests hold it,
because it makes two different claims.

`internal/lint/tags_test.go` asserts that a tag gates _code at all_: one gating
nothing must be listed in `notYetGating` with a reason, and one that starts
gating something fails the test until it is taken off that list. That list is
empty now, so every declared tag gates real code and any new one has to arrive
with either its feature or its excuse. A file that only records the tag is set,
or a package that is nothing but a doc comment, does not count as gating,
because that would report exactly the reassurance the audit exists to withhold.

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
make build         # full     prompt,render,mcp,write,admin
make build-agent   # agent    mcp,write
make build-reader  # reader   mcp
make build-ci      # ci       (none)
make build-all     # all four
```

| Profile  | Guarantee                                                                              |
| -------- | -------------------------------------------------------------------------------------- |
| `full`   | Everything. Assumes a human at a terminal.                                             |
| `agent`  | No TTY assumptions, no interactivity, no human-readable rendering. Cannot block on input. |
| `reader` | **Physically cannot mutate Jira.** The mutating commands are not in the binary.        |
| `ci`     | Query only, smallest possible.                                                         |

### What each is for, and what it does not do

**The intended pairing is one scoped credential per profile, not one credential
shared between them.** A profile is the second of two independent limits: the
token says what the account may do, and the binary says what this client can ask
for. Either alone leaves a single failure; together, two things have to be wrong
before something unintended reaches Jira. Reading the table below as "this is
how I constrain something holding my personal token" is the misuse it is
easiest to fall into, and step 2 above says why that does not hold.

| Profile | Use it for | Pair it with | It does **not** |
| --- | --- | --- | --- |
| `full` | A person at a terminal | Your own account | Constrain you in any way, and it is not meant to |
| `agent` | Unattended automation that must write | A token scoped to what that automation writes | Reduce authority at all — it holds everything `full` does, minus interactivity |
| `reader` | Anything that should only ever read: an agent, a dashboard, a report | A read-scoped token, or a read-only account | Stop the holder reading your credential, or repointing its own config |
| `ci` | A pipeline that queries and nothing else | A CI-specific token, rotated with the pipeline | Contain an MCP server — `mcp serve` is absent, which is the point |

Three limits apply to **all four** and are worth stating once:

- **Local authority is identical across profiles.** Contexts and credentials can
  be created, edited, deleted, and repointed from any of them, and
  `auth token` prints the credential from any of them.
- **A profile is only a control where you choose what is installed.** In an
  image you build, it is strong: the caller has no alternative binary. On a
  workstation where `jr` is also on the `PATH`, the caller picks.
- **`agent` is a portability profile, not a permission one.** It exists so a
  process with no terminal cannot hang waiting for one. It is not a smaller
  authority than `full` and must not be chosen as though it were.

`make size` asserts the reader build stays under 12 MB. It is a backstop against
something large arriving: a TUI toolkit, a display-server client, a browser
launcher, anything dragging in `os/exec` and a shell. None of those has a tag
waiting for it any more, which makes the budget the earlier of the two warnings
rather than the second. It is **not** a measure of what a profile excludes, and
it should not be read as one.

It is not a boundary either, and the distinction is not academic.
`golang.org/x/term` is imported with no build tag by `internal/cli/env.go` and
links into all four profiles, `ci` included. That is deliberate: `isTerminal` is
what keeps the progress line off a pipe, so it has to work in every build and
cannot sit behind the `prompt` tag beside `promptSecret`. The budget did not
notice it arrive and could not have, because it is a few kilobytes against four
megabytes of headroom.

So the rule is about what a dependency *does*, not what it weighs. Reading
whether a file descriptor is a terminal is permitted everywhere. Drawing on one
is not, and what enforces that is the build tags and the import graph rather
than a byte count. `internal/lint/notice_test.go` links every profile on three
platforms and fails when a module reaches a binary without appearing in
`NOTICE`, which is how `x/term` was found in the first place.

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
| `full`   | 66       | none                                  |
| `agent`  | 64       | `completion`, `sprint close`          |
| `reader` | 44       | the above, plus the 20 mutating verbs |
| `ci`     | 43       | the above, plus `mcp serve`           |

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
or `reader` user actually gets. `.golangci.yml` turns every tag on
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
