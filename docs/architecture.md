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
  site/                    # deployment probe, per-site metadata cache, user directory
  idem/                    # the idempotency ledger: what a mutation already did
  commands/                # links every resource in, so their inits run
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

`resource/*` may import `registry`, `transport`, `auth`, `site`, `jql`, `adf`,
`render`, `errs`.

**Nothing may import `resource/*`** except `cmd`, `tui`, `mcp`, `workflow`, and
`internal/commands`, which exists only to blank-import resources so their init
functions run, and is what lets the contract tests see the full command surface.

**Resources never import each other.** A cross-resource operation, adding an
issue to a sprint, lives in `workflow` or in the calling layer. This is what
keeps each resource independently compilable, which is what makes compile-out
work and what lets a new resource be added without touching an existing one.

`workflow` holds `sprint add`, `epic add`, and `epic remove`, all behind the
`write` tag. Each moves issues into or out of a container, so each touches the
issue resource and the container's, and neither could live in either one. What
they need from `resource/issue` is `ParseKey`: a local copy would be another
reimplementation of the one function this project has an invariant about, and
the first one nobody would think to keep in step. `internal/lint` allows a
`write`-gated file in `workflow` for exactly this reason and nowhere else; a
mutation that does not span two resources belongs with the thing it mutates.

`sprint create`, `sprint start`, and `sprint close` are the near miss that shows
where the line is. All three change a sprint, none of them touches an issue, so
all three live in `resource/sprint` beside the reads — while `sprint add`, which
differs from them only in moving issues, cannot. The test is what a verb has to
import, not which noun it is spelled with.

That rule is why **Jira metadata lives in `site`, not in the resource that
lists it.** `issue list --field "Story Points"` has to resolve a name to
`customfield_10042`, and it cannot ask the field resource to do it; `issue move`
will have to resolve a transition name to an id without asking the meta
resource. So `site` owns the fetching and the resolution for fields, issue
types, transitions, create metadata, and the user directory, and
`resource/field`, `resource/meta`, and `resource/user` are the command surfaces
over them.

`resource/user` is the clearest case: `issue assign ENG-1 "Ada Lovelace"` has to
resolve a display name to an accountId, and `jr user list` reports the same
value. Two definitions of what a user is would be two answers to that, so the
type and the deployment split, accountId against username, `query` against
`username`, live in `site` and the resource renders them.

A resource reaches all of it through one `registry.Session.Metadata` call, so
the cache is shared: two commands resolving the same field name in the same day
make one request between them.

**A kind's shape is declared where its node is built.** Every output kind
registers a `render.Schema` from the same file as the code that builds it,
because the two have to agree and the shortest distance between them is one
screen. `render.Write` checks every document against its kind's schema before
emitting a byte, which is what makes `jr contract` a description of the output
rather than a description of somebody's intent. `internal/cli/contract_test.go`
fails on a kind with no schema and on a schema for no kind.

**Query policy lives in `internal/jql` for the same reason.** The default
`ORDER BY`, the key tiebreaker, and the keyset precondition are in
`jql.AppendOrder` and `jql.SortsByKey` rather than in the resource that queries
issues, because two commands now have to agree on them: `issue list`, which
sends the query, and `jql explain`, which says what would be sent. A second copy
would make the explanation a second implementation, and the two would disagree
on the first change to either.

**Not all of it is cached, and that is a per-kind judgement.** The field
catalogue and create metadata change when an administrator edits a screen, so a
day-old answer is still the answer. An issue's available transitions change when
the issue moves, so they are fetched every time and not memoized within a
process either: two calls in one run can legitimately differ, and answering the
second from the first would hide it.

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

A command never writes to stdout. It returns a `render.Doc` (a
format-independent tree) and `internal/cli` decides the format and writes it.
That is what lets every command support every format without knowing any of them
exist, and it is why adding the fifth format needed one writer and no change to
any command: `markdown`, in a build carrying the `render` tag.

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
the other, and a resource that ships only the Cloud recording has tested half
of what it claims to.

**Record them; do not write them.** Every cassette in this repository was
written by hand, and three encoded an assumption rather than the API: an
endpoint removed in Jira 9.0, a parameter of the wrong type, an expand nobody
documents as necessary. All three passed their tests. A cassette proves a
request is *unchanged*; only a recorded one proves it was ever *right*.

`JIRA_RECORD=<path>` writes an invocation's whole conversation to a cassette.
It is an environment variable rather than a flag because a flag would join the
command surface, appear in `jr schema`, and need declaring on every command,
for something no caller of this tool should reach for.

A recording is scrubbed **as it is written**, never as a later step somebody has
to remember, on the same reasoning as credential redaction: a file that only
becomes safe if a second command is run is a file that gets committed before it
is. The host becomes `recorded.invalid`; account ids, UUIDs, emails, and avatar
URLs become fixed placeholders, and `JIRA_RECORD_SCRUB="from=to,..."` renames
whatever else a particular instance carries, a display name, a project key.

**A recording keeps only the response headers something reads.** `transport.KeptHeaders`
lists them and names the reader of each; everything else is dropped as the
interaction is recorded, so it never reaches memory as part of the tape and
never reaches a file. That is a stronger guarantee than reporting it afterwards,
and it exists for the case a report cannot cover: a header carrying identity in
a shape nobody anticipated. Data Center answers with `X-AUSERNAME`, whose value
is the authenticated account name and which matches no pattern here — not an
email, not a UUID, not a host, not a long hex run. Trimming is not free, and the
list is the price: a header dropped here cannot answer a question later, so the
rule is keep what something reads, name the reader, and record fresh when the
question changes.

Afterwards the cassette is checked for residue and anything identifier-shaped is
reported on stderr, **including in the headers that survived** — the scrubber
has always rewritten header values with tight patterns, and for a long time the
loose second opinion read the bodies and not the headers, which is the wrong
half of the file to be complementary on.

**That check deliberately does not reuse the scrubber's own
patterns.** The first version did, and missed a real account id for exactly that
reason: the pattern was too narrow, so the scrubber left the value and the check
meant to catch the miss was blind in the same place. A guard that shares a
definition with the thing it guards cannot catch that definition being wrong, so
the residue patterns are looser and expected to produce false positives.

The third check is for **a declared identifier surviving in a form the caller did
not declare**. `Replace` is a literal `strings.ReplaceAll`, so a short project
key cannot be listed bare — `GE=ORG` would rewrite `GET` and `Set-Cookie` — and
the caller enumerates `GE-`, `"GE"`, `(GE)` instead. Whichever form they forget
is invisible to both checks above, because a project key is not a shape this
package can know. `Cassette.StemResidue` strips the wrapping punctuation off
each declared target and reports any surviving whole-word occurrence of what is
left, which finds `(GE)` and does not fire on `GET`. It reports and never
replaces: a stem match is a hint, and rewriting on a guess is how a fixture
stops being a recording.

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

| Path                                     | Contents                                           | Mode |
| ---------------------------------------- | -------------------------------------------------- | ---- |
| `$XDG_CONFIG_HOME/jr/`                   | The config directory                               | 0700 |
| `$XDG_CONFIG_HOME/jr/config.toml`        | Contexts, defaults. Hand-editable.                 | 0644 |
| `$XDG_STATE_HOME/jr/`                    | The state directory                                | 0700 |
| `$XDG_STATE_HOME/jr/credentials.toml`    | Stored credentials                                 | 0600 |
| `$XDG_STATE_HOME/jr/idempotency.toml`    | What a mutating request already did                | 0600 |
| `$XDG_CACHE_HOME/jr/<site>/`             | One site's cache directory                         | 0700 |
| `$XDG_CACHE_HOME/jr/<site>/<key>.json`   | Deployment probe, field catalogue, create metadata | 0600 |

Every row is asserted by `TestTheDocumentedModesAreTheOnesOnDisk` in
`internal/lint`, which drives each file's real write path and stats the result.
The table is parsed from this document rather than repeated in the test, so a
mode changed in one place and not the other fails rather than drifting. It was
written before that test existed and one row was already describing something
other than a decision: `idempotency.toml` reached 0600 because that is
`os.CreateTemp`'s default, with no `Chmod` anywhere and nothing reading the mode
back.

`config.toml` is 0644 inside a 0700 directory, and the asymmetry is deliberate.
The file mode is what travels when the file is copied into a dotfiles
repository, and 0644 says it is not a secret; the directory mode stays behind,
so 0700 costs nothing the 0644 was buying and keeps site hostnames and project
keys away from other users of the machine. The cache directory is 0700 for a
sharper version of the same reason: its entries are *named* for the site, so a
listing publishes the hostname even though every file in it is 0600.

`os.MkdirAll` leaves an existing directory's mode alone, so these apply to a new
install. An existing 0755 is not repaired on read, changing permissions nobody
asked this tool to change is its own surprise.

The table covers what `jr` writes, which is not the whole credential chain.
`DefaultChain` has three providers: the environment, the store above, and
`~/.netrc`. **`.netrc` is read at whatever mode it has, deliberately**, and it
is the one place a credential this tool uses may be world-readable.

The store is refused when others can read it, because `jr` creates that file,
writes it at 0600, and owns it, refusing is holding the tool to its own
guarantee. `.netrc` is none of those things: it usually predates `jr` on the
machine, it is shared with curl and git, and curl reads a 0644 file without
complaint. Refusing would make `jr` the one tool that broke over a mode it did
not set, and warning on every invocation would leave the credential just as
exposed while adding noise about the least authoritative source in the chain.
It is the same principle as the paragraph above: a mode nobody asked this tool
to police is not this tool's to police. `chmod 600 ~/.netrc` is still worth
doing, curl reads a 0600 file too, and is the user's call.

That row is absent from the table above rather than added to it, because every
row there is driven by a real write path in
`TestTheDocumentedModesAreTheOnesOnDisk` and `jr` has no write path for
`.netrc`. A row it could not drive would be a claim nothing checks, which is
the failure that test exists to prevent. `TestANetrcIsReadWhateverItsMode` in
`internal/auth` pins the behaviour instead.

User resolution is deliberately not cached: the field catalogue is one
immutable snapshot of a whole site, and this is one search per input. A cached
answer would also outlive somebody leaving, which is exactly the account a
caller must not still be assigning work to.

The three are separate because they have different lifetimes and different
backup expectations: config is hand-written and worth keeping, state is
machine-written and worth keeping, cache is machine-written and disposable. One
directory for all of them means a user who clears a cache loses their contexts.

Credentials live under **state, not config**, and that placement is the point.
The config is meant to be hand-edited, shared, and committed to a dotfiles
repository. A credential in it would be published by the first person who tried.
`config.toml` holds a credential _reference_; the store holds the secret, at
mode 0600, and is refused on read if it is readable by anyone else, reading it
anyway and warning would mean the credential is used, and stays exposed, every
time.

An XDG variable that is relative is ignored rather than resolved against the
working directory, which would otherwise put a user's contexts somewhere
different depending on where they ran the command.

**The idempotency ledger is state, not cache, and the distinction is the
point.** Everything under cache can be re-fetched, so losing it costs a round
trip. The ledger cannot be re-derived from anywhere, and losing it means a
retried create makes a second issue. It is the one file here where a corrupt
read is an error rather than a miss, for the same reason.

Writes to it are serialized with an `O_EXCL` lock file, because the promise is
that two processes racing with one key cannot both be told they claimed it, and
a read-modify-write of a shared file without a lock gives exactly that. A lock
older than `idem.LockStale` is presumed abandoned and broken; age is the only
usable signal, since a pid means nothing across containers or after a reboot.

Because a lock can be broken, it carries an id, and a release removes the file
only when the id is still its own. Releasing by path composed with breaking by
age into a third writer: a holder that stalled past `LockStale` had its lock
broken, and then on waking deleted the *new* holder's lock, letting a third
process in while the second was still mid-write. A run that finds its lock gone
or replaced reports `LEDGER_LOCK_STOLEN` rather than assuming its write
survived — it may have been the one another run's rename replaced, and a claim
that is not in the ledger is a create that runs twice.

Metadata caching is a feature, not an optimization: resolving a custom field
name to `customfield_10042` should not cost a round trip on every invocation.
`--refresh` busts it, the TTL defaults to 24h, and `jr field list` warms it.

## Testing

| Layer           | Method                                                         | Gate                                                |
| --------------- | -------------------------------------------------------------- | --------------------------------------------------- |
| `jql/`          | Table-driven, plus a fuzzer asserting no input escapes quoting | 100% of renderer branches                           |
| `adf/`          | Golden files, round-trip property test, three fuzzers           | Corpus of ≥200 real documents, asserted             |
| `resource/*`    | Pure struct-in/struct-out unit tests, plus a fuzzer on anything that parses | 90%                                     |
| `transport/`    | Replayed fixtures, Cloud + DC                                  | Every endpoint                                      |
| Fixture evidence | Cassettes grouped by package and deployment, each group needing a recording behind it | A group with none names itself and its reason, and the list can only shrink |
| Output contract | Golden files per kind, per format                              | Any diff requires a version bump in the same commit |
| Documented contract | Every `kind="…" v="…"` in the docs compared to the registry's kinds | The number a reader copies matches the one the binary emits |
| Command reference | `docs/commands.md` rendered from the registry and compared | Exact under the full profile; every command present is documented under a reduced one |
| CLI surface     | Snapshot tests of `--help`, `schema`, exit codes               | Any diff is reviewed                                |
| Architecture    | Import-graph assertions                                        | Every PR                                            |
| Build profiles  | Matrix build of all profiles, a size assertion, and a command count per profile | Every PR                           |
| Fuzzing         | `make fuzz`, all 20 targets, built with the full tag set       | Every PR, 60s per target                            |
| Complexity      | `make complexity`, gocognit over `internal/` and `cmd/`        | Cognitive 15, or an exemption naming its score and its reason |

**The complexity limit is enforced, and its one exception is written down.**
15 was the number every complexity card in this project had been measured
against, and nothing read it: not `.golangci.yml`, not a `make` target, not a
hook. Thirty-four functions had drifted over it. `internal/lint/complexity_test.go`
now fails on anything above 15 that is not in its `exempt` map, on an exemption
that has grown past the score it was granted at, and on an exemption for a
function that is no longer over the limit. An entry there carries the score
decomposed into what it is made of and a reason the parts cannot go; there is
one, `adf.scanInline`, and most of its 20 is Go's error propagation written
where the errors happen.

**A parser guarantees its own output is safe.** An issue key, an epic
reference, and a board id all end up as URL path segments. Most callers escape
them and one did not, and the difference between the two was which author
remembered, so what a parser accepts is safe unescaped, and the escaping is a
second layer rather than the only one. Each of those parsers has a fuzz target
asserting exactly that, with the inputs that used to get through as seeds.

Recorded HTTP contract tests are mandatory, not optional. Pure-function unit
tests would not have caught any of the incumbent bugs this project exists to
avoid, all of them live at the seam between the CLI and a real Jira.

**Which is a rule the tree does not yet keep, and the gap is now enumerated
rather than described.** A cassette that was written and one that was recorded
replay identically, so nothing in the suite could tell how much of it was
evidence. Grouping every cassette by package and deployment answered it:
eleven recordings, all Cloud, covering four groups of eighteen — and **no Data
Center conversation anywhere rested on a recording**, against 62 cassettes
asserting what somebody believed Data Center does. `unrecorded` in
`internal/lint/evidence_test.go` is that inventory. Each entry names a group
and why there is no recording behind it: five wait on a scrum board in the
Cloud sandbox, nine on a Data Center instance that is not production. The test
refuses an entry whose group has since been recorded, so the list can only
shrink, and refuses a new group that arrives with neither a recording nor a
reason.

## Bodies that do not fit in memory

`transport` buffers a response by default, capped at 64MB, right for JSON,
wrong for an attachment. `Request.Stream` hands the body back unread instead,
and `Response.Close` releases it.

Three things follow from that, and each is a rule rather than a detail:

**A failed streamed request is buffered anyway.** An error body is small and is
the only thing that says what went wrong. Handing back an unread stream would
leave a caller draining it to find out why its request failed.

**The per-attempt timeout bounds getting a response, not reading one.** The
caller reads a streamed body after `Do` returns, and a 30-second deadline has no
idea how large the file is. A stream is bounded to first byte and then handed
over; the caller's own context governs the transfer, which is what makes Ctrl-C
work on a long download.

**A server-supplied URL is checked before it is followed.**
`transport.Client.Relative` converts an absolute URL the server chose into a
site-relative path and refuses one naming another scheme, host, port, or context
path. Data Center reports an attachment's content that way, and unlike a
mistyped `--site` this is not a mistake the caller made or could see.

**An upload body is a factory, not a reader.** `Request.BodySource` is re-opened
on every attempt, for the same reason `Body` is bytes: a retry has to send the
same content again, and a reader is spent by the first one. A source that cannot
be re-opened fails with `BODY_NOT_REPLAYABLE` rather than sending a short body,
the failure mode without it is the worst kind, where the retry succeeds having
uploaded nothing.

## Keeping this current

Update this document in the same change that alters any of:

- The set of packages under `internal/` or `pkg/`.
- What may import a resource, or what a resource may import.
- A responsibility moving between packages.
- The config, state, or cache paths.
- The testing gates table.
