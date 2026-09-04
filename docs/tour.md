# A short tour

What each of `jr`'s promises looks like in practice, and the failure it exists
to prevent. Skim the bold sentences if you are in a hurry.

This page is the argument. [recipes.md](recipes.md) is the reference: the same
commands organised by the task you came to do, rather than by the promise they
demonstrate. If you are trying to get something done, start there.

New to `jr`? [getting-started.md](getting-started.md) takes you from nothing to
your first query in about five minutes.

<details>
<summary><strong>Reading one issue.</strong> XML by default, because one issue is a record, and the markup is named rather than converted.</summary>

### Reading one issue

```console
$ jr issue get ENG-101
<result kind="issue.get" v="9" site="https://your-company.atlassian.net">
  <issue key="ENG-101" type="Story" priority="High" project="ENG" parent="ENG-1"
         precondition="eyJkIjoiY2xvdWQiLCJrIjoiRU5HLTEwMSIsInUiOiIyMDI2LTA4LTA0VDExOjMyOjA3LjQxMloifQ">
    <summary>...</summary>
    <status category="in-progress">In Progress</status>
    <description format="markdown"><![CDATA[ ... ]]></description>
```

XML by default, because one issue is a record and a description full of
newlines, quotes, and code fences is exactly the mixed content an escaping tax
would make unreadable. The markup is **named, never guessed**: `wiki` on Data
Center, carried through exactly as the server stores it, and `markdown` on
Cloud, converted from the document Jira holds by a converter that refuses
rather than approximates. `--raw-body` names it `adf` and emits that document
untouched. A literal `]]>` inside the text is split across two CDATA sections
rather than closing the block early.

The issue shape is the same one `issue list` emits for a row, so a caller parses
both identically; `get` simply has more of it filled in.

A malformed key is rejected locally: `jr issue get foo` is exit 2 without a
round trip, because a 404 for a typo reads like a missing issue. That holds on a
cold cache, where the deployment probe would otherwise go first and answer a
typo with `NETWORK` at exit 9, a refusal published as worth retrying. Every
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

</details>

<details>
<summary><strong>Streaming.</strong> Rows reach stdout as each page arrives, and a streamed result is byte-identical to a buffered one.</summary>

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

</details>

<details>
<summary><strong>Fields.</strong> <code>--field</code> adds to the default set rather than replacing it, and what you ask for reaches the output.</summary>

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

**Setting one is the same word and a different shape.** On `issue create` and
`issue edit`, `--field` takes `id=value` and writes the field. The `=` is what
separates the two senses, and a bare name on a write is refused rather than
read as a column request:

```console
$ jr issue edit ENG-250 --field 'Story Points=5'
$ jr issue create --type Story --summary Retry --field customfield_10042=5
```

The value is typed from the same catalogue, so a number field refuses a value
that is not a number before anything is sent, and repeating one id builds an
array. Where Jira reports a type that says nothing about what to send, which it
does for Epic Link and most plugin fields, the refusal names `--field-json`,
whose value is sent exactly as written:

```console
$ jr issue edit ENG-250 --field-json customfield_11350='"ENG-42"'
```

</details>

<details>
<summary><strong>Who touched an issue.</strong> Who an issue belongs to is a different question from who worked on it, and the difference returns a plausible answer.</summary>

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
Its `--help` names those four fields, and a test holds the help to the query.
A bundle that does not say what it covers is a bundle whose result is short for
reasons you cannot see.

Two limits, stated rather than worked around. **Comments are not searchable.**
JQL has no field for comment authorship, so nothing here answers "issues I
commented on"; `--involving` says so instead of quietly approximating it. And
**`CHANGED` names one field at a time.** There is no way to ask whether _any_
field changed, so `--changed-field` defaults to `status` and anything else has
to be asked for by name. It is refused on its own, because a flag that selects
what another flag looks at changes no output by itself.

`--watcher` and `--voter` exist and are deliberately outside `--involving`:
Jira allows both for yourself only unless your credential can manage watchers
or view voters, and folding them in would make the bundle succeed or fail by
permission rather than by what it matched.

Every one of these takes a display name, an email, an id, or the word
`currentUser`, and an unresolvable name is refused rather than sent. `watcher =
"Ada Lovelace"` against Cloud matches nothing and comes back complete, empty,
and successful, which is indistinguishable from "you are watching nothing".

</details>

<details>
<summary><strong>Opening one in a browser.</strong> A bare URL and never a terminal hyperlink, because a data column never carries an escape sequence.</summary>

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
a bare URL anyway, so ⌘/ctrl-click works _and_ `cut -f6 | xargs open` works.
The clickable string and the parseable string are the same string.

Off by default, because forty bytes a row for something most callers throw away
is not a default. The column is **appended**, after any `--field` columns, so
turning it on cannot move a column you already parse. In the structured formats
it is a `<url>` element, declared optional in the schema. `jr contract` shows
it, and adding it bumped `issue.list` to v3 and `issue.get` to v4.

The link is built from the base URL the deployment reports about itself, not
from the site you configured. Those are usually the same string and are allowed
to differ (a reverse proxy, an internal hostname, a context path), and the one
Jira reports is the one its own notification emails use. A site that reports no
base URL is `NO_BASE_URL` and exit 1, refused in validation before a single row
reaches stdout, rather than a link assembled from a guess.

Jira's own `self` on an issue is not this. It is the REST endpoint, and it
opens JSON.

</details>

<details>
<summary><strong>Not overwriting somebody else's edit.</strong> A lost write errors nothing and exits 0 twice, so reads hand out a precondition and writes take it back.</summary>

### Not overwriting somebody else's edit

Two callers edit one issue. The first sets the summary; the second, holding a
copy read before that, sets the priority and sends along the summary it read.
Jira applies both. The first edit is gone, both commands exit 0, and both say
what they set. Nothing was truncated, nothing errored, nothing lied. The write
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
validator on an issue: no `ETag`, no `Last-Modified`, and `PUT /issue/{key}`
honours no `If-Match`. So this is a read, a comparison, and then the write,
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

</details>

<details>
<summary><strong>Pagination.</strong> Keyset before offsets, an <code>ORDER BY</code> on every query, and no offset flag at all.</summary>

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
cursor. Data Center pages by **keyset** where it can: the token names the last
row seen, and the next page resumes with `AND issuekey < "ENG-102"`, not
`startAt=50`. That matters: an offset cursor shifts when anyone creates an issue
mid-run, so a long `--limit all` silently skips or repeats rows while reporting
itself complete. A keyset cursor names a position in the data and cannot shift.

Three things make it unavailable, and then the walk falls back to offsets: a
`--sort` on another field, an `--order asc` that reverses the key, or a query
that is not confined to one project. The last one is the newest and the least
obvious. `ORDER BY issuekey` orders across projects, running the last ENG issue
straight into the first ABC one, but `issuekey < "ENG-1"` does not compare
across them at all: it selects inside ENG and returns nothing. So a bound taken
at the end of a page excluded every project the walk had not reached, the short
page that came back read as the set running out, and
`issue list --all-projects --limit all` reported a fraction of the instance at
`complete="true"`. Both deployments do this, measured; a query scoped to one
project never could.

Two checks sit under it. Because the scheme rests on JQL comparing keys by
number rather than as text (`ENG-999` sorts _below_ `ENG-1000`, which a string
comparison gets backwards), each page is verified to start below its cursor.
And because every one of a walk's stop conditions is the server answering about
the narrowed query it was just sent, a walk that ends reconciles what it fetched
against the count the server gave for the query it started from. A server that
disagrees with either is an error, not a quietly short result.

A token minted against Cloud is refused against Data Center rather than read as
offset zero.

Which deployment a site is gets **detected, not declared**: probed once from
`/rest/api/2/serverInfo` and cached for a day under `$XDG_CACHE_HOME`. A value
frozen into config goes stale the moment the server is upgraded, and the failure
then looks like an endpoint that used to work returning 404.

</details>

<details>
<summary><strong>Contexts and credentials.</strong> Five ways in, no flag that takes a token as its value, and read-only as a one-way latch.</summary>

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
# 1. Type it. jr asks, with echo off, and nothing is recorded anywhere.
$ jr auth login --site acme.atlassian.net --email ada@example.com
API token for acme.atlassian.net:

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

The prompt is the human path and it is not the only one: a script never sees a
terminal, so forms 2 to 5 are unchanged and unaffected. What you type at the
prompt does not echo and does not enter the shell history, which is the property
a flag value cannot have and the reason there is no `--token` flag to reach for.

**The agent, reader, and ci builds do not prompt.** They have no interactive
prompt compiled in, so a terminal on stdin is refused with these options listed
rather than waited on: there is nobody there to answer, and a command waiting
with no reader is indistinguishable from a hang. That is the invariant intact —
nothing blocks silently, and nothing blocks where no human is.

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

</details>

<details>
<summary><strong>JQL.</strong> Built and never concatenated, with one renderer owning the only place a string is quoted.</summary>

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
validated, so `--created-after 2020-13-45` is exit 2 with `month 13 is out of
range` rather than an empty result set that reads like "no matching issues".

Reading a value back is the same package's job. `jql.Unquote` is the inverse of
that renderer, and it exists because Jira answers in JQL's own spelling: ask its
label endpoint about `a,b` and it replies with a quoted literal, and about
`back\slash` with the backslash doubled. Comparing that to what somebody typed
would report every value needing quotes as missing.

Four fuzzers back it up: `make fuzz`. The value round-trip one found a real bug
during development: a byte that is not valid UTF-8 was being silently replaced
with U+FFFD, which would have queried for something other than what the caller
asked for. It is now a structured refusal. That fuzzer reads its own output back
through `jql.Unquote` rather than through a copy of it, because a property
asserted against a reimplementation of the thing under test is a property the
thing does not have.

</details>

<details>
<summary><strong>Transport.</strong> The only package that imports <code>net/http</code>, and redaction happens where the event is built rather than where it is printed.</summary>

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

</details>
