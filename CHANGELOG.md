# Changelog

Notable changes, written for somebody deciding whether to upgrade.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project follows [semantic versioning](https://semver.org/spec/v2.0.0.html)
with one addition that matters more here than the version number itself:

**Every output kind carries its own schema version, and those move
independently of the release.** A consumer pins `kind` and `v` from the document
it parses, not the release it installed. Any change to a kind's shape is listed
under **Output contract** below, with the kind and its new version, because that
is the section somebody with a script needs to read.

The release workflow reads the section for the tag it is building and refuses to
publish without one — so a release with nothing written here does not happen by
accident.

## [Unreleased]

Nothing yet.

## [0.9.3] - 2026-08-21

**Any field on an issue can now be written.** `issue create` and `issue edit`
took a fixed set of fields and had no escape hatch, so every custom field on a
screen was unreachable and the only way to set story points was to leave this
tool for `curl`, losing the dry run, the precondition, and the validation on
the way out.

Additive on every count. Every invocation you make today still runs, means the
same thing, and produces the same document, with two exceptions that are both
new spellings rather than changed ones. Three kinds gained an optional element
and moved a schema version; if you parse output, read **Output contract**
below, because that list is the part with consequences.

Patch, and the first release here where a kind's version moves for an additive
change. See the new stability row for why that does not make it breaking.

### Added

- **`--field id=value` on `issue create` and `issue edit`.** The id or the name
  both work, and the value is typed from your site's own field catalogue, so a
  number field refuses a value that is not a number before anything is sent.
  Repeat one id to build an array; nothing is split on commas, because a comma
  is a character a value may contain.

      jr issue edit ENG-101 --field 'Story Points=5'
      jr issue create --type Story --summary Retry --field customfield_10140=5

  On a read `--field` selects a column and takes a bare name. The `=` is what
  separates the two senses, and a bare name on a write is refused rather than
  read as the other one.

- **`--field-json id=<json>` on the same two commands**, whose value is sent
  exactly as written. It is not a convenience. Jira reports Epic Link, Rank,
  Team, Parent Link and most plugin fields as schema type `any`, which says
  nothing about what to send, so `--field` refuses those by name and points
  here rather than guessing:

      jr issue edit ENG-101 --field-json customfield_11350='"ENG-42"'

- **`--header` on `auth token`**, which prints `Authorization: <value>` on one
  line and nothing else, so a shell can capture the whole output:

      curl -H "$(jr auth token --header)" ...

  Without it the command emits a document like every other command, and no
  format in this contract is safe to interpolate into a header. Capturing one
  whole and using it produced curl status `000`, which is what curl also
  reports for a network failure, so the mistake did not announce itself.
  `--header` with an explicit `--format` is refused with `HEADER_AND_FORMAT`:
  they name two different outputs.

- **Jira's own type key for a custom field**, as an optional `custom-type`
  element on `field list`, `meta createmeta` and `meta transitions`. It was
  being read off the wire and discarded. It is the only thing that says what
  JSON a value has to be: measured against a Data Center, thirteen custom
  fields all carry it and five of them report schema type `any`, which without
  the key makes those five one undifferentiated type.

### Fixed

- **A flag that is really a positional argument now says so.**
  `jr sprint add --sprint 128 ENG-1` was refused with an empty suggestion list,
  because the candidates were the command's flags and `sprint` is a declared
  argument. It now answers `sprint is an argument, not a flag`, with the usage
  line. Exact matches only, so a typo of a real flag still gets the flag
  suggestion it always did.

### Output contract

Three kinds moved, all for the same added optional element. Nothing was removed
or renamed, no default column set changed, no exit code or error `code` means
anything different, and no input that was accepted before is refused now.

- **`field.list` v1 → v2.** New optional `custom-type` element, carrying Jira's
  `schema.custom` key for a custom field and absent on a system field. **The
  default TSV columns are unchanged**, so a script splitting on tabs keeps the
  four cells it had; the element reaches `xml`, `json` and `yaml`.
- **`meta.createmeta` v1 → v2.** The same element on every field of a create
  screen, with the same default columns.
- **`meta.transitions` v2 → v3.** The same element, and this kind moved only
  because it shares its field shape with `meta.createmeta`. Nothing else about
  a transition changed.

Six new error `code` strings, all on inputs that were previously refused as an
unknown flag: `FIELD_NOT_KV`, `FIELD_HAS_A_FLAG`, `FIELD_TYPE_UNSUPPORTED`,
`FIELD_NOT_A_NUMBER`, `FIELD_NOT_JSON`, and `DUPLICATE_FIELD`, plus
`HEADER_AND_FORMAT` on `auth token`. Each exits 2 and each is listed in
[output-contract.md](docs/output-contract.md) and
[troubleshooting.md](docs/troubleshooting.md).

**The stability policy has two corrections**, and this release is what found
them. `v` was documented as moving "on breaking change", which was never what
the gate enforced: `make golden` refuses a changed shape at an unchanged
version, additive or not. And there was no row for a kind version moving on its
own. There is now, and it says what this release does: kind versions move
independently of the release, so price the release on the rows describing what
changed and list the kinds here.

## [0.9.2] - 2026-08-19

**Emphasis this tool refused to write, and could have written, now converts.**
The count of such documents in its own adversarial corpus went from 75 to zero.

Nothing about any document's shape moved. No kind's schema version moved, no
default column set changed, no exit code or error `code` means anything
different, and no input that converted before converts differently now. If your
bodies convert today, this release changes nothing you do.

Patch on every count, and it is the second patch of the day: 0.9.1 settled the
markdown a body converts to, and this one is about the bodies that never got
that far.

### Fixed

- **Emphasis with no spelling is no longer refused when a spelling exists.**
  Writing a run of emphasis means choosing three things at every position: which
  mark opens the span, how far it reaches, and which of `*` or `_` it is written
  with. The writer fixed all three at the moment it reached them, so a choice
  that was correct where it was made could leave the rest of the line with
  nothing, and the document was refused over a decision three nodes earlier.

  It searches the line now, in the same order it used to walk it, and only after
  the ordinary walk has refused. A body that converts today converts identically.

- **The writer asks the reader about a spelling its own rules refuse.** Two of
  those rules were approximations of how a reader pairs delimiters: one refused
  a bold span whose content held a stray `*`, which is only a collision when
  that `*` does not pair with another one inside. `**a*b*c**` is fine and
  `**a*bc**` is not, and only the reader can tell them apart.

  So the last thing the writer tries is dropping those rules, writing the
  candidate, reading it back, and comparing it with the document it started
  from. A candidate the reader does not agree with is not written. Together with
  the search above, this took the emphasis refusals to zero.

- **A code block whose language starts with its own fence character is no longer
  mangled.** The opening line of a fenced block is a run of the fence character
  and then the language, so a language of ``~`x`` after `~~~` read as a fence of
  four and a language of `` `x ``: a character of content gone, and a closing
  fence that no longer closed anything. The document came back as `a code fence
  that is never closed`, pointing at a line nobody wrote. A space separates them
  now, written only where it is needed.

### Output contract

No kind's schema version moved.

- `docs/output-contract.md` now says that the emphasis spelling is searched over
  the whole line rather than fixed one span at a time, and that a spelling the
  writer's own rules refuse is offered to the reader before it is given up on.
  Both describe behaviour a caller can observe only as fewer refusals.

## [0.9.1] - 2026-08-19

**Bodies this tool refused to convert, and could have converted, now convert.
And the markdown a body converts to is a fixed point: read it back, write it
again, and you get the same characters.**

Nothing about any document's shape moved. No kind's schema version moved, no
default column set changed, and no exit code or error `code` means anything
different. If `issue get` and `issue create` do what you need today, this
release changes nothing you do. It is for the bodies that exited 2 with
`MARKDOWN_UNSUPPORTED` and for anyone whose text came back different when they
piped it through twice.

Patch on every count. Sixty-three inputs move from refused to accepted, which
[the stability policy](docs/output-contract.md#stability-policy) calls additive
in as many words, and nothing moved the other way in either golden.

### Fixed

- **Three or more emphasis spans against each other are no longer refused.**
  Every emphasis span wrote itself expecting the next one to open with an
  asterisk, so a span with an emphasis neighbour took the underscore and left
  the asterisk for it. Two spans can both be satisfied that way and three
  cannot: the middle one has an underscore on its left and an asterisk expected
  on its right, and no character is left. `_a_**b**_c_` is a document Jira
  stores and CommonMark reads back as exactly the three spans it came from, and
  it was refused, along with every other document of that shape:

  ```console
  $ jr issue get ENG-1
  ADF_UNREPRESENTABLE: the document contains emphasis that markdown cannot
  spell unambiguously here, which markdown cannot represent (at doc > paragraph)
  ```

  62 inputs in the converter's own corpus were refused this way.

- **A span the writer could not follow is reconsidered rather than refused.**
  The writer took the first spelling that worked at each position and never went
  back, so a span that was correct on its own could leave the rest of the line
  with no spelling, and the document was refused over a choice made three nodes
  earlier. It searches the line now, in the same order, and only after the
  ordinary walk has already refused, so a body that converts today converts
  identically and no faster path pays for it.

- **The markdown a body converts to no longer keeps changing.** A mark on
  whitespace cannot be written down, so it is dropped when that whitespace lands
  at the edge of an emphasised span. Which span an edge belongs to is decided
  while writing, and only one marked space landed at an edge per conversion, so
  a body with two of them needed three conversions before its text stopped
  moving. Reading a body out of `issue get` and piping it back in did not give
  you the same document you started with, and nothing said which of the two
  answers was the real one. The conversion settles before it returns.

  It settles only through a document it is still carrying exactly. A text node
  holding a newline is written with the newline, and although reading that back
  joins the lines with a space the way a soft break does, the newline is a
  character and settling never buys stability with one.

### Output contract

No kind's schema version moved.

- The stability policy has a new row, for changing the text a command emits
  inside a field whose shape is unchanged. It is additive where that text was
  not stable and breaking where it was, and it exists because the settling
  change above is the first one that needed it and the policy answered by
  precedent rather than by rule three times before.
- `docs/output-contract.md` now states that a body's markdown is a fixed point,
  that the emphasis spelling is chosen over the whole line rather than one span
  at a time, and that a document needing a single delimiter run to close one
  mark and open another is still refused. The last of those was already the
  behaviour and was not written down.

## [0.9.0] - 2026-08-18

**A table row holding more cells than its header row is refused now, in both
directions, where it used to lose the extra cells and say nothing.**

That is the whole of what changed for a caller. If no table you read out of Jira
or send to it has a row wider than its header, this release changes nothing you
do, and you are upgrading for a silent loss you were not hitting.

Minor rather than a patch on one count and only one: an input that used to be
accepted is now refused. The rest of the release is additive or internal and
would not have moved the version by itself, which is the arithmetic
[the stability policy](docs/output-contract.md#stability-policy) now states in
each row rather than leaving to be assembled from two sections.

### Fixed

- **A table row wider than its header row no longer loses its extra cells.**
  `ToMarkdown` took its width from the header row and wrote exactly that many
  cells per row, so everything past that width was dropped, with no error and
  exit 0. A body row holding `KEPT`, `DROPPED`, and `ALSO-DROPPED` under a
  one-cell header came out as:

  ```
  | H |
  | --- |
  | KEPT |
  ```

  It is now `ADF_UNREPRESENTABLE` at exit 2, naming the row rather than the
  table, because the next question is always which one:

  ```console
  $ jr issue get ENG-1
  ADF_UNREPRESENTABLE: the document contains a table row of 3 cells under a
  header row of 1, which markdown cannot represent (at doc > table > tableRow 2)
  ```

  `--raw-body` emits the document exactly as Jira sent it, as it does for every
  other refusal, so nothing is unreachable.

  **The same refusal covers markdown you send.** `FromMarkdown` built each row
  from its own pipe count with no reference to the header, so a one-cell header
  over `| b | c |` built two cells under one. GFM ignores the excess cells of a
  long row by definition, so that markdown is a one-column table whose second
  cell does not exist, and this tool was turning it into content on its way to
  Jira. The two defects hid each other: the parser kept a cell GFM discards and
  the writer dropped a cell that existed, so the round trip came out looking
  clean.

  **A row with *fewer* cells than the header is still padded**, and the
  asymmetry is deliberate. The cells a short row gains are empty, so nothing a
  reader sees is invented; a cell dropped from a long row is content somebody
  wrote.

  Measured before it shipped: three of the 1852 inputs in this converter's
  markdown corpus build such a row, and **none of the 247 documents recorded
  from a real Jira**. If you have never seen a ragged table, that is why.

### Changed

- **Every rule in the stability policy now says which position of the release
  version it moves.** It classified a change as major or minor and left a
  demotion two sections down to be applied by the reader, which is a rule
  assembled from two places and it was assembled inconsistently: 0.6.0, 0.7.0
  and 0.8.0 each took the minor position for purely additive content and each
  should have been a patch. The document now lists where it was not followed.
  Nothing is re-tagged and no published release changes; what changes is how the
  next number is picked. The policy also gained the two rows whose absence
  caused it, for **adding a command** and **adding a kind**, both additive.

### Output contract

- **Nothing moved.** No kind changed shape, no kind version was bumped, no
  default column set changed, and no exit code or error `code` changed meaning.
  `ADF_UNREPRESENTABLE` is not new; it now covers one more construct, which is
  listed with the others in
  [output-contract.md](docs/output-contract.md).

### Internal

Not user-visible, and here because it is what found the bug above.

- **What the converter loses is pinned, not only what it refuses.** The
  round-trip fuzzer permits the first conversion to change the text, for a real
  reason: emphasis has two delimiter characters, so the same span written with
  underscores and with asterisks is one document written two ways. The allowance
  is not restricted to spelling, so a first pass that moved a mark, dropped a
  node, or changed a table's shape converged just as readily and the fuzzer was
  green on all of it. Sixty-five inputs in its own corpus lose something on that
  pass. They are a golden now, and the table defect was the first thing it
  caught.

## [0.8.0] - 2026-08-18

`jr doctor` explains why this tool will not work here, one check per layer
between the binary and an answer from Jira, and reports every one of them
whether it passed or not.

**It exits 0 whenever the checks ran, whatever they found.** A diagnostic that
exited non-zero on a finding would make "did the diagnostic run" and "is this
configuration healthy" the same signal, and telling those apart is the only
reason to run it. Branch on the document: `doctor/@status` for the roll-up, or
one check's `@status` for the layer you care about. A non-zero exit means the
command itself could not run.

The failures it exists for are the ones where the first request fails and the
reason is three layers below it. A 401 from a Data Center under a context path
is indistinguishable, from the error alone, from a 401 because the token
expired, a 401 because a proxy stripped the header, and a 401 because the site
URL lost its context path and the request reached a different application.
Each of those was answerable before, by running six commands and knowing which
six.

Minor rather than a patch on two counts, both additive: a new command and a new
kind. **No existing kind moved a schema version**, no default column set
changed, and no exit code or error `code` changed meaning.

### Added

- **`jr doctor`**, which reports eight checks in the order the layers stack:
  the configuration, the credential, the site URL and its context path, the
  proxy and TLS settings, the deployment probe, the clock, the account, and
  whatever the site discloses about rate limits. Each carries `ok`, `failed`,
  or `skipped`.

  ```console
  $ jr doctor --format tsv | grep -v $'\tok$'
  field           value
  @status         failed
  @failed         1
  account/@status failed
  account/@code   UNAUTHORIZED
  account/summary Jira rejected the credentials
  account/remedy  run `jr auth login`, or check that the token has not expired
  ```

  Every check is reported, including the ones that passed, because a diagnostic
  that prints only problems cannot be told apart from one whose checks never
  ran. A `failed` check carries the same `code`, `detail`, and `remedy` the
  failing layer would have produced on its own, so
  [troubleshooting.md](docs/troubleshooting.md) answers a code found here or
  anywhere else. A `skipped` check names the check it was waiting on: read down
  and act on the first failure, because the ones below it are usually the same
  cause counted again.

  It needs no credential and no reachable site, and it is in every profile
  including `reader` and `ci`. A site that answers the deployment probe
  anonymously is still probed when nothing is stored, so "this site is reachable
  and is Data Center 10.4" reaches somebody whose actual problem is that they
  never logged in.

  Three things it reports that no other command will: the URL a request really
  resolves to, built by the same code that builds a request, so a base and an
  endpoint that disagree about a context path are visible; the proxy in effect,
  which comes from `HTTPS_PROXY` and `NO_PROXY` and which nobody configured
  here; and whether the deployment came from the probe, from its cache, or from
  `--api-version`, since a stale cached answer and a fresh one are different
  claims. `--refresh` forces the probe. The clock is never cached, because a
  cached clock is not a clock.

- **`CLOCK_SKEW`**, the one error `code` only `jr doctor` produces. It compares
  this machine's clock against the site's own and fails the check at a minute or
  more apart, because a minute is the finest bound JQL has: no operator this
  tool can send bisects one, measured on both deployments. A machine a minute
  out asks Jira for a different window than the one it computed, and every
  `--since` cursor, relative date, and worklog window is affected silently.

- **What a site discloses about rate limiting**, reported verbatim by the
  `limits` check: Cloud states a policy on every response and a default Data
  Center sends none at all. A site that advertises nothing says so rather than
  reporting zeros.

### Output contract

- **`doctor` v1**, new. One roll-up plus one element per check, each with a
  closed `status` of `ok`, `failed`, or `skipped`, its own typed attributes, and
  a `summary`. `jr contract` prints the whole shape.

Nothing else moved. No existing kind changed shape, and every command that ran
before runs the same way.

## [0.7.1] - 2026-08-18

Emphasis this converter could spell is no longer refused, and a strike is never
written in two pieces.

**Nothing changed shape.** No kind moved a schema version, no default column set
gained a field, and no exit code or error `code` changed meaning.

A patch, and the row that decides it is [accepting an input that used to be
refused](docs/output-contract.md#stability-policy): fifteen markdown bodies that
exited 2 now convert, which is minor, cascading to the patch position the way
every minor does in `0.y.z`. Nothing moved the other way. No markdown input that
used to be accepted is refused, measured over the 2371 inputs in the converter's
verdict golden, which is the file that exists to see exactly that.

One class of Cloud *document* does move in the other direction, and it is the
third item below. It is not the row above read backwards: that row is about
input a caller sends, and this is a body Jira stores. The old answer for it was
text that said something the document did not, so there is nothing there that a
consumer could have been reading correctly.

### Fixed

- **A span whose text opens on an escaped delimiter is no longer refused.** The
  writer asks whether a live delimiter sits strictly inside a span and would
  close it early, and it read the escapes from one byte too far in, so the
  asterisk in `\*0` counted as live. `\*0` is what emphasis over the text `*0`
  renders to, so the asterisk spelling was refused over a collision that is not
  there, and the underscore cannot close in front of the digit that follows.

  ```console
  $ jr issue comment add ENG-101 '**\*0**0' --body-format markdown
  ```

  Fifteen inputs are affected, every one of them a span whose content opens on
  an escape. They are not new spellings: they are documents that always had one.

- **Emphasis in front of a link is no longer refused.** The writer asks what
  character its closing delimiter will sit against, and for a neighbour carrying
  a link the answer is the bracket that opens it, not the first character of the
  link's text. A bracket is punctuation and the text usually is not, and
  emphasis ending in punctuation can close in front of the first and not the
  second, so `*a.*` in front of a link exited 2. It asks the order the writer
  opens marks in now, so a link's bracket and a strike's tilde both come from
  the list that decides them.

- **A strike is never written in two pieces, and where that leaves no spelling
  the body is refused rather than written wrongly.** `~~` has no flanking rules
  in GFM, so nothing beside it can make it inert, and the only thing that
  changes what it means is another `~~` flush against it: four tildes are text
  to a reader. Writing a span narrower is how this converter answers a span it
  cannot spell, and for a strike that leaves the rest of the mark to open its
  own span at the cut with nothing in between, so the narrower spelling was the
  corruption. A strike over emphasised `a.` and a word came back as
  `~~*a.*~~~~b~~` in the `format="markdown"` body of `jr issue get`.

  Those four tildes came back as text on the next conversion, and the document
  no longer said what Jira stored. It is written the other way round now, with
  the emphasis outside, `*~~a.~~*~~b~~`, which says what it says. **Where the
  span opens on a node carrying nothing but the strike there is no other mark to
  put outside, and the body now exits 2** with `ADF_UNREPRESENTABLE` naming the
  construct, where before it produced the four tildes and exited 0. `--raw-body`
  emits the document exactly as Jira sent it, with `format="adf"`.

  None of the 247 real Cloud documents in this converter's corpus is in that
  class. It needs a strike over emphasis that markdown itself cannot spell,
  which is emphasis ending in punctuation with a word character after it.

- **One spelling changes.** Strong over two literal asterisks was written
  `__\*\*__` and is written `**\*\***` now, which is the asterisk form this
  writer prefers when nothing beside it merges. Both read back as the same
  document, and one document in the 247-document corpus is written this way.

### Output contract

- No kind moved. The refusals above are `ADF_UNREPRESENTABLE`, which is the code
  this conversion has always used for a document markdown cannot represent, and
  the newly accepted inputs answer with the documents they always would have.

## [0.7.0] - 2026-08-17

`jr issue changes` answers "what changed since last time" without gaps or
repeats. Every other Jira poller answers it by listing everything and diffing,
which cannot see a change that reverted, cannot say _what_ moved, is blind to
every field the columns do not project, and re-fetches five thousand issues to
discover that three moved.

**The cursor is a window, not a row, and that is the whole design.** The obvious
resume point is the last row's timestamp and key. It cannot be exact: JQL cannot
express a bound finer than a minute and neither comparison operator bisects one
— measured on both deployments — so a pair cursor has to be applied by this
client, comparing timestamps as this tool publishes them, to the second, against
the order the server walked at whatever precision it stores. Two rows inside one
second sort one way here and possibly the other way there, and the row on a page
boundary is then skipped or repeated. A poller built that way passes every test
anybody writes and drops a transition once a week under load.

So a poll reports `(previous bound, this walk's start]`. Both ends are instants
this tool chose rather than rows it saw, and the next poll compares against the
same instant with `>` where this one used `<=`. Two consecutive answers cover
every instant exactly once, and a bulk edit that stamps four hundred changes with
one timestamp falls entirely inside one or entirely inside the next.

Minor rather than a patch on three counts, all additive: a new command, a new
kind, and a new optional element on the envelope. **No existing kind moved a
schema version**, no default column set changed, and no exit code or error
`code` changed meaning.

### Added

- **`jr issue changes --since <cursor|date>`**, an incremental feed of recorded
  changes across every issue in scope, oldest first.

  ```console
  $ jr issue changes --since -1h --format json
  ```

  ```json
  {
    "kind": "issue.changes",
    "v": 1,
    "complete": true,
    "count": 2,
    "changes": [ … ],
    "next-since-token": "eyJkIjoiY2xvdWQiLCJ0IjoiMjAyNi0wOC0xN1QxNDozMDowMFoifQ"
  }
  ```

  Pass that token back as `--since` for the next poll. A date or an offset
  starts a new feed. `docs/recipes.md` has the shell loop.

- **`next-since-token`, a new envelope element, and it is not
  `next-page-token`.** A page token says the answer was cut short; a since token
  says it was whole and names where the next answer starts, so it appears on a
  **complete** result — the combination that is refused for a page token. A feed
  carrying its cursor in the existing field would tell every consumer the answer
  was truncated, and exit 3 forever.

- **A poll that was not whole issues no cursor at all.** Cut short by `--limit`,
  by `--max-requests`, or by a changelog the server would not send in full, the
  run exits 3 and carries no token, because advancing past a window that was only
  partly reported is how a feed loses a change silently. Poll again with the same
  `--since`.

### Fixed

- **`jr issue activity` no longer reports a clipped changelog as a complete
  feed.** Cloud's `expand=changelog` on the search is a paged bean bounded at
  forty entries and says so in every response; this tool read neither the bound
  nor the count. An issue with more than forty saves lost its oldest ones and the
  run exited 0 with `complete="true"`.

  It went unmet because the clip lands on the oldest saves — Cloud returns that
  projection newest-first, so a feed about the last week is _usually_ inside the
  newest forty.

  A clipped changelog now makes the run incomplete, exit 3, with the warning
  naming `event` as it already did for a clipped comment thread. **A run over a
  heavily edited Cloud issue that used to exit 0 will now exit 3**, which is the
  answer it should have been giving; a script branching on `$?` will see it.
  Fetching the rest is a further change and is not in this release.

### Output contract

- `issue.changes` v1 is new. No existing kind moved: every schema version is
  unchanged from 0.6.0.
- `next-since-token` is a new optional element on a collection envelope, present
  only on `issue.changes` and only when the poll covered its window. Adding an
  optional element is a minor change under the
  [stability policy](docs/output-contract.md#stability-policy), and no consumer
  that does not read it is affected. TSV carries it no more than it carries
  `site` or `complete`: it has no envelope.
- No new error `code` on an existing command. `issue changes` introduces
  `INVALID_SINCE_TOKEN`, `SINCE_AFTER_NOW`, and `NO_SERVER_TIME`, all on itself
  and all documented in `docs/troubleshooting.md`.
- **`issue.activity` v1 is unchanged in shape and changed in when it is
  complete.** Nothing about the document moved; a run that was silently missing
  changelog entries now says so. That is the truncation rule this contract
  already states, applied to a source that was not counted.

## [0.6.0] - 2026-08-17

A refusal now tells you what to do about it. It names the record it refused, it
names the flag or the verb you probably meant, and when a streamed collection
fails partway it says how many rows are already on stdout.

**Nothing changed shape.** No kind moved a schema version, no default column set
gained a field, no exit code or error `code` changed meaning, and no input that
used to be accepted is refused now.

Minor rather than a patch, and the row that decides it is
[adding a new optional element](docs/output-contract.md#stability-policy):
`INVALID_USAGE` and `UNKNOWN_COMMAND` carry a `detail` they never carried
before. The near misses for two of those paths also moved out of `remedy`,
where a consumer may have learned to read them, into `detail`, which is the
field this contract tells you candidates live in.

### Added

- **A mistyped flag, verb, or command name comes back with the near miss.**

  ```console
  $ jr issue list --assignne ada
  # before: unknown flag: --assignne, and nothing else
  # now:    detail: did you mean: --assignee
  ```

  Mistyping a field name and mistyping a flag name are the same mistake, and
  only the first was answered. The second has the cheapest candidate set in the
  tool: a field costs a request and a catalogue, and a flag is the command's own
  declaration, already in memory and a few dozen strings long.

  Four refusals of a mistyped name existed and three of them ranked candidates
  differently. `jr schema list` matched on substring, so it offered every command
  containing the word; an unknown subcommand used cobra's own suggester; a field
  used edit distance. They are one rule now, `internal/nearest`, and a caller
  gets the same idea of "close" wherever they mistype something.

  **Nothing close still means nothing said.** A refusal listing three unrelated
  candidates reads as an answer and costs a turn to rule out, which is what the
  substring rule was doing. Shorthand flags are not ranked at all: `-q` is one
  character and every other shorthand is one edit from it.

  The candidates go in `detail` and the remedy stays put. Both command paths
  used to return the guess *as* the remedy, so a caller offered a wrong
  suggestion lost the pointer to the command that lists everything.

### Fixed

- **A refusal names the record it refused.** A value that no output format can
  carry is still refused, and the refusal now says which record holds it:

  ```console
  $ jr issue comment list ENG-101
  # message stays: the text of issue.comment.list/comments/comment/body holds
  #                a character no output format can carry
  # before: detail: U+001B at byte 5
  # now:    detail: U+001B at byte 5; in comment id=10234
  ```

  Reported from a Data Center site where one raw `U+001B` in one comment made
  every comment-reading command against a whole project exit 1. The refusal was
  correct and named a schema path, which is the same string for every comment in
  the thread, so finding the offending record meant bisecting `--limit` across a
  few hundred issues. The identity was on the record the whole time.

  It works one layer in as well: a comment inlined by `issue get
  --with-comments` names the comment and the issue both. Where a kind's records
  carry no `key`, `id`, or `name`, every attribute is named instead, which is
  what an activity event needs, since it is identified by the issue it happened
  on together with what kind of event it was.

- **A streamed collection that fails partway says what it left on stdout.**

  ```console
  $ jr issue comment list ENG-101 > out.tsv
  # remedy: ... 2 rows of this collection reached stdout before it failed: TSV
  # streams, so a row is bytes the moment it is written. What is there is the
  # answer up to the failure and not a complete one.
  ```

  `--format tsv` emits each row as it arrives, which is what makes a long list
  pipeable, so a failure on the fortieth row cannot unwrite the thirty-nine
  before it. The same collection refused for the same reason writes nothing at
  all under `xml`, `json`, and `yaml`, which buffer until the last page lands.
  That split is stated now rather than left to be discovered: closing it would
  mean buffering every format, and a collection that has to be complete before
  it is emitted cannot be piped into anything while it runs.

  It applies to any mid-stream failure, not only a refused value. A transport
  error on page three leaves the same partial feed.

### Output contract

- No kind moved. Every schema version is unchanged from 0.5.0.
- No new error `code`, and none changed meaning. `INVALID_USAGE` and
  `UNKNOWN_COMMAND` gain a `detail` where they had none, and
  `UNRENDERABLE_VALUE`'s `detail` gains the record's identity after the byte
  offset. Both are prose in a field this contract already declared optional and
  already told you not to branch on.
- **"A failing command writes nothing at all to stdout" now has two exceptions
  written down where it had one.** The half-applied mutation was always there.
  The second is the streamed collection above, which is not new behaviour: it is
  behaviour that was true for as long as TSV has streamed and was described
  nowhere. A consumer that treats a zero exit as the condition for reading
  stdout is unaffected by either.

## [0.5.0] - 2026-08-17

`--jql` asks Jira what a raw fragment means before sending it, rather than
passing on an empty answer to a question the server did not understand.

**Nothing changed shape.** No kind moved a schema version, no default column set
gained a field, and no exit code or error `code` changed meaning. One error
`code` is new, `JQL_NOT_UNDERSTOOD`, and a consumer that has never seen it was
never being sent it.

A minor rather than a patch, and the row that decides it is
[refusing an input that used to be accepted](docs/output-contract.md#stability-policy).
A `--jql` fragment naming a field that does not exist used to be sent and
answered with zero rows and exit 0; it now exits 2. Nothing about the output's
shape moves, so a script still parses every document identically, and the
invocation it was built around now refuses. That row is major, and major moves
the minor position while the version starts with a zero.

One of the changes below goes the other way, and it is that row read backwards:
a link on inline code is
[accepted where it used to be refused](docs/output-contract.md#stability-policy),
which is minor. The remaining two move no verdict in either direction. The
whitespace fix was measured across a 2,197-entry corpus and every accept and
refuse is identical to the release before it; what changed is which characters
survive. The retry change is diagnostic output only. Minor cascades to the patch
position and cannot move the minor one while a refusal is doing so, or the two
would be indistinguishable in the only place a consumer looks.

### Changed

- **A raw `--jql` fragment is checked before it is sent.** Cloud's search
  endpoint answers a query it knows is meaningless with HTTP 200 and no rows,
  which is indistinguishable from an honest "nothing matched":

  ```console
  $ jr issue list --jql 'nosuchfield = 1'
  # before: exit 0, complete="true", zero rows
  # now:    exit 2, JQL_NOT_UNDERSTOOD, carrying Jira's own message
  ```

  Everything reached through a typed flag already had a floor: `--field`
  resolves against the catalogue and every user-valued flag goes through user
  resolution. `--jql` had none, and it is what a caller reaches for when a flag
  will not express the question.

  **A warning refuses as firmly as an error.** The warning is where Jira reports
  a value that does not exist for a field, so `assignee = "nobody-xyz"` is valid
  JQL naming nobody, and answering it with an empty result is the same wrong
  answer as a misspelled field. It also makes `--jql` agree with `--assignee`,
  which already refuses a user it cannot resolve.

  Both deployments, one path. Cloud uses its parse endpoint; Data Center uses a
  search bounded to zero rows, which is the closest thing it has. Measured on a
  local Data Center rather than assumed: its search refuses three classes on its
  own and answers the unknown-user class with the same confident empty Cloud
  does.

  The check costs one request, about 0.2s, and only when `--jql` is present. It
  runs before a streaming command has written its header, because a verdict
  arriving later could only be a warning on stderr, which is invisible to an MCP
  caller and would split the contract by format for one input.

  **One consequence worth knowing before you upgrade:** `--max-requests 1` with
  `--jql` now fails, because the check and the search are two requests.

  What it does not close, documented rather than implied shut: on Cloud the
  operand of a `WAS`, `CHANGED TO`, or `CHANGED FROM` predicate is validated by
  neither endpoint, so `status was "NoSuchStatusXYZ"` is still answered empty
  there. Data Center refuses it. That gap is Atlassian's.

### Fixed

- **A link on inline code is accepted, because Jira stores it.** `jr issue get`
  writes a link whose text is inline code, and feeding that back was refused.
  Two of the 247 documents in the corpus are exactly that shape, and they were
  the only ones this tool could write and could not read.

  The rule is that `code` takes no formatting, and a link is not formatting: it
  is where the text points rather than how it looks. A mark nobody has tested
  against `code` is still refused rather than sent and answered with a 400 that
  names neither.

  The error message also names the mark that clashed. It read `emphasis on
  inline code` for every case, which is wrong for `strike` and is how this went
  unnoticed for a day: a link was refused by an error describing a construct the
  document did not contain.

- **The reader keeps the whitespace markdown does not count.** The block parser
  decided structure with Unicode's whitespace set, which includes the vertical
  tab, the form feed, NEL, and the non-breaking space. Markdown's set is a space
  and a tab. Deciding a block with the wider one ate those characters at a line
  edge, with no refusal and no warning:

  ```
  # x<NBSP>          came back as the heading text "x"
  | a<VT> |          came back as the cell "a"
  0\n<NBSP>\n0       read as blank, splitting one paragraph into two
  ```

  Jira keeps the character, which is what decides the direction: a heading and a
  paragraph each ending in U+00A0 went to a real site and came back with the
  U+00A0 intact. A non-breaking space at a line edge is not exotic. It is what a
  paste out of a word processor leaves.

  Measured over a 2,197-entry corpus: the accept and refuse verdicts are
  identical in both directions, and 5 documents build differently. All five are
  Unicode spaces, which is exactly the class this changes and nothing else.

- **`--debug` says why a request was not retried.** The retry policy made two
  decisions and reported neither.

  A POST that got a 5xx is deliberately not replayed, because it may have been
  processed before the failure and retrying it is how one `issue create` becomes
  two issues. That decision left a trace identical to a run where the policy was
  never consulted: one attempt, a 503, and nothing to say the count was on
  purpose. It now says so, on both paths that can end an exchange, a status that
  will not be replayed and a connection that dropped.

  A `Retry-After` is capped at 30 seconds so one command cannot become an
  hour-long hang. The trace now carries what the server asked for beside what
  was waited, and only when the two differ:

  ```
  [http] retry attempt=1 GET … status=429 wait=30s asked=1h0m0s reason="rate limited"
  ```

  Measured afterwards against both deployments: Cloud advertises a one-second
  burst window on every response, so the cap is roughly two orders of magnitude
  from firing there, and a default Data Center sends no rate-limit header at
  all. Raising `--retries` remains the right answer to a Cloud 429.

### Output contract

- No kind moved. Every schema version is unchanged from 0.4.0.
- `JQL_NOT_UNDERSTOOD` is a new error `code`, at exit 2, and the only addition
  to the error table. It is raised by `issue list` and `issue activity`, the two
  commands that take a raw `--jql` and then act on it.

  `jql validate` and `jql explain` also take `--jql` and do **not** raise it.
  Reporting the server's opinion is their whole job, so they answer with
  `valid` and any warnings rather than refusing, and that has not changed.

## [0.4.0] - 2026-08-16

`jr` reads emphasis the way CommonMark does, and refuses markdown it cannot
write back rather than writing something else.

**Nothing changed shape.** No kind moved a schema version, no default column set
gained a field, and no exit code or error `code` changed meaning. Every document
in the 247-entry corpus of bodies Jira Cloud actually stored converts to exactly
the markdown 0.3.3 produced: `internal/adf/testdata/corpus.golden` is unchanged
across this release.

**What did change is that some bodies now refuse.** A minor rather than a patch,
because refusing an input that used to be accepted is major under the stability
policy, and major moves the minor position while the version starts with a zero.
The bodies affected are ones whose document has no markdown spelling at all, and
they used to be written as markdown that reads back as something else. That is
the trade this release makes in one sentence: a refusal you can see, in place of
a body that quietly said something different. `--raw-body` emits the document
untouched and is the way through.

### Fixed

- **Emphasis is CommonMark's delimiter-run algorithm rather than an
  approximation of it.** The scanner it replaces looked for a closing run of
  exactly the length it had opened with, which cannot express a nested span
  ending flush against its parent. `**bold *and italic***` came back as one text
  node with the asterisks still in it: both marks dropped, no refusal, exit 0, on
  markdown anybody would write. The rule of three, the flanking rules, and the
  openers-bottom table are all in now, and about thirty-five cases from the
  specification are a table in the tests.

- **A delimiter is written only where a reader can flank it.** A run against
  punctuation cannot close in front of a word character, so emphasis over a
  struck word wrote `*0~~0~~*0`, whose closing asterisk opens a second span and
  closes nothing. Both asterisks read back as text and the emphasis was gone.
  Where one spelling does not work the writer now tries the others before
  refusing, so documents that used to be refused outright are written.

- **A link title is escaped like every other piece of text.** It escaped the
  backslash and the quote and nothing else, which is every character that can end
  the title early and none of the ones that can end the paragraph early. A title
  spanning lines put a block quote, a heading or a list marker at the start of a
  line, and the paragraph came apart before the link was built. Jira Cloud does
  store a newline in a title, so this is a body the server hands back.

- **A control character inside a span is punctuation, not the end of the text.**
  Emphasis over content beginning or ending with one was refused, because the
  check that classifies it read "no character" and "the character NUL" as the
  same thing, and whitespace is the one class that can neither open nor close.

- **A line start survives the whitespace in front of it.** A vertical tab, a form
  feed or a non-breaking space before a `=` on the line under a paragraph reached
  the reader as a setext heading underline, because the two halves disagreed
  about which characters are whitespace.

- **The mark reaching furthest along a run opens first.** Opening the narrower
  one cut the wider into pieces, and each piece shed the mark from the whitespace
  at its edge, so `*__0__ __0__ __0__*` lost one space of emphasis per
  conversion until there were none left.

### Changed

- **Emphasis with no unambiguous spelling is refused** with `ADF_UNREPRESENTABLE`
  at exit 2, rather than written as something a reader takes differently.
  Emphasis that ends in punctuation and is followed by a word character has no
  spelling in either character at any width, which is a limit of markdown and not
  of this tool.

- **A link title holding a blank line is refused** the same way. Escaping cannot
  reach it: a blank line ends the paragraph, and there is no character to put a
  backslash in front of.

### Performance

- A paragraph of many inline nodes no longer rebuilds the output buffer once per
  span to ask two questions about its last byte. A run of 1600 nodes spent 982MB
  of 1.01GB on that one line and took 393 times as long as one of 50. Output is
  byte-identical.

## [0.3.3] - 2026-08-14

`jr jql validate` stops calling a query clean when Jira said something about it.

**Nothing changed shape, and nothing that worked stops working.** No kind moved
a schema version, no default column set gained a field, no exit code or error
`code` changed meaning, and no input that used to be accepted is refused. A
query that was valid is still valid. What changed is that a warning Jira was
already sending now reaches you.

A patch rather than a minor, and the row that decides it did not exist before
this release, which is the fourth time in four releases that picking the number
found a missing rule. The policy is written around a kind's *shape*, and here the
shape is exactly what did not move: `warning` has been an optional repeated child
of `jql.validate` since v1, published in `jr contract` and in the output
contract. What moved is how often it is filled in. That is now
[a row of its own](docs/output-contract.md#stability-policy), minor, cascading to
the patch position the way every minor does in `0.y.z`.

### Fixed

- **A value that does not exist is reported on Cloud, not only on Data Center.**
  `jr jql validate` asked Cloud's parse endpoint in a mode that withholds this
  class of diagnostic, so a query naming a user who does not exist came back
  valid and silent:

  ```console
  $ jr jql validate --jql 'assignee = "someone-who-left"'
  <query valid="true" method="parse">
    <jql>assignee = "someone-who-left"</jql>
    <warning>The value 'someone-who-left' does not exist for the field 'assignee'.</warning>
  </query>
  ```

  The `<warning>` line is the new one. Data Center reported this all along,
  because it answers this question with a zero-row search and reads the
  `warningMessages` that come back; the two deployments now answer identically.

  Five spellings were affected, all of them naming a person: `assignee`,
  `reporter`, `creator`, `watcher`, and a value inside an `IN` list. Errors were
  never affected, on either deployment.

  **`valid` does not move.** A warning means Jira will run the query, so
  promoting it to `valid="false"` would refuse queries the server answers, in the
  one command whose job is to report the server's opinion rather than hold one.
  If you branch on the attribute alone you will see no change at all; if you want
  to know whether a clause matches nothing because nothing matches it or because
  the value was typed wrong, read the warnings.

  The replacement mode was swept against the old one over 25 queries before the
  change. It returns identical errors on every one, adds the five warnings above,
  and stays clean over twelve queries that legitimately match nothing, including
  an unused label, a text search for a string nothing contains, `IS EMPTY`,
  `currentUser()`, and `startOfWeek()`.

### Output contract

- No kind moved. `jql.validate` stays at **v1**: `warning` was always in its
  schema, and this release fills it in where Cloud used to leave it out.

## [0.3.2] - 2026-08-14

`jr` stops refusing relative periods that Jira accepts.

**Nothing changed shape, and nothing that worked stops working.** No kind moved
a schema version, no default column set gained a field, no exit code or error
`code` changed meaning, and every date form 0.3.1 accepted is still accepted and
still means the same thing. What changed is that four forms which exited 2 now
answer.

A patch rather than a minor because accepting an input that used to be refused
is minor under the stability policy, and the demotion this project uses in
`0.y.z` cascades: a change the policy calls minor moves the patch position. That
policy row did not exist before this release, which is the third time in three
releases that picking the number found a missing rule.

### Added

- **The relative periods Jira accepts on a date field.** Its units are
  case-insensitive for every unit and it takes a compound period, and `jr` took
  neither. All four of these used to be `INVALID_DATE` at exit 2, and all four
  are answered by Cloud and by Data Center 10.4:

  ```console
  $ jr issue list --updated-after -7D
  $ jr issue list --updated-after -2H
  $ jr issue list --updated-after -1W
  $ jr issue list --updated-after "-4w 2d"
  ```

  A compound carries its sign once, on the front, and its components sum, so
  `-1w 7d` selects exactly what `-14d` selects. It resolves in this process as
  well as at the server, which means `jr issue activity --since "-4w 2d"` bounds
  the events and not only the search.

- **`y` in a date function's duration argument.** `--updated-after 'endOfDay(-1y)'`
  was refused here and accepted by both deployments.

### Fixed

- **A date function's argument is a different grammar, and was being checked
  against the wrong one.** A period on a date field and a duration argument to a
  date function disagree about case, about which units exist, and about `M`:

  |          | `--updated-after -1M`  | `--updated-after 'endOfDay(-1M)'` |
  | -------- | ---------------------- | --------------------------------- |
  | units    | `m` `h` `d` `w` `M`    | `y` `M` `w` `d` `h` `m`           |
  | case     | insensitive            | **sensitive**                     |
  | `M`      | **minutes**            | **months**                        |
  | compound | accepted               | refused                           |

  One pattern was validating both, which is what refused `endOfDay(-1y)` above.
  There are two now, each generated from the table of units that gives those
  characters their meaning, so a unit cannot be accepted by a pattern and unknown
  to the code that reads it. `docs/output-contract.md` carries the comparison.

  **`--updated-after -1M` is still one minute**, and `endOfDay(-1M)` is still one
  month. Both were already true; neither is a change.

- **A relative offset too large for a `time.Duration` is refused rather than
  wrapped.** `-1000000d` is a legal query and 2739 years, and multiplying it into
  an `int64` of nanoseconds wrapped to a negative, so a bound written as the past
  named an instant in the future. Only `issue activity --since` resolves an
  offset locally, so only it could be affected.

### Output contract

- **Nothing moved.** `jr contract` reports the same versions 0.3.1 did for every
  kind.
- `docs/output-contract.md` gains the two-grammar comparison, the compound
  period's arithmetic, and a stability policy row for **accepting an input that
  used to be refused**: minor, because every invocation a script already makes
  still runs and still means the same thing.

## [0.3.1] - 2026-08-14

A document now names the Jira it came from.

**Nothing changed shape.** No kind moved a schema version, no default column set
gained a field, no exit code or error `code` changed meaning, and no input that
worked in 0.3.0 stops working. A script written against 0.3.0 parses 0.3.1
identically and behaves identically.

A patch rather than a minor because an added optional attribute is minor under
the stability policy, and the demotion this project uses in `0.y.z` cascades: a
change the policy calls minor moves the patch position.

### Added

- **`site` on the envelope**, naming the instance an answer came from. It is the
  base URL the request was actually sent to, including any context path, and not
  the one configured; the two differ under a context path, and differing is the
  case worth reporting.

  ```xml
  <result kind="issue.list" v="7" site="https://acme.atlassian.net">
  ```

  ```json
  { "kind": "issue.list", "v": 7, "site": "https://acme.atlassian.net", ... }
  ```

  **Absent, not empty**, on a command that reached no Jira. `jr version` and
  `jr context list` name no instance rather than an empty one.

  It exists because two answers from two Jiras were byte-identical once both were
  on disk. The case that prompted it was a command run against the wrong instance
  returning a well-formed, `complete="true"`, exit-0 document about it, with
  nothing anywhere in the output to say so. Nothing had misbehaved; the answer
  simply did not say what it was an answer about.

  **TSV carries none**, because TSV has no envelope. That is the same limit
  truncation has in that format, where `complete="false"` becomes a structured
  stderr warning plus exit 3. A column was considered and rejected: it would
  change a default column set, which is breaking, and repeat one value on every
  row. A caller who needs provenance in a pipeline asks for `--format json`.

### Output contract

- **Nothing moved.** `jr contract` reports the same versions 0.3.0 did for every
  kind. The per-kind shapes describe the payload, and `site` is on the envelope
  every kind shares, so `make golden` rewrote no shape file.
- The envelope section of `docs/output-contract.md` documents `site`, what it is
  precisely, and why TSV does not carry it.

## [0.3.0] - 2026-08-14

Three date defects, all of them found in one transcript of somebody using 0.2.1
to ask what they had worked on that week. They got the answer, and they got it
by routing around the tool twice.

**Nothing changed shape.** No kind moved a schema version, no default column set
gained a field, and no exit code changed meaning, so a script written against
0.2.1 parses 0.3.0 identically.

**It is still a minor-position bump**, because three forms that used to be
accepted are now refused, and one refusal moved from Jira to here and changed
the `code` it carries. A script parses every document the same way and may stop
working all the same. The stability policy had no row for that and now has two.

### Fixed

- **`issue activity --since` bounds the events, not only the search.**
  `--since "2026-08-10 00:00"` returned events from before the bound, at exit 0,
  `complete="true"`, with nothing on stderr. `--since` does two jobs: it goes to
  the server as `updated >=`, which bounds the *issues*, and it is resolved here
  into the instant each *event* is compared against. An issue updated yesterday
  holds comments from years ago, so a bound that reaches only the first answers a
  question about issues while claiming to answer one about events.

  The two halves disagreed about what a date is. Four of the seven forms
  `--since` accepted resolved to "no bound at all". They are one enumeration now,
  read by the query builder and the event filter alike.

- **An absolute `--since` is read in the Jira account's timezone.** It was read
  in UTC, so even the two forms that did work were wrong by the account's
  offset: five hours of over-reporting on `America/Chicago`, and nine hours of
  silently dropped events on `Asia/Tokyo`. This costs one `GET /myself`, and only
  for an absolute date; a relative offset names an instant and still costs
  nothing.

- **`M` is minutes, not months.** `relativeOffset` read it as thirty days. Jira's
  period units are case-insensitive, so `M` and `m` are one unit and this was out
  by a factor of 43,200: `--updated-after -1M` asked the server for the last
  minute and told the local filter it meant the last month. Measured against both
  deployments, where `-60M`, `-60m` and `-1h` return the same rows and `-43200M`
  returns what `-30d` returns.

  **This changes no query.** The string was always sent verbatim and Jira always
  read it as minutes; what was wrong was what `jr` believed it meant, which only
  `issue activity` acted on.

- **`--worklog-after` and `--worklog-before` refuse a time of day on Data
  Center**, where Jira refuses it, instead of spending a request to be told. They
  still accept one on Cloud, where Jira accepts it. The rule is the field and the
  deployment together — `updated` takes a minute on both — so a blanket refusal
  would have invented a limit half the installed base does not have.

### Changed

- **A date function on `issue activity --since` is refused** with
  `UNBOUNDABLE_DATE`, rather than bounding the issues and comparing the events
  against nothing. Computing `startOfWeek()` here means choosing the day a week
  starts on, and that choice is Jira's. Use an absolute date or a relative
  offset; every other date flag still passes a function straight through.

- **The zone database is compiled in.** `time.LoadLocation` otherwise reads a
  host copy that a Windows binary and a scratch container do not have. About
  450 KB, against roughly 4 MB of headroom under the reader binary's size budget.

### Output contract

- **Nothing moved.** `jr contract` reports the same versions 0.2.1 did for every
  kind.
- Three new error codes, all exit 2: `UNBOUNDABLE_DATE`,
  `NO_ACCOUNT_TIMEZONE`, and `UNKNOWN_ACCOUNT_TIMEZONE`. The last two are a site
  that did not report the account's zone, or reported one no database has; both
  deployments send one, so neither is expected in practice.
- **`INVALID_DATE` now fires where `BAD_REQUEST` did**, for a worklog date
  carrying a time of day on Data Center. Same exit, different `code`, because the
  refusal happens here now instead of at the far end.
- The stability policy gained the two rules this release needed and did not have.
  **Refusing an input that used to be accepted is major**, and so is **moving a
  refusal earlier so the same input carries a different `code`**. Neither moves a
  kind's shape, which is what the rest of the policy is written around, and both
  break a caller's working command. Fixing a wrong answer does not spare the
  version: the caller has to change what they send either way, and the version
  number is the only place they find that out before it happens.

## [0.2.1] - 2026-08-13

One new check and one thing removed. **Nothing changed shape**: no kind moved a
schema version, no default column set gained a field, and no exit code or error
`code` changed meaning, so a script written against 0.2.0 parses 0.2.1
identically.

A patch rather than a minor because the demotion this project uses in `0.y.z`
cascades. A breaking change moves the minor position, so a feature that breaks
nothing cannot also move it. `docs/output-contract.md` now says that; it had
said only the first half, and this is the first release that had to choose.

### Added

- **`issue list` says so when a label filter names a label nothing carries.**
  `--label retyr` returned a header, no rows, `complete="true"`, and exit 0, and
  so did `--label retry` on a day nothing carried it: a typo and a fact were
  indistinguishable. A label no issue on the site carries now produces the
  warning `UNKNOWN_LABEL` on stderr. **The query still runs and still exits 0**,
  because asking about a label nobody uses is a legal question with a correct
  answer, and stdout is byte-identical to what 0.2.0 emitted. `--not-label` is
  checked too, where an unknown value widens the result set instead of emptying
  it.

  It costs one request per distinct label, and one more only when it is about to
  warn. Those count against `--max-requests` like any other. Nothing is cached,
  because a label exists exactly as long as an issue carries it.

  It stays quiet rather than guessing where it cannot tell: a site that reports
  no labels at all, a result set filled to the server's cap, any failed request,
  and an MCP tool call, which discards stderr and so does not spend the requests
  either. Silence is never a claim that a label will match, because the check
  is site-wide and a query may be scoped to one project.

### Removed

- **The `tui`, `browser`, and `clipboard` build tags.** They came from the
  spec's tag table, written before any code was, and no build ever carried a
  feature behind them. `jr version` prints a shorter tag list as a result: the
  full profile now reports `tags=prompt,render,mcp,write,admin`. No command,
  flag, or output changed, because there was nothing behind them to remove.

### Output contract

- **Nothing moved.** `jr contract` reports the same versions 0.2.0 did for every
  kind.
- The stability policy gained two rules it needed and did not have. **Adding a
  warning `code` is minor and bumps no kind version**, because a warning is a
  separate document on stderr and a command that gains one emits the same result
  it did before; changing what an existing warning code means is major, for the
  reason an error code is. And **the pre-1.0 demotion cascades**, so a minor
  change moves the patch position.
- The warnings this tool emits are now documented as one set, namely
  `RESULT_TRUNCATED`, `POSSIBLE_DUPLICATE`, and `UNKNOWN_LABEL`, rather than
  each being described where it happens to arise.

### Documentation

- **`jr auth token --help` documented a pipeline that cannot work.** Its example
  was `curl -H "Authorization: $(jr auth token)"`, and the value is one field of
  a record, so curl received an XML document and failed with an argument error
  that reads like a curl problem. The help now shows the form that works, and a
  test runs it.
- **The `jr version` example in `docs/getting-started.md` named 0.1.0** at
  0.2.0, and carried a tag set three tags out of date. It shows the untagged
  form a clone prints instead, which cannot go stale, and two gates now hold it
  there: one refusing a release string in any hand-written document, and the
  existing worked-version-banner check, which had been reading four files and
  not that one.

## [0.2.0] - 2026-08-13

One breaking change, and it is a small one with a wide blast radius: an
attribute that was always present may now be absent. **A consumer that reads
`total` on a comment container has to handle it not being there.** Nothing else
in the output moved.

A minor rather than a major because this project is in `0.y.z`, where semver
puts the public API outside its stability guarantee. Tagging 1.0.0 would be a
claim about how stable this tool intends to be from then on, which is a decision
to make on its own rather than as a side effect of one change. `docs/output-contract.md`
now says so, because it did not.

### Output contract

- **`total` is optional on the `comments` container.** `issue.list` v6 → v7 and
  `issue.get` v8 → v9. It is written only when the server reported a count, and
  its absence means this tool does not know how long the thread is. **An absent
  `total` always comes with `complete="false"`** and exit 3, exactly as a thread
  known to be clipped does, so a consumer already branching on `complete` needs
  no new branch. One reading it as an integer does: the attribute may not be
  there, and a missing one is not `0`. In TSV the `comments-total` cell is empty
  rather than zero.
- No other kind moved. `jr contract` reports the same versions 0.1.1 did for
  everything else.

### Fixed

- **An embedded thread reported a clipped result as complete.** `total` was
  required on the container and built from an integer that decoded to zero when
  the server sent no count, so `count >= total` held for every thread and one
  holding two comments of an unknown number was published as `total="0"
  complete="true"` at exit 0. Neither path that fills that container can page to
  settle it: `issue get --with-comments` fetches one bounded page by design, and
  `issue list --with-comments` receives the thread as a projection inside a
  search response. A required attribute left no way to say "unknown", which is
  why the fix is a shape change rather than a better default.
- **0.1.1 said this was already fixed for three paths and had fixed one.** Its
  entry named `issue get --with-comments`, the projections behind `issue list
  --with-comments`, and the worklog top-up in `issue activity`. Only the first
  was true: the other two decided completeness in a different function, on an
  integer nobody had converted, and shipped unchanged. The claim is corrected
  here rather than quietly superseded.
- **`issue activity` built its feed from worklogs it never refetched.** The
  command tops up any issue whose worklog projection was clipped, because both
  deployments inline the oldest twenty and a feed about recent work wants the
  newest. Deciding whether to top up read the same absent count as zero,
  concluded the projection was whole, and made no request, so the feed silently
  omitted recent entries and called itself complete. This half had no attribute
  to get wrong, which is why it outlived the release that claimed it.

### Documentation

- The stability policy gained the rule this release needed and did not have:
  **making a required element or attribute optional is major.** It reads like
  "adding a new optional element or attribute", which is minor, and is its
  opposite: an addition is something no existing consumer looks for, and this is
  something an existing consumer already reads.
- It also says what major means before 1.0.0, which nothing did. The document
  opened with "breaking it requires a major bump" while the project sat at
  0.1.1, so read literally it demanded 1.0.0 for any breaking change.
- `docs/releasing.md` gained a "Which number to bump" section pointing at that
  policy. It described the shape of a version and what to do about a wrong one,
  and never said how to choose the next.

## [0.1.1] - 2026-08-13

A code sweep of every package, and seven changes out of it. Six are places
where this tool said something that was not so; the seventh is the fuzzer that
should have been guarding the escaping rules all along.

Nothing changed shape. No kind moved a schema version, no default column set
gained a field, and no exit code or error `code` changed meaning, so a script
written against 0.1.0 parses 0.1.1 identically. What changes is that three
conditions which used to report success now report the failure they always
were.

### Fixed

- **A paged sub-resource reported a clipped result as complete** when the
  server sent no `total`. It decoded to zero, and the first non-empty page
  satisfied the loop's `startAt >= total`, so `jr issue comment list` on a
  three-comment thread emitted two rows, `complete="true"`, and exit 0. The
  same arithmetic governed `issue worklog list` and `issue history`. An absent
  count is not a count of nothing: the listing keeps asking, and the empty page
  it gets is the answer.
- **The same absent `total` decided completeness** for `issue get
  --with-comments`, for the projections behind `issue list --with-comments`,
  and for the worklog top-up in `issue activity`. None of those can page to
  resolve it, so an unknown thread length is now `complete="false"` and exit 3
  rather than a claim.
- **A buffered response larger than 64 MiB was truncated silently.** The read
  stopped at the cap and returned no error, so the caller was handed part of an
  answer presented as the whole of one. In practice it surfaced as `Jira
  returned a search result this tool cannot read`, blaming the server for
  something this client did. It is refused now, and the recorder refuses rather
  than writing a cassette that lost its tail.
- **Two differently named elements in one page were emitted as TSV and refused
  as XML.** The check compared against counters the same function advanced
  afterwards, so a whole batch went unexamined and the same document was
  accepted or rejected by `--format`.
- **A refused collection still emitted a closing document**: a bare TSV header,
  or an envelope reading `count="0" complete="true"` for a result that had just
  been rejected. No command reached it, because the CLI returns the error
  first, but a refusal now latches in the stream rather than depending on every
  caller remembering to.
- **The progress line counted "issues" whatever it was counting**, so `jr
  project list` reported "12 issues" on a terminal, and nine listings reported
  a ratio of `n / n` at the moment `--limit` had cut the result short and exit
  3 was about to say otherwise. Both are stderr on a terminal only, and neither
  is visible to a script.

### Output contract

- **`RESPONSE_TOO_LARGE` is new.** Exit 1, not retryable: a response too large
  once is too large again, so advertising a retry would spend a caller's budget
  on a certainty. It replaces a decode failure blamed on Jira, and it fires
  only where this tool previously truncated in silence.
- No kind moved. `jr contract` reports the same schema versions 0.1.0 did.

### Internal

- The output contract's own escapers have fuzz targets: the TSV cell round
  trip; its composition with `JoinList`, which is the rule that keeps a status
  named `Ready, Set` from becoming two statuses; and the refusal of anything
  XML 1.0 cannot carry, held to a definition written independently of the one
  it guards. 20 targets to 23.
- `internal/adf`'s recursion is bounded by `encoding/json` and now has a test
  saying so, because it is a bound this package depends on and does not own.

## [0.1.0] - 2026-08-13

First release, and the first build anybody can pin.

Everything to date is collapsed into this one entry rather than enumerated.
Nothing below was ever published, so no consumer pinned any of it, and the
kinds that moved through eight schema versions in a private tree moved with
nobody downstream. What a first-time reader needs is what the tool does, which
is the README, not how it got here.

From here the sections mean what they say: a change listed under **Output
contract** is a change somebody's script can see.

The entries below are the last of the private tree, kept because they are
recent enough to be worth reading.

### Added

- `jr issue history` — who changed what on an issue, and when.
- `jr issue activity` — comments, transitions, field changes, and worklogs
  merged into one time-ordered feed.
- `jr issue list --with-comments` — every row's comment thread, in the request
  the page already cost.
- An interactive token prompt on `jr auth login` in builds carrying the
  `prompt` tag, so logging in no longer requires a shell pipeline.

### Changed

- The MCP server no longer publishes the commands that reconfigure it or reveal
  its credential: `auth_login`, `auth_logout`, `auth_token`, and the four
  `context` writers are off the wire and refused by name.

### Output contract

- `issue.list` v5 → v6, and `issue.get` v7 → v8. The comment container moved
  onto the shape both kinds share, and gained `total` beside `count` plus an
  optional `start-at`. A consumer reading `complete` on that container should
  now also read `total`, and on Cloud `start-at`, or it will treat the newest
  twenty comments as the whole thread.
- `issue.activity` v1 and `issue.history` v1 are new.

[unreleased]: https://github.com/kmoneil/jr/compare/v0.9.3...main
[0.9.3]: https://github.com/kmoneil/jr/releases/tag/v0.9.3
[0.9.2]: https://github.com/kmoneil/jr/releases/tag/v0.9.2
[0.9.1]: https://github.com/kmoneil/jr/releases/tag/v0.9.1
[0.9.0]: https://github.com/kmoneil/jr/releases/tag/v0.9.0
[0.8.0]: https://github.com/kmoneil/jr/releases/tag/v0.8.0
[0.7.1]: https://github.com/kmoneil/jr/releases/tag/v0.7.1
[0.7.0]: https://github.com/kmoneil/jr/releases/tag/v0.7.0
[0.6.0]: https://github.com/kmoneil/jr/releases/tag/v0.6.0
[0.5.0]: https://github.com/kmoneil/jr/releases/tag/v0.5.0
[0.4.0]: https://github.com/kmoneil/jr/releases/tag/v0.4.0
[0.3.3]: https://github.com/kmoneil/jr/releases/tag/v0.3.3
[0.3.2]: https://github.com/kmoneil/jr/releases/tag/v0.3.2
[0.3.1]: https://github.com/kmoneil/jr/releases/tag/v0.3.1
[0.3.0]: https://github.com/kmoneil/jr/releases/tag/v0.3.0
[0.2.1]: https://github.com/kmoneil/jr/releases/tag/v0.2.1
[0.2.0]: https://github.com/kmoneil/jr/releases/tag/v0.2.0
[0.1.1]: https://github.com/kmoneil/jr/releases/tag/v0.1.1
[0.1.0]: https://github.com/kmoneil/jr/releases/tag/v0.1.0
