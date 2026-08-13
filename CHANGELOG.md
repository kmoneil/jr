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

[unreleased]: https://github.com/kmoneil/jr/compare/v0.1.0...main
[0.1.0]: https://github.com/kmoneil/jr/releases/tag/v0.1.0
