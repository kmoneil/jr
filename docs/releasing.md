# Releasing

A tag is the release. Pushing one builds every profile for every platform,
attaches them to a GitHub release with checksums and build provenance, and
stamps the version into each binary and into the `User-Agent` a Jira
administrator sees in their access log.

None of that can be withdrawn once somebody has fetched it, which is why the
steps below are in this order.

## Before you tag

1. **`main` is green.** The release workflow runs `make ci` again on the tagged
   commit rather than trusting an earlier run, because a tag can be placed on
   any commit — including one that never had one.
2. **`CHANGELOG.md` has a section for the version.** The workflow reads it for
   the release notes and **refuses to publish without it**. That is deliberate:
   a release nobody wrote anything about is one nobody can decide whether to
   take.
3. **Every schema version moved in this release is listed under Output
   contract** in that section, with its kind. A consumer pins `kind` and `v`
   from the document, so that list is the part of the notes with consequences.
4. **The section is dated the day you tag.** The heading carries a date and the
   workflow does not check it, so a changelog prepared on one day and tagged the
   next publishes notes dated wrong.
5. **The README says nothing a tag has just made false.** There are three
   places, and on the first release all three change:

   | Where | What it says today |
   | --- | --- |
   | The badge row | `status-pre--release` |
   | The status banner | "pre-release, and deliberately untagged", and every build reports `0.0.0-untagged+<sha>` |
   | **Install** | "No release binary yet, and that is deliberate" |

   Install is the one with a reader waiting on it: from the first tag there are
   sixteen archives, `checksums.txt`, and an attestation, and somebody who
   arrives wanting the tool should not be told to install Go first. Build from
   source stays, below the download.

   Nothing asserts these three, because a sentence about a release cannot be
   checked against a repository that has not made one. They are here instead.

## Cutting it

```console
$ git switch main && git pull
$ $EDITOR CHANGELOG.md          # move Unreleased into the new version, dated today
$ $EDITOR README.md             # the status banner, on the first release
$ git switch -c docs/changelog-0-1-0
$ git commit -am "docs(changelog): prepare 0.1.0"
$ gh pr create --fill && gh pr merge --squash
$ git switch main && git pull
$ git tag v0.1.0
$ git push origin v0.1.0
```

The tag goes on `main` after the changelog has landed, not before — the workflow
reads `CHANGELOG.md` **from the tagged commit**.

## What the version looks like

`scripts/version.sh` produces it, always semver, in four cases:

| State | Reports |
| --- | --- |
| Tagged, clean tree | `1.2.0` |
| Tagged, moved on | `1.2.0+3.gabc1234` |
| Never tagged | `0.0.0-untagged+abc1234` |
| No git at all | `0.0.0-unknown` |

Commits-since-tag are build metadata rather than a prerelease suffix, because
semver orders a prerelease *below* the release it hangs off — three commits
after `v1.2.0` would otherwise sort before it.

**A tag that is not itself semver fails the build rather than being stamped.**
`git tag nightly` used to produce `nightly+1.g2acd8a4`, which reached the binary
and the access log. The script validates its own output and the Makefile checks
it.

The release job asserts the built binary reports the tag. A release whose
`jr version` disagrees with the tag people fetched is unpinnable, which is the
one thing this project's whole contract exists to prevent.

## What ships

Four profiles × two operating systems × two architectures, as
`jr-<profile>_<version>_<os>_<arch>.tar.gz`, each containing the binary named
`jr` plus `LICENSE`, `NOTICE`, and `README.md`.

The profiles ship separately because they are the product rather than a build
convenience — see [build-profiles.md](build-profiles.md). Somebody choosing
`jr-reader` for an agent needs it for their own machine, not only for
`linux/amd64`.

`checksums.txt` covers every archive, and
[build provenance](https://docs.github.com/actions/security-guides/using-artifact-attestations)
is attached, so a consumer can verify an archive came from this workflow on this
repository at this commit:

```console
$ gh attestation verify jr-reader_0.1.0_darwin_arm64.tar.gz --repo kmoneil/jr
```

## If a release is wrong

**Do not move the tag.** Anyone who fetched it has the old commit under the same
name, and their `jr version` will disagree with yours forever. Cut the next
patch version, and say what was wrong in its changelog entry.

Deleting a release on GitHub does not unpublish the tag from clones that already
have it.
