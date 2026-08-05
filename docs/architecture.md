# Architecture

```
cmd/jr/                    # main, process wiring only
internal/
  buildinfo/               # what this binary is and what it can do
  exitcode/                # the frozen exit code table
  errs/                    # the structured error every failure carries
  render/                  # xml | json | tsv | yaml writers, the output contract
  registry/                # command metadata → cobra, MCP, schema, docs
  cli/                     # the cobra tree, stdout/stderr split, exit codes
  transport/               # HTTP, retry, redaction, rate limit
  auth/                    # credential providers: keyring, netrc, OAuth, mTLS
  jctx/                    # named contexts, resolution, precedence
  jql/                     # AST, builder, renderer, tokenizer
  adf/                     # ADF ⇄ markdown, golden-tested
  resource/                # one isolated package per Jira resource
  workflow/                # operations spanning more than one resource
  tui/                     # build tag: tui
  mcp/                     # build tag: mcp
  lint/                    # architecture tests
pkg/jira/                  # public Go library, no CLI concepts
```

`internal/jctx` is the spec's `internal/context`, renamed so resource packages
can import it alongside the standard library's `context` without aliasing
either.

## The dependency rule

`resource/*` may import `transport`, `auth`, `jql`, `adf`, `render`, `errs`.

**Nothing may import `resource/*`** except `cmd`, `tui`, `mcp`, and `workflow`.

**Resources never import each other.** A cross-resource operation — adding an
issue to a sprint — lives in `workflow` or in the calling layer. This is what
keeps each resource independently compilable, which is what makes compile-out
work and what lets a new resource be added without touching an existing one.

Four more rules, all enforced in `internal/lint/importgraph_test.go` against the
real import graph from `go list`, so a build-tagged file cannot hide an import:

- **Only `render` encodes output.** Nothing else imports `encoding/xml`,
  `encoding/csv`, or a YAML package. The output contract is reviewable only
  while it lives in one place.
- **Only `transport` speaks HTTP.** Nothing else imports `net/http`, so header
  redaction cannot be bypassed by a package that builds its own client.
- **Foundation packages stay leaves.** `exitcode`, `errs`, `render`,
  `buildinfo`, `jql`, and `adf` never reach up into `cli`, `resource`, `tui`,
  `mcp`, or `workflow`.
- **`pkg/jira` has no CLI concepts.** No flags, no exit codes, no output
  formats, no cobra.

## One registration, three surfaces

`internal/registry` holds the single description of each command: path, summary,
flags, args, output kinds, exit codes, required tags, and the function that runs
it. From that one declaration come the cobra tree, `jr schema`, and the MCP tool
list. They cannot drift because there is only one of them.

A command never writes to stdout. It returns a `render.Doc` — a
format-independent tree — and `internal/cli` decides the format and writes it.
That is what lets every command support all four formats without knowing any of
them exist.

A command that returns a kind it did not declare is rejected before anything is
written, because a consumer dispatching on the declared kind would silently
mis-parse it.

## Recorded fixtures

`internal/transport` provides the record/replay mechanism every resource tests
against. A resource ships a cassette per deployment under its own `testdata/`:

```
internal/resource/issue/testdata/list.cloud.json
internal/resource/issue/testdata/list.datacenter.json
```

Both are required. Cloud and Data Center differ in API version, body format,
and pagination shape, so a fixture recorded against one proves nothing about
the other — and a resource that ships only the Cloud recording has tested half
of what it claims to.

Three properties make a fixture-backed test trustworthy:

- **An unmatched request is an error.** Falling through to the network would be
  green in CI, where there are no credentials, while exercising nothing.
- **Matching ignores headers.** Matching on `User-Agent` or `X-Request-Id`
  would invalidate every cassette on every release, or on every run.
- **Query and JSON field order are canonicalized.** A change in the order a
  caller builds parameters changes nothing about the request, and should not
  break a fixture.

`Replayer.Unplayed` reports interactions a test never triggered, which is
usually a test that stopped covering what it claims to.

Credentials are redacted as an interaction is recorded, not as it is written, so
a token cannot reach a fixture file even if recording is interrupted.

## Config, state, cache

| Path                              | Contents                                              |
| --------------------------------- | ----------------------------------------------------- |
| `$XDG_CONFIG_HOME/jr/config.toml` | Contexts, defaults. Hand-editable.                    |
| `$XDG_STATE_HOME/jr/`             | Idempotency ledger, view history, last cursor         |
| `$XDG_CACHE_HOME/jr/<site>/`      | Field IDs, issue types, transitions, deployment probe |

Metadata caching is a feature, not an optimization: resolving a custom field
name to `customfield_10042` should not cost a round trip on every invocation.
`--refresh` busts it, the TTL defaults to 24h, and `jr field list` warms it.

## Testing

| Layer           | Method                                                         | Gate                                                |
| --------------- | -------------------------------------------------------------- | --------------------------------------------------- |
| `jql/`          | Table-driven, plus a fuzzer asserting no input escapes quoting | 100% of renderer branches                           |
| `adf/`          | Golden files, round-trip property test                         | Corpus of ≥200 real documents                       |
| `resource/*`    | Pure struct-in/struct-out unit tests                           | 90%                                                 |
| `transport/`    | Recorded fixtures, Cloud + DC                                  | Every endpoint                                      |
| Output contract | Golden files per kind, per format                              | Any diff requires a version bump in the same commit |
| CLI surface     | Snapshot tests of `--help`, `schema`, exit codes               | Any diff is reviewed                                |
| Architecture    | Import-graph assertions                                        | Every PR                                            |
| Build profiles  | Matrix build of all profiles plus a size assertion             | Every PR                                            |
| Fuzzing         | `make fuzz`, every target                                      | Every PR, 60s per target                            |

Recorded HTTP contract tests are mandatory, not optional. Pure-function unit
tests would not have caught any of the incumbent bugs this project exists to
avoid — all of them live at the seam between the CLI and a real Jira.

## Keeping this current

Update this document in the same change that alters any of:

- The set of packages under `internal/` or `pkg/`.
- What may import a resource, or what a resource may import.
- A responsibility moving between packages.
- The config, state, or cache paths.
- The testing gates table.
