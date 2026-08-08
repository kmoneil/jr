# jr

A Jira client whose output is a versioned contract.

Built for scripts and agents first, humans second. Every result is a document
with a `kind`, a schema version, and a promise about what it means, so a caller
can pin it, verify it, and act on it without reading the output to check whether
it looks right.

> **Status: pre-release, and deliberately untagged.** The command surface is
> complete and tested: 60 commands in the full build, 40 in the reader. Every
> build reports itself as `0.0.0-untagged+<sha>` until there is a release worth
> pinning. See [Not yet implemented](#not-yet-implemented) for what is knowingly
> absent.

## Quickstart

```console
$ make build                                     # → bin/jr, needs Go 1.26

# Cloud: create an API token at
# https://id.atlassian.com/manage-profile/security/api-tokens
$ printf '%s' "$TOKEN" | bin/jr auth login \
      --site your-company.atlassian.net \
      --email you@company.com --token-stdin

# Data Center: a personal access token stands alone, no email
$ printf '%s' "$TOKEN" | bin/jr auth login \
      --site jira.company.com --token-stdin

$ bin/jr user me                                 # proves the credential works
$ bin/jr context edit <name> --project ENG       # stop typing --project
$ bin/jr issue list --assignee currentUser
```

`auth login` verifies the credential before storing it, and creates your first
context so the next command has somewhere to point. A token is never taken as a
flag value, because an argument lands in the shell history and the process list.

Full walkthrough in [docs/getting-started.md](docs/getting-started.md); worked
examples for common tasks in [docs/recipes.md](docs/recipes.md).

## Why this exists

Because the guarantee is the product, and a guarantee is not a feature you can
add.

`jr` is built around a small number of promises that have to hold on every path
at once. Every result carries a `kind` and a schema version. A result set that
was truncated is never reported as complete, in any format. Nothing is guessed:
a date that does not parse, a field name that does not resolve, an assignee that
matches two people, a deployment the probe does not recognise, all fail rather
than being approximated into something plausible. A credential is never written
to a log, a config file, or a process argument.

Each of those is worth nothing in isolation. "Never reports a truncated result
as complete" means something when it is true of all sixty commands, all four
formats, both the streaming and the buffered path, and every future command
somebody adds next year. That is not a patch, and it is not ten patches. It is a
property of how the whole thing is put together: one registry that every command
is declared in, one package allowed to encode output, one package allowed to
speak HTTP, one place where a JQL string can be quoted, and a test suite whose
job is to fail when any of it stops being true.

So the honest description of what we wanted is not a feature the existing
clients are missing. It is a different centre of gravity, and asking a
maintainer to accept a change of purpose is not a fair thing to ask. The
established Jira CLIs are built for a person at a terminal, they are good at
that, and they have users who are happy. Some of the promises above can only be
kept by refusing things that currently work, such as an offset-shaped pagination
flag or a partial result that exits zero. Imposing that on somebody else's users
through a pull request would be the wrong way to treat them. Promising it on day
one, in a tool nobody depends on yet, is just the contract.

This is meant to sit alongside those tools rather than replace them. Different
audience, different bargain. If you want a rich interactive Jira experience,
they are the better answer and there is no argument here. If you are writing a
script or pointing an agent at Jira and you need to know that the output means
what it says, that is what this is for.

The TUI, when it arrives, will be a consumer of this tool rather than the
product, which is the same idea from the other end.

## Install

There is no release binary yet. Build from source, with Go 1.26:

```
git clone <this repo> && cd jira-cli
make build          # → bin/jr
make hooks          # install the pre-commit gate; contributors only, once per clone
```

Then see the [quickstart](#quickstart) above, or
[docs/getting-started.md](docs/getting-started.md) for the walkthrough.

## What works today

60 commands in the full build, 40 in the reader.

```
jr auth      login logout status token
jr context   create edit list use show delete
jr issue     list get create edit move assign delete clone watch
jr issue     comment list add edit delete
jr issue     link list add remove | worklog list add delete
jr issue     attachment list download upload
jr project   list get components versions statuses
jr user      list get me
jr board     list get
jr sprint    list get add close
jr epic      list get add remove
jr jql       validate explain
jr field     list
jr meta      transitions createmeta
jr mcp       serve
jr version | schema | contract
```

Global: `--format tsv|xml|json|yaml`, plus `markdown` in a build with the
`render` tag, for reading and never for parsing. Also `--context`, `--site`,
`--project`, `--readonly`, `--describe`, `--debug`, `--refresh`, `--retries`,
`--max-requests`, and `--limit` on collections. `JIRA_FORMAT`, `JIRA_CONTEXT`,
`JIRA_SITE`, `JIRA_PROJECT`, and `JIRA_READONLY` set the same things from the
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

- `jr ui`, and the `tui` tag that would gate it. The TUI is a consumer of this
  tool, not the product, so it is the lowest priority there is.
- OAuth, mTLS, and a system-keyring credential provider, with the `browser` tag
  that would gate the first. The provider interface is in place; a keyring
  implementation shells out, so it will arrive behind its own build tag rather
  than in the reader profile.
- `--no-color`, and the `clipboard` tag. Nothing emits ANSI and nothing copies,
  so both flags would be flags that do nothing.

Everything else described in this README is built. 60 commands in the full
build, and `internal/lint` asserts the tag table above against the binaries
rather than against this sentence. If a tag here is said to gate nothing and
starts gating something, the build fails until this list is corrected.

### Reading one issue

```console
$ jr issue get ENG-101
<result kind="issue.get" v="5">
  <issue key="ENG-101" type="Story" priority="High" project="ENG" parent="ENG-1"
         precondition="eyJkIjoiY2xvdWQiLCJrIjoiRU5HLTEwMSIsInUiOiIyMDI2LTA4LTA0VDExOjMyOjA3LjQxMloifQ">
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
both identically; `get` simply has more of it filled in.

A malformed key is rejected locally: `jr issue get foo` is exit 2 without a
round trip, because a 404 for a typo reads like a missing issue. That holds on a
cold cache, where the deployment probe would otherwise go first and answer a
typo with `NETWORK` at exit 9 — a refusal published as worth retrying. Every
command taking an identifier is held to it by
`TestALocalRefusalOutranksTheDeploymentProbe`.

For reading rather than parsing, a build with the `render` tag has a fifth
format:

```console
$ jr issue get ENG-101 --format markdown
# issue ENG-101

| Field | Value |
| --- | --- |
| key | ENG-101 |
| summary | Retry logic drops the last error |
| status | In Progress |

## description

## Repro
...
```

`markdown` is the one format outside the output contract. It is never a
default, it carries no schema version, it may change in any release, and the
agent, reader, and ci profiles do not have it. Do not parse it; that is what
`tsv`, `json`, and `xml` are for.

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
and `complete` and neither is known until the end, so the formats you would
reach for on a 5,000-row dump are exactly the ones that stream. Streamed output
is byte-identical to buffered.

Progress goes to stderr **only when stderr is a terminal**. Piped or redirected,
nothing is emitted, so a machine sees the same bytes whether or not someone is
watching.

### Fields

`--field` adds to the default set rather than replacing it, and what you ask
for reaches the output, including the TSV columns, which is the default
format:

```console
$ jr issue list --limit 2 --field customfield_10042
key      status  assignee  updated               summary   customfield_10042
ENG-250  Open              2026-08-01T09:15:00Z  issue 250  5
```

A custom field arrives from Jira in several shapes: a number, `{"value": …}` for
a select, an array for a multi-select. Each reduces to one cell, and anything
that will not reduce is emitted as compact JSON rather than dropped. A
field the server did not return is present and empty, so "no value" is
distinguishable from "I asked for something that does not exist".

A field can be named or given by id. `--field "Story Points"` resolves against
the site's field catalogue, which is cached with a TTL, so a name costs one
request on a cold cache and none after it. A name that matches nothing is
refused locally with the near matches, rather than being sent for Jira to
reject opaquely.

If your team has fields that belong on every issue, store them on the context
instead of typing them each time:

```console
$ jr context edit work --field "Story Points" --field Team
$ jr issue get ENG-250          # both fields included
$ jr issue list --field Sprint  # both, plus Sprint
```

The context's set and `--field` are added together, the context first, so an
ad-hoc field never reorders your columns and never drops the set. A field named
in both places appears once. `--no-context-fields` ignores the stored set for
one invocation.

Two things to know before storing a set. Every read then resolves it, so a
field renamed in Jira fails `issue get` and `issue list` until the context is
corrected. The error says so and names the fix. And every read consults the
field catalogue, which is one request per TTL rather than per command, but is
not free on a cold cache.

### Who touched an issue

`--assignee` and `--reporter` ask who an issue belongs to. That is a different
question from who worked on it, and the difference is the kind that returns a
plausible answer:

```console
$ jr issue list --assignee currentUser --updated-after -7d
```

reads like "what I worked on this week" and is not. `updated` means somebody
updated the issue. A ticket assigned to you that a bot relabelled on Tuesday is
in that result, and everything you did to somebody else's ticket is not.

The filters that ask the second question:

```console
$ jr issue list --involving currentUser --updated-after -7d
$ jr issue list --changed-by currentUser --changed-after -1w
$ jr issue list --worklog-author currentUser --worklog-after -7d
$ jr issue list --was-assignee ada@example.com   # whoever holds it now
$ jr issue list --creator currentUser            # who filed it, which cannot be edited
```

`--involving` is one person across `assignee`, `reporter`, `creator`, and
`worklogAuthor`, OR-ed together and parenthesized inside your project scope.
Its `--help` names those four fields, and a test holds the help to the query —
a bundle that does not say what it covers is a bundle whose result is short for
reasons you cannot see.

Two limits, stated rather than worked around. **Comments are not searchable.**
JQL has no field for comment authorship, so nothing here answers "issues I
commented on"; `--involving` says so instead of quietly approximating it. And
**`CHANGED` names one field at a time** — there is no way to ask whether *any*
field changed — so `--changed-field` defaults to `status` and anything else has
to be asked for by name. It is refused on its own, because a flag that selects
what another flag looks at changes no output by itself.

`--watcher` and `--voter` exist and are deliberately outside `--involving`:
Jira allows both for yourself only unless your credential can manage watchers
or view voters, and folding them in would make the bundle succeed or fail by
permission rather than by what it matched.

Every one of these takes a display name, an email, an id, or the word
`currentUser`, and an unresolvable name is refused rather than sent. `watcher =
"Ada Lovelace"` against Cloud matches nothing and comes back complete, empty,
and successful — indistinguishable from "you are watching nothing".

### Opening one in a browser

`--url` appends the browse link, on `issue list` and `issue get`:

```console
$ jr issue list --involving currentUser --url
key       status       assignee  updated               summary       url
ENG-1000  Done                   2026-08-02T10:00:00Z  Second        https://acme.atlassian.net/browse/ENG-1000
ENG-101   In Progress  Ada       2026-08-01T10:00:00Z  First         https://acme.atlassian.net/browse/ENG-101
```

**A bare URL, deliberately.** A terminal hyperlink is an OSC 8 escape sequence
wrapped around display text; it would make the cell clickable and it would put
escape bytes in a data column, and stdout is data only. Most terminals linkify
a bare URL anyway, so ⌘/ctrl-click works *and* `cut -f6 | xargs open` works —
the clickable string and the parseable string are the same string.

Off by default, because forty bytes a row for something most callers throw away
is not a default. The column is **appended**, after any `--field` columns, so
turning it on cannot move a column you already parse. In the structured formats
it is a `<url>` element, declared optional in the schema — `jr contract` shows
it, and adding it bumped `issue.list` to v3 and `issue.get` to v4.

The link is built from the base URL the deployment reports about itself, not
from the site you configured. Those are usually the same string and are allowed
to differ — a reverse proxy, an internal hostname, a context path — and the one
Jira reports is the one its own notification emails use. A site that reports no
base URL is `NO_BASE_URL` and exit 1, refused in validation before a single row
reaches stdout, rather than a link assembled from a guess.

Jira's own `self` on an issue is not this. It is the REST endpoint, and it
opens JSON.

### Not overwriting somebody else's edit

Two callers edit one issue. The first sets the summary; the second, holding a
copy read before that, sets the priority and sends along the summary it read.
Jira applies both. The first edit is gone, both commands exit 0, and both say
what they set. Nothing was truncated, nothing errored, nothing lied — the write
was simply lost. Survivable when the only caller was a person at a terminal;
`jr mcp serve` ships, and the population of concurrent editors is now however
many agents somebody points at one board.

`issue get` reports a `precondition`, and `issue edit`, `issue move`, and
`issue assign` take it back:

```console
$ jr issue edit ENG-101 --priority High --if-unchanged eyJkIjoiY2xvdWQi...
STALE_WRITE: ENG-101 changed after the precondition was taken, so this write was not sent
  detail: the precondition describes 2026-08-04T11:32:07.412Z and the issue now reads 2026-08-04T11:41:55.008Z
  remedy: re-read it with `jr issue get ENG-101`, decide again against what it says
          now, and retry with the precondition from that read
$ echo $?
7
```

**It is a check with a window, and it says so.** Neither deployment offers a
validator on an issue — no `ETag`, no `Last-Modified`, and `PUT /issue/{key}`
honours no `If-Match` — so this is a read, a comparison, and then the write,
about one round trip wide. A write that ran one carries
`<precondition method="read-compare"/>`, because a conditional request the
server evaluates and a read-then-write are not the same promise, and the
difference belongs in the output rather than in a caller's assumption.

It is opt-in for a structural reason rather than a cautious one. `jr` is
one-shot and holds no earlier read: a default-on check would fetch the issue and
compare it against itself microseconds earlier, detecting nothing and costing a
request. The baseline has to come from whoever did the reading. Adding it bumped
`issue.get` to v5 and `issue.edit`, `issue.move`, and `issue.assign` to v2;
`issue.list` is untouched, because a row nobody read is not a baseline.

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

Exit 3 because the result set did not run out; the limit stopped it. The
stderr warning carries a `next-page-token`, and passing it back to
`--page-token` resumes exactly where it stopped.

**Every query carries an `ORDER BY`.** Without one the ordering is the server's
undocumented default, free to differ between two requests, so a paged result
could interleave two orderings and nobody would see it. The default is
`ORDER BY issuekey DESC`, because the key is the only field that is both unique
and immutable. A `--sort` you supply keeps the key as a tiebreaker, since a
bulk edit gives every issue the same timestamp and ties would otherwise break
arbitrarily.

**There is no offset flag, and the token is opaque on purpose.** Cloud pages by
cursor. Data Center pages by **keyset**: the token names the last row seen, and
the next page resumes with `AND issuekey < "ENG-102"`, not `startAt=50`. That
matters: an offset cursor shifts when anyone creates an issue mid-run, so a
long `--limit all` silently skips or repeats rows while reporting itself
complete. A keyset cursor names a position in the data and cannot shift.

Keyset needs the key ordering, so a `--sort` on another field falls back to
offsets. The result says which was used. And because the whole scheme rests on
JQL comparing keys by number rather than as text (`ENG-999` sorts _below_
`ENG-1000`, which a string comparison gets backwards), each page is verified to
start below its cursor. A server that disagrees is an error, not a quietly
short result.

A token minted against Cloud is refused against Data Center rather than read as
offset zero.

Which deployment a site is gets **detected, not declared**: probed once from
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

`--readonly` is a **one-way latch within an invocation**. Any of the flag,
`JIRA_READONLY`, or a context created read-only turns it on, and nothing a
command does turns it off. A context created for auditing is a statement about
what it is for, and an invocation that merely omits the flag must not quietly
promote itself to read-write. `JIRA_READONLY=0` does not clear it either.

Changing what a context is for is a separate act, and there is one way to do it:

```console
$ jr context edit audit --unset readonly
```

That is an edit of your configuration rather than something an invocation can
do to itself. Without it, narrowing a context to read-only would be undoable
only by deleting and re-creating it, which loses its project, board, and field
defaults. That would be a latch that punished the cautious choice.

Credentials never touch the config file. `config.toml` is meant to be
hand-edited and kept in a dotfiles repository, so it holds a _reference_; the
secret lives under the state directory at mode 0600, and is refused on read if
it is readable by anyone else.

There are five ways in, and no flag takes a token as its value, because an
argument lands in the shell history and the process list, where anyone on the
machine can read it.

`auth login` verifies before it writes anything: it probes the deployment and
fetches the account, so a wrong host, a missing context path, or a bad token is
refused there rather than surfacing two commands later as something unrelated.
It reports who you authenticated as. `--no-verify` skips the check, for
preparing a configuration offline.

```console
# 1. Type it. The shell reads it, so nothing echoes and nothing is recorded.
$ printf 'API token: '; read -rs TOKEN; echo
$ printf '%s' "$TOKEN" | jr auth login --site acme.atlassian.net \
      --email ada@example.com --token-stdin
$ unset TOKEN

# 2. Pipe it, from a secret manager or anywhere else.
$ pass show jira/token | jr auth login --site acme.atlassian.net \
      --email ada@example.com --token-stdin

# 3. From a file, which is what most secret managers write.
$ jr auth login --site acme.atlassian.net --email ada@example.com \
      --token-file ~/.secrets/jira

# 4. Environment. No login step at all, and every command just works.
$ export JIRA_API_TOKEN=... JIRA_EMAIL=ada@example.com   # Cloud
$ export JIRA_API_TOKEN=...                              # Data Center PAT

# 5. .netrc, shared with curl and git.
machine acme.atlassian.net login ada@example.com password ...
```

`jr auth login` never prompts. If stdin is a terminal it refuses and lists these
options rather than waiting for input nobody knew to type. A headless build has
no human to wait for.

That refusal is why the first form hands the reading to the shell rather than
asking for it back. `read -s` does not echo, and what is typed at a `read`
prompt never enters the history — so a human gets the interactive login they
wanted, and the tool keeps the invariant that nothing ever blocks on input.
Trailing whitespace is trimmed from a token however it arrives, so a stray
newline is not a wrong token.

Logging in with `--site` also **creates the first context**, so the next command
has somewhere to point. If contexts already exist none are touched: you have a
setup, and guessing which one a new credential belongs to would be worse than
doing nothing. Sources are tried environment, then the store, then `.netrc`. The
environment comes first so CI can override what is on disk without editing it,
and `.netrc` comes last because it is shared with every other tool on the
machine.

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

Queries are inspected by tokenizing, never by regex. `summary ~ "project = FOO"`
does not constrain the project, and a regex says it does. Dates are
validated, so `--created 2020-13-45` is exit 2 with `month 13 is out of range`
rather than an empty result set that reads like "no matching issues".

Four fuzzers back it up: `make fuzz`. The value round-trip one found a real bug
during development: a byte that is not valid UTF-8 was being silently replaced
with U+FFFD, which would have queried for something other than what the caller
asked for. It is now a structured refusal.

### Transport

`internal/transport` is the only path to Jira, and the only package that
imports `net/http`. It owns retry, the request budget, and redaction.

**Redaction is structural, not a rule to follow.** A credential is replaced when
a trace event is _built_, inside the package, so it never reaches whatever is
formatting the output. A debug formatter cannot leak a token because it is never
given one. URLs count too: userinfo, credential-shaped query parameters, and the
URL inside a `*url.Error` are all scrubbed.

**A POST is not replayed after a 503.** The server may have processed it before
failing, and retrying is how one `issue create` becomes two issues. A 429 is
different, because it is refused before processing, and so is a request whose
caller holds an idempotency key. That distinction is the difference between a
retry that is safe and one that silently duplicates work.

Retries count against `--max-requests`, because a retry is another request from
the server's side and a budget that ignored them would bound nothing. Exhausting
the retry budget exits 8 or 9, never 0.

Recorded fixtures replay against both Cloud and Data Center, and an unmatched
request is an error. A fixture test that fell through to the network would be
green in CI, where there are no credentials, while exercising nothing.

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
{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"issue_list", ...
```

The protocol is spoken directly rather than through an SDK: what is needed is
JSON-RPC 2.0 over stdio with three methods, and the profiles this ships in are
the ones meant to carry the least. The wire format is asserted by test.

## Documentation

Start here if you are new:

| Document                                             | Covers                                                                        |
| ---------------------------------------------------- | ----------------------------------------------------------------------------- |
| [docs/getting-started.md](docs/getting-started.md)   | Install, get a token, log in, first query, reading the output                 |
| [docs/recipes.md](docs/recipes.md)                   | Worked examples: searching, bulk edits, exports, sprints, CI, agents          |
| [docs/troubleshooting.md](docs/troubleshooting.md)   | Every common failure by error code, and what to do about it                   |
| [docs/commands.md](docs/commands.md)                 | Every command and its flags, generated from the registry and asserted current |

Reference:

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
make docs           # regenerate docs/commands.md from the registry
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

## How this was built

This tool was built with AI assistance. Most of the lines here were typed by a
language model. All of them were directed, reviewed, and accepted by a human
author who is responsible for the result. It is stated because you would
reasonably want to know, not because it is an excuse or a selling point.

The division of labour is worth being precise about, because "written by AI" has
come to imply something that did not happen here. The model was fast, tireless,
and wrote most of the code. It did not decide that a truncated result must never
report itself complete in any format. It did not decide that a credential should
be redacted where the trace event is built rather than where it is printed, so
that no future formatter can leak one. It did not decide that a number in a
document is worthless until something asserts it, or that a fixture nobody
recorded has to say so in a field a lint can read. Those decisions are what the
code is, and each one was made by a person who then had to insist on it, more
than once, against a perfectly reasonable draft that did it the other way.

That is the part that is not evenly distributed. The model is available to
everyone reading this. Knowing which confident answer to distrust, and being
willing to throw away a good afternoon's work because the reasoning behind it
was plausible rather than checked, is the input that was actually scarce.

Humans and models produce slop in roughly equal measure. Neither one is the
reason software is good or bad. What decides that is the verification: what is
actually tested, what is measured against a real system instead of recalled, and
which claims something would catch if they stopped being true. A careful human
and a careful model with the same test suite land in the same place, and so do a
careless one of each.

So the rules for this repository are aimed at that, and they are enforced rather
than professed. The specific failure mode worth designing against is confident
plausibility: an endpoint recalled from memory looks exactly like one read off a
real response, and a fixture that encodes what its author assumed passes exactly
like one recorded from a server.

- **Claims about Jira are measured, not remembered.** Three bugs shipped and
  were found by pointing a real build at a real instance, not by any test.
  `meta createmeta` called an endpoint removed in Jira 9.0. `jql validate` sent
  `validateQuery=strict` where Data Center takes a boolean. `project list` never
  populated its lead column, because neither deployment expands the lead unless
  asked. In each case a hand-written fixture encoded the assumption rather than
  the API, and passed happily for months. The ADF converter is the same lesson
  applied ahead of time: its corpus is 247 documents Jira Cloud actually stored
  plus 23 it refused, and the round-trip fuzzer over them found fourteen bugs
  that no hand-written case would have.
- **Where the evidence does not exist, the repository says so.** Every Data
  Center fixture in the suite is constructed, because the only Data Center
  instance available is production and recording against it is refused. Each
  cassette carries whether it was recorded or written, a lint keeps that field
  honest, and the gap is tracked as a blocked task rather than described as
  coverage. A constructed fixture proves a response is handled. It can never
  prove a request was accepted.
- **A documented number nothing checks is a number that was true once.** The
  build-profile table is asserted by building each profile and counting what the
  binary reports. Its four counts were four releases stale before that test
  existed. The error-code table is asserted by resolving each call site to the
  exit it actually produces, which found a code documented at exit 1 and built
  at exit 9, quietly advertising a refusal as retryable.
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

## Open questions

Carried from the spec:

1. ~~**TSV vs XML default for lists.**~~ **Decided: TSV stays.** A hundred
   issues cost 2,930 tokens as TSV and 12,755 as XML, which is 4.35x, or 9,825
   tokens saved per page. The same document as a single record is 1.21x, because one
   record has one of everything and the framing has nothing to compound over.
   That is why the default follows content shape instead of being one format
   everywhere. Numbers and method in
   [docs/output-contract.md](docs/output-contract.md#what-the-defaults-cost);
   reproduce with `make cost`.
2. **Whether `pkg/jira` is a supported public library** or an internal detail.
   Still open; the import lint keeps it CLI-free either way.
3. ~~**Write-side ADF.**~~ **Decided and shipped:** a documented subset with
   loud rejection of the rest, via `--body-format text|markdown|adf`. Loud
   rejection beat silent mangling.
