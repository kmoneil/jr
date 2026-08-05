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
*specification* emits nothing; a bad *row* leaves partial output and a non-zero
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

An unparseable `--format` still produces a readable error: the diagnostic falls
back to XML rather than failing twice.

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
