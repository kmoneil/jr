# Output contract

The output shape is a public API. It is versioned, and a change that breaks it
moves the minor position of the release while the version starts with a zero;
an additive one moves the patch. See [Stability policy](#stability-policy),
where every rule says which position it moves.

This document describes what `jr` emits. It is the reference a consumer pins
against; `jr contract` emits the machine-readable form of the same thing.

## Streams

**stdout carries the result and nothing else.** Never a progress spinner, never
a warning, never a "Fetching…". A command that fails writes nothing at all to
stdout, so a consumer piping stdout never parses a half-result.

There are exactly two exceptions, and both are cases where something had already
happened by the time the failure arrived.

The first is a write that half-happened: a multi-request mutation that applied
some of what it was asked writes its result document and _then_ exits non-zero,
because the failure alone cannot say which part was applied and "nothing
happened" is the dangerous assumption to leave a caller with. See
[Mutations](#mutations). A _document_ that arrives with a non-zero exit is always
one of those.

The second is a streamed collection. `--format tsv` emits each row as it
arrives, which is what makes a long list pipeable at all, so a failure on the
fortieth row leaves thirty-nine rows and a header on stdout and no way to
retract them. The rows that are there are the answer up to the failure and not a
complete one, and **the exit code is the only thing that says so**, because TSV
has no envelope to carry `complete="false"`. The error on stderr says how many
rows went out, so a caller reading it knows what the bytes it already has are
worth.

This is a split along `--format`: the same collection, refused for the same
reason, writes nothing at all under `xml`, `json`, and `yaml`, because those
buffer until the last page lands. It is stated rather than closed, because
closing it means buffering every format, and a collection that has to be
complete before it is emitted cannot be piped into anything while it runs. A
consumer that treats a zero exit as the condition for reading stdout is
unaffected by any of it.

**stderr carries everything else**, always structured, always in the requested
format: errors, and the truncation warning that accompanies exit 3.

`--help` output goes to stdout, because it is what the caller asked for. It is
not a result document and carries no envelope.

## Formats

| Content                                          | Default | Rationale                                                               |
| ------------------------------------------------ | ------- | ----------------------------------------------------------------------- |
| Collections (`list`, `search`, `schema`)         | `tsv`   | Rectangular data is rectangular, and TSV is the cheapest encoding of it |
| Records and documents (`get`, `view`, `version`) | `xml`   | Mixed content, no escaping tax, self-describing                         |

`jr version`'s `release` attribute is always a semantic version, in every case,
a tagged build, an untagged tree, a dirty one, and a source tree with no git at
all. `scripts/version.sh` produces it and `internal/lint/version_test.go` holds
it to that shape, because the same string goes out as the `User-Agent` and lands
in a Jira administrator's access log. It used to be whatever `git describe
--always` returned, which on an untagged tree is a bare commit hash: it names no
release, sorts against nothing, and does not announce that it is not a version.
The User-Agent also carries the build profile: `jr/1.2.0 (reader)` tells an
administrator the client that touched an issue could not have written to it.

A tag that is not itself a semantic version fails the build rather than being
stamped. `git tag nightly` produced `nightly+1.g2acd8a4` and `git tag rel/2024`
produced a string holding a character semver does not allow at all, both
straight through `${tag#v}` into the binary and out onto the wire. The script
now validates its own output and names the offending tag, and the Makefile
checks it: `$(shell)` keeps a script's output and discards its exit status, so
a refusal alone would have stamped an empty release and built anyway. The four
cases and the refusals are exercised in repositories the test builds, because
this repository is only ever in one of them at a time.

All four formats (`tsv`, `xml`, `json`, and `yaml`) are available on every
command. The defaults are a convenience; `--format` is the contract.
`JIRA_FORMAT` sets the default globally; `--format` overrides it. An
unrecognized value is exit 2 listing the valid ones, never a silent fallback.

### `complete` can appear inside a record, not only on a collection

A collection says whether it is exhaustive in its envelope. A record could not
say it at all: `Doc.IsComplete` returned true for every record, and said so in
its own comment, until `issue get --with-comments` had to carry a comment
thread. A thread is paged, so a bounded one is the normal case rather than an
edge, and _`complete="false"` or exit 3_ is unqualified.

So a **container inside a record** may carry `complete`, beside the `count` it
already has:

```xml
<issue key="ENG-101">
  ...
  <comments count="50" complete="false">
```

A record holding any container marked incomplete is itself incomplete: it exits
3 and writes the same structured stderr warning a truncated collection does,
with `element` naming which part was cut, because "this issue is partial" does
not say which.

**A consumer that learned to look for `complete` on the collection envelope must
now also look inside a record.** Only containers that can genuinely be bounded
declare it; `labels` and `components` arrive whole with the issue and do not,
so a value that is always true is not something to check.

### And inside a row of a collection

`issue list --with-comments` brings each row's thread back in the request the
page already costs. That makes the same container appear in a listing, and it
makes a collection able to be incomplete for a second reason: every row arrived
and part of one row did not.

```xml
<issues count="2" complete="false">
  <issue key="ENG-3">
    ...
    <comments count="20" total="25" complete="false" start-at="5">
```

Three attributes, and each answers a question the others cannot:

- `count` is how many comments arrived.
- `total` is how many the server says the thread has. A caller deciding whether
  to spend a request on `issue comment list` needs the size of what it would
  fetch, and `complete` only ever says whether the two agree. It is **written
  only when the server reported a count**; see below.
- `start-at` is where in the thread the first returned comment sits. It is
  **written only when it is not zero**, and it is not decoration: Data Center
  inlines the whole thread from the oldest comment, and Cloud caps the
  projection at twenty and returns the **newest** twenty. A 25-comment issue
  therefore arrives from Cloud as comments 6 to 25. `count` and `complete`
  together cannot distinguish that from the first twenty, so without this a
  consumer would be reassembling a conversation from a fragment whose position
  in it is unstated.

The run exits 3 and writes the truncation warning, as any incomplete result
does. **The warning names the element rather than offering `--limit`**, because
raising a bound that was never reached would fetch no further comment:

```
code    RESULT_TRUNCATED
kind    issue.list
element comments
remedy  every row is here and one of them holds part of a paged subresource;
        read that subresource with the command that pages it
```

There is no next-page token, for the same reason `issue history` has none
against Data Center: the result set was exhausted, and what is missing is inside
a row.

`issue activity` is the same shape one level up. Its rows are events rather than
issues, so the clipped container is not in the document at all — the comments it
could not read were never rendered — and the warning names `event` to say the
feed is missing some of what it merged.

**Three sources can clip it, not one.** The comment thread above is the first.
The second is worklogs, which both deployments inline oldest-first and cap at
twenty, so the feed spends one request per issue that has more and reports what
is still short. The third is the changelog, and it was reported as nothing at
all until 2026-08-17: `expand=changelog` on the Cloud search is a paged bean
bounded at forty entries, it says so in every response, and this tool read
neither the bound nor the count. An issue with more than forty saves lost its
oldest ones and the feed exited 0 with `complete="true"`. It is now counted like
the other two, in saves rather than in the rows they flatten into, so a run over
a busy Cloud issue exits 3 where it used to be silently short. Data Center
reports a `maxResults` that mirrors what it sent, so its projection says nothing
about a bound and nothing there has been observed to clip.

### An absent `total` means the thread's length is unknown

`total` is optional, and its absence is an answer rather than a gap. A server
that sent no count has not said how long the thread is, and neither path that
fills this container can ask again: `issue get --with-comments` fetches one
bounded page by design, and `issue list --with-comments` gets the thread as a
projection inside a search response, where there is no second request to make.

```xml
<comments count="5" complete="false">
```

**An absent `total` always comes with `complete="false"`**, so the run exits 3
and the record or the row is incomplete, exactly as a thread known to be clipped
is. It is a reason to spend a request on `issue comment list` rather than a
reason to skip one: five comments with no count may be all of them, or may be
five of four hundred, and this tool cannot tell which.

That is the change that took `issue.list` to v7 and `issue.get` to v9. Both
previously wrote `total="0"` when the server sent no count, and `0` is a number
a consumer can branch on: `count >= total` held for any thread, so a clipped one
was published `complete="true"` at exit 0. A required attribute left the tool no
way to say it did not know, so removing the requirement was the fix rather than
choosing a better default.

### `markdown` is presentation, and carries no promise

A build with the `render` tag has a fifth format, `markdown`. **It is not part
of this contract.** Everything else in this document describes the four formats
above: the envelope, the kinds, the schema versions, and the stability policy.
`markdown` exists so a person reading an issue on a terminal does not have to
read XML, and it may change in any release, with no version bump and no note.

Do not parse it. If something is parsing output, it wants `tsv`, `json`, or
`xml`, all of which are versioned and none of which will change shape without a
release that moves the minor position.

Three things keep the exception contained rather than making it a hole:

- It is **never a default.** `DefaultFor` stays TSV for collections and XML for
  records, so no existing script can receive markdown by accident; a caller has
  to ask for it by name.
- It is **absent from a build without the tag.** The agent, reader, and ci
  profiles refuse `--format markdown` with exit 2, do not list it in `--format`,
  and do not advertise it in the MCP tool schema. Asserted from both sides in
  `internal/render` and by `internal/lint/profiles_test.go`.
- It is **lossy on purpose, in two places.** A leaf carrying both text and
  attributes renders its text and drops the attributes:
  `<status category="in-progress">In Progress</status>` reads as `In Progress`.
  The text is what a person is reading for. And a carriage return is normalised
  to a newline, because this is the format that reaches a terminal and a
  terminal reading `\r` returns the cursor to column 0 — `Closed as
duplicate\rDO NOT MERGE` displays as the second half alone, with the first
  half present in the data and absent from the screen. The source is an issue
  summary, so it is written by whoever can file a ticket. Everything either case
  touches is still in the other four formats, which is where anything parsing
  should be looking.

It is still goldened in `internal/render/testdata`, and that is not a
contradiction. Those files are regression tests: change the writer and the diff
shows up, and they carry none of the schema-version obligation that
`internal/cli/testdata/kinds` does, which is the only golden set
`internal/lint/goldens_test.go` holds to a version bump.

### What the defaults cost

The split is per content shape rather than one format everywhere, and that was
settled by measuring rather than by taste. `issue list --limit 100`, rendered
from the same document in each format:

| Format | Bytes  | Tokens (proxy) | vs TSV | Tokens/row |
| ------ | ------ | -------------- | ------ | ---------- |
| `tsv`  | 7,977  | 2,930          | 1.00x  | 29.3       |
| `xml`  | 35,030 | 12,755         | 4.35x  | 127.5      |
| `json` | 45,088 | 15,959         | 5.45x  | 159.6      |
| `yaml` | 33,085 | 12,866         | 4.39x  | 128.7      |

The same document as a single record, `issue get`:

| Format | Bytes | Tokens (proxy) | vs TSV |
| ------ | ----- | -------------- | ------ |
| `tsv`  | 592   | 218            | 1.00x  |
| `xml`  | 791   | 264            | 1.21x  |
| `json` | 842   | 295            | 1.35x  |
| `yaml` | 683   | 241            | 1.11x  |

**The token columns are a proxy and are labelled as one.** They were counted
with `cl100k_base`, which is OpenAI's tokenizer, not the one any Claude model
uses, it undercounts Claude by roughly 15-20% on prose and by more on
structured text, which is exactly the shape being measured here. The bytes are
exact, and they put the structured formats at 4.15-5.65x TSV against the
proxy's 4.35-5.45x, the same band and the same conclusion, reached
independently. They do not agree on the figure, and they disagree on whether
XML or YAML is the cheaper of the two. So the _decision_ stands on the bytes;
the token columns are corroboration, and neither is a Claude token count. Run
`make cost` with credentials to replace them with one, it counts through
Anthropic's own `count_tokens` endpoint.

A hundred rows is where framing compounds: a structured format spells every
field name once per row, and TSV spells it once for the whole result. That is
9,825 tokens saved per hundred issues against XML, or 77%. One record has one
of everything, so the multiple collapses to 1.21x, and what is left is that a
record carries nested and mixed content a rectangular format has nowhere to
put. The defaults follow the shape because the saving does.

**The saving is on five columns, not on the issue.** TSV emits the declared
columns; `created`, the issue id, the status category, the assignee's account
id and the labels are in the XML for every row and in the TSV for none of them.
`--format xml` is how you get them, and it is one flag.

### What the defaults cost the parser

Tokens are what a payload costs the model that reads it. The other consumer is
the process that reads it, which pays on every invocation whether or not a model
is involved. The same hundred issues, decoded into typed structs:

| Format | Time    | vs TSV | Throughput | Allocated | Allocations |
| ------ | ------- | ------ | ---------- | --------- | ----------- |
| `tsv`  | 13.7us  | 1.0x   | 583 MB/s   | 17.6 KB   | 102         |
| `xml`  | 777us   | 56.8x  | 45 MB/s    | 337.0 KB  | 9,257       |
| `json` | 325us   | 23.7x  | 139 MB/s   | 58.4 KB   | 853         |
| `yaml` | 1,602us | 117.0x | 21 MB/s    | 741.5 KB  | 14,831      |

The parse spread is an order of magnitude wider than the token spread, 4.35x
becomes 56.8x, because a token count scales with the bytes and a parser scales
with the structure. TSV is two splits and an unescape; XML is a state machine
and an allocation per element.

**Read that as a shape, not as a bill.** All four are dominated by one HTTP
round trip, so no caller should pick a format for this reason alone. What does
travel is the garbage: YAML allocates 22x the payload to read it, and 14,831
allocations per page is a number that shows up in a run that pages a hundred
times, or on a runtime with a small heap.

Measured 2026-08-06 against a payload built from the summaries Jira Cloud
actually returned for the sandbox's sample project. `o200k_base` differs from
`cl100k_base` by under 1% on every token row above, and the byte ratios land in
the same band again, three counts of the same thing, agreeing on the shape.
That is the useful part: the ratio is a property of the framing rather than of
whose vocabulary is counting, which is also why a proxy tokenizer was enough to
decide §12.2 and is not enough to publish a number. Parse figures are a Go
benchmark on one machine, so their ratios carry and their absolute times do
not. Reproduce all of it with `make cost`.

Neither table is the enforced part. `TestFormatCostFavoursTSVForCollections`
asserts the ratio the default rests on, with no tokenizer and no network, so a
writer change cannot erode the premise quietly.
`TestEveryFormatParsesBackToTheSameRows` decodes each format and holds it to the
values it must recover, the only place in this repository that reads `jr`
output back rather than writing it.

## Envelope

Every successful XML response:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<result kind="issue.list" v="8" site="https://acme.atlassian.net">
  <issues count="3" complete="true">
    <issue key="ENG-101">
      <summary>Retry logic drops the last error</summary>
      <status category="in-progress">In Progress</status>
      <assignee id="712020:8f3a" display="Ada Lovelace"/>
      <reporter id="712020:4c11" display="Grace Hopper"/>
      <updated>2026-08-04T11:32:07Z</updated>
    </issue>
  </issues>
</result>
```

- `kind`, stable identifier for the payload shape. An agent dispatches on it.
- `v`, schema version for that kind, incremented whenever the kind's shape
  changes, additively or otherwise. It said "on breaking change" until 2026-08-21
  and that was never what the gate enforced: `make golden` refuses a changed
  shape at an unchanged version, so an added optional element moves `v` too.
  **A `v` that moved does not by itself say the release is breaking.** See the
  stability policy, and the row for this case below.
- `site`, **the Jira this answer came from**: the base URL the request was
  actually sent to, including any context path. Present on every document from a
  command that reached a site, and **absent** on one that did not — `jr version`
  and `jr context list` name no Jira rather than an empty one. See
  [Provenance](#provenance) below.
- `project` and `board`, **the scope this answer was computed over**. Present
  when the command asked the context for one, absent when it did not, on the
  same rule as `site` and for the same reason: an empty attribute would read as
  a project whose key is the empty string. They are the answer to a question the
  envelope could not previously be asked — a truncated result is reported
  incomplete, and a *scoped* one is reported complete, because it is, within a
  frame nothing named. See [Scope](#scope-and-what-a-global-flag-reaches).
- `complete`, **`true` only if the result set is exhaustive.** If a limit
  truncated it, `complete="false"` and `<next-page-token>` is present. There is
  no third state and no way to get a truncated set that claims to be complete.
  This is the single most important attribute in the format.
- `next-since-token`, where the **next answer** starts. Only a feed emits one;
  see below.

### `next-since-token` is not `next-page-token`

They look alike and mean opposite things, so a consumer must not treat one as
the other.

| | `next-page-token` | `next-since-token` |
| --- | --- | --- |
| Says | this answer was cut short | this answer was whole |
| Present when | `complete="false"` | the poll covered its whole window |
| Passed back as | `--page-token` | `--since` |
| Emitted by | any paged collection | `issue changes` |

A page token resumes a result set that was not finished. A since token names the
boundary between this answer and the next one, so it appears on a **complete**
result — the combination that is rejected before writing for a page token, which
is exactly why it cannot be the same field. A feed carrying its cursor in
`next-page-token` would tell every existing consumer that the answer was
truncated, and exit 3 forever.

**A feed that was cut short carries neither.** `issue changes` issues no cursor
when `--limit`, the request budget, or a changelog the server would not send in
full stopped it covering the window it opened, because advancing past a window
that was only partly reported is how a feed drops a change and says nothing. The
run exits 3 and the caller polls again with the same `--since`.

**TSV carries no token**, as it carries no `site` and no `complete`: it has no
envelope. A polling consumer asks for a structured format.

JSON and YAML hoist the envelope to the top level rather than transliterating
the XML tree:

```json
{
  "kind": "issue.list",
  "v": 8,
  "site": "https://acme.atlassian.net",
  "count": 3,
  "complete": true,
  "issues": [ … ]
}
```

TSV emits a header row and nothing else, no envelope, no counts, no site.

### TLS, and what the context reports about it

`context.get` and `context.list` moved to **v2** together, and everything the
move added is optional: a consumer reading v1 reads v2 unchanged.

- `ca-bundle`, `client-cert`, and `client-key` are **paths**, present only when
  the context sets them. Absent means the system trust store and no client
  certificate, which is every Cloud site and most Data Center ones; an attribute
  restating that on every context would be noise on the common case. Nothing
  here is a secret, and the path is what somebody has to check when a chain
  fails.
- `proxy` appears on the **effective** form only, from `HTTPS_PROXY` and
  `NO_PROXY`. It is not a setting this tool has: the standard library has always
  honored those variables, nothing said it was happening, and a request going
  somewhere nobody chose looks like a fault at the site rather than a hop in
  between. Any userinfo in it is stripped, on the same rule the trace applies to
  a request URL.

**There is no way to disable certificate verification.** Not a flag, not an
environment variable, not a context setting. A private CA is trusted with
`--ca-bundle` and mTLS is answered with a client certificate; those are the
legitimate needs. Every tool that has shipped an `--insecure` has it in a wiki
page somewhere as the standard fix for a certificate problem, and none of the
header redaction, the off-site URL refusal, or the relative-path rule survives a
connection nobody verified. It is stated here so it reads as a decision rather
than an omission, and `TestNothingCanDisableCertificateVerification` is what
keeps it one.

**A TLS setting that was named is used or refused, never ignored.** A bundle
that cannot be read, or that holds no certificate, exits 2 with
`INVALID_CA_BUNDLE` naming the file. Falling back to the system roots would
produce the same verification error the caller was trying to fix, with nothing
saying the file was never read. Half a client certificate is
`INCOMPLETE_CLIENT_CERT` and a key that does not match its certificate is
`INVALID_CLIENT_CERT`, both refused before the request rather than at the
handshake, where the error names the server instead of the file.

`doctor`'s transport check reports the same three paths and the same proxy,
and moved to **v2** on 2026-09-01 to add what a connection actually did, which
configuration cannot say. All three are optional and present only once a
response has arrived:

- `tls` is whether the connection was encrypted. `false` is a plain `http://`
  site that answered. Absent means no response was received, or the run was a
  replayed fixture, which makes no connection at all; in both cases the check
  says so in its summary rather than reporting a chain nobody verified.
- `tls-version` is the negotiated protocol as `crypto/tls` names it, `TLS 1.3`.
  A `TLS 1.2` against a site that speaks 1.3 is a middlebox nobody mentioned.
- `verified-against` is `system` or `bundle`. `bundle` means no chain verified
  without a certificate from the configured `ca-bundle`; `system` means the
  chain went through the trust store, and with a bundle configured that is a
  setting doing nothing, which the summary says in words. The issuer is
  deliberately not reported: a root's subject can name an employer, and this
  document is what gets pasted into a bug report.

### Provenance

`site` names the instance, because an answer outlives the shell that produced
it. Two documents from two Jiras were otherwise indistinguishable once both were
on disk, and the case that added this was a command run against the wrong
instance returning a well-formed, complete, exit-0 document about it. Nothing
misbehaved; the answer simply did not say what it was an answer about.

Three things it is, precisely:

- **The URL that was used, not the one configured.** They differ under a context
  path, and differing is the case worth reporting.
- **Data, not a diagnostic.** It is on the envelope rather than on stderr,
  because stderr is discarded by exactly the caller that most needs it — an MCP
  tool call never sees any.
- **Not the context name.** A context name is local and means nothing to
  whoever reads the file next. The site is the fact.

**TSV carries none**, because TSV has no envelope. That is the same limit
truncation has in that format, where `complete="false"` becomes a structured
stderr warning plus exit 3. A column was considered and rejected: it would
change a default column set, which is a breaking change, and repeat one value on
every row. So a caller who needs provenance in a pipeline asks for a structured
format, and `jr issue list --format json` is the whole of the answer.

Adding it moved no kind version. The per-kind shapes describe the payload, and
this is on the envelope every kind shares.

`project` and `board` are the same argument one level in, and they arrived the
same way: from a command run against a scope nobody stated, returning a
well-formed, complete, exit-0 document about a quarter of the answer. `site`
says which Jira; these say which slice of it, and all four bullets above apply
unchanged — the scope that was **used** rather than the one configured, data
rather than a diagnostic, and absent in TSV.

The one difference is worth stating because it is what makes them checkable.
`site` is a property of the connection and can be read at the boundary at any
time. A scope is something a command *reads*, and reading it again at the
boundary would answer for an invocation that did not: `--all-projects` consults
the context for nothing and would be stamped with the context's project. So
the session is wrapped, the read is recorded, and the envelope reports what the
query used. `TestTheEnvelopeNamesTheScopeTheAnswerWasComputedOver` fails on the
obvious implementation.

## Streaming

**TSV streams. The structured formats buffer.**

A collection command writes rows as each page arrives, so a long paged run
produces output immediately rather than after its last request. That is not a
performance nicety: it is what lets `jr issue list --limit all | head -20` stop
early, and what leaves a caller who interrupts a hundred-request run with the
rows already fetched instead of nothing.

XML, JSON, and YAML cannot stream, because their envelopes carry `count` and
`complete` and neither is known until the last page lands. Those formats buffer
and emit once, exactly as before, streamed output for a given result is
byte-identical to buffered output, and a test asserts it.

This works because of the arrangement above: TSV's completeness signal lives on
stderr and in the exit code rather than in the payload. A row can be written
before anyone knows whether the set will be exhausted, because the answer was
never going to appear in the TSV body.

The trade is that a malformed row cannot be caught before its predecessors have
been written. The column specification is validated before the header, so a bad
_specification_ emits nothing; a bad _row_ leaves partial output and a non-zero
exit. Piping to `head` closes the pipe and the process exits 141, which is
ordinary Unix behavior and not an error.

## Progress

A long run reports its progress on stderr **only when stderr is a terminal**.
On a pipe or a redirect nothing is emitted at all, so the rule that stderr
carries only structured diagnostics is untouched, there is no structured form
of "42% done" worth defining, and a machine reading stderr sees byte-identical
output whether or not a human happened to be watching.

## Truncation

A truncated result is signalled three ways, and a consumer only needs to notice
one of them:

1. **Exit 3** (`PARTIAL`), in every format.
2. **A structured warning on stderr**, in every format, carrying code
   `RESULT_TRUNCATED` and the resume token.
3. **`complete="false"` in the envelope**, in every format that has one.

TSV has no envelope, which is why 1 and 2 exist: a script that checks `$?`
cannot miss it.

A result that is complete carries no next-page token. Setting both is rejected
before a byte is written.

**A truncation is not always something a second request would fix.** Usually it
is the caller's `--limit` or `--max-requests`, and resuming gets the rest.
`issue history` against Data Center has a third cause: that deployment has no
paged changelog route at all — `/rest/api/2/issue/{key}/changelog` answers 404 —
and serves the whole history alongside the issue under `expand=changelog`. It
also ignores `startAt` and `maxResults` there, returning the same entries again
rather than the next ones, so where the server reports holding more entries than
it sent, the result says `complete="false"` and exits 3 with **no token and no
way to ask for the remainder**. A consumer that treats exit 3 as "call again
with the token" has to tolerate there being no token, which it must anyway:
`complete="false"` is a statement about the answer and never a promise about
what a further request would do.

**A walk can also fail rather than truncate.** Truncation says the answer stops
where a bound put it. When a paged walk stops on its own while holding fewer
rows than Jira counted for the query it started from, that reading is not
available: nothing bounded it, so there is no bound to raise, and the rows it
is missing were never requested, so there is no position to resume from.
That is `PAGINATION_SHORT` at exit 9, naming both counts, and it is an error
rather than a short result. Under `tsv` the rows fetched before it are already
on stdout, as they are for every failure a streaming command hits mid-run.

## Warnings

A warning is a structured document on stderr carrying a `code` and a `message`,
in whatever format the invocation asked for. It never changes the exit code and
never reaches stdout. There are six, and each exists because something true
about the answer cannot be read off the answer itself.

| Code               | Emitted by                                      | What it says                                                                                                                                                                                                                        |
| ------------------ | ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `RESULT_TRUNCATED` | any collection                                  | The result is not exhaustive. It accompanies exit 3 and `complete="false"`, and carries the resume token where one exists.                                                                                                            |
| `POSSIBLE_DUPLICATE` | `issue create`, `issue clone`                 | An identical request succeeded within the last 60 seconds and this one carried no idempotency key.                                                                                                                                   |
| `UNKNOWN_LABEL`    | `issue list`, from `--label` and `--not-label`   | No issue on this site carries that label. The query still runs and still exits 0.                                                                                                                                                    |
| `SCOPE_MISMATCH`   | `issue list`, `issue activity`, `issue changes` | A raw `--jql` selects a project the effective scope excludes, so those rows cannot come back. The query still runs and still exits 0.                                                                                                 |
| `UNKNOWN_CHANGED_FIELD` | `issue history`, from `--changed-field`    | No recorded change on that issue touched any of the named fields. It names the fields the issue does hold, and still exits 0.                                                                                                         |
| `EMPTY_RESULT`     | any collection, when it is complete and holds no rows | The bounds the zero-row answer was computed over: the row count, the context scope or `scope=none`, and any bound the command resolved rather than the caller typed. Still exits 0.                                                    |

`UNKNOWN_LABEL` exists because an empty answer to a mistyped label and an empty
answer to a correct one are the same bytes: `--label retyr` returns a header, no
rows, `complete="true"`, and exit 0, and so does `--label retry` on a day
nothing carries it. Asking about a label nobody uses is a legal question with a
correct answer, so it is not refused; not being able to tell the two apart is
what was wrong, and one line on stderr is the whole of the fix.

What it does **not** say is that a label it stayed quiet about will match. The
check is site-wide and the query may be scoped to a project, so a label alive
somewhere else is not reported. It costs one request per distinct label plus
one more only when it is about to warn, those requests count against
`--max-requests` like any other, and nothing is cached: a label exists exactly
as long as an issue carries it. Where the check cannot be made at all, whether
the route is absent, the site reports no labels, or the request failed, nothing
is said rather than something guessed.

## Scope, and what a global flag reaches

Thirteen flags are declared on the root and inherited by every command, and
they are not all the same kind of thing. `--format` decides how an answer is
printed. `--project` decides what the answer **is**, on the commands that read
it, and nothing in the result says so.

`jr schema <command>` reports them in a `<global-flags>` block, and each one
carries an `affects` attribute for **that command**:

```xml
<global-flag name="project" type="string" required="false" repeatable="false" affects="result">
  <usage>project key, overriding the context's</usage>
</global-flag>
<global-flag name="format" type="enum" required="false" repeatable="false" affects="invocation">
  ...
</global-flag>
```

| `affects` | Means |
| --- | --- |
| `result` | The flag narrows what this command's result set is, and nothing in the result document says it was in effect. |
| `provenance` | The flag decides which Jira answers. The envelope's `site` attribute reports which one did, so it is never silent. |
| `invocation` | The flag changes how the command runs or how its answer is printed, not what the answer is. |

It is per command-and-flag, not per flag: `--project` is `result` on
`issue list` and `invocation` on `auth token`, which does not read it.

**A global a command shadows is not listed.** `meta createmeta` declares a
`--project` of its own, so cobra routes the value there and omits the global
from `--help`; `<global-flags>` omits it for the same reason, and the flag
appears among the command's own `<flags>` where it belongs.

`affects="result"` is what an agent should branch on when it needs to know
whether its result set was computed over the whole site. Today the only two
flags that ever carry it are `--project` and `--board`.

### The envelope says which scope was used

`jr schema` says a flag *can* narrow this command. The envelope says whether it
*did*:

```xml
<result kind="issue.list" v="8" site="https://jira.example/jira" project="ENG">
```

**It reports the scope the command read, not the scope the context holds**, and
the difference is the whole design. `jr issue list --all-projects` asks the
context for no project, so its envelope names none — and a document that named
`ENG` there would be describing a frame its rows did not come from, which is
this attribute's own failure mode inverted. `jr --project OPS issue list`
against a context set to `ENG` names `OPS`, because that is what the query used.

A command that never consults the context emits neither attribute. `jr version`
and `jr issue get ENG-1` name no scope: the first has no Jira and the second was
told exactly which issue.

**TSV carries no envelope and so carries neither**, exactly as it carries no
`site`. That is the same limitation `complete` has there, and the reason
truncation becomes a stderr warning plus exit 3 in that format.

Neither attribute moves any kind's schema version. The envelope is written
outside every registered kind shape — `jr contract` describes payloads — which
`make golden` confirms by not moving a byte of `testdata/kinds`.

### `EMPTY_RESULT`

`SCOPE_MISMATCH` covers the case where the caller typed the contradiction
themselves. This covers the case where they typed nothing, so there was nothing
to compare and nothing was said.

A populated collection describes its own frame. The rows name their projects and
their dates, so somebody who narrowed the question by accident can see it in what
came back. Zero rows is the one result with no data left to infer a frame from,
and it is byte-identical whatever the frame was.

    jr issue activity --since 2026-09-02 --user currentUser   # context project = ENG
    → EMPTY_RESULT: 0 events, project=ENG, since=2026-09-02T05:00:00Z, user=<id>

Three kinds of note, and the rule for what earns one is whether `jr` chose it:

- **the count**, always, as `0 <collection>`;
- **the scope**, read through the same watcher the envelope uses, so the warning
  and the document cannot disagree. A command that lifted the scope with
  `--all-projects` reports `scope=none` rather than omitting the note, because
  an unscoped sweep and a sweep whose scope nobody reported are opposite answers
  and a missing key cannot tell them apart;
- **bounds the command resolved**, contributed per command. `issue activity`
  reports the instant a bare `--since` became, which is read in the Jira
  account's timezone rather than the caller's and appears on no envelope in any
  format, and the account a `--user currentUser` resolved to.

A flag the caller typed literally never appears. `--kind comment` is still on
their command line, and repeating input at somebody cannot tell them anything
they did not already know.

XML, JSON and YAML already carry the scope on the envelope. The warning is
written in every format anyway: the resolved bounds are on no envelope at all,
and a warning that appeared under some formats and not others would be a second
rule to learn. Exit 0 stays, because an empty answer is an honest one. Not being
able to tell what it was empty *about* is what was missing.

### `SCOPE_MISMATCH`

`jr` holds both halves of one contradiction inside a single process: the literal
`GOV-208` in the caller's `--jql`, and `project = IDO` in its own context. It
ANDs them, correctly and as documented, into a query that cannot match by
construction, and answers with a complete, empty, exit-0 result — the same bytes
as an honest "nothing matched".

    jr issue list --jql 'key = OPS-208'      # context project = ENG
    → SCOPE_MISMATCH on stderr, exit 0

It reads the fragment as **tokens**, so the text inside `summary ~ "key = OPS-1"`
is a value and does not warn, and it fires only on *positive* selection:
`project = OPS` and `key in (…)` count, `project != OPS` and `project not in (…)`
do not. A caller excluding a project is not asking for it.

Exit 0 stays, because the query is legal and the rows that came back are real.
`--all-projects` lifts the scope and silences it; so does naming the scope you
meant with `--project`.

Two things it does **not** say, in the same shape `UNKNOWN_LABEL` uses:

- **It compares against the effective scope, not the site's catalogue.** A
  renamed project key still resolves on the server, so `key = OLD-1` against a
  scope of `NEW` is a mismatch by string comparison and not by meaning. That is
  the one false positive it can produce; closing it would cost a request on
  every invocation carrying a `--jql`, and a line of stderr is the cheaper side
  of that trade.
- **It says nothing about whether an in-scope project will match.** Naming the
  project you are scoped to never warns, and such a query can still return
  nothing for every other reason a query returns nothing.

Over the 22 distinct `--jql` fragments in this repository's README, docs, skill
assets, and command examples, exactly one warns against a scope of `ENG`, and it
is `key = OPS-1`, which genuinely cannot match.

## Documents and mixed content

Long text is emitted as a child element, never an attribute, and never escaped
beyond XML minimums:

````xml
<description format="markdown"><![CDATA[## Repro

```go
client.Do(req)  // returns err == nil on 5xx
```
]]></description>
````

`format` is one of `markdown` (ADF converted), `adf` (raw JSON, with
`--raw-body`), or `wiki` (Server/DC).

Nothing is added around the section. The value a consumer parses out of the
element is the value the tool holds, byte for byte, and **there is nothing to
strip** — no leading newline, no trailing newline, no indentation before the
closing tag. The opening tag, `<![CDATA[`, the value's first character, and the
closing `]]></description>` all sit against each other, which is why the example
above is not pretty-printed the way the rest of the document is.

That costs a blank line before a fenced code block when a human reads raw XML.
The alternative costs every consumer a trim that a general XML parser does not
do for it, on the path that carries descriptions, comment bodies, worklog
comments, and dry-run request bodies. This document tells you to parse the
output; it cannot then hand you a value with the writer's own framing attached.

A literal `]]>` inside the text is split across two CDATA sections
(`]]]]><![CDATA[>`), which is the only way to carry that sequence.

### Carriage returns in XML

A carriage return is written as `&#13;`, in element text and in attributes
alike. Inside CDATA, where a numeric reference would be five literal
characters, the section is closed and reopened around it:
`before]]>&#13;<![CDATA[after`.

This is not cosmetic. XML 1.0 §2.11 requires a processor to translate `\r\n`
and any lone `\r` to `\n` _before parsing_, so a raw carriage return on the wire
reaches a consumer as a newline — the value it reads is not the value Jira
holds. Escaping is the only spelling that survives, and it applies inside CDATA
too, because the normalization runs on the raw input rather than on parsed
content.

Newline and tab are **not** escaped in element text. Neither is altered by
§2.11 there, and escaping them would make every multi-line description
unreadable for no fidelity gained. Both _are_ escaped in an attribute value,
where a separate rule — attribute-value normalization — turns each into a
space.

The other three formats were never affected: TSV escapes `\r` as `\r` because a
record is one line, and JSON and YAML carry it through their own escaping. A
value that means one thing in `--format json` and another in `--format xml` is
the contract splitting along an axis nobody declared, which is what this rule
prevents.

### ADF converted to markdown

Cloud stores a body as an Atlassian Document Format document. It is reported as
markdown, and **the conversion is lossless or it fails**: a document holding
something markdown cannot represent exits 2 with `ADF_UNREPRESENTABLE`, naming
the construct and where in the document it sits. Nothing is approximated, and
nothing is dropped to make a document fit.

`--raw-body` emits the document exactly as Jira sent it, with
`format="adf"`. It is the escape hatch for a refusal and for anything that
needs the original bytes.

Five Jira constructs have no CommonMark or GFM spelling. Each is written as a
link with a documented scheme, so the value survives the conversion and a
consumer can recognise it:

| ADF       | Markdown                                                       |
| --------- | -------------------------------------------------------------- |
| `mention` | `[@Ada Lovelace](jira-user:557058:abc)`                        |
| `media`   | `![alt](jira-media:<collection>/<id>)`                         |
| `status`  | `[Blocked](jira-status:red)`                                   |
| `date`    | `[2026-08-06](jira-date:1785974400000)`                        |
| `panel`   | `> [!WARNING]`: GitHub alert syntax, with ADF's own panel type |

A `media` node that carries a URL rather than an id, an external or linked
image, keeps that URL instead. An `inlineCard`, `blockCard`, or `embedCard`
becomes the bare URL it points at. An `emoji` becomes the character it stands
for, or its `:short-name:` where it has no character.

Presentation is not content and is dropped deliberately: a panel keeps its type
and loses its colour, an image keeps its id and alt text and loses its layout
and width, a status keeps its text and colour and loses its local id. Markdown
has no page, so there is nowhere for a position on one to go.

Three more things move rather than being dropped or refused. A line break at the
very start or end of a block is discarded, because markdown cannot write one
there and Jira does not render one either, it is what pressing shift-enter at
the end of a paragraph leaves behind. And whitespace at the edge of an
emphasised span moves outside it. Markdown cannot emphasise a leading or trailing
space, `* x*` is an asterisk and a word, not a span, and Jira's editor
produces one whenever somebody bolds a word and then types the space after it.
The space is written outside the delimiters instead. Every character is
unchanged and so is what a reader sees; only the extent of the mark moves, by
exactly the whitespace nobody can see it on. And a GFM table is rectangular, so a
table row holding fewer cells than the header row is padded to the header's
width with empty ones: the cells it gains are empty, so nothing a reader sees is
invented, and the row keeps the shape the header says the table has. A row
holding *more* cells than the header is refused rather than padded, and the
asymmetry is the point: those cells hold content somebody wrote, GFM has nowhere
to put them, and writing the table without them is the silent truncation this
contract exists to prevent.

Everything else is refused by name. That includes underlined, coloured,
superscript, subscript, aligned, indented, and annotated text; collapsible
sections; multi-column layouts; decision lists; macros and extensions; custom
panels, whose colour is content; table cells that span rows or columns, hold
more than a single paragraph, or sit in a table with no header row; a table row
holding more cells than its header row, which GFM discards rather than writes;
and any
node type or mark this build does not know. A node-level JSON field the schema
does not define is refused too, rather than ignored, ignoring one converts a
document while silently leaving part of it out.

Link destinations use CommonMark's angle-bracket form
(`[text](<https://example.invalid/a(b)>)`) where the URL holds a bracket, a
space, or an angle bracket. Percent-encoding is not used, because a `%28`
already in the URL and one this tool wrote are the same three characters, so
an address holding a line ending, and an attachment id holding the `/` that
separates it from its collection, are refused rather than encoded one way.

Emphasis picks between the `*` and `_` spellings so that its delimiters never
run together with a neighbouring span's. The choice is made over the whole
inline run rather than one span at a time: a span whose neighbour is also
emphasis takes the underscore and leaves the asterisk for it, because an
underscore is inert between word characters and the neighbour may need the
asterisk to close at all. Three spans against each other cannot all be given
the asterisk that way, so the run is spelled a second time with each span
reading the delimiter actually beside it, and `_a_**b**_c_` is written rather
than refused.

Which mark opens a span, how far it reaches, and which of the two characters it
is written with are searched over the whole run rather than fixed one position
at a time. Any of the three can be the reason something later has no spelling,
and the third is invisible from where it is made: a nested span written `**x**`
is correct in itself and can leave the span around it with no spelling at all,
where writing it `__x__` costs nothing and leaves the asterisk free. The search
returns the first complete answer in the order the spans would otherwise have
been taken, and only reaches for a second character once nothing else works, so
a document that converts is unaffected by any of it.

A spelling the writer's own rules refuse is offered to the reader before it is
given up on. Two of those rules are approximations of how a reader pairs
delimiters rather than rules in their own right, and the last thing the writer
tries is dropping them, writing the candidate, reading it back, and comparing it
with the document it started from. A candidate the reader does not agree with is
not written.

Where no candidate reads back as what the document says, the conversion is
refused rather than written down and hoped over. That refusal is still real: a
document can hold marks markdown has no arrangement for, and `--raw-body` emits
it exactly as Jira sent it.

Strikethrough has no such choice: it is `~~`, and two of them with nothing
between them are four tildes to a reader rather than the end of one span and
the start of another. So a strike is never written narrower than the mark it
came from. Where that leaves no way to write it, the document is spelled with
another mark on the outside instead, `*~~a.~~*~~b~~` rather than a strike cut
in two, and where the span has no other mark to put there it is refused. What
reaches this is a strike over emphasis markdown itself cannot spell: emphasis
ending in punctuation with a word character after it.

**The markdown a body converts to is a fixed point.** Reading it back and
writing it again gives the same characters, so the body you read out of
`issue get` is the body you get by piping it back in. That is not free: one
conversion of a document is not always stable, because a mark on whitespace is
dropped when that whitespace lands at the edge of a span, and which span an edge
belongs to is decided while writing. Two mark runs that overlap without nesting
force a cut, the cut can leave a marked space at the head of what is left, and
only one such space lands there per conversion, so a document with two of them
took three conversions to stop moving.

The conversion settles before it returns, and it settles only through a document
it is still carrying exactly. What a settling conversion sheds is a mark on a
space, which the paragraph above already says moves outside its span. Anything
else and the first conversion's text stands: a text node holding a newline is
written with the newline, and although reading that back joins the lines with a
space the way a soft break does, the newline is a character and settling never
buys stability with one.

## Types

XML has no attribute types, so **every attribute is a string** in JSON and YAML,
and so is every element's text. Four fields are exceptions, promoted to their
natural types because they are the ones a caller branches on:

| Field               | Where                                   | Type            |
| ------------------- | --------------------------------------- | --------------- |
| `v`                 | result and diagnostic envelopes         | number          |
| `count`             | result envelope, and any list container | number          |
| `complete`          | result envelope                         | boolean         |
| `exit`, `retryable` | diagnostic envelope                     | number, boolean |

**A list is always an array.** An empty list is `[]`, never an absent field, so
a consumer never has to distinguish "none" from "not applicable". A list
container's `count` is derived from its children and cannot disagree with them.

### The reporter is reported

`issue.list` v8 and `issue.get` v9 carry a `reporter` element, on the same terms
as `assignee`: always present, and empty when the server discloses nobody.

It was asked for on every request from the first version of this tool — it is in
the default field set — parsed, and then rendered in no format at all, so
`--reporter ada` filtered on a value nothing could show. Found by asking why
`--field reporter` did nothing, which it also did.

### `--age` adds a column, and never changes one

`jr issue list --age` and `jr issue get --age` append an `age` element rendering
how long ago the issue was last updated — `3 hours`, `14 days` — beside an
`updated` that is untouched and still RFC 3339 in UTC.

That shape is the point. Rendering the timestamp itself as "3 hours" would break
every consumer that parses it, silently, and for the caller who put the flag in
a shell alias months earlier. Appending instead costs a consumer nothing and
leaves both forms available in one row, which is the same bargain `--url` makes.

```console
$ jr issue list --age | cut -f4,6
updated                 age
2026-08-10T14:30:31Z    3 hours
```

It is coarse and in one unit, with no "ago" — the column says that already — and
**it stops at days**. A month has no fixed length and a year has two, so a
coarser unit would mean whichever divisor this tool happened to pick, and a
reader could not tell which. `412 days` is longer to read and is the number this
tool actually knows.

An issue with no `updated` gets no age rather than `0 seconds`, which would
claim it had just been touched. A timestamp ahead of the local clock reports the
floor rather than a negative age: that would be reporting clock skew, which is
not what the column is about.

### Timestamps out are UTC; dates in are not

Every timestamp this tool **emits** is RFC 3339 in UTC. That is a property of
the output and not of the server: an issue's `created` and `updated` are not RFC
3339 on either deployment. They arrive as `2026-08-11T16:37:31.272+0000` from a
recorded Data Center and as `2026-08-06T11:30:39.194-0500` from a recorded Cloud
site — an offset with no colon in both, differing in the zone it names and not
in the spelling. Normalizing here is what makes them one shape.

Every date a caller **sends** is evaluated by Jira, in the timezone on the
**Jira account's profile**. Not UTC, and not the clock of the machine running
`jr`, which has no way to tell the server what it is. This applies to
`--created-after` and every date flag beside it, to a raw `--jql`, to
`startOfDay()` and the rest of the `startOf`/`endOf` family, and to a bare
literal like `2026-08-10 00:00`.

Measured against a Cloud site on 2026-08-10, from a host running UTC, for an
issue created at `2026-08-10T14:02:37Z`:

```
--created-after "2026-08-10 09:02"   matches
--created-after "2026-08-10 09:03"   does not      ⇒ the literal clock is UTC-5
--created-after 'startOfDay("+9h")'  matches
--created-after 'startOfDay("+10h")' does not      ⇒ startOfDay() is 05:00:00Z
```

The account said `America/Chicago`, which in August is UTC−5, and the two agree.

So `--created-after startOfDay()` means "since 05:00Z" for that caller: a
request for _today_ that silently omits the first five hours of it, reported
`complete="true"` at exit 0. There is nothing wrong with the result — it is the
answer to the question Jira was asked.

**`jr user me` reports the zone**, as `timezone` on `user.get` v2, and that is
the whole of the fix. The dates are passed through rather than resolved here on
purpose: `startOfWeek()` carries Jira's own notion of when a week starts, and a
client that computed an instant would be substituting its own. Where a caller
means their own day rather than the account's, the way to say so is an absolute
literal converted into the account's zone — `docs/recipes.md` has the
conversion.

### A minute is not accepted on every field

`updated >= "2026-08-12 18:13"` parses on both deployments. `worklogDate` does
not, on one of them. Measured 2026-08-14 against Data Center 10.4 and the Cloud
sandbox:

| Clause                              | Data Center | Cloud    |
| ----------------------------------- | ----------- | -------- |
| `updated >= "2026-08-10 00:00"`     | accepted    | accepted |
| `worklogDate >= "2026-08-10 00:00"` | **refused** | accepted |
| `worklogDate >= "2026-08-10"`       | accepted    | accepted |
| `worklogDate >= "-7d"`              | accepted    | accepted |

Data Center says so itself:

```
Date value '2026-08-10 00:00' for field 'worklogDate' is invalid.
Valid formats include: 'YYYY/MM/DD', 'YYYY-MM-DD', or a period format
e.g. '-5d', '4w 2d'.
```

So `--worklog-after` and `--worklog-before` refuse a time of day on Data Center,
with `INVALID_DATE` at exit 2, before the request is spent. They accept one on
Cloud, because Cloud accepts one: the rule is the field and the deployment
together, and a blanket refusal would invent a limit half the installed base
does not have.

### A relative period is spelled two ways, and `M` is both of them

Every date flag also takes a period relative to now. It is spelled one way as a
value on a date field and a different way as a duration argument to a date
function, and the two disagree. Measured 2026-08-14 against Cloud and Data
Center 10.4, identical on both:

|          | on a field, `--updated-after -7d`      | in a function, `--updated-after 'endOfDay(-1d)'` |
| -------- | -------------------------------------- | ------------------------------------------------ |
| units    | `m` `h` `d` `w` `M`                    | `y` `M` `w` `d` `h` `m`                          |
| case     | insensitive: `-7D` is `-7d`            | **sensitive**: `-1D`, `-1W`, `-1H`, `-1Y` refused |
| `M`      | **minutes**                            | **months**                                       |
| `y`      | refused                                | accepted                                         |
| compound | `-4w 2d` accepted                      | refused                                          |

**`M` is the row to read twice.** `--updated-after -1M` is one minute and
`--updated-after 'endOfDay(-1M)'` is one month, and neither spelling is a
mistake at the server. There is no seconds unit in either.

A compound period carries its sign once, on the front, and its components sum:
`-1w 7d` selects exactly what `-14d` selects. A sign on any later component is
refused, and so is a run-on with no space:

```
-4w 2d      accepted
-1d 2h 3m   accepted, and any number of components
-4w -2d     refused by Jira
4w2d        refused by Jira
```

`jr` refuses what Jira refuses in each grammar, with `INVALID_DATE` at exit 2
and before a request is spent, and accepts everything else. It carried one
grammar for both until 2026-08-14, which made it refuse `endOfDay(-1y)` that
Jira accepts and refuse `-7D` and `-4w 2d` that both deployments accept.

### `issue activity --since` is the one date resolved here

Every other date flag is a clause in a query, and the server evaluates it.
`issue activity` cannot work that way: comments are not searchable in JQL on
either deployment, so three of its four event kinds are matched in this process,
and `--since` has to bound the events as well as the issues the query selected.
An issue updated yesterday holds comments from years ago.

So this one flag is resolved locally, and what that costs is different per form:

| Form                       | How it resolves                                                          | Requests |
| -------------------------- | ------------------------------------------------------------------------ | -------- |
| `-7d`, `+30m`, `2w`, `-4w 2d` | An offset names an instant, which is the same in every zone. A compound sums its components, here as well as at the server. | none     |
| `2026-08-10`, with or without a time of day | A wall clock, read in the **account's** zone, because that is the clock Jira reads it in. | one GET to `/myself` |
| `startOfWeek()` and any other function | Refused. See below.                                           | none     |

A function is refused rather than approximated because computing one means
choosing the day a week starts on, and the paragraph above is the reason not to.
The alternative shipped first and was worse than either: the bound reached the
query, the events were compared against nothing, and the feed reported itself
`complete="true"` at exit 0 while carrying events from outside the window.

| Code                       | Exit | Meaning                                                                                                                                                  |
| -------------------------- | ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `UNBOUNDABLE_DATE`         | 2    | `--since` names no instant this process can compute: a date function, on the one command that has to compare dates itself. `remedy` names the forms that work. |
| `NO_ACCOUNT_TIMEZONE`      | 2    | An absolute `--since`, and the site did not report the account's zone. Both deployments send one, so this is a site breaking its own contract; reading the literal as UTC anyway would be wrong by the offset with nothing in the output to say so. |
| `UNKNOWN_ACCOUNT_TIMEZONE` | 2    | The site named a zone this build cannot resolve. The zone database is compiled in, so this is a name no database has.                                     |

All three are exit 2 and not 9. A missing field is malformed data, which is what
9 means, but 9 is also `retryable`, and a second attempt reads the same profile
and fails the same way. What the caller has is a combination this site cannot
support and a remedy that works on the next invocation.

## TSV escaping

Every record is one line and every field is one column. Within a field:

| Character       | Emitted as |
| --------------- | ---------- |
| `\`             | `\\`       |
| tab             | `\t`       |
| newline         | `\n`       |
| carriage return | `\r`       |

Split on `\t` and `\n` with no defensive code. A column path that does not
resolve produces an empty cell, but no shipped column has such a path, and
`TestEveryColumnNamesAValue` refuses one that does. A column whose path walks to
an element with no text of its own can only ever be blank, which is a column
that cannot show what its header says.

### Lists in a cell

XML and JSON carry a list as a list. TSV has one cell per column, so a column
over a list flattens: values are joined with `,`, and a `,` or `\` inside a
value is escaped with a backslash.

| Character | Emitted as |
| --------- | ---------- |
| `\`       | `\\`       |
| `,`       | `\,`       |

This is applied before the TSV escaping in the table above, so a consumer
unescapes the cell first and then splits on a comma not preceded by a
backslash. A status named `Ready, Set` arrives as one value rather than
silently becoming two.

Only `project statuses` uses this today, for its `statuses` column.

A record rendered as TSV becomes two columns, `field` and `value`, one row per
leaf. Field names use the same path syntax as column definitions: `/` for
nesting, `@` for an attribute, and a `[i]` suffix when a name repeats among
siblings.

## Exit codes

| Code | Name         | Meaning                                                    |
| ---- | ------------ | ---------------------------------------------------------- |
| 0    | `OK`         | Success, result complete                                   |
| 1    | `ERROR`      | Generic runtime failure                                    |
| 2    | `USAGE`      | Bad flags, missing required input, unsupported combination |
| 3    | `PARTIAL`    | Succeeded but result set was truncated                     |
| 4    | `AUTH`       | Missing, invalid, or expired credentials                   |
| 5    | `NOT_FOUND`  | Referenced issue/project/board does not exist              |
| 6    | `PERMISSION` | Authenticated but not authorized                           |
| 7    | `CONFLICT`   | Precondition failed, transition invalid, version mismatch  |
| 8    | `RATE_LIMIT` | Throttled after retry budget exhausted                     |
| 9    | `REMOTE`     | Jira returned 5xx or malformed data                        |
| 10   | `BLOCKED`    | Refused by local policy (read-only mode, missing `--yes`)  |

Codes are stable forever. New conditions get new codes; an existing code never
changes meaning. The table is frozen in `internal/exitcode/exitcode_test.go`.

An error with no structured status exits 1, never 0.

## Errors

Errors go to stderr in the requested format, always with a machine-stable
`code`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<error v="1">
  <code>JQL_SYNTAX</code>
  <message>Unclosed quote in --jql at position 34</message>
  <detail>project = ENG AND summary ~ "unclosed</detail>
  <remedy>Quote the whole expression in single quotes, or escape inner double quotes.</remedy>
  <retryable>false</retryable>
  <exit>2</exit>
  <exit-name>USAGE</exit-name>
  <request-id>2f1c9a4e-0b77-4f0e-9d3a-1a2b3c4d5e6f</request-id>
</error>
```

`retryable` exists so an agent does not burn its budget retrying a syntax error,
and does not give up on a 503. It is `true` only for `RATE_LIMIT` and `REMOTE`.
It is always present, never omitted when false.

`detail` and `remedy` are present when there is something useful to say.
`request-id` is present for any failure that reached Jira.

### Errors about scope

| Code          | Exit | Meaning                                                                                                                                                                                                                                                     |
| ------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `EMPTY_SCOPE` | 2    | `--project ""` or `--board ""`. An empty scope does not lift the scope: it falls back to the context's, so the query runs against a project the caller did not name and returns a complete, empty, exit-0 result. Refused rather than silently reinterpreted. |

It is raised on the **flag** and never on the environment. An empty
`JIRA_PROJECT` is a shell that has the variable exported and unset, which is a
configuration rather than a request; refusing it would make an environment
variable a landmine, which is the reasoning `--format` already follows.

### Resolution failures

A value naming something on the server, a field, and in time a user or a
transition, is resolved against the site before the request is built, never
sent for Jira to reject. The refusal carries the candidates, because an error
that only says "unknown" leaves the caller to go and read a catalogue to find
their typo.

| Code                   | Exit | Meaning                                                                                                                                                                                                                                          |
| ---------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `UNKNOWN_FIELD`        | 2    | No field by that id, name, or clause name. `detail` lists the near misses, each with its id.                                                                                                                                                     |
| `AMBIGUOUS_FIELD`      | 2    | Several fields share that name. `detail` lists every candidate with its id; pass the id.                                                                                                                                                         |
| `INVALID_FIELD`        | 2    | The field resolved, but its id cannot be an element name or collides with one the command already emits.                                                                                                                                         |
| `UNRENDERABLE_FIELD`   | 2    | The field resolved and is a subresource, not a value: a comment thread, a worklog, an attachment list, an issue link, or a counter like `votes`. `remedy` names the command that reads it. Distinct from `INVALID_FIELD`, which is about the id. |
| `UNKNOWN_TRANSITION`   | 2    | The issue offers no such move _right now_. `detail` lists every transition it does offer, with its id and destination.                                                                                                                           |
| `AMBIGUOUS_TRANSITION` | 2    | Two transitions share that name and lead to different statuses. `detail` lists both.                                                                                                                                                             |
| `UNKNOWN_ISSUE_TYPE`   | 2    | The project offers no such type. `detail` lists the ones it does. It used to be 2 or 5 depending on which lookup answered; the type name is now always resolved before the fields are fetched, so there is one path and one code.                |
| `AMBIGUOUS_ISSUE_TYPE` | 2    | Several types share that name; pass the id.                                                                                                                                                                                                      |
| `UNKNOWN_USER`         | 2    | No user with that display name, email, or id. `detail` lists the plausible near misses with their ids, and is absent where the search returned nothing that shares a word with what was typed. A partial match is a near miss, not a resolution. |
| `AMBIGUOUS_USER`       | 2    | Several users share that display name. `detail` lists every candidate with its id, whether the account is inactive, and whether it is an app rather than a person.                                                                               |
| `UNKNOWN_PROJECT`      | 5    | The project does not exist, or this credential may not create in it. Reported for either status the createmeta route answers an unaddressable project with, a 10.3 Data Center says 400, and 404 is equally plausible elsewhere.                 |
| `FIELD_NOT_KV`         | 2    | `--field` or `--field-json` on a write was given something that is not `id=value`. On a write `--field` sets a field; on `issue get` and `issue list` it selects a column, and only the second takes a bare name.                                |
| `FIELD_HAS_A_FLAG`     | 2    | The field resolved and a typed flag already writes it. `message` names the flag. Two spellings of one write is a silent last-one-wins, so it is refused rather than resolved.                                                                    |
| `FIELD_TYPE_UNSUPPORTED` | 2  | The field resolved and `--field` will not guess at its schema type. `message` carries the type, `detail` carries Jira's own type key where the site reported one, and `remedy` names `--field-json`.                                            |
| `FIELD_NOT_A_NUMBER`   | 2    | A number field was given a value that is not a number. Refused locally, against the site's own catalogue, so it costs no write.                                                                                                                  |
| `FIELD_NOT_JSON`       | 2    | A `--field-json` value did not parse. `detail` is the decoder's own words.                                                                                                                                                                       |
| `DUPLICATE_FIELD`      | 2    | One field was named twice: twice through `--field` where the field is not an array, or once through each flag. Repeating `--field` on an array field is how an array is built and is not this.                                                   |

Field resolution costs one request against a cold cache and none against a warm
one; a command that names nothing to resolve makes no extra request at all.

**Transitions are never cached.** They depend on the issue's current status, so
a stored copy answers the question as it stood when it was stored. Create
metadata _is_ cached, because it changes when an administrator edits a screen
rather than when an issue moves. An `UNKNOWN_TRANSITION` therefore lists the
whole available set rather than near matches: a move missing from it is far more
often blocked from the current status than misspelled.

An unparseable `--format` still produces a readable error: the diagnostic falls
back to XML rather than failing twice.

### A command that writes bytes instead of a document

Two invocations write raw bytes to stdout and emit no result document. Both are
opt-in by flag, neither is a default, and the exception is narrow on purpose: a
document and something that is not one on the same channel means one corrupts
the other.

`jr issue attachment download --output -` writes the file. Writing to a path is
the ordinary case and emits `issue.attachment.download` saying what was written,
where, and how many bytes, counted while writing, so if it disagrees with the
size the listing reported, this is the one that happened.

`jr auth token --header` writes one line, `Authorization: <value>`, and a
newline. Without the flag the command emits `auth.token` like any other record.
It exists because no format in this contract is safe to capture whole: all four
are documents, and `TOKEN=$(jr auth token)` interpolated into a header is a
malformed header that curl reports as status 000, which is what it also reports
for a network failure. `--header` makes capturing the whole output correct:

```sh
curl -H "$(jr auth token --header)" ...
```

It is not a `--format`, so the four formats stay four, and nothing generic
learns to emit a bare value. `--header` together with an explicit `--format`
is `HEADER_AND_FORMAT` and exit 2: they name two different outputs, and
accepting both would mean ignoring one. `JIRA_FORMAT` is not grounds for that
refusal, because it is set once for a whole shell.

A caller with no stdout to spare, `mcp serve`, where bytes would land on the
JSON-RPC stream as a frame the peer cannot parse, gets `NO_STDOUT` and exit 2
rather than a corrupted session. Both invocations refuse there.

### Verdicts

A command whose whole product is a judgement reports it and exits 0, even when
the judgement is negative. `jr jql validate` on a query that does not parse
exits 0 with `valid="false"` and the reasons attached.

That is deliberate and it is the opposite of the rule everywhere else, so it is
worth being explicit. An exit code cannot carry a list, and the reasons are what
the command is for, an agent checking a query before it acts needs to know
which field was wrong and where. Exiting non-zero would suppress stdout and
collapse Jira's own error text, positions included, into a single line of prose.

Branch on the attribute, not the status. A non-zero exit from one of these means
the question could not be answered at all: no credential, no network, a 500.

The verdict also records who reached it. `method="parse"` is Cloud's parse
endpoint, `method="search"` is Data Center's zero-row search, and
`method="local"` means this tool decided without asking, the query did not lex,
or its parentheses did not balance. The three are not the same claim, and a
consumer that treats them as one is trusting a lexer with a question only the
server can answer.

A verdict can be `valid="true"` and still carry `warning` elements, so read the
document rather than only the attribute. Jira warns when it will run the query
and something in it names nothing on this site, most often a value that does not
exist for the field: a user who has left, or was never here, or was typed wrong.
The clause is legal and matches nothing, which is why it is a warning and not a
refusal, and why `valid="true"` with a warning and `valid="true"` without one
lead to different next steps. Both deployments report this. Cloud did not until
2026-08-14, because the parse endpoint was asked in a mode that withholds it.

`jr doctor` is the second command of this class and the clearer case for the
rule. It reports eight checks, one per layer: configuration, credential, site,
transport, deployment, clock, account, and rate limits. Each is `ok`, `failed`,
or `skipped`, and it exits 0 whenever it ran, whatever it found. A diagnostic that exited non-zero
on a finding would make "did the diagnostic run" and "is this configuration
healthy" the same signal, and telling those two apart is the only reason to run
it. Branch on `doctor/@status` for the roll-up, or on one check's `@status` for
the layer you care about.

**A failed check carries `code`, `detail`, and `remedy` on stdout, and none of
that is an error.** They are the strings the failing layer would have produced
had an ordinary command hit it, reported as data rather than raised, so
`docs/troubleshooting.md` answers a code found in either place. The rule that
errors go to stderr is unchanged: nothing here is an error. A non-zero exit from
`jr doctor` means the command itself could not run, which is a bad flag or a
format it cannot write, and never a finding.

`skipped` is a third answer that `jql validate` has no need of, and it exists
because eight checks in a stack fail together. A check whose input never arrived
names the check it was waiting on rather than repeating one cause eight times,
so the first `failed` reading down the document is the one to act on.

### Errors about reaching the site

`NO_SUCH_ENDPOINT`, `NETWORK`, `TIMEOUT`, `MALFORMED_SERVER_INFO`,
`UNKNOWN_DEPLOYMENT`, and `OFF_SITE_URL` carry where the site came from in
their `detail`: `the site came from context "work"`, `from --site`, or
`from JIRA_SITE`.

Three things can supply a site and which one won is visible nowhere else, so
"the site is not reachable" used to require a second command, `jr context
show`, before it could be acted on. It is an addition to the detail and never
a replacement: the endpoint that failed is still the first thing there.

Nothing else carries it. An error that explains everything explains nothing, and
"which site was that" is the next question for a connection failure and not for
a mistyped flag.

### Refusals the server sends

Most of the above are decided before a request goes out. Two are not, and both
are worth naming because the server's own answer sends the caller to the wrong
place.

| Code                  | Exit | Meaning                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| --------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `SPRINTS_REFUSED`     | 2    | Jira refused a sprint listing for that board. Only a scrum board has sprints, and a 400 whose remedy reads "check the request" sends somebody looking at their flags. `detail` keeps the server's own message, so the likely cause is offered without being asserted.                                                                                                                                                                                                                                                                |
| `AUTH_SCHEME_REFUSED` | 4    | The instance does not accept the credential's scheme at all. Jira Data Center 11 disables HTTP Basic by default and answers every call `403 Basic Authentication has been disabled on this instance` — with no header saying so, which is why the body is what identifies it. Distinct from `UNAUTHORIZED`, which is a credential this instance would accept and did not recognise, and from `FORBIDDEN`, which is exit 6 and means the account lacks a right. The remedy is a personal access token; no permission change can help. |

### Idempotency

A mutating command that carries an idempotency key records `(site, key)` before
it sends anything, and the outcome afterwards. A repeat with the same key
returns the original result rather than making a second one.

| Code                      | Exit | Meaning                                                                                                                                                                                                |
| ------------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `IDEMPOTENCY_KEY_REUSED`  | 7    | The key was already used for a different request — another operation, or on `issue move` another issue or another transition. Answering one with the other's result would be worse than refusing.      |
| `INVALID_IDEMPOTENCY_KEY` | 2    | 1 to 128 characters of letters, digits, and `. _ : -`.                                                                                                                                                 |
| `LEDGER_INVALID`          | 1    | The ledger could not be parsed. It is refused rather than ignored: everywhere else an unreadable cache is a miss because the cost is a round trip, and here the cost is a duplicate issue.             |
| `LEDGER_LOCKED`           | 1    | Another run is writing the ledger and did not finish.                                                                                                                                                  |
| `LEDGER_LOCK_STOLEN`      | 1    | This run was presumed dead while it held the ledger, so another run broke its lock and this run's write may have been lost. The request may or may not have been sent; read the issue before retrying. |

An attempt that claimed a key and then died leaves the claim pending, and a
retry inside `idem.StaleClaim` is **refused** rather than allowed. The first
request may have been processed, so "I do not know" has to behave like "it
happened", allowing it is the duplicate this exists to prevent. Past that
window the claim is handed over, and the caller is told the earlier attempt's
outcome is unknown rather than being left to assume nothing happened.

Without a key, an identical request that succeeded within 60s produces a
structured **warning** on stderr and nothing else. It is not blocked: two
deliberate identical creates, or a second pass around a workflow loop, are
legitimate things to want, and a caller who did not ask for idempotency does not
silently get it.

`issue create`, `issue clone`, and `issue move` accept `--idempotency-key`.
`issue edit` and `issue assign` do not need one: both are `PUT`s, so the
transport already replays them after an upstream error and setting a field twice
sets it once. A transition is the mutation that is not idempotent, and
`issue.move` v3 marks a replayed one `replayed="true"` on the `issue` element,
the way `issue.create` v1 does.

A replayed move sends **nothing at all**, including the read that resolves the
transition name. By the time a caller retries, a transition that did apply has
left the issue somewhere that name is no longer offered from, so resolving it
first would answer a safe retry with `UNKNOWN_TRANSITION`. The key is therefore
bound to the issue and the transition as typed: a second run naming either
differently is refused rather than answered with the first one's result.

### Mutations

Every mutating command accepts `--dry-run`, requires the `write` build tag, and
declares exit 10. A reader binary does not contain them at all, that is the
linker's guarantee, not a runtime check.

Read-only mode and the missing-confirmation refusal are enforced in the CLI
layer from the command's declaration, not by each command, so a verb cannot ship
having forgotten them. Both happen before any network call, so a blocked command
costs nothing and cannot half-happen.

The two are relaxed differently for `--dry-run`, and the asymmetry is
deliberate. A missing `--yes` is a step the caller has not taken yet, so a
preview is allowed, you look at the request in order to decide whether to
confirm it. A read-only context is a statement about what that context is _for_,
so the latch stays one-way and a dry run is refused too.

`--dry-run` emits kind `dry-run` v2: a `requests` list holding every request the
command would send, each with its method, path, query, and body verbatim. Each
is built from the same `transport.Request` the command was about to send, so the
preview and the real thing cannot drift, and a body can be pasted into `curl`.
It never carries a credential, the document renders each request as the command
built it, before the transport attaches one.

The list is a list even when it holds one request, which is all but two of the
mutating commands. A shape that varied with the count would be a shape every
consumer has to branch on. v1 was a bare `request` record, and it was true of
every command until `epic add` on Cloud became one request per issue.

**A write that applies to several things can stop in the middle.** `epic add`
and `epic remove` on Cloud set the parent field on each issue in turn, because
that field is what carries epic membership on both project styles and it has no
batched spelling. So `epic.add` v2 and `epic.remove` v2 carry `requested` and
`applied` counts on the container and a `status` on each issue — `moved`,
`failed`, or `not-attempted` for the ones after the failure, which were never
sent. A run stops at the first failure.

This is the one case where a command writes a result document to stdout _and_
exits non-zero. The exit is the failing request's own, not a new code and not
exit 3: exit 3 means a truncated result set, which a write does not have. The
rule it bends — a failing command writes nothing at all to stdout — is right for
a command that did nothing and wrong for one that did half the work, because a
caller told only `PERMISSION` assumes nothing happened and something did.

### Preconditions

`issue edit`, `issue move`, and `issue assign` accept `--if-unchanged`, which
refuses the write if the issue has changed since the caller read it. Without it
two callers editing one issue both exit 0 and the earlier write is lost, with
nothing truncated, nothing in error, and nothing to say it happened.

`issue.get` v9 carries a `precondition` attribute, which is what the flag takes.
It is opaque: what it holds is the millisecond timestamp Jira served, and the
`updated` element is RFC 3339 to the second, so conditioning on the published
value would leave a whole second in which another edit is invisible. It also
names the issue and the deployment, so one from another issue or another site is
refused rather than compared.

**A baseline knows the second or the millisecond, and says which.** The two
endpoints it can come from do not agree. Measured on Data Center 10.4.0, for one
issue at one moment: the search returned `2026-09-03T21:57:13.000+0000` and the
issue endpoint returned `2026-09-03T21:57:13.679+0000`. The search answers from
the index and the index keeps only the second. Cloud returns the millisecond
from both.

So a baseline from `issue get` is millisecond-precise, one from
`issue list --precondition` or from a plan is second-precise, and the token
carries which. The comparison is as sharp as the token and no sharper.

What that costs, stated rather than implied: two edits inside one second are
indistinguishable to a listing's baseline. That window is the server's, and the
alternative was what shipped in 0.11.0 and 0.12.0, where a listing's baseline
was compared at a precision it never had and refused **every** write on Data
Center.

`issue.list` v8 carries the same attribute per row, but only with
`--precondition`, and it is absent otherwise. The flag exists because the
arithmetic without it is bad: "list the blocked issues, edit the three that
matter" otherwise costs one request for the listing and one `issue get` per row
to obtain a baseline the listing already held. A row carries Jira's own
`updated`, which is all a token is minted from, so the flag spends bytes rather
than requests. It is off by default because most listings are not read by a
caller about to write, and §2.4 says no field appears unless it was requested.

A token from a row and a token from `issue get` are the same value for the same
version of the same issue: both are minted from the `updated` the server sent.
Neither is a promise that the issue has not changed since. That is what
`--if-unchanged` checks, one round trip before the write.

An issue Jira reports no `updated` for gets no token from either command. The
attribute is absent rather than empty, because a baseline that matches anything
is worse than no baseline at all.

**The check is not atomic, and says so.** Neither deployment offers a validator
on an issue: there is no `ETag`, no `Last-Modified`, and `PUT /issue/{key}`
honours no `If-Match`. So the check is a read, a comparison, and then the write,
and it has a window one round trip wide. A verb that ran one records it:

```xml
<result kind="issue.edit" v="2" site="https://acme.atlassian.net">
  <issue key="ENG-101" action="edited">
    <precondition method="read-compare"/>
  </issue>
</result>
```

`method` is an enum, and `read-compare` is its only value today. It is published
rather than assumed because a conditional request the server evaluates and a
read-then-write are not the same promise, and a caller entitled to know which
one they got should not have to infer it from the absence of a claim. The
element is absent when the flag was not passed; it never appears saying a check
did not happen.

| Code                       | Exit | Meaning                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| -------------------------- | ---- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `READ_ONLY`                | 10   | A context, `--readonly`, or `JIRA_READONLY` forbids changing Jira. It is a one-way latch within an invocation: nothing a command does turns it off, and `JIRA_READONLY=0` does not clear a context that was created read-only. Changing what a context is for is a separate act, `context edit --unset readonly`.                                                                                                                                                                                                                                                                                                                                                                       |
| `CONFIRMATION_REQUIRED`    | 10   | A destructive command was run without `--yes`. Not raised for `--dry-run`: a preview is not the thing being confirmed, and you look at it in order to decide.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `IDEMPOTENT_IN_FLIGHT`     | 7    | Another run holds this key and has not finished; it may already have done the work.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `STALE_WRITE`              | 7    | `--if-unchanged` was given a precondition and the issue has changed since it was taken, so nothing was sent. `detail` carries both versions. This is the §6.3 stale write: without the flag the later write silently overwrites the earlier one and both callers exit 0.                                                                                                                                                                                                                                                                                                                                                                                                                |
| `INVALID_PRECONDITION`     | 2    | `--if-unchanged` was given something this tool did not issue: not a token at all, one naming no issue, one for another issue, one minted against the other deployment, or an empty value. An empty one is refused rather than read as an absent flag, because a row with no `updated` carries no token and a loop hands that empty cell straight to the flag; omitting the flag still writes unconditionally, which is a different request. Refused rather than compared, because comparing a foreign value would report "the issue changed", which is a claim about this issue that nobody checked. Everything but the deployment is settled locally, so a typo costs no round trip.                                                                                                                                                                                                                                                                     |
| `INVALID_ENCODING`         | 2    | Text that is not valid UTF-8. It is refused, never repaired: substituting U+FFFD would put a character in Jira the caller never wrote.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `CONFLICTING_LABEL_FLAGS`  | 2    | `--label` replaces the whole set, so it cannot be combined with `--add-label` or `--remove-label`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `AMBIGUOUS_LINK_DIRECTION` | 2    | A link type's name was given where a direction was needed. `"Blocks"` reads either way; `detail` offers both phrasings.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `UNKNOWN_LINK_TYPE`        | 2    | No relationship by that phrase. `detail` lists every phrase the site offers, because link wording is customizable.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `INVALID_DURATION`         | 2    | Not a Jira duration. The format is a count of `w`, `d`, `h`, or `m`, largest first. Nothing is converted between them: a working week is a site setting.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `SELF_LINK`                | 2    | Both ends of a link are the same issue.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `SELF_EPIC`                | 2    | An epic was named as one of the issues to move into it.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `SELF_PARENT`              | 2    | An issue was named as its own parent. Settled locally, so the cycle costs no round trip.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `NOTHING_TO_EDIT`          | 2    | An edit was given nothing to change, `issue edit` with no field, `context edit` with no setting.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `CONFLICTING_EDIT`         | 2    | `context edit` was asked to set and clear the same setting. Both at once has no single right answer, and picking one would make the result depend on an implementation detail nobody can see.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `JQL_NOT_UNDERSTOOD`       | 2    | Jira does not understand a raw `--jql`, and would answer it with no rows. Cloud's search endpoint returns HTTP 200 and an empty result for a query it knows is meaningless, which is indistinguishable from an honest "nothing matches", so the query is checked before the command runs and `detail` carries Jira's own words. A warning refuses as firmly as an error: an unknown *value* is what Jira reports as a warning, and it is the same wrong answer as an unknown field. |
| `UNCONSTRAINED_QUERY`      | 2    | `issue list --limit all` with no filter would page until the instance is exhausted and return every issue in every project the credential can see. The default bound makes an unfiltered query harmless, one request, fifty rows, so only the pairing is refused. `--all-projects` is how to mean it.                                                                                                                                                                                                                                                                                                                                                                                   |
| `INVALID_API_VERSION`      | 2    | `--api-version` accepts 2 or 3. Cloud serves v3; Data Center serves v2.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `TOO_MANY_ISSUES`          | 2    | More issues than the agile API moves at once. It is refused rather than split across requests: two requests can half-succeed, and the outcome would be neither moved nor not moved.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `DESTINATION_EXISTS`       | 7    | A download would replace a file that is already there. It refuses rather than overwriting, because a download that silently replaced a file is indistinguishable from one that worked, and the file it replaced is not recoverable. `--force` allows it.                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `OFF_SITE_URL`             | 1    | A URL the server supplied points outside the configured site, another host, another scheme, or outside the context path, and this tool will not follow it. Data Center reports an attachment's content as an absolute URL; following it on trust is how a credential reaches a host nobody chose. The check does not depend on how the URL is spelled: `//host/path` names a host while carrying no scheme, and a path beginning with `/` is resolved against the site's origin, so both are held to the same rule as an absolute URL. The refusal never echoes the URL, one can carry userinfo or a signed parameter, and names only the part that differs.                            |
| `UNBOUNDED_RESPONSE`       | 1    | A streamed response ran past 2 GiB without ever declaring a length. A body carrying a `Content-Length` is limited to it by the HTTP client, and one that ends early fails — so a download either arrives whole or fails, and neither needs this. A response with no declared length is bounded by nothing at any layer, and it also cannot be checked for completeness, so it is refused rather than written. Not retryable: a server that streams without declaring a length will do the same next time. The ceiling does not apply to a _declared_ length, because capping one would refuse an attachment somebody legitimately stored.                                               |
| `RESPONSE_TOO_LARGE`       | 1    | A buffered response body is larger than the 64 MiB this client will hold. It is refused rather than clipped: reading to a limit and stopping returns the first 64 MiB with no error at all, so the caller would be handed part of an answer presented as the whole of one, and a JSON consumer would then report that Jira sent something unreadable when what happened is that this client stopped reading. Not retryable, for the reason `UNBOUNDED_RESPONSE` gives: a body too large once is too large again. Narrow the request, or ask for the resource that streams.                                                                                                              |
| `UNRENDERABLE_VALUE`       | 1    | A value in the result holds a character no output format can carry, most of C0, `U+FFFE`, `U+FFFF`, or a byte that is not valid UTF-8. XML 1.0 forbids these outright, so escaping is not available: `&#1;` is no more legal than the raw byte. The refusal does not depend on `--format`, even though JSON and YAML could encode one, because the flag chooses an encoding and not what this tool is willing to say. Distinct from `INVALID_ENCODING`, which is exit 2 and is a value the _caller_ supplied: this one comes back from Jira, so the caller has done nothing to correct. The message names the field and `detail` names the record holding it, by `key`, `id`, or `name`; in a streamed collection the rows before it are already on stdout and `remedy` says how many. |
| `UNSAFE_FILENAME`          | 1    | A download with no `--output` takes its destination from the filename the server reports, and that filename is not one. A name carrying a directory separator, a parent reference, or an absolute path would put the bytes somewhere nobody asked for; Data Center reports the filename on the attachment itself, so the value is the server's rather than the caller's. The name is still reported in full by `issue attachment list`, refusing to _write_ it is not refusing to _say_ it. Pass `--output <path>` to name the destination yourself. Exit 1 and not 9: the server returns the same filename next time, so retrying cannot help, and 9 publishes a refusal as retryable. |
| `BODY_NOT_REPLAYABLE`      | 1    | A retry needed the request body again and could not get it, a body read from a pipe cannot be sent twice. The request fails rather than going out short, because a second attempt carrying nothing would be accepted as a successful upload of an empty file.                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `TRANSITION_TAKES_NO_COMMENT` | 2 | `--comment` was given for a transition whose screen has no comment field. Jira does not refuse this: Data Center takes the transition and discards the comment, answering 204, so the caller is told a comment was added that does not exist. Refused before the transition is sent, from the field list `expand=transitions.fields` already returns. `jr meta transitions` shows which transitions accept one. |
| `SPRINT_NOT_ACTIVE`        | 7    | Only a running sprint can be closed. The sprint is read first, so the wrong state costs one read and no mutation.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `SPRINT_NOT_FUTURE`        | 7    | Only a planned sprint can be started. The two wrong states get different remedies: an active sprint is already running and can be closed, and a closed one cannot be reopened by any API.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `SPRINT_HAS_NO_DATES`      | 2    | `sprint start` on a sprint that has no window, with none supplied. Jira will not run a sprint that has no dates, so this is the server's rule refused a round trip earlier. It names only the half that is missing: a sprint created with both dates needs neither flag.                                                                                                                                                                                                                                                                                                                                                                                                                |
| `INVALID_SPRINT_DATE`      | 2    | `--start` or `--end` is not an RFC 3339 timestamp. A bare date names no time and no zone, and choosing one would decide when somebody's iteration begins on their behalf. An offset is accepted and normalized to UTC, which is a spelling of an instant rather than a different instant.                                                                                                                                                                                                                                                                                                                                                                                               |
| `INVALID_SPRINT_WINDOW`    | 2    | The sprint would end before it starts. Jira refuses the same thing — "the start date of a sprint must be before the end date" — so this is that verdict without the round trip. On `sprint start` the pair checked is the effective one, so `--end` alone can be backwards against a start date the sprint already holds.                                                                                                                                                                                                                                                                                                                                                               |
| `INVALID_SPRINT_NAME`      | 2    | `sprint create` was given no name, or one that is entirely whitespace. Anything else is accepted: a sprint name is free text, it never reaches a URL path, and what a team calls its iteration is not this tool's business.                                                                                                                                                                                                                                                                                                                                                                                                                                                             |

### Plans and applies

`issue edit` takes more than one key only through a plan, and there is no other
path to a bulk write. `--plan-out <file>` writes `issue.plan` v1 and sends
nothing; `--apply <file>` runs one and emits `issue.apply` v1. Both documents
also go to stdout, so a caller with no shell to redirect with, which is every
caller over MCP, still gets the document every other command produces.

```console
$ jr issue edit ENG-101 ENG-102 ENG-103 --add-label triaged --plan-out plan.xml
$ jr issue edit --apply plan.xml
```

**A plan costs one request, whatever its row count.** Each row carries the
baseline `--if-unchanged` would take, and all of them come from a single
`key IN (...)` search rather than an `issue get` each. Fifty rows is the cap,
and it is about how much a person reads rather than about what the API can
carry: a plan nobody read is the failure this surface exists to prevent.

**A plan carries intent, never requests.** A row names an issue, its baseline
and its idempotency key; the change is written once because a plan applies one
change to many issues. `--apply` rebuilds every request through the same builder
the single-issue edit uses, so nothing in the file becomes a method, a path or a
body. That is deliberate and it is the reason the shape is what it is: a plan of
recorded requests would make any file executable input carrying the caller's
credential.

**A failing row does not stop the run.** Every row is attempted, and each is
reported `applied`, `skipped` or `failed` with its own error `code`. This is the
opposite of `epic add`, which stops at the first failure and reports the rest
`not-attempted`, and the difference is deliberate: `epic add` moves a set into
an epic as one coherent operation, and a plan is a batch of independent edits
that happen to share a change. One stale ticket is no reason to abandon
thirty-nine others.

**A partial apply writes its document and exits non-zero.** The exit is the
cause's own, not a new code: `7` for a stale row, `6` for permission, `8` for a
rate limit. There is no "partial write" exit code, because `3` means a truncated
*result set*, which a write does not have, and minting a second one would
flatten the reason into a number that cannot tell a caller whether backing off
would help. The document says which rows landed, and it reaches stdout, which is
the single exception to "a failing command writes nothing to stdout".

When more than one row fails, the exit is the first failure that is **not**
`STALE_WRITE`, and the first otherwise. A stale row is the ordinary outcome of
two people touching one ticket; a permission error or a rate limit is systemic
and makes every row after it suspect.

**Re-running an apply is the resume, and takes no flag.** Each row claims the
ledger under the idempotency key the plan recorded, so a row that already landed
is reported `skipped` and no request is sent. An idempotency key means "do this
at most once"; honouring that only when asked would make the default the
duplicate.

| Code                       | Exit | Means                                                                                                                                                                                                                                                                          |
| -------------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `BULK_NEEDS_A_PLAN`        | 2    | More than one key was given without `--plan-out`. The convenient spelling is refused on purpose: it would give up the reviewable document, and then nobody writes a plan again.                                                                                                   |
| `INVALID_PLAN`             | 2    | The file `--apply` was given is not a plan this build can run: not a plan document, a version this build does not read, a plan for another verb, no rows, more rows than the cap, one issue twice, a row whose key is not an issue key, or an idempotency key the ledger cannot record. Refused rather than read leniently, because what this reader accepts becomes an edit sent with your credential. |
| `NO_BASELINE`              | 2    | A row was planned for an issue Jira reported no `updated` for, so there is no baseline to check it against. Reported per row: the run continues and that row is not written, because writing it would be the unchecked write `--if-unchanged` exists to refuse.                   |
| `PLAN_NOT_READ`            | 2    | The plan file could not be opened.                                                                                                                                                                                                                                              |
| `PLAN_NOT_WRITTEN`         | 1    | The plan could not be written to the path `--plan-out` named. Nothing was sent either way: a plan makes no mutating request.                                                                                                                                                     |
| `PLAN_TAKES_NO_KEYS`       | 2    | `--apply` was given issue keys as well. It takes its issues from the plan.                                                                                                                                                                                                       |
| `PLAN_CARRIES_THE_CHANGE`  | 2    | `--apply` was given field flags as well. Whether the flag overrode the plan or the plan won, the document somebody reviewed would stop being the change that runs.                                                                                                                |
| `PLAN_CARRIES_THE_BASELINE`| 2    | `--apply` was given `--if-unchanged` as well. The plan already holds one baseline per row.                                                                                                                                                                                        |
| `CONFLICTING_PLAN_FLAGS`   | 2    | `--plan-out` with `--apply`, or `--plan-out` with `--dry-run`. The first pair writes a plan and runs one; the second both send nothing and each names a different document, and a command emits one.                                                                              |
| `UNKNOWN_ISSUE`            | 5    | A key given to `--plan-out` is not an issue this credential can read. Refused while planning rather than recorded, because a plan is what you read *instead* of finding out at apply time.                                                                                        |

### The sprint lifecycle

A sprint is created, started, and closed, and the three verbs are gated
differently because their blast radii differ. `sprint create` and `sprint start`
need `write`; `sprint close` needs `write` **and** `admin`, because ending an
iteration returns every unfinished issue to the backlog and no API reopens one.
So an agent build can plan an iteration and begin one, and cannot end one.

Neither of the first two is destructive and neither takes `--yes`: starting a
sprint is undone by closing it, and a sprint that was never started holds
nothing to lose.

`sprint.create` v1 and `sprint.start` v1 report the sprint as the server now
holds it, plus an `action`, because both endpoints answer with the whole sprint
rather than a bare acknowledgement. The id in `sprint.create` is the only place a
caller learns what their new sprint was numbered.

```xml
<result kind="sprint.start" v="1" site="https://acme.atlassian.net">
  <sprint id="4" state="active" board="3" action="started">
    <name>Sprint 14</name>
    <goal>Ship the importer</goal>
    <start>2026-08-17T09:00:00Z</start>
    <end>2026-08-31T09:00:00Z</end>
  </sprint>
</result>
```

**The window belongs to the sprint, not to the request.** Jira will not run a
sprint that has no dates, but a sprint created with them starts from a body
carrying nothing but the state. So `--start` and `--end` are required exactly
when the sprint has nothing to supply, and `SPRINT_HAS_NO_DATES` names only the
missing half. Requiring them unconditionally would refuse a request the server
accepts.

Dates are RFC 3339 in and RFC 3339 out. What goes to Jira is what a read
reports, so the two can be compared without either being reformatted first.

### Body text on write

Text bound for a description or a comment is **contained, never converted**,
unless `--body-format` says otherwise.

| `--body-format`  | What happens                                              |
| ---------------- | --------------------------------------------------------- |
| `text` (default) | The text is contained, not interpreted.                   |
| `markdown`       | The text is parsed and becomes the document it describes. |
| `adf`            | The text is a document, as JSON, and is sent as given.    |

Under `text`, Data Center takes a string of wiki markup and gets the text as
typed. Cloud will not accept a string where a document belongs, so the text is
wrapped in the minimal document that holds it: a blank line starts a paragraph,
a single newline is a line break. Nothing is interpreted, `**bold**` reaches
Jira as six characters on both deployments, and the server decides what they
mean. That is the difference between containing text, which is exact, and
converting it, which is not.

`markdown` and `adf` are **Cloud only**. Data Center stores wiki markup, there
is no markdown-to-wiki converter here, and it will not take a document at all,
so both are refused with `BODY_FORMAT_UNSUPPORTED` rather than approximated.

Under `markdown` the subset is CommonMark's block and inline structure minus
what ADF has no node for, plus GFM tables, task lists, and strikethrough, plus
the `jira-` link schemes above, so a body read out of Jira goes back in as the
document it came from. A single newline inside a paragraph joins its lines,
because that is what markdown means by one; a trailing backslash is a hard
break.

### A raw query is checked before it runs

`--jql` is the one input this tool passes through rather than builds, so it is
the one with no floor under it. Everything reached through a typed flag has one:
`--field` resolves against the site's catalogue and every user-valued flag goes
through the user lookup.

Cloud's search endpoint answers a query it knows is meaningless with **HTTP 200
and no rows**. `--jql 'nosuchfield = 1'` used to come back well-formed,
complete, exit 0, and empty, which is indistinguishable from an honest "nothing
matches". So a command that sends a raw fragment asks Jira what it means first,
and refuses with `JQL_NOT_UNDERSTOOD` at exit 2 carrying Jira's own message.

It runs on both deployments, by the route each one has: Cloud's parse endpoint,
and on Data Center a search bounded to zero rows, which is the closest thing
available there. **A warning refuses as firmly as an error**, because an unknown
*value* is what Jira reports as a warning: `assignee = "nobody-xyz"` is valid
JQL naming nobody, and answering it with an empty result is the same wrong
answer as a misspelled field. It also makes `--jql` agree with `--assignee`,
which refuses a user it cannot resolve.

The check costs **one extra request, and only when `--jql` is present**. A
consequence worth stating: `--max-requests 1` together with `--jql` now fails,
because the check and the query are two requests.

**What it does not cover.** On Cloud, the operand of a `WAS`, `CHANGED TO`, or
`CHANGED FROM` predicate is validated by neither the parse endpoint nor the
search, so `--jql 'status was "NoSuchStatusXYZ"'` is still answered with a
complete empty result there. Data Center parses it and refuses. The gap is
Atlassian's rather than this tool's, and it is written here rather than implied
shut.

Emphasis is CommonMark's delimiter-run algorithm rather than an approximation
of it, flanking rules and the rule of three included, so a closing run is spent
across the spans that need it: `**bold *and italic***` is a strong span holding
an emphasised one. A scanner that looks for a closing run of one exact length
reads those three asterisks as belonging to nothing and hands back a paragraph
with the asterisks still in it, which is the failure mode this replaced.

Anything else exits 2 with `MARKDOWN_UNSUPPORTED`, naming the construct **and
the line it is on**: setext headings, indented code blocks, unclosed fences,
lazily continued blockquotes, aligned table columns, an image beside other
text, emphasis around an image or a mention, and a link with no address.
Where CommonMark and this parser would read the same text differently, it
refuses rather than choosing.

Some refusals are Jira's content model rather than markdown's, established by
posting each combination to a real site: `em`, `strong`, or `strike` on inline
code, and a blockquote, panel, table, rule, heading, or task list nested where
Jira will not store one. Jira's own answer to those is `INVALID_INPUT; comment:
INVALID_INPUT`, which names neither the node nor where it was.

**A link on inline code is accepted**, because Jira stores it. `code` takes no
formatting, and a link is not formatting: it is where the text points rather
than how it looks. The refusal used to cover it and to call it emphasis, which
made ``[`x`](url)`` a body this tool would write and then refuse to read.

Reading is the other direction and is described under [ADF converted to
markdown](#adf-converted-to-markdown): a Cloud body comes back as markdown, or
as the document itself with `--raw-body`, and a Data Center body comes back as
`wiki` untouched.

A create result carries `replayed="true"` when it came from the ledger rather
than from Jira. It is otherwise byte-identical to the original: a consumer
diffing two runs must not see a difference that means nothing.

## Stability policy

Every rule below says which position of the release version moves. That is the
question somebody actually has, and until 2026-08-18 this document answered a
different one: it classified a change as major or minor and left the reader to
apply a demotion two sections further down. Three releases got the wrong number
that way, so the classification and its consequence are now in the same clause.

**A change is either breaking or additive.** Breaking means an invocation a
caller already makes, or a document a consumer already parses, stops working or
changes shape. Additive means everything a caller does today still does the same
thing, and there is something new beside it.

While the release version starts with a zero, and it will for a long time:

| The change is | The release moves       | Example         |
| ------------- | ----------------------- | --------------- |
| breaking      | the **minor** position  | 0.8.0 to 0.9.0  |
| additive      | the **patch** position  | 0.8.0 to 0.8.1  |

The rows:

- Adding a new optional element or attribute: **additive, so the patch position
  moves.** It moves the kind's schema version as well, because `make golden`
  refuses a changed shape at an unchanged version, and that bump is not a second
  question: see the row below.
- A kind's schema version moving on its own: **it decides nothing.** Every kind
  carries its own version and those move independently of the release, which is
  what CHANGELOG.md tells a consumer to pin against. Price the release on the
  rows that describe what actually changed, and list the kind and its new
  version under **Output contract** so a consumer pinning `v` finds out. This
  row was missing until 2026-08-21, when three kinds moved for one added
  optional element and the only reading available made a patch look breaking.
- Making a required element or attribute optional: **breaking, so the minor
  position moves.** It reads like the row above and is its opposite. An addition
  is something no existing consumer looks for; this is something an existing
  consumer already reads, and it will now sometimes not be there.
- Adding a field to a command's _default_ column set: **breaking, so the minor
  position moves.** Agents diff output.
- Changing an exit code's meaning, an error `code` string, or a `kind`:
  **breaking, so the minor position moves.**
- Refusing an input that used to be accepted: **breaking, so the minor position
  moves.** Nothing about the output's shape moves, so a script still _parses_
  every document identically, and the invocation it was built around now exits
  2. This row was missing until 0.3.0, whose whole content is three date forms
  that used to be accepted and answered wrongly. Fixing a wrong answer is not a
  reason to spare the version: the caller has to change what they send either
  way, and the version number is the only place they find that out before it
  happens.
- Accepting an input that used to be refused: **additive, so the patch position
  moves.** It is the row above read backwards. Every invocation a script already
  makes still runs and still means the same thing; what changed is that a form
  which exited 2 now answers. The reader this matters to is the one who
  *worked around* the refusal, and the changelog is where they find it. The row
  exists because the two directions are not symmetric and a policy that names
  only one of them is silent rather than permissive about the other, which is
  how 0.3.0 nearly shipped as a patch.
- Moving a refusal earlier, so that the same input carries a different `code`:
  **breaking, so the minor position moves**, for the same reason as refusing one
  and against the same reader. `BAD_REQUEST` from Jira and `INVALID_DATE` from
  `jr` describe one mistake, and a consumer branching on `code` sees a string it
  has never seen. The exit is unchanged and that is not enough, because `code`
  is the field this contract tells people to branch on.
- Adding a warning `code`: **additive, so the patch position moves**, and it
  bumps no kind version. A warning is a separate document on stderr, so no
  existing consumer is reading for one it has never seen, and a command that
  gains a warning emits the same result it did before. Changing what an existing
  warning code means is **breaking**, for the reason an error code is.
- Populating an optional element the schema already declared, for inputs where
  it used to be absent: **additive, so the patch position moves**, and it bumps
  no kind version. The shape did not move: the element was in the schema, in
  `jr contract`, and in this document, and a consumer was already told it might
  be there. What moved is how often it is. This is the row the previous one read
  backwards from: a warning gaining a `code` is new information in a place
  nobody was reading, and this is new information in a place everybody was told
  to read and some inputs never filled. The reader it matters to is the one who
  inferred a rule from the silence, the way "no `warning` child means the query
  is clean" was true of every Cloud query before 0.3.3 and is now true only of
  clean ones. Emptying an element that used to be populated is **breaking**,
  because that reader's inference fails in the direction that loses information
  rather than gains it.
- Changing the text a command emits inside a field whose shape is unchanged,
  for inputs where that text was **not stable**: **additive, so the patch
  position moves.** Nothing a consumer parses changes shape, and the only
  documents affected are ones where two runs of the same command already
  disagreed with each other, so a consumer diffing them was already watching
  them move. Changing it for inputs where the text **was** stable is
  **breaking**, because that is a document somebody has recorded and diffs
  against, and it is the same reader the default-column-set row protects. This
  row did not exist until 0.9.1, whose markdown writer began settling a body
  before returning it: one conversion of a document was not a fixed point, and
  which of the two answers you got depended on how many times the text had been
  round-tripped. The number came out patch either way, and the row is here
  because a number that comes from an argument rather than a rule is how three
  releases got the wrong one.
- Changing an error's `remedy` or `detail` wording, with the `code` and the
  exit unchanged: **additive, so the patch position moves.** This is the row
  above read against a different field, and it comes out the other way. `code`
  is what this contract tells a consumer to branch on, and `remedy` is
  documented two sections up as present "when there is something useful to
  say": guidance for a person, whose whole job is to be better this release
  than last. Reading it under the stable-text row would make every reworded
  error message a breaking change, which would freeze the part of the output
  most worth improving. The row was written on 2026-09-04 for 0.13.1, whose
  `UNKNOWN_USER` remedy began naming `currentUser`, and the alternative
  reading was available: a remedy is deterministic for a fixed input, and
  twenty goldens do pin remedy text for other codes. What decides it is that
  those goldens pin a *document's shape* and this contract names `code` as the
  branch. **Moving text between `remedy` and `detail` is not this row** and is
  breaking, because that is a consumer reading a different field: see 0.6.0,
  where the near misses moved and the release took the minor.
- Adding a command: **additive, so the patch position moves.** No invocation
  anybody makes changes, no document anybody parses changes shape, and
  `jr schema` grows by a leaf. The reader it matters to is looking for a
  capability, and a capability is something you find in the changelog by name,
  not by noticing a digit moved. **This row did not exist until 2026-08-18 and
  its absence cost three releases**, because a new command is the most visible
  thing a release can contain and the demotion makes it look like the least.
- Adding a `kind`: **additive, so the patch position moves**, on the same
  terms. A kind nothing emitted before has no consumer to break: nothing
  dispatches on a name no command produced. **The exception is a kind a command
  that already exists starts emitting**, which is **breaking**, because a
  consumer dispatching on `kind` sees one it has never seen from an invocation
  it was already making. That is the same failure as changing a `kind`, arriving
  by addition, and it is why the row is about which command emits it rather than
  about the kind alone.
- `jr contract` (or `jr --contract`) dumps the machine-readable schema for every
  kind this build can emit, so a consumer can pin and verify. That is not a rule
  about versions; it is what makes the rules checkable.

### Why the positions are shifted, and what changes at 1.0.0

This project is in `0.y.z`, where
[semver §4](https://semver.org/spec/v2.0.0.html#spec-item-4) says the public API
is not to be considered stable and anything may change. So the whole scale sits
one position to the right of where semver puts it: breaking takes the minor,
additive takes the patch, and **the major position is not in use.**

Tagging 1.0.0 is a claim about how stable this tool intends to be from then on,
and no single breaking change is a reason to make that claim. It is also not a
countdown: after 0.9.0 comes **0.10.0**, which semver orders above it, and the
leading zero stays until somebody decides the contract is stable rather than
until the digits run out.

**Additive changes take the patch position so that a breaking one is visible.**
That is the whole reason for the shift. A consumer looks at one digit to decide
whether to read the changelog carefully, and if a new command and a changed
default column set both move the same digit, that digit has stopped telling them
anything. The cost is real and it is paid on purpose: `jr doctor` shipped in a
release whose number says "patch", which understates it to a human reading the
notes and is exactly right for a script.

**At 1.0.0 every row above moves one position left**: breaking takes the major,
additive takes the minor, and patch means a fix that changes nothing in this
document. The rows say "minor" and "patch" today because that is what is true
today; the release that tags 1.0.0 rewrites them in the same commit, and that is
the whole of the migration.

The kind versions are unaffected by any of this, and they are what a consumer
should actually be pinning. They are per-kind and start at 1, so `issue.get`
going 8 to 9 says exactly what changed for someone parsing `issue.get`, and says
nothing about the twenty-odd kinds that did not move. The release version tells
you a shape moved somewhere; `kind` and `v` tell you whether it was yours.

A Conventional Commit's `!` and its `BREAKING CHANGE:` footer mark the shape
change wherever it lands, and they do not decide where. `feat(issue)!:` in a
0.x release moves the minor position, and the footer is still how the changelog
and anyone reading `git log` find it.

### Three releases took the wrong position, and these rows are why

Checked against the tag history on 2026-08-18, when the two rows for a command
and a kind were written:

```
0.3.1  added an optional attribute            additive  patch  correct
0.3.2  accepted input that used to be refused additive  patch  correct
0.3.3  populated an optional element          additive  patch  correct
0.4.0  some bodies now refuse                 breaking  minor  correct
0.5.0  a --jql fragment now exits 2           breaking  minor  correct
0.6.0  a new optional element on two errors   additive  minor  should have been 0.5.1
0.7.0  a command, a kind, an optional element additive  minor  should have been 0.6.1
0.8.0  a command and a kind                   additive  minor  should have been 0.7.1
```

Each of the last three said in its own changelog section, in plain words, that
it was "minor rather than a patch" and that its content was additive. Nobody
misread the policy: the policy had no row for a new command or a new kind, so
each release reached for the nearest precedent, and the precedent was the
previous release that had done the same thing. **A rule that has to be assembled
from two sections gets assembled differently by the same person on different
days**, which is what those three numbers are.

Nothing is re-tagged. A published tag cannot be moved once anyone has fetched
it, the numbers only ever went up, and no consumer was misled in the dangerous
direction: every one of those releases claimed *more* change than it contained.
They are listed here because a policy that quietly disagrees with the history it
governs is worth less than one that says where it was not followed.

## Verifying against `jr contract`

`jr contract` v2 carries each kind's element schema alongside its name, version,
and emitters. v1 let a consumer pin a version; v2 lets it check a response
against the shape, which is the half §3.5 promised and the first version could
not deliver.

Each kind reports one element: its attributes with types, optionality, and any
closed set of values; its child elements with the same, plus whether each may be
absent or repeated; and its text, when it carries any.

The types are promises about the shape of the text, not JSON types, every
format here is textual. `int` parses as a decimal integer. `bool` is exactly
`true` or `false`, never `yes` or `1`. `timestamp` is RFC 3339 in UTC, which is
why Jira's own formats are normalized before they are emitted. `date` is
`YYYY-MM-DD` with no time, because Jira stores some dates without one and
inventing midnight in a timezone would be a value nobody set.

**An empty value satisfies every type.** "Present but empty" is a fact this tool
emits deliberately, an unassigned issue, a context with no default project,
and it is not the same as absent. A consumer checks for the element, then for
the value.

Some shapes are open, and say so. `issue list --field "Story Points"` adds a
`<customfield_10042>` element, and no fixed list can name it; those kinds carry
an `<extra>` element saying where the names come from. Every other kind is
closed, and an element outside the schema is a contract violation rather than a
curiosity.

**An optional element is still declared.** `issue.list` and `issue.get` carry a
`<url>` that appears only under `--url`. It is declared in the schema and so is
part of the contract — `jr contract` shows it, a consumer can branch on it —
and it is absent by default because §2.4 admits no field into the output that
was neither requested nor documented as a default. It is not a field Jira sends:
Jira's own `self` is the REST endpoint, which returns JSON, so this is built
from the deployment's `baseUrl`. A site that reports no `baseUrl` makes `--url`
exit 1 with `NO_BASE_URL` rather than producing a link built from a guess.

**A field the server did not send is absent, not defaulted.** An optional
attribute can be missing because this deployment has no such value, and "false"
and "not said" are answers a consumer has to be able to tell apart. Two
attributes were unconditional until 2026-08-11 and printed a default instead:

| Kind                                | Field        | Absent when                                                                    |
| ----------------------------------- | ------------ | ------------------------------------------------------------------------------ |
| `meta.transitions` v3               | `has-screen` | The server sent no `hasScreen`, which on Data Center is every transition        |
| `project.list` v2, `project.get` v2 | `private`    | The server sent no `isPrivate`, which on Data Center is every project           |

Both rendered `false` there, on the strength of a field the response does not
contain. A consumer branching on `has-screen="false"` skipped a form Jira would
have shown, and `private="false"` was an assertion about who can see a project
that nobody had asked the server.

Two more were already absent, and were documented as meaning something they do
not. Their shape is unchanged; what changed is the reading:

| Kind                       | Field                     | What absent means                                                                                                                                                     |
| -------------------------- | ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `board.list`, `board.get`  | `project`, `project-name` | The server did not place the board. Data Center sends no `location` on any board, so this is absent there for every board, including one plainly on a project. It does **not** mean "not on a project" |
| `project.components`       | `lead`                    | The server sent no lead. Data Center sends `assignee` and `realAssignee` on a component and never `lead`, so this is absent there for every component                  |

None of the four is recoverable by asking differently. `has-screen` is absent
under `expand=transitions.fields`, `expand=transitions`, `expand=hasScreen`, and
no expand at all; a board carries no `location` under `expand=location`,
`includeLocation=true`, `expand=projects`, or on `/board/{id}`; `isPrivate` is
absent with and without `expand=lead,description`. Verified against Jira
Software Data Center 9.12.38 and 10.4.0, which is both lines a customer is
likely to be on, and neither is a regression in one release.

The columns stay in the default sets on both deployments, so `project` on
`board list` and `lead` on `project components` are **empty cells** on Data
Center rather than missing columns. A TSV whose columns move between sites is
one no script can `cut` a field out of, and TSV has no way to say "absent"
anyway. To tell absent from empty, read a format with an envelope.

**The schema is checked on every document this tool writes.** That is the only
reason to trust it. `render.Write` validates before it emits a byte, so a
payload that does not match its published shape fails with `SCHEMA_VIOLATION`
and exit 1, and stdout stays empty. A schema that were merely published
alongside the code would describe the output as somebody once believed it to be.

## Golden files

The contract is enforced by golden files, not by review discipline:

- `internal/render/testdata/`, every writer, every payload shape. The envelope,
  the escaping rules, and the truncation signal, which do not vary between
  builds.
- `internal/cli/testdata/kinds/`, one file per kind and schema version, holding
  that kind's element shape as `jr contract` prints it. `issue.get.v2.xml` is
  the shape of `issue.get` at v2.
- `internal/cli/testdata/<profile>/`, end-to-end output for each built-in
  command, recorded once per shipped profile: `full`, `agent`, `reader`, `ci`.

`make golden` rewrites all of them, running the per-profile set under every
shipped tag set. **A diff in a golden file is a change every consumer sees.**
Bump the schema version of the affected kind in the same commit.

The split is not cosmetic. A kind's _shape_ is the same in every build that has
the kind, so it is pinned once and every profile compares against the same file.
What differs between builds is which kinds exist and which commands emit them,
and that is what the per-profile sets carry, `contract.tsv` is the inventory,
`schema.tsv` the command surface, `version.xml` the tag list.

**The version rule is mechanical, not remembered.** `make golden` refuses to
overwrite `<kind>.v<N>.xml` with different content: a changed shape at an
unchanged version cannot be regenerated, and the failure says to bump the
version instead. Doing so writes `<kind>.v<N+1>.xml` and leaves the old file as
the record of what that version was. `internal/lint` builds every shipped
profile and asserts each kind it emits has a golden, so a kind behind the
`write` tag cannot go unpinned just because the default suite is the `ci` build.

## Keeping this current

Update this document in the same change that alters any of:

- A `kind` or its schema version, **including the worked examples above**.
  `internal/lint/kindversions_test.go` compares every `kind="…" v="…"` printed
  in this file and in the README against `registry.Kinds`, the same source
  `jr contract` prints from. `make golden` pins each kind's _shape_ and refuses
  a changed shape at an unchanged version; nothing pinned the number where a
  reader looks for it, and the examples here had been stale for two bumps
  before the test existed. A document whose first instruction is "branch on `v`"
  cannot print a `v` no build has emitted.
- A command's default column set.
- The result, error, or warning envelope.
- An exit code's meaning, or the addition of a new one.
- An error `code` string, or the conditions under which `retryable` is true.
  The tables above are asserted against the source by
  `internal/lint/errorcodes_test.go`: a documented code has to exist, produce
  the exit printed beside it, and produce only that one. They are a curated
  subset, this tree emits some two hundred codes, so a code missing from them
  is one the contract does not promise, not one that is undocumented by
  accident.
- The format of `jr version`'s `release` string, or what `scripts/version.sh`
  accepts and refuses. The guarantee above is one a consumer can parse, and
  nothing used to tell the next person editing that script that a document
  depended on it. `internal/lint/version_test.go` asserts both, the four cases
  in repositories it builds, and every worked `jr 1.2.0 (…)` example in this
  file and in `docs/build-profiles.md` against the profiles the Makefile ships.
- The escaping rules of any writer, or the type-promotion table above.
- **A change nobody has a stability-policy row for.** Cutting a release is when
  that gap shows, and the answer has three times been the previous release
  rather than this document. Write the row in the same change as the behaviour,
  not in the changelog that needs it: a row costs a paragraph, and a precedent
  set in prose costs the next person the same half hour and can disagree with
  the rule nobody thought to check it against.
- Which format is the default for a content shape.
- What the ADF converter carries, drops, or refuses, in either direction,
  including the `jira-` link schemes, which are as much a part of the contract
  as an element name.
- Where the golden files live, or which builds they are recorded against.
