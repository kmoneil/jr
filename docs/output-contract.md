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

### What the defaults cost

The split is per content shape rather than one format everywhere, and that was
settled by measuring rather than by taste. `issue list --limit 100`, rendered
from the same document in each format:

| Format | Bytes  | Tokens | vs TSV  | Tokens/row |
| ------ | ------ | ------ | ------- | ---------- |
| `tsv`  | 7,977  | 2,930  | 1.00x   | 29.3       |
| `xml`  | 35,030 | 12,755 | 4.35x   | 127.5      |
| `json` | 45,088 | 15,959 | 5.45x   | 159.6      |
| `yaml` | 33,085 | 12,866 | 4.39x   | 128.7      |

The same document as a single record, `issue get`:

| Format | Bytes | Tokens | vs TSV |
| ------ | ----- | ------ | ------ |
| `tsv`  | 592   | 218    | 1.00x  |
| `xml`  | 791   | 264    | 1.21x  |
| `json` | 842   | 295    | 1.35x  |
| `yaml` | 683   | 241    | 1.11x  |

A hundred rows is where framing compounds: a structured format spells every
field name once per row, and TSV spells it once for the whole result. That is
9,825 tokens saved per hundred issues against XML, or 77%. One record has one
of everything, so the multiple collapses to 1.21x — and what is left is that a
record carries nested and mixed content a rectangular format has nowhere to
put. The defaults follow the shape because the saving does.

**The saving is on five columns, not on the issue.** TSV emits the declared
columns; `created`, the issue id, the status category, the assignee's account
id and the labels are in the XML for every row and in the TSV for none of them.
`--format xml` is how you get them, and it is one flag.

Measured 2026-08-06 with `cl100k_base`, against a payload built from the
summaries Jira Cloud actually returned for the sandbox's sample project.
`o200k_base` differs by under 1% on every row above, which is the useful part:
the ratio is a property of the framing, not of whose vocabulary is counting.
Reproduce with `make cost`. The relationship the default rests on is asserted
by `TestFormatCostFavoursTSVForCollections`, which needs no tokenizer and no
network.

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

| ADF                | Markdown                                        |
| ------------------ | ----------------------------------------------- |
| `mention`          | `[@Ada Lovelace](jira-user:557058:abc)`         |
| `media`            | `![alt](jira-media:<collection>/<id>)`          |
| `status`           | `[Blocked](jira-status:red)`                    |
| `date`             | `[2026-08-06](jira-date:1785974400000)`         |
| `panel`            | `> [!WARNING]` — GitHub alert syntax, with ADF's own panel type |

A `media` node that carries a URL rather than an id — an external or linked
image — keeps that URL instead. An `inlineCard`, `blockCard`, or `embedCard`
becomes the bare URL it points at. An `emoji` becomes the character it stands
for, or its `:short-name:` where it has no character.

Presentation is not content and is dropped deliberately: a panel keeps its type
and loses its colour, an image keeps its id and alt text and loses its layout
and width, a status keeps its text and colour and loses its local id. Markdown
has no page, so there is nowhere for a position on one to go.

Two more things move rather than being dropped or refused. A line break at the
very start or end of a block is discarded, because markdown cannot write one
there and Jira does not render one either — it is what pressing shift-enter at
the end of a paragraph leaves behind. And whitespace at the edge of an
emphasised span moves outside it. Markdown cannot emphasise a leading or trailing
space — `* x*` is an asterisk and a word, not a span — and Jira's editor
produces one whenever somebody bolds a word and then types the space after it.
The space is written outside the delimiters instead. Every character is
unchanged and so is what a reader sees; only the extent of the mark moves, by
exactly the whitespace nobody can see it on.

Everything else is refused by name. That includes underlined, coloured,
superscript, subscript, aligned, indented, and annotated text; collapsible
sections; multi-column layouts; decision lists; macros and extensions; custom
panels, whose colour is content; table cells that span rows or columns, hold
more than a single paragraph, or sit in a table with no header row; and any
node type or mark this build does not know. A node-level JSON field the schema
does not define is refused too, rather than ignored — ignoring one converts a
document while silently leaving part of it out.

Link destinations use CommonMark's angle-bracket form
(`[text](<https://example.invalid/a(b)>)`) where the URL holds a bracket, a
space, or an angle bracket. Percent-encoding is not used, because a `%28`
already in the URL and one this tool wrote are the same three characters — so
an address holding a line ending, and an attachment id holding the `/` that
separates it from its collection, are refused rather than encoded one way.

Emphasis picks between the `*` and `_` spellings so that its delimiters never
run together with a neighbouring span's. Where neither spelling would be read
back as what the document says, the conversion is refused rather than written
down and hoped over.

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
resolve produces an empty cell — but no shipped column has such a path, and
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
| `UNKNOWN_ISSUE_TYPE`   | 2      | The project offers no such type. `detail` lists the ones it does. It used to be 2 or 5 depending on which lookup answered; the type name is now always resolved before the fields are fetched, so there is one path and one code. |
| `AMBIGUOUS_ISSUE_TYPE` | 2      | Several types share that name; pass the id.                                                                                                                         |
| `UNKNOWN_USER`         | 2      | No user with that display name, email, or id. `detail` lists the plausible near misses with their ids, and is absent where the search returned nothing that shares a word with what was typed. A partial match is a near miss, not a resolution. |
| `AMBIGUOUS_USER`       | 2      | Several users share that display name. `detail` lists every candidate with its id, whether the account is inactive, and whether it is an app rather than a person.  |
| `UNKNOWN_PROJECT`      | 5      | The project does not exist, or this credential may not create in it. Reported for either status the createmeta route answers an unaddressable project with — a 10.3 Data Center says 400, and 404 is equally plausible elsewhere. |

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

`jr issue attachment download --output -` writes the file to stdout and emits no
result document. It is the only command that does this and the exception is
narrow on purpose: a file and a result on the same channel means one corrupts
the other, and the caller who asked for the file gets it.

Writing to a path is the ordinary case and emits `issue.attachment.download`
saying what was written, where, and how many bytes — counted while writing, so
if it disagrees with the size the listing reported, this is the one that
happened.

A caller with no stdout to spare — `mcp serve`, where bytes would land on the
JSON-RPC stream as a frame the peer cannot parse — gets `NO_STDOUT` and exit 2
rather than a corrupted session.

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

### Errors about reaching the site

`NO_SUCH_ENDPOINT`, `NETWORK`, `TIMEOUT`, `MALFORMED_SERVER_INFO`,
`UNKNOWN_DEPLOYMENT`, and `OFF_SITE_URL` carry where the site came from in
their `detail`: `the site came from context "work"`, `from --site`, or
`from JIRA_SITE`.

Three things can supply a site and which one won is visible nowhere else, so
"the site is not reachable" used to require a second command — `jr context
show` — before it could be acted on. It is an addition to the detail and never
a replacement: the endpoint that failed is still the first thing there.

Nothing else carries it. An error that explains everything explains nothing, and
"which site was that" is the next question for a connection failure and not for
a mistyped flag.

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
| `NOTHING_TO_EDIT`         | 2    | An edit was given nothing to change — `issue edit` with no field, `context edit` with no setting. |
| `CONFLICTING_EDIT`        | 2    | `context edit` was asked to set and clear the same setting. Both at once has no single right answer, and picking one would make the result depend on an implementation detail nobody can see. |
| `UNCONSTRAINED_QUERY`     | 2    | `issue list --limit all` with no filter would page until the instance is exhausted and return every issue in every project the credential can see. The default bound makes an unfiltered query harmless — one request, fifty rows — so only the pairing is refused. `--all-projects` is how to mean it. |
| `INVALID_API_VERSION`     | 2    | `--api-version` accepts 2 or 3. Cloud serves v3; Data Center serves v2. |
| `TOO_MANY_ISSUES`         | 2    | More issues than the agile API moves at once. It is refused rather than split across requests: two requests can half-succeed, and the outcome would be neither moved nor not moved. |
| `DESTINATION_EXISTS`      | 7    | A download would replace a file that is already there. It refuses rather than overwriting, because a download that silently replaced a file is indistinguishable from one that worked, and the file it replaced is not recoverable. `--force` allows it. |
| `OFF_SITE_URL`            | 1    | The server pointed at a host other than the configured site, and this tool will not follow it. Data Center reports an attachment's content as an absolute URL; following it on trust is how a credential reaches a host nobody chose. The refusal never echoes the URL — one can carry userinfo or a signed parameter. |
| `BODY_NOT_REPLAYABLE`     | 1    | A retry needed the request body again and could not get it — a body read from a pipe cannot be sent twice. The request fails rather than going out short, because a second attempt carrying nothing would be accepted as a successful upload of an empty file. |
| `SPRINT_NOT_ACTIVE`       | 7    | Only a running sprint can be closed. The sprint is read first, so the wrong state costs one read and no mutation. |

### Body text on write

Text bound for a description or a comment is **contained, never converted** —
unless `--body-format` says otherwise.

| `--body-format` | What happens                                              |
| --------------- | --------------------------------------------------------- |
| `text` (default) | The text is contained, not interpreted.                  |
| `markdown`      | The text is parsed and becomes the document it describes.  |
| `adf`           | The text is a document, as JSON, and is sent as given.     |

Under `text`, Data Center takes a string of wiki markup and gets the text as
typed. Cloud will not accept a string where a document belongs, so the text is
wrapped in the minimal document that holds it: a blank line starts a paragraph,
a single newline is a line break. Nothing is interpreted — `**bold**` reaches
Jira as six characters on both deployments, and the server decides what they
mean. That is the difference between containing text, which is exact, and
converting it, which is not.

`markdown` and `adf` are **Cloud only**. Data Center stores wiki markup, there
is no markdown-to-wiki converter here, and it will not take a document at all,
so both are refused with `BODY_FORMAT_UNSUPPORTED` rather than approximated.

Under `markdown` the subset is CommonMark's block and inline structure minus
what ADF has no node for, plus GFM tables, task lists, and strikethrough, plus
the `jira-` link schemes above — so a body read out of Jira goes back in as the
document it came from. A single newline inside a paragraph joins its lines,
because that is what markdown means by one; a trailing backslash is a hard
break.

Anything else exits 2 with `MARKDOWN_UNSUPPORTED`, naming the construct **and
the line it is on**: setext headings, indented code blocks, unclosed fences,
lazily continued blockquotes, aligned table columns, an image beside other
text, emphasis around an image or a mention, and a link with no address.
Where CommonMark and this parser would read the same text differently, it
refuses rather than choosing.

Some refusals are Jira's content model rather than markdown's, established by
posting each combination to a real site: emphasis on inline code, and a
blockquote, panel, table, rule, heading, or task list nested where Jira will
not store one. Jira's own answer to those is `INVALID_INPUT; comment:
INVALID_INPUT`, which names neither the node nor where it was.

Reading is the other direction and is described under [ADF converted to
markdown](#adf-converted-to-markdown): a Cloud body comes back as markdown, or
as the document itself with `--raw-body`, and a Data Center body comes back as
`wiki` untouched.

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

## Verifying against `jr contract`

`jr contract` v2 carries each kind's element schema alongside its name, version,
and emitters. v1 let a consumer pin a version; v2 lets it check a response
against the shape, which is the half §3.5 promised and the first version could
not deliver.

Each kind reports one element: its attributes with types, optionality, and any
closed set of values; its child elements with the same, plus whether each may be
absent or repeated; and its text, when it carries any.

The types are promises about the shape of the text, not JSON types — every
format here is textual. `int` parses as a decimal integer. `bool` is exactly
`true` or `false`, never `yes` or `1`. `timestamp` is RFC 3339 in UTC, which is
why Jira's own formats are normalized before they are emitted. `date` is
`YYYY-MM-DD` with no time, because Jira stores some dates without one and
inventing midnight in a timezone would be a value nobody set.

**An empty value satisfies every type.** "Present but empty" is a fact this tool
emits deliberately — an unassigned issue, a context with no default project —
and it is not the same as absent. A consumer checks for the element, then for
the value.

Some shapes are open, and say so. `issue list --field "Story Points"` adds a
`<customfield_10042>` element, and no fixed list can name it; those kinds carry
an `<extra>` element saying where the names come from. Every other kind is
closed, and an element outside the schema is a contract violation rather than a
curiosity.

**The schema is checked on every document this tool writes.** That is the only
reason to trust it. `render.Write` validates before it emits a byte, so a
payload that does not match its published shape fails with `SCHEMA_VIOLATION`
and exit 1, and stdout stays empty. A schema that were merely published
alongside the code would describe the output as somebody once believed it to be.

## Golden files

The contract is enforced by golden files, not by review discipline:

- `internal/render/testdata/` — every writer, every payload shape. The envelope,
  the escaping rules, and the truncation signal, which do not vary between
  builds.
- `internal/cli/testdata/kinds/` — one file per kind and schema version, holding
  that kind's element shape as `jr contract` prints it. `issue.get.v2.xml` is
  the shape of `issue.get` at v2.
- `internal/cli/testdata/<profile>/` — end-to-end output for each built-in
  command, recorded once per shipped profile: `full`, `agent`, `reader`, `ci`.

`make golden` rewrites all of them, running the per-profile set under every
shipped tag set. **A diff in a golden file is a change every consumer sees.**
Bump the schema version of the affected kind in the same commit.

The split is not cosmetic. A kind's *shape* is the same in every build that has
the kind, so it is pinned once and every profile compares against the same file.
What differs between builds is which kinds exist and which commands emit them,
and that is what the per-profile sets carry — `contract.tsv` is the inventory,
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

- A `kind` or its schema version.
- A command's default column set.
- The result, error, or warning envelope.
- An exit code's meaning, or the addition of a new one.
- An error `code` string, or the conditions under which `retryable` is true.
- The escaping rules of any writer, or the type-promotion table above.
- Which format is the default for a content shape.
- What the ADF converter carries, drops, or refuses, in either direction —
  including the `jira-` link schemes, which are as much a part of the contract
  as an element name.
- Where the golden files live, or which builds they are recorded against.
