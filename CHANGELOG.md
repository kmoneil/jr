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

[unreleased]: https://github.com/kmoneil/jr/compare/v0.3.3...main
[0.3.3]: https://github.com/kmoneil/jr/releases/tag/v0.3.3
[0.3.2]: https://github.com/kmoneil/jr/releases/tag/v0.3.2
[0.3.1]: https://github.com/kmoneil/jr/releases/tag/v0.3.1
[0.3.0]: https://github.com/kmoneil/jr/releases/tag/v0.3.0
[0.2.1]: https://github.com/kmoneil/jr/releases/tag/v0.2.1
[0.2.0]: https://github.com/kmoneil/jr/releases/tag/v0.2.0
[0.1.1]: https://github.com/kmoneil/jr/releases/tag/v0.1.1
[0.1.0]: https://github.com/kmoneil/jr/releases/tag/v0.1.0
