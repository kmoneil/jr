#!/bin/sh
# Print the version this build should report, always in semver.
#
# `jr version` and the User-Agent both carry this string, and the User-Agent is
# what a Jira administrator sees in their access logs. `git describe --always`
# degrades to a bare commit hash when nothing is tagged, which is what this
# replaces: `jr/786d271` tells an administrator nothing, does not sort, and does
# not even announce that it is not a version.
#
# Three cases, all semver-shaped so a consumer can parse one rule:
#
#   tagged, clean       1.2.0
#   tagged, moved on    1.2.0+3.gabc1234        (and .dirty if the tree is)
#   never tagged        0.0.0-untagged+abc1234  (and .dirty)
#   no git at all       0.0.0-unknown
#
# Commits-since-tag goes in build metadata rather than a prerelease suffix.
# `git describe` writes it as `1.2.0-3-gabc1234`, and semver orders any
# prerelease *below* the release it hangs off — so three commits after v1.2.0
# would sort before v1.2.0. Build metadata is ignored in precedence, which is
# the honest place for "this is v1.2.0 plus some commits".
#
# 0.0.0 rather than a plausible-looking default: a build nobody tagged should
# not claim a release number somebody might act on.
#
# Every exit goes through `emit`, which refuses rather than prints. The
# guarantee above was written in four places and checked in none, and
# `version=${tag#v}` passes any tag straight through: `git tag nightly` used to
# stamp `nightly+1.g2acd8a4`, and `git tag rel/2024` stamped a string holding a
# character semver does not allow at all. Both reached the binary and the
# access log.
set -eu

# The semver grammar as an ERE. Deliberately not the full specification — it
# does not reject a leading zero in a numeric prerelease identifier, which is a
# conformance detail no consumer of this string will act on. What it rejects is
# what this script can actually produce from a tag nobody checked.
SEMVER='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'

# emit prints a version or prints nothing at all. The second argument is the tag
# the version was derived from, when there was one, because the fix for a
# refusal is `git tag` and the operator needs to know which one is wrong.
#
# Nothing reaches stdout on the refusing path. The Makefile stamps whatever this
# script prints, so a refusal that still printed would be stamped.
emit() {
	if printf '%s' "$1" | grep -Eq "$SEMVER"; then
		printf '%s\n' "$1"
		exit 0
	fi
	if [ -n "${2-}" ]; then
		echo "version.sh: the tag '$2' does not produce a semantic version: '$1'" >&2
	else
		echo "version.sh: computed '$1', which is not a semantic version" >&2
	fi
	echo "version.sh: this string is stamped into the binary and sent as the" >&2
	echo "version.sh: User-Agent, where a Jira administrator reads it to work out" >&2
	echo "version.sh: which release is talking to them. Releases are tagged" >&2
	echo "version.sh: vMAJOR.MINOR.PATCH, e.g. v1.2.3." >&2
	exit 1
}

if ! git rev-parse --git-dir >/dev/null 2>&1; then
	emit "0.0.0-unknown"
fi

# `git status --porcelain` rather than `git diff HEAD`, which is what
# `git describe --dirty` uses: a diff against HEAD sees modified tracked files
# and not an untracked one, and an untracked .go file in a package is compiled
# into the binary. A build carrying code that is in no commit must not report
# the tag as though it were that tag. Ignored paths do not count — bin/ and
# coverage.out are build output, so `make build` does not make the tree dirty.
#
# The exit status is checked rather than the output alone. `[ -n "$(git status
# --porcelain 2>/dev/null)" ]` reads a failure as a clean tree, which is the
# same quiet-failure shape the rest of this script is written against: not
# knowing whether the tree is clean is different from knowing it is.
if ! status=$(git status --porcelain 2>&1); then
	echo "version.sh: git status failed, so this script cannot tell whether the" >&2
	echo "version.sh: tree is clean:" >&2
	echo "$status" | sed 's/^/version.sh:   /' >&2
	exit 1
fi
dirty=
if [ -n "$status" ]; then
	dirty=".dirty"
fi

# --long so the format is identical whether or not HEAD is the tag itself,
# which is what lets the parsing below be one path rather than two.
if described=$(git describe --tags --long 2>/dev/null); then
	sha=${described##*-}      # gabc1234
	rest=${described%-*}      # v1.2.0-3
	count=${rest##*-}         # 3
	tag=${rest%-*}            # v1.2.0
	version=${tag#v}
	if [ "$count" = "0" ] && [ -z "$dirty" ]; then
		emit "$version" "$tag"
	fi
	emit "$version+$count.$sha$dirty" "$tag"
fi

sha=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
emit "0.0.0-untagged+$sha$dirty"
