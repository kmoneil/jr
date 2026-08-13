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

[unreleased]: https://github.com/kmoneil/jr/compare/v0.2.0...main
[0.2.0]: https://github.com/kmoneil/jr/releases/tag/v0.2.0
[0.1.1]: https://github.com/kmoneil/jr/releases/tag/v0.1.1
[0.1.0]: https://github.com/kmoneil/jr/releases/tag/v0.1.0
