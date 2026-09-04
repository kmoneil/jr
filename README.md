<div align="center">

![jr](docs/assets/jr.png)

### A client for Jira whose output is a versioned contract

Built for scripts and agents first, humans second.
It would rather fail than hand you something that merely looks right.

![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-f5a623)](LICENSE)
[![Release](https://img.shields.io/github/v/release/kmoneil/jr?label=release&color=f5a623)](https://github.com/kmoneil/jr/releases/latest)
![MCP: built in](https://img.shields.io/badge/MCP-server%20built%20in-f5a623)
![Network in tests: none](https://img.shields.io/badge/tests-never%20touch%20the%20network-4c1)

**[Install](#install)** ·
[Getting started](docs/getting-started.md) ·
[Recipes](docs/recipes.md) ·
[Commands](docs/commands.md) ·
[Troubleshooting](docs/troubleshooting.md)

</div>

---

Every result is a document with a `kind`, a schema version, and a promise about
what it means. Pin it, parse it, act on it, and never squint at the output
wondering whether it is the whole answer.

The whole idea, in one command:

```console
$ jr issue list --limit 2; echo "exit $?"
key      status       assignee      updated               summary
ENG-101  In Progress  Ada Lovelace  2026-08-04T11:32:07Z  Retry logic drops...
ENG-102  To Do                      2026-08-04T09:00:00Z  Tabs and newlines...
exit 3
```

Two rows came back and there were more, so the exit is 3 and the warning went
to stderr carrying a token to resume from. stdout stayed exactly as parseable
as it was. No script downstream will ever mistake that page for the whole result
set, and the same is true of every command, in every format, on every path.

> **Status: released, and pinnable.** The command surface is complete and
> tested: 67 commands in the full build, 45 in the reader. Every output kind
> also carries its own schema version, and those move independently of the
> release, so a consumer pins `kind` and `v` from the document it parses rather
> than the version it installed. See
> [Not yet implemented](#not-yet-implemented) for what is knowingly absent.

## Install

```console
$ brew install kmoneil/tap/jr
```

macOS and Linux, both architectures. That is the whole step, and it brings the
shell completions with it.

<details>
<summary>Other ways: release archives, verifying a download, building from source</summary>

Shortest path on both macOS and Linux, and on macOS it is also the one that
never meets Gatekeeper: Homebrew fetches with `curl`, which does not attach
`com.apple.quarantine`, and an unsigned binary without that attribute is not
refused. It installs the full profile and the shell completions.

Every release also carries four profiles for linux and darwin, on amd64 and
arm64. `jr-full` is everything; the others are in
[build profiles](#build-profiles), and the one you want for an agent is probably
`jr-reader`, which cannot change anything in Jira because it does not contain the
code that could. Those are tarballs rather than formulae, because the machine
running an agent is usually a container:

```console
$ gh release download --repo kmoneil/jr --pattern 'jr-full_*_darwin_arm64.tar.gz'
$ tar xzf jr-full_*_darwin_arm64.tar.gz
$ install jr-full_*/jr ~/.local/bin/jr
```

Each release also has a `checksums.txt` over every archive, and a build
provenance attestation, so an archive can be traced to the workflow run and the
commit that produced it:

```console
$ gh attestation verify jr-full_*_darwin_arm64.tar.gz --repo kmoneil/jr
```

`gh` is in that download line for a second reason. macOS attaches
`com.apple.quarantine` to anything a browser downloads, and Gatekeeper kills a
quarantined executable that is not signed with an Apple Developer ID and
notarized by Apple, which these are not: they are cross-compiled on Linux
runners that hold no signing identity. Fetched with `gh` or `curl` the attribute
is never set and nothing refuses. If you already have a browser copy that will
not run, see [it will not start](docs/troubleshooting.md#it-will-not-start).

Or build from source, which needs Go 1.26:

```
git clone https://github.com/kmoneil/jr && cd jr
make build          # → bin/jr
make hooks          # install the pre-commit gate; contributors only, once per clone
```

Then see the [quickstart](#quickstart) above, or
[docs/getting-started.md](docs/getting-started.md) for the walkthrough.

</details>

## Your first query

One login and three commands. The longest part is creating the token.

**Jira Cloud.** Create an API token at
<https://id.atlassian.com/manage-profile/security/api-tokens>, then:

```console
$ jr auth login --site your-company.atlassian.net --email you@company.com
API token for your-company.atlassian.net:
```

**Jira Data Center or Server.** Create a personal access token from your
profile menu. It stands alone, so there is no email to pair with it:

```console
$ jr auth login --site jira.company.com
API token for jira.company.com:
```

`jr` prompts with echo off. A token is never taken as a flag value, because an
argument lands in your shell history and in the process list. `auth login`
checks the credential against the site before storing it, so a bad token fails
here rather than three commands later.

Then you are querying:

```console
$ jr user me                                   # proves the credential works
$ jr context list                              # auth login made one, named for the site
$ jr context edit your-company --project ENG   # stop typing --project
$ jr issue list --assignee currentUser
```

Stuck on any of that? [getting-started.md](docs/getting-started.md) is the
same path with every branch spelled out, and
[troubleshooting.md](docs/troubleshooting.md) lists every failure by its error
code.

## Where to go next

| I want to... | Read |
| --- | --- |
| install it and run my first query | [getting-started.md](docs/getting-started.md) |
| do a specific task: search, bulk edit, export, sprints, CI | [recipes.md](docs/recipes.md) |
| fix an error I am seeing | [troubleshooting.md](docs/troubleshooting.md) |
| look up a command or a flag | [commands.md](docs/commands.md) |
| see what makes this different, with worked examples | [tour.md](docs/tour.md) |
| parse the output from a script | [output-contract.md](docs/output-contract.md) |
| point an agent at Jira | [MCP](#mcp) · [the agent skill](#the-agent-skill) |
| pick a build profile | [build-profiles.md](docs/build-profiles.md) |
| work on `jr` itself | [architecture.md](docs/architecture.md) · [invariants.md](docs/invariants.md) |

Design spec: `_plans/design/jira-cli-v2-spec.md`.

## Why this exists

**First, honestly: I built it for me.** I wanted my own Jira work to be
scriptable without babysitting it: to pipe a query into something else at two
in the morning and trust the rows, to point an agent at a board without
wondering what it would do next. And I wanted control over the tool itself:
which commands exist, what they refuse, what a build even contains, and what
happens on the day the answer is "I can't". That is a short list of wants, and
none of it was satisfied by adding flags to something else.

Everything below is what those two wants turn into when you take them
seriously, because the guarantee is the product, and a guarantee is not a
feature you can bolt on afterwards.

Picture the failure this is built against. A nightly script lists everything in
a project, gets fifty rows because that is where the API stopped, exits 0, and
files a tidy report. It does that for a month. Nothing errored, nothing was
obviously wrong, and the only way to catch it was to already suspect it. That
is not a bug you fix once. It is a property of a tool that would rather
produce something than admit it cannot.

Invert that and you get the whole design:

- **Truncation is never silent.** `complete="false"`, or exit 3, or both, in
  every format, streamed or buffered, with a token to resume from.
- **Nothing is guessed.** A date that will not parse, a field name that
  resolves to nothing, an assignee matching two people, a deployment the probe
  does not recognise: each is a refusal with a code and a remedy, never a
  plausible substitute.
- **stdout is data.** Warnings, progress, and errors are structured and go to
  stderr. A failing command writes nothing at all to stdout, with two exceptions
  the contract names: a write that half-happened, and the rows a streamed TSV
  collection had already sent when it failed.
- **Exit codes never change meaning.** New conditions get new numbers, and the
  table is frozen by test.
- **Credentials stay out of the places they leak from.** Never a flag value,
  never the config file, never a log line, never a `%v`.

Each of those is worth nothing in isolation. "Never reports a truncated result
as complete" only means something when it is true of every command, all four
formats, both the streaming and the buffered path, and whatever gets added next
year. That is not a patch, and it is not ten patches. It is a property of how
the thing is put together: one registry every command is declared in, one
package allowed to encode output, one allowed to speak HTTP, one place a JQL
string can be quoted, and a test suite whose only job is to fail the day any of
that stops being true.

**This is not a complaint about the other Jira CLIs.** They are built for a
person at a terminal, they are good at it, and their users are happy. Keeping
the promises above means refusing things that work perfectly well for that
audience, such as an offset-shaped pagination flag or a partial result that
exits zero. Arriving in somebody else's project to take those away would be a
poor way to treat their users. Promising it on day one, in a tool nobody
depends on yet, is just the contract.

So: different audience, different bargain. Want a rich interactive Jira
experience? Use those. Writing a script, or pointing an agent at Jira, and need
to know the output means what it says? That is this.

The TUI, when it arrives, will be a consumer of this tool rather than the
product, which is the same idea from the other end.

## What works today

Everything below is built, tested, and asserted by the suite: 67 commands in
the full build, 45 in the reader.

```
jr auth      login logout status token
jr context   create edit list use show delete
jr issue     list get create edit move assign delete clone watch
jr issue     history activity changes
jr issue     comment list add edit delete
jr issue     link list add remove | worklog list add delete
jr issue     attachment list download upload
jr project   list get components versions statuses
jr user      list get me
jr board     list get
jr sprint    list get create add start close
jr epic      list get add remove
jr jql       validate explain
jr field     list
jr meta      transitions createmeta
jr mcp       serve
jr version | schema | contract | skill | doctor | completion
```

Global: `--format tsv|xml|json|yaml`, plus `markdown` in a build with the
`render` tag, for reading and never for parsing. Also `--context`, `--site`,
`--project`, `--board`, `--readonly`, `--describe`, `--debug`, `--refresh`,
`--retries`, `--max-requests`, `--api-version`, `--ca-bundle`, and `--limit` on
collections. `JIRA_FORMAT`, `JIRA_CONTEXT`, `JIRA_SITE`, `JIRA_PROJECT`,
`JIRA_BOARD`, `JIRA_READONLY`, and `JIRA_CA_BUNDLE` set the same things from the
environment.

```console
$ jr schema --limit 3; echo "exit $?"
name         summary                                                            mutating  destructive  kind
auth.login   Store a credential for a site                                      false     false        auth.status
auth.logout  Remove a stored credential                                         false     true         auth.status
auth.status  Report which credential a site would use, and where it comes from  false     false        auth.status
exit 3
```

That exit 3 is the point. The truncation warning went to stderr; stdout stayed
parseable.

### Not yet implemented

Nothing below is stubbed or partially wired. A flag that would silently no-op is
not shipped at all.

- `jr ui`. The TUI is a consumer of this tool, not the product, so it is the
  lowest priority there is.
- OAuth, mTLS, and a system-keyring credential provider. The provider interface
  is in place; a keyring implementation shells out, so it will arrive behind its
  own build tag rather than in the reader profile.
- `--no-color`. Nothing emits ANSI, so it would be a flag that does nothing.

The `tui`, `browser`, and `clipboard` tags used to be declared for the first
three, and were dropped on 2026-08-13 having never gated anything: they came
from the spec's tag table, written before any code was, and no build ever
carried a feature behind them. A tag that names a capability no build can
perform is the one thing this tool promises not to do, and each of them is a
two-line file on the day somebody needs it.

Everything else described in this README is built. 67 commands in the full
build, and `internal/lint` asserts that number against the binaries rather than
against this sentence. Every tag the build declares now gates real code, and
`internal/lint/tags_test.go` fails the day one stops.

## A short tour

What each promise looks like in practice, and the failure it exists to prevent:
reading an issue, streaming, fields, pagination, contexts and credentials, JQL,
and the transport. It is a page of its own so that this one stays short.

**[Read the tour →](docs/tour.md)**

## MCP

```
jr mcp serve      # speaks the Model Context Protocol on stdin/stdout
```

Every command in the build becomes a tool, generated from the same registry that
builds the command tree and `jr schema`, so a tool cannot drift from the command
behind it, and adding a command adds a tool for free.

It is also the truth about the binary. A reader build advertises no mutating
tools because it does not contain any; an agent introspecting the server sees
what is there rather than a list of tools that will refuse.

A tool call returns the same output the command would print, in the same
formats, with the same defaults. A failure returns the same structured error, so
one error contract holds whichever way you called: machine-stable code, remedy,
and whether retrying can help.

There is no exit code in a tool reply, so a truncated result carries its warning
in the content instead. It is never reported as complete.

```console
$ jr mcp serve <<< '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"auth_status", ...
```

The protocol is spoken directly rather than through an SDK: what is needed is
JSON-RPC 2.0 over stdio with three methods, and the profiles this ships in are
the ones meant to carry the least. The wire format is asserted by test.

## The agent skill

```
jr skill                  # the instructions an agent needs, as Markdown
jr skill workflows        # one reference: workflows, failures, or gotchas
```

`jr schema` says what exists. It cannot say which of five near-synonymous
filters answers the question that was asked, what to do with exit 3, or that a
refusal is information rather than an obstacle to route around. That last one is
why this ships at all: a model's default is to find a way past a blocker, and
`UNCONSTRAINED_QUERY` "solved" with `--jql 'project is not empty'` is the
whole-instance sweep the refusal exists to prevent.

**It describes the binary that printed it.** The command inventory is generated
from that build's registry, so a reader build's skill lists no mutating commands
because a reader build holds none:

```console
$ bin/jr-reader skill | grep 'commands, profile'
45 commands, profile `reader`, tags `mcp`.
```

Install it by symlinking the copy in this repository, which `make skill`
regenerates and a test refuses to let go stale:

```console
$ ln -s "$PWD/skills/jr" ~/.claude/skills/jr
```

Or generate it from the binary, for a host that reads a skill directory, an
`AGENTS.md`, or anything else that takes Markdown. The full instructions, both
ways, are in [recipes.md](docs/recipes.md#installing-the-skill).

It is in every profile including `ci`, because the build that most needs to
explain itself is the smallest one.

## Build profiles

Features are selected at compile time, so an excluded one contributes zero
bytes and zero attack surface, and `jr schema` does not list it. A reader
build does not refuse to write; it does not contain the code that could.

| Build               | Profile | What you get                                    |
| ------------------- | ------- | ----------------------------------------------- |
| `make build`        | full    | Everything, including the human-facing extras   |
| `make build-agent`  | agent   | No TTY, no interactivity, cannot block on input |
| `make build-reader` | reader  | Physically cannot mutate Jira                   |
| `make build-ci`     | ci      | Query only, smallest possible                   |

## Development

Commits follow Conventional Commits (`feat(issue):`, `fix(transport):`); the
`commit-msg` hook checks the header shape. Cutting a release is
[docs/releasing.md](docs/releasing.md).

```
make test           # the default build
make test-profiles  # the suite under every shipped tag set
make fuzz           # every fuzz target, FUZZTIME each (default 60s)
make golden         # rewrite the output-contract golden files
make docs           # regenerate docs/commands.md from the registry
make dc-up          # a licensed local Jira Data Center to record against
make dc-record      # re-record every Data Center cassette against it
make dc-down        # destroy it, its database, and its licence
make cost           # what each format costs, in tokens (needs uv, and network)
make vuln           # govulncheck over every profile's code (needs network)
make lint fmt vet
make ci             # everything CI enforces
```

The golden files under `internal/render/testdata/` and `internal/cli/testdata/`
**are** the output contract. Every kind's shape is pinned once per version in
`internal/cli/testdata/kinds/`, and each shipped profile has its own recorded
set beside it. A diff in one is a change every consumer sees: bump the schema
version of the affected kind in the same commit, which `make golden` will insist
on rather than quietly rewrite.

One thing is deliberately undecided: whether `pkg/jira` becomes a supported
library or stays an internal detail. It is an empty package today and the import
lint keeps it CLI-free either way, so do not import it yet. The output contract
is the interface, and it is the one with a version on it.

## Security

[SECURITY.md](SECURITY.md) is the threat model, what is impossible by
construction rather than by care, and how to report a vulnerability: privately,
to kevin@oneil.xyz, with an acknowledgement inside three working days.

Every claim in it names the test that holds it, and a lint fails the build when
a cited test stops existing. It is also honest about the other direction: there
is a section on what a hostile Jira can still do to you, because a threat model
that only lists wins is not one.

## License

[Apache License 2.0](LICENSE). Use it, fork it, ship it inside something you
sell; the licence text is the whole of what is asked. Copyright 2026 Kevin
O'Neil, because a licence is a grant from somebody, and the notice is who.

Nothing is vendored, and [NOTICE](NOTICE) lists the five direct dependencies a
binary links, with their licences and the one upstream NOTICE that has to travel
with them.

## Trademarks

Jira and Atlassian are trademarks of Atlassian Pty Ltd. `jr` is an independent
third-party client, not affiliated with, sponsored by, or endorsed by Atlassian.

The same statement is in [NOTICE](NOTICE), which travels inside every release
archive, so it reaches somebody holding only the tarball.

## How this was built

Built with AI assistance. I directed, reviewed, and accepted every design
decision in it, and the choices are mine. Noted for transparency, not as a
selling point or an excuse. The warranty and liability terms are the Apache-2.0
ones in [LICENSE](LICENSE); nothing here modifies them.

- **Claims about Jira are measured, not remembered.** Five bugs shipped and were
  found by pointing a real build at a real instance, not by any test.
  `meta createmeta` called an endpoint removed in Jira 9.0. `jql validate` sent
  `validateQuery=strict` where Data Center takes a boolean. `project list` never
  populated its lead column, because neither deployment expands the lead unless
  asked. `issue attachment download` required an `id` a real Data Center does
  not send, so it had failed on every Data Center since the verb shipped, and
  failed as retryable, inviting the caller to try again against a response that
  will never change. In each case a hand-written fixture encoded the assumption
  rather than the API, the last of them by carrying a field the server omits,
  and passed happily for months. The fifth had no fixture to be wrong: Jira Data
  Center 11 disables HTTP Basic by default, and the 403 that produces was
  reported as a permission problem, at the exit for one, with a remedy pointing
  at project permissions. The ADF converter is the same lesson applied ahead of
  time: its corpus is 247 documents Jira Cloud actually stored plus 23 it
  refused, and the round-trip fuzzer over them keeps finding bugs no
  hand-written case would have. Every input it has found is an `f.Add` seed in
  the test source with the defect written beside it, so the list is readable
  rather than a number in this sentence that was true once.
- **Where the evidence does not exist, the repository says so, and then goes
  and gets it.** Every Data Center fixture here was constructed until August
  2026, because the only Data Center available was production and recording
  against it is refused. They are recordings now, taken from a local Jira
  Software Data Center: `scripts/dc` stands one up under the three-hour timebomb
  licence Atlassian publishes for running a Data Center product without the SDK,
  fetched at run time and never committed, and `make dc-up dc-record` remakes
  all of them: the reads, the transport contract, thirteen write verbs, and a
  second pass under a context path, which no fixture had carried and in which
  three defects had been argued from documentation and fixed unobserved. Each
  cassette still carries whether it was recorded or written, the ledger of
  unrecorded deployments is empty, and a recording no manifest says how to
  remake fails the build. A constructed fixture proves a response is handled. It
  can never prove a request was accepted.
- **A documented number nothing checks is a number that was true once.** The
  build-profile table is asserted by building each profile and counting what the
  binary reports. Its four counts were four releases stale before that test
  existed. The error-code table is asserted by resolving each call site to the
  exit it actually produces, which found a code documented at exit 1 and built
  at exit 9, quietly advertising a refusal as retryable.
- **A declaration nothing exercises is a claim, not a behaviour.** Every gate
  here read the registry, and a flag can be typed, enumerated, given an exit
  code, rendered into the reference page and classified in a test's exemption
  list while doing nothing at all. `--order` was: it was harvested, passed
  along, and dropped by a branch that only looked at `--sort`, so
  `issue list --order asc` came back descending and said nothing. Flags are now
  driven: each one, on each command, on both deployments, with it and without
  it, against a transport that records what was asked. The requests, the
  columns, the document, or the error has to differ. That found `--body-format`
  dead on `issue create` and `issue edit` on its first run, where the
  documentation had been right and the wiring had never existed. The examples
  `--help` publishes are re-parsed by the binary for the same reason: one of
  them shipped with its arguments in an order the command does not take.
- **A gate that runs and cannot fail is worse than none**, because it reads as
  coverage. The dead-code pass added to catch symbols compiled into profiles
  that never use them first shipped as a fake: its build-tags flag did not
  override the config, so it loaded the same eight tags and reported clean. It
  was caught by reinstating the two symbols it was written for and watching the
  new gate pass. Reverting to red is the only thing that separates a test from a
  decoration.
- **The suite never touches the network.** Every host in a test uses a reserved
  TLD, enforced by a lint, after `auth login` grew credential verification and
  the suite began sending test tokens to a plausible-looking domain that turned
  out to exist. Nothing in the tests had changed; a behaviour change had turned
  an inert string into a real request.

None of that makes the code correct. It makes the claims checkable, which is the
part you cannot verify by reading a diff, and it is the standard this project
should be held to no matter who or what typed it.

---

<div align="center">

**[Getting started](docs/getting-started.md)** ·
[Recipes](docs/recipes.md) ·
[Commands](docs/commands.md) ·
[Output contract](docs/output-contract.md) ·
[Troubleshooting](docs/troubleshooting.md) ·
[Security](SECURITY.md)

Apache 2.0 · Built by Kevin O'Neil

_It won’t fake results just to look good._

</div>
