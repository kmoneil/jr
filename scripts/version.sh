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
set -eu

if ! git rev-parse --git-dir >/dev/null 2>&1; then
	echo "0.0.0-unknown"
	exit 0
fi

# `git status --porcelain` rather than `git diff HEAD`, which is what
# `git describe --dirty` uses: a diff against HEAD sees modified tracked files
# and not an untracked one, and an untracked .go file in a package is compiled
# into the binary. A build carrying code that is in no commit must not report
# the tag as though it were that tag. Ignored paths do not count — bin/ and
# coverage.out are build output, so `make build` does not make the tree dirty.
dirty=
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
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
		echo "$version"
	else
		echo "$version+$count.$sha$dirty"
	fi
	exit 0
fi

sha=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
echo "0.0.0-untagged+$sha$dirty"
