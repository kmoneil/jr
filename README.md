# jr

A Jira client whose output is a versioned contract.

Built for scripts and agents first, humans second. Every result carries a kind
and a schema version. A result set that was truncated is never reported as
complete. Any request that cannot be honored exactly fails instead of
approximating.

A clean-room rewrite of the ideas in `ankitpokhrel/jira-cli`, not a fork.

> **Status: early.** The output contract, the command registry, the build
> profiles, and the exit-code discipline are implemented and tested. No Jira
> transport yet — see [What works today](#what-works-today).

## Why

The incumbent is a TUI that grew a CLI, and every scriptability feature in it is
an escape hatch _out_ of the interactive layer. The consequences are structural:

- Cursor pagination bolted under an offset-shaped flag, so `--paginate 50:2`
  silently returns the same rows as `--paginate 0:2`.
- JQL built by string concatenation, so a user's `OR` escapes the project scope.
- Invalid dates falling through to literals instead of erroring.
- Non-TTY stdin segfaulting `edit`, `move`, and `delete`.
- `--debug` printing the `Authorization` header.

The common thread: **the tool prefers producing something over admitting it
can't.** Survivable for a human who notices the output looks wrong. Fatal for an
agent, which will confidently act on it.

## Install

```
git clone <this repo> && cd jira-cli
make hooks          # install the pre-commit gate, once per clone
make build          # → bin/jr
```

## What works today

```
jr context create|list|use|show|delete   # named site/project pairings
jr auth login|logout|status|token        # credentials, per site

jr version          # build identity and compiled-in capabilities
jr schema           # every command this build contains, as data
jr schema <name>    # one command in full: flags, args, exit codes, output kinds
jr contract         # every output kind this build can emit, and its version
```

Global: `--format tsv|xml|json|yaml`, `--context`, `--site`, `--project`,
`--readonly`, `--describe`, and `--limit` on collections. `JIRA_FORMAT`,
`JIRA_CONTEXT`, `JIRA_SITE`, `JIRA_PROJECT`, and `JIRA_READONLY` set the same
things from the environment.

```console
$ jr schema
name      summary                                                     mutating  destructive  kind
contract  Dump the machine-readable output contract for every kind    false     false        contract
schema    Describe every command this build contains                  false     false        schema.commands
version   Print the build identity and its compiled-in capabilities    false     false        version

$ jr schema --limit 1; echo "exit $?"
name      summary                                                   mutating  destructive  kind
contract  Dump the machine-readable output contract for every kind  false     false        contract
exit 3
```

That exit 3 is the point. The truncation warning went to stderr; stdout stayed
parseable.

### Not yet implemented

Nothing below is stubbed or partially wired — a flag that would silently no-op
is not shipped at all.

- All Jira resources: `issue`, `epic`, `sprint`, `board`, `project`, `user`,
  `field`, `meta`. Contexts and credentials are configurable, but nothing yet
  uses them to make a request.
- `adf` — the package exists with its contract documented and no
  implementation.
- OAuth, mTLS, and a system-keyring credential provider. The provider interface
  is in place; a keyring implementation shells out, so it will arrive behind its
  own build tag rather than in the reader profile.
- Deployment auto-detection and the metadata cache. `$XDG_CACHE_HOME/jr/<site>/`
  is resolved but nothing writes to it yet.
- `jr jql validate` and `jr jql explain`. The JQL library is complete (see
  below) but `validate` is specified as a round trip to Jira's parse endpoint,
  and shipping a local-only check under that name would overclaim.
- `--page-size`, `--page-token`, `--max-requests`, `--retries`, `--dry-run`,
  `--yes`, `--readonly`, `--no-color`, `--debug`.
- `jr mcp serve`, `jr ui`.
- `--contract` reports each kind's name, version, and emitters. Per-kind element
  schemas land with the resources that define them.
- The four build profiles produce identical binaries, because no tag currently
  gates any code. The machinery is tested; there is just nothing to exclude yet.

### Contexts and credentials

Contexts are kubectl-style, and a project is always a default rather than a
requirement:

```console
$ jr context create work --site acme.atlassian.net --project ENG
$ jr context create audit --site acme.atlassian.net --readonly
$ jr context list
name     current  site                        project  board  readonly
audit    false    https://acme.atlassian.net                  true
work     true     https://acme.atlassian.net  ENG             false

$ jr context show --project OPS      # what would this invocation actually use?
```

`--readonly` is a **one-way latch**. Any of the flag, `JIRA_READONLY`, or a
context created read-only turns it on, and nothing turns it off — a context
created for auditing is a statement about what it is for, and an invocation that
merely omits the flag must not quietly promote itself to read-write.

Credentials never touch the config file. `config.toml` is meant to be
hand-edited and kept in a dotfiles repository, so it holds a *reference*; the
secret lives under the state directory at mode 0600, and is refused on read if
it is readable by anyone else.

```console
$ printf '%s' "$TOKEN" | jr auth login --site acme.atlassian.net \
      --email ada@example.com --token-stdin
$ jr auth status
```

The token comes from stdin rather than a flag because an argument lands in the
shell history and the process list. Sources are tried environment, then the
store, then `.netrc` — the environment first so CI can override what is on disk
without editing it, `.netrc` last because it is shared with every other tool on
the machine.

`auth.Secret` has `String` and `Format` methods that print `REDACTED`, so a
`%+v` on a credential while debugging cannot leak one. `Reveal()` is the only
way out and it is greppable. `jr auth token` is the single place a secret is the
requested output.

### JQL

`internal/jql` is complete and is what every resource will build queries with.
It is a typed builder over an AST with a single renderer, and that renderer is
the only place in the project where a string is quoted.

```go
q := jql.New().
    Project("ENG").                  // project = "ENG"
    In("labels", labels...).         // values are data, never syntax
    Raw(userSuppliedJQL).            // always parenthesized
    OrderBy("updated", jql.Desc)
```

```console
project = "ENG" AND labels IN ("retry", "transport")
  AND (summary ~ "x" OR priority = Highest) ORDER BY updated DESC
```

The parentheses around the raw fragment are the difference between a filter and
a scope escape: without them the user's `OR` binds looser than the `AND` that
scoped the query, and it returns every `Highest` issue in every project the
caller can see.

Queries are inspected by tokenizing, never by regex — `summary ~ "project =
FOO"` does not constrain the project, and a regex says it does. Dates are
validated, so `--created 2020-13-45` is exit 2 with `month 13 is out of range`
rather than an empty result set that reads like "no matching issues".

Three fuzzers back it up: `make fuzz`. The value round-trip one found a real
bug during development — a byte that is not valid UTF-8 was being silently
replaced with U+FFFD, which would have queried for something other than what
the caller asked for. It is now a structured refusal.

### Transport

`internal/transport` is the only path to Jira, and the only package that
imports `net/http`. It owns retry, the request budget, and redaction.

**Redaction is structural, not a rule to follow.** A credential is replaced when
a trace event is *built*, inside the package, so it never reaches whatever is
formatting the output. A debug formatter cannot leak a token because it is never
given one. URLs count too: userinfo, credential-shaped query parameters, and the
URL inside a `*url.Error` are all scrubbed.

**A POST is not replayed after a 503.** The server may have processed it before
failing, and retrying is how one `issue create` becomes two issues. A 429 is
different — it is refused before processing — and so is a request whose caller
holds an idempotency key. That distinction is the difference between a retry
that is safe and one that silently duplicates work.

Retries count against `--max-requests`, because a retry is another request from
the server's side and a budget that ignored them would bound nothing. Exhausting
the retry budget exits 8 or 9, never 0.

Recorded fixtures replay against both Cloud and Data Center, and an unmatched
request is an error — a fixture test that fell through to the network would be
green in CI, where there are no credentials, while exercising nothing.

## Documentation

| Document                                           | Covers                                                                        |
| -------------------------------------------------- | ----------------------------------------------------------------------------- |
| [docs/output-contract.md](docs/output-contract.md) | Formats, envelope, truncation, escaping, exit codes, errors, stability policy |
| [docs/build-profiles.md](docs/build-profiles.md)   | Build tags, shipped profiles, compile-out enforcement                         |
| [docs/architecture.md](docs/architecture.md)       | Package layout, the dependency rule, testing gates                            |

Design spec: `_plans/design/jira-cli-v2-spec.md`.

## Build profiles

Features are selected at compile time. An excluded feature contributes zero
bytes and zero attack surface, and `jr schema` does not list it.

```
make build         # full     everything
make build-agent   # agent    no TTY, no interactivity, no browser, no clipboard
make build-reader  # reader   physically cannot mutate Jira
make build-ci      # ci       query only, smallest possible
```

## Development

```
make test           # the default build
make test-profiles  # the suite under every shipped tag set
make fuzz           # every fuzz target, FUZZTIME each (default 30s)
make golden         # rewrite the output-contract golden files
make lint fmt vet
make ci             # everything CI enforces
```

The golden files under `internal/render/testdata/` and `internal/cli/testdata/`
**are** the output contract. A diff in one is a change every consumer sees:
bump the schema version of the affected kind in the same commit.

## Open questions

Carried from the spec, still undecided:

1. **TSV vs XML default for lists.** TSV wins on token cost; consistency across
   commands may be worth the tokens. Decide by measuring a real 100-issue
   payload.
2. **Whether `pkg/jira` is a supported public library** or an internal detail.
3. **Write-side ADF.** Leaning toward a documented subset with loud rejection of
   the rest — loud rejection beats silent mangling.
