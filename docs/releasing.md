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
5. **Nothing in the docs claims a version in prose.** This was four places
   before 0.1.0: a `status-pre--release` badge, a status banner naming
   `0.0.0-untagged`, an Install section saying there was no release binary, and
   the same in `docs/getting-started.md`. All four are gone, and their
   replacements were deliberately written so that no later release has to touch
   them:

   - the badge reads the latest release from GitHub, so it updates itself;
   - the banner says the tool is pinnable without saying at what version;
   - both install paths use `gh release download --pattern`, which needs no
     version number at all.

   So this step is a check rather than an edit.

   **A fifth place was missed by all of that, and is now asserted.**
   `docs/getting-started.md` printed a worked `jr version` naming 0.1.0 and
   went on printing it through 0.1.1 and 0.2.0. It is sample output rather
   than a sentence, which is exactly why reading the prose did not catch it.
   `internal/lint/releaseversions_test.go` now refuses any release string in
   any hand-written document except the `1.2.0` placeholder and the
   `0.0.0-untagged+<sha>` form a clone prints, neither of which any build
   produces. The claim that could not be checked was "this is the current
   release"; the one that can is "no document names a release at all".

   The same example also showed a tag set three tags out of date, and the gate
   that reads worked version banners,
   `TestTheWorkedVersionExamplesAreOnesTheCodeCouldPrint`, was not reading
   this file. It is now.

## Cutting it

```console
$ git switch main && git pull
$ $EDITOR CHANGELOG.md          # move Unreleased into the new version, dated today
$ git switch -c docs/changelog-0-1-0
$ git commit -am "docs(changelog): prepare 0.1.0"
$ gh pr create --fill && gh pr merge --squash
$ git switch main && git pull
$ git tag v0.1.0
$ git push origin v0.1.0
```

The tag goes on `main` after the changelog has landed, not before — the workflow
reads `CHANGELOG.md` **from the tagged commit**.

## Which number to bump

[The stability policy](output-contract.md#stability-policy) decides it, and the
kinds decide the policy: what the release version does follows from whether a
kind's shape moved and how, not from how much code changed.

The one thing to know before reading it is that **this project is in `0.y.z`, so
a change the policy calls major moves the minor position.** 0.1.1 to 0.2.0, not
1.0.0. Tagging 1.0.0 says this tool's shape is stable from then on, and that is
a decision to make on its own and not as a side effect of one breaking change.

A commit carrying `!` and a `BREAKING CHANGE:` footer is how you find those
changes in `git log` since the last tag. It does not by itself say which
position moves.

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

## If the release did not finish

Different from the section below, and the difference is the whole of what to do.
A release that is *wrong* exists and somebody may have fetched it. A release
that did not *finish* does not exist: the tag is public and there is nothing
behind it.

**Re-run the workflow. Do not re-tag.**

```console
$ gh run list --workflow=release.yml --limit 1
$ gh run rerun <id> --failed
```

The tag is already correct, which is why moving it is the wrong instinct even
though the release is missing. The gate is the first of three jobs and `build`
and `publish` both hang off it, so a red gate means nothing was compiled and
nothing was published. There is no half-published state to clean up.

This has happened once, on the first attempt at `v0.1.1`: `sum.golang.org`
returned an `INTERNAL_ERROR` while the gate was installing the tools `make ci`
expects. The re-run was green in every job. The tools are pinned and cached
since, so that fetch is off the common path rather than retried, but a scan, a
registry, or a runner can still fail transiently and the answer is the same one.

One thing to know while reading the run list: **a re-run overwrites the run's
conclusion**, so a release that failed and was re-run reads `success` afterwards
and leaves no trace of the flake. If you want the failure recorded, record it
somewhere other than the run history.

## If a release is wrong

**Do not move the tag.** Anyone who fetched it has the old commit under the same
name, and their `jr version` will disagree with yours forever. Cut the next
patch version, and say what was wrong in its changelog entry.

Deleting a release on GitHub does not unpublish the tag from clones that already
have it.
