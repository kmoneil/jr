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
jr version          # build identity and compiled-in capabilities
jr schema           # every command this build contains, as data
jr schema <name>    # one command in full: flags, args, exit codes, output kinds
jr contract         # every output kind this build can emit, and its version
```

Global: `--format tsv|xml|json|yaml`, `--describe`, `--limit` on collections.
`JIRA_FORMAT` sets the default format.

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
  `field`, `jql`, `meta`.
- `auth` and `context`. No credentials, no sites, no config file.
- `transport`, `jql`, `adf` — the packages exist with their contracts documented
  and no implementation.
- `--page-size`, `--page-token`, `--max-requests`, `--retries`, `--dry-run`,
  `--yes`, `--readonly`, `--no-color`, `--debug`.
- `jr mcp serve`, `jr ui`.
- `--contract` reports each kind's name, version, and emitters. Per-kind element
  schemas land with the resources that define them.
- The four build profiles produce identical binaries, because no tag currently
  gates any code. The machinery is tested; there is just nothing to exclude yet.

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
