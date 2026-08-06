# jr

A Jira client whose output is a versioned contract.

Built for scripts and agents first, humans second. Every result carries a kind
and a schema version. A result set that was truncated is never reported as
complete. Any request that cannot be honored exactly fails instead of
approximating.

A clean-room rewrite of the ideas in `ankitpokhrel/jira-cli`, not a fork.

> **Status: early.** The output contract, the transport, JQL, contexts,
> credentials, and `issue list` are implemented and tested. Most resources are
> not — see [Not yet implemented](#not-yet-implemented).

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
jr issue list       # query issues; pages until --limit is satisfied
jr issue get KEY    # one issue in full, with its description

jr context create|list|use|show|delete   # named site/project pairings
jr auth login|logout|status|token        # credentials, per site

jr version          # build identity and compiled-in capabilities
jr schema           # every command this build contains, as data
jr schema <name>    # one command in full: flags, args, exit codes, output kinds
jr contract         # every output kind this build can emit, and its version
```

Global: `--format tsv|xml|json|yaml`, `--context`, `--site`, `--project`,
`--readonly`, `--describe`, `--debug`, `--refresh`, `--retries`,
`--max-requests`, and `--limit` on collections. `JIRA_FORMAT`, `JIRA_CONTEXT`,
`JIRA_SITE`, `JIRA_PROJECT`, and `JIRA_READONLY` set the same things from the
environment.

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

- The rest of the resources: `epic`, `sprint`, `board`, `project`, `user`,
  `field`, `meta`, and every `issue` verb other than `list` and `get`.
- `adf` — the package exists with its contract documented and no
  implementation. Until it lands, a Cloud description is emitted as raw ADF
  JSON with `format="adf"`, and a Data Center one as wiki markup with
  `format="wiki"`. Both are carried through unchanged: a half-conversion called
  markdown would be worse than either.
- OAuth, mTLS, and a system-keyring credential provider. The provider interface
  is in place; a keyring implementation shells out, so it will arrive behind its
  own build tag rather than in the reader profile.
- `jr jql validate` and `jr jql explain`. The JQL library is complete (see
  below) but `validate` is specified as a round trip to Jira's parse endpoint,
  and shipping a local-only check under that name would overclaim.
- `--dry-run` and `--no-color`. Nothing mutates yet, and nothing is colored.
- `jr ui`.
- `--contract` reports each kind's name, version, and emitters. Per-kind element
  schemas land with the resources that define them.
- Only the `mcp` tag gates code so far, so `ci` differs from the other three
  profiles and they do not yet differ from each other.

### Reading one issue

```console
$ jr issue get ENG-101
<result kind="issue.get" v="1">
  <issue key="ENG-101" type="Story" priority="High" project="ENG" parent="ENG-1">
    <summary>...</summary>
    <status category="in-progress">In Progress</status>
    <description format="wiki"><![CDATA[ ... ]]></description>
```

XML by default, because one issue is a record and a description full of
newlines, quotes, and code fences is exactly the mixed content an escaping tax
would make unreadable. The markup is **named, never converted**: `wiki` on Data
Center, `adf` on Cloud. A literal `]]>` inside the text is split across two
CDATA sections rather than closing the block early.

The issue shape is the same one `issue list` emits for a row, so a caller parses
both identically — `get` simply has more of it filled in.

A malformed key is rejected locally: `jr issue get foo` is exit 2 without a
round trip, because a 404 for a typo reads like a missing issue.

### Streaming

Rows reach stdout as each page arrives, so output starts immediately instead of
after the last of a hundred requests:

```console
$ jr issue list --limit all --project ENG | head -20   # stops after one page
$ jr issue list --limit all --project ENG > all.tsv    # first row before the last request
```

Measured against a deliberately slow test server, the first row arrives in a
fraction of the whole run rather than after it. An interrupt leaves you with
what arrived; `head` closes the pipe and the run stops early.

TSV streams. XML, JSON, and YAML buffer, because their envelopes carry `count`
and `complete` and neither is known until the end — so the formats you would
reach for on a 5,000-row dump are exactly the ones that stream. Streamed output
is byte-identical to buffered.

Progress goes to stderr **only when stderr is a terminal**. Piped or redirected,
nothing is emitted, so a machine sees the same bytes whether or not someone is
watching.

### Fields

`--field` adds to the default set rather than replacing it, and what you ask
for reaches the output — including the TSV columns, which is the default
format:

```console
$ jr issue list --limit 2 --field customfield_10042
key      status  assignee  updated               summary   customfield_10042
ENG-250  Open              2026-08-01T09:15:00Z  issue 250  5
```

A custom field arrives from Jira in several shapes — a number, `{"value": …}`
for a select, an array for a multi-select — and each reduces to one cell.
Anything that will not reduce is emitted as compact JSON rather than dropped. A
field the server did not return is present and empty, so "no value" is
distinguishable from "I asked for something that does not exist".

Field *names* are not resolved yet, only ids. `--field "Story Points"` is
refused with what to pass instead, rather than being sent for Jira to reject
opaquely. Name resolution needs `jr field list` and the metadata cache.

### Pagination

`--limit` is what you want; the page size is transport tuning. The client pages
until the limit is satisfied, so nobody has to loop by hand:

```console
$ jr issue list --project ENG --limit all      # every match, however many pages
$ jr issue list --limit 2; echo "exit $?"
key      status       assignee      updated               summary
ENG-101  In Progress  Ada Lovelace  2026-08-04T11:32:07Z  Retry logic drops...
ENG-102  To Do                      2026-08-04T09:00:00Z  Tabs and newlines...
exit 3
```

Exit 3 because the result set did not run out — the limit stopped it. The
stderr warning carries a `next-page-token`, and passing it back to
`--page-token` resumes exactly where it stopped.

**Every query carries an `ORDER BY`.** Without one the ordering is the server's
undocumented default, free to differ between two requests — so a paged result
could interleave two orderings and nobody would see it. The default is
`ORDER BY issuekey DESC`, because the key is the only field that is both unique
and immutable. A `--sort` you supply keeps the key as a tiebreaker, since a
bulk edit gives every issue the same timestamp and ties would otherwise break
arbitrarily.

**There is no offset flag, and the token is opaque on purpose.** Cloud pages by
cursor. Data Center pages by **keyset** — the token names the last row seen and
the next page resumes with `AND issuekey < "ENG-102"`, not `startAt=50`. That
matters: an offset cursor shifts when anyone creates an issue mid-run, so a
long `--limit all` silently skips or repeats rows while reporting itself
complete. A keyset cursor names a position in the data and cannot shift.

Keyset needs the key ordering, so a `--sort` on another field falls back to
offsets. The result says which was used. And because the whole scheme rests on
JQL comparing keys by number rather than as text — `ENG-999` sorts *below*
`ENG-1000`, which a string comparison gets backwards — each page is verified to
start below its cursor. A server that disagrees is an error, not a quietly
short result.

A token minted against Cloud is refused against Data Center rather than read as
offset zero.

Which deployment a site is gets **detected, not declared** — probed once from
`/rest/api/2/serverInfo` and cached for a day under `$XDG_CACHE_HOME`. A value
frozen into config goes stale the moment the server is upgraded, and the failure
then looks like an endpoint that used to work returning 404.

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

There are four ways in, and no flag takes a token as its value — an argument
lands in the shell history and the process list, where anyone on the machine
can read it.

`auth login` verifies before it writes anything: it probes the deployment and
fetches the account, so a wrong host, a missing context path, or a bad token is
refused there rather than surfacing two commands later as something unrelated.
It reports who you authenticated as. `--no-verify` skips the check, for
preparing a configuration offline.

```console
# 1. Pipe it.
$ printf '%s' "$TOKEN" | jr auth login --site acme.atlassian.net \
      --email ada@example.com --token-stdin

# 2. From a file, which is what most secret managers write.
$ jr auth login --site acme.atlassian.net --email ada@example.com \
      --token-file ~/.secrets/jira

# 3. Environment. No login step at all — every command just works.
$ export JIRA_API_TOKEN=... JIRA_EMAIL=ada@example.com   # Cloud
$ export JIRA_API_TOKEN=...                              # Data Center PAT

# 4. .netrc, shared with curl and git.
machine acme.atlassian.net login ada@example.com password ...
```

`jr auth login` never prompts. If stdin is a terminal it refuses and lists these
options rather than waiting for input nobody knew to type — a headless build has
no human to wait for.

Logging in with `--site` also **creates the first context**, so the next command
has somewhere to point. If contexts already exist none are touched: you have a
setup, and guessing which one a new credential belongs to would be worse than
doing nothing. Sources are tried environment, then the
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

## MCP

```
jr mcp serve      # speaks the Model Context Protocol on stdin/stdout
```

Every command in the build becomes a tool, generated from the same registry
that builds the command tree and `jr schema` — so a tool cannot drift from the
command behind it, and adding a command adds a tool for free.

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
{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"issue_list", ...
```

The protocol is spoken directly rather than through an SDK: what is needed is
JSON-RPC 2.0 over stdio with three methods, and the profiles this ships in are
the ones meant to carry the least. The wire format is asserted by test.

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

Commits follow Conventional Commits (`feat(issue):`, `fix(transport):`); the
`commit-msg` hook checks the header shape.

```
make test           # the default build
make test-profiles  # the suite under every shipped tag set
make fuzz           # every fuzz target, FUZZTIME each (default 30s)
make golden         # rewrite the output-contract golden files
make lint fmt vet
make ci             # everything CI enforces
```

The golden files under `internal/render/testdata/` and `internal/cli/testdata/`
**are** the output contract. Every kind's shape is pinned once per version in
`internal/cli/testdata/kinds/`, and each shipped profile has its own recorded
set beside it. A diff in one is a change every consumer sees: bump the schema
version of the affected kind in the same commit, which `make golden` will insist
on rather than quietly rewrite.

## Open questions

Carried from the spec, still undecided:

1. **TSV vs XML default for lists.** TSV wins on token cost; consistency across
   commands may be worth the tokens. Decide by measuring a real 100-issue
   payload.
2. **Whether `pkg/jira` is a supported public library** or an internal detail.
3. **Write-side ADF.** Leaning toward a documented subset with loud rejection of
   the rest — loud rejection beats silent mangling.
