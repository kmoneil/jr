# Output contract

The output shape is a public API. It is versioned, and breaking it requires a
major bump.

This document describes what `jr` emits. It is the reference a consumer pins
against; `jr contract` emits the machine-readable form of the same thing.

## Streams

**stdout carries the result and nothing else.** Never a progress spinner, never
a warning, never a "Fetching…". A command that fails writes nothing at all to
stdout, so a consumer piping stdout never parses a half-result.

**stderr carries everything else**, always structured, always in the requested
format: errors, and the truncation warning that accompanies exit 3.

`--help` output goes to stdout, because it is what the caller asked for. It is
not a result document and carries no envelope.

## Formats

| Content                                          | Default | Rationale                                                               |
| ------------------------------------------------ | ------- | ----------------------------------------------------------------------- |
| Collections (`list`, `search`, `schema`)         | `tsv`   | Rectangular data is rectangular, and TSV is the cheapest encoding of it |
| Records and documents (`get`, `view`, `version`) | `xml`   | Mixed content, no escaping tax, self-describing                         |

All four formats — `tsv`, `xml`, `json`, `yaml` — are available on every
command. The defaults are a convenience; `--format` is the contract.
`JIRA_FORMAT` sets the default globally; `--format` overrides it. An
unrecognized value is exit 2 listing the valid ones, never a silent fallback.

## Envelope

Every successful XML response:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<result kind="issue.list" v="1">
  <issues count="3" complete="true">
    <issue key="ENG-101">
      <summary>Retry logic drops the last error</summary>
      <status category="in-progress">In Progress</status>
      <assignee id="712020:8f3a" display="Ada Lovelace"/>
      <updated>2026-08-04T11:32:07Z</updated>
    </issue>
  </issues>
</result>
```

- `kind` — stable identifier for the payload shape. An agent dispatches on it.
- `v` — schema version for that kind, incremented on breaking change.
- `complete` — **`true` only if the result set is exhaustive.** If a limit
  truncated it, `complete="false"` and `<next-page-token>` is present. There is
  no third state and no way to get a truncated set that claims to be complete.
  This is the single most important attribute in the format.

JSON and YAML hoist the envelope to the top level rather than transliterating
the XML tree:

```json
{
  "kind": "issue.list",
  "v": 1,
  "count": 3,
  "complete": true,
  "issues": [ … ]
}
```

TSV emits a header row and nothing else — no envelope, no counts.

## Streaming

**TSV streams. The structured formats buffer.**

A collection command writes rows as each page arrives, so a long paged run
produces output immediately rather than after its last request. That is not a
performance nicety: it is what lets `jr issue list --limit all | head -20` stop
early, and what leaves a caller who interrupts a hundred-request run with the
rows already fetched instead of nothing.

XML, JSON, and YAML cannot stream, because their envelopes carry `count` and
`complete` and neither is known until the last page lands. Those formats buffer
and emit once, exactly as before — streamed output for a given result is
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
carries only structured diagnostics is untouched — there is no structured form
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

## Documents and mixed content

Long text is emitted as a child element, never an attribute, and never escaped
beyond XML minimums:

````xml
<description format="markdown">
<![CDATA[
## Repro

```go
client.Do(req)  // returns err == nil on 5xx
```
]]>
</description>
````

`format` is one of `markdown` (ADF converted), `adf` (raw JSON, with
`--raw-body`), or `wiki` (Server/DC).

A literal `]]>` inside the text is split across two CDATA sections
(`]]]]><![CDATA[>`), which is the only way to carry that sequence.

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

## TSV escaping

Every record is one line and every field is one column. Within a field:

| Character       | Emitted as |
| --------------- | ---------- |
| `\`             | `\\`       |
| tab             | `\t`       |
| newline         | `\n`       |
| carriage return | `\r`       |

Split on `\t` and `\n` with no defensive code. A column path that does not
resolve produces an empty cell.

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

### Resolution failures

A value naming something on the server — a field, and in time a user or a
transition — is resolved against the site before the request is built, never
sent for Jira to reject. The refusal carries the candidates, because an error
that only says "unknown" leaves the caller to go and read a catalogue to find
their typo.

| Code                   | Exit   | Meaning                                                                                                                                                             |
| ---------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `UNKNOWN_FIELD`        | 2      | No field by that id, name, or clause name. `detail` lists the near misses, each with its id.                                                                        |
| `AMBIGUOUS_FIELD`      | 2      | Several fields share that name. `detail` lists every candidate with its id; pass the id.                                                                            |
| `INVALID_FIELD`        | 2      | The field resolved, but its id cannot be an element name or collides with one the command already emits.                                                            |
| `UNKNOWN_TRANSITION`   | 2      | The issue offers no such move _right now_. `detail` lists every transition it does offer, with its id and destination.                                              |
| `AMBIGUOUS_TRANSITION` | 2      | Two transitions share that name and lead to different statuses. `detail` lists both.                                                                                |
| `UNKNOWN_ISSUE_TYPE`   | 2 or 5 | The project offers no such type. `detail` lists the ones it does. Exit 5 when the answer came from a createmeta lookup, 2 when a type name was resolved before one. |
| `AMBIGUOUS_ISSUE_TYPE` | 2      | Several types share that name; pass the id.                                                                                                                         |
| `UNKNOWN_PROJECT`      | 5      | The project does not exist, or this credential may not create in it.                                                                                                |

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

### Verdicts

A command whose whole product is a judgement reports it and exits 0, even when
the judgement is negative. `jr jql validate` on a query that does not parse
exits 0 with `valid="false"` and the reasons attached.

That is deliberate and it is the opposite of the rule everywhere else, so it is
worth being explicit. An exit code cannot carry a list, and the reasons are what
the command is for — an agent checking a query before it acts needs to know
which field was wrong and where. Exiting non-zero would suppress stdout and
collapse Jira's own error text, positions included, into a single line of prose.

Branch on the attribute, not the status. A non-zero exit from one of these means
the question could not be answered at all: no credential, no network, a 500.

The verdict also records who reached it. `method="parse"` is Cloud's parse
endpoint, `method="search"` is Data Center's zero-row search, and
`method="local"` means this tool decided without asking — the query did not lex,
or its parentheses did not balance. The three are not the same claim, and a
consumer that treats them as one is trusting a lexer with a question only the
server can answer.

### Refusals the server sends

Most of the above are decided before a request goes out. One is not, and it is
worth naming because the server's own answer sends the caller to the wrong
place.

| Code               | Exit | Meaning                                                                                                                                                                                                    |
| ------------------ | ---- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `SPRINTS_REFUSED`  | 2    | Jira refused a sprint listing for that board. Only a scrum board has sprints, and a 400 whose remedy reads "check the request" sends somebody looking at their flags. `detail` keeps the server's own message, so the likely cause is offered without being asserted. |

### Idempotency

A mutating command that carries an idempotency key records `(site, key)` before
it sends anything, and the outcome afterwards. A repeat with the same key
returns the original result rather than making a second one.

| Code                      | Exit | Meaning                                                                                                                                                                                    |
| ------------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `IDEMPOTENCY_KEY_REUSED`  | 7    | The key was already used for a different operation. Answering one with the other's result would be worse than refusing.                                                                    |
| `INVALID_IDEMPOTENCY_KEY` | 2    | 1 to 128 characters of letters, digits, and `. _ : -`.                                                                                                                                     |
| `LEDGER_INVALID`          | 1    | The ledger could not be parsed. It is refused rather than ignored: everywhere else an unreadable cache is a miss because the cost is a round trip, and here the cost is a duplicate issue. |
| `LEDGER_LOCKED`           | 1    | Another run is writing the ledger and did not finish.                                                                                                                                      |

An attempt that claimed a key and then died leaves the claim pending, and a
retry inside `idem.StaleClaim` is **refused** rather than allowed. The first
request may have been processed, so "I do not know" has to behave like "it
happened" — allowing it is the duplicate this exists to prevent. Past that
window the claim is handed over, and the caller is told the earlier attempt's
outcome is unknown rather than being left to assume nothing happened.

Without a key, an identical request that succeeded within 60s produces a
structured **warning** on stderr and nothing else. It is not blocked: two
deliberate identical creates are a legitimate thing to want, and a caller who
did not ask for idempotency does not silently get it.

### Mutations

Every mutating command accepts `--dry-run`, requires the `write` build tag, and
declares exit 10. A reader binary does not contain them at all — that is the
linker's guarantee, not a runtime check.

Read-only mode and the missing-confirmation refusal are enforced in the CLI
layer from the command's declaration, not by each command, so a verb cannot ship
having forgotten them. Both happen before any network call, so a blocked command
costs nothing and cannot half-happen.

The two are relaxed differently for `--dry-run`, and the asymmetry is
deliberate. A missing `--yes` is a step the caller has not taken yet, so a
preview is allowed — you look at the request in order to decide whether to
confirm it. A read-only context is a statement about what that context is *for*,
so the latch stays one-way and a dry run is refused too.

`--dry-run` emits kind `dry-run` v1: the request itself, with its method, path,
query, and body verbatim. It is built from the same `transport.Request` the
command was about to send, so the preview and the real thing cannot drift, and
the body can be pasted into `curl`. It never carries a credential — the document
renders the request as the command built it, before the transport attaches one.

| Code                      | Exit | Meaning                                    |
| ------------------------- | ---- | ------------------------------------------- |
| `READ_ONLY`               | 10   | A context, `--readonly`, or `JIRA_READONLY` forbids changing Jira. It is a one-way latch; nothing turns it off. |
| `CONFIRMATION_REQUIRED`   | 10   | A destructive command was run without `--yes`. Not raised for `--dry-run`: a preview is not the thing being confirmed, and you look at it in order to decide. |
| `IDEMPOTENT_IN_FLIGHT`    | 7    | Another run holds this key and has not finished; it may already have done the work. |
| `INVALID_ENCODING`        | 2    | Text that is not valid UTF-8. It is refused, never repaired: substituting U+FFFD would put a character in Jira the caller never wrote. |
| `CONFLICTING_LABEL_FLAGS` | 2    | `--label` replaces the whole set, so it cannot be combined with `--add-label` or `--remove-label`. |
| `AMBIGUOUS_LINK_DIRECTION`| 2    | A link type's name was given where a direction was needed. `"Blocks"` reads either way; `detail` offers both phrasings. |
| `UNKNOWN_LINK_TYPE`       | 2    | No relationship by that phrase. `detail` lists every phrase the site offers, because link wording is customizable. |
| `INVALID_DURATION`        | 2    | Not a Jira duration. The format is a count of `w`, `d`, `h`, or `m`, largest first. Nothing is converted between them: a working week is a site setting. |
| `SELF_LINK`               | 2    | Both ends of a link are the same issue. |
| `SELF_EPIC`               | 2    | An epic was named as one of the issues to move into it. |
| `NOTHING_TO_EDIT`         | 2    | `issue edit` was given no field to change. |
| `TOO_MANY_ISSUES`         | 2    | More issues than the agile API moves at once. It is refused rather than split across requests: two requests can half-succeed, and the outcome would be neither moved nor not moved. |
| `SPRINT_NOT_ACTIVE`       | 7    | Only a running sprint can be closed. The sprint is read first, so the wrong state costs one read and no mutation. |

### Body text on write

Text bound for a description or a comment is **contained, never converted**.

Data Center takes a string of wiki markup and gets the text as typed. Cloud will
not accept a string where a document belongs, so the text is wrapped in the
minimal Atlassian Document Format document that holds it: a blank line starts a
paragraph, a single newline is a line break. Nothing is interpreted — `**bold**`
reaches Jira as six characters on both deployments, and the server decides what
they mean.

That is the difference between containing text, which is exact, and converting
it, which is not. Turning markdown into real ADF marks is a separate job with
its own failure modes, and when it lands an unrepresentable construct will be
refused by name rather than approximated.

Reading is unchanged: a body comes back with a `format` attribute of `wiki` or
`adf` and its content untouched.

A create result carries `replayed="true"` when it came from the ledger rather
than from Jira. It is otherwise byte-identical to the original: a consumer
diffing two runs must not see a difference that means nothing.

## Stability policy

- Adding a new optional element or attribute: **minor**.
- Adding a field to a command's _default_ column set: **major**. Agents diff
  output.
- Changing an exit code's meaning, an error `code` string, or a `kind`:
  **major**.
- `jr contract` (or `jr --contract`) dumps the machine-readable schema for every
  kind this build can emit, so a consumer can pin and verify.

## Golden files

The contract is enforced by golden files, not by review discipline:

- `internal/render/testdata/` — every writer, every payload shape.
- `internal/cli/testdata/` — end-to-end output for each built-in command.

`make golden` rewrites them. **A diff in a golden file is a change every
consumer sees.** Bump the schema version of the affected kind in the same
commit.

## Keeping this current

Update this document in the same change that alters any of:

- A `kind` or its schema version.
- A command's default column set.
- The result, error, or warning envelope.
- An exit code's meaning, or the addition of a new one.
- An error `code` string, or the conditions under which `retryable` is true.
- The escaping rules of any writer, or the type-promotion table above.
- Which format is the default for a content shape.
