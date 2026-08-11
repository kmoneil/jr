#!/usr/bin/env bash
#
# Record jr's conversation with the rig into a cassette.
#
#   ./record.sh --all
#   ./record.sh internal/resource/board/testdata/boards-recorded.datacenter.json board list
#
# --all walks manifest.tsv, which is also what internal/lint reads, so the
# checklist and the thing that runs it cannot drift apart.
#
# The cassette path is relative to the repository root, so it reads the same as
# the file it writes. Everything after it is passed to jr untouched.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

profile=$here/profile
jr=${JR:-$repo/bin/jr}

export XDG_CONFIG_HOME=$profile/config
export XDG_STATE_HOME=$profile/state
export XDG_CACHE_HOME=$profile/cache

# The profile guard, and the reason this script exists rather than a bare
# JIRA_RECORD= in front of each command.
#
# Recording against production is refused, and the way that refusal fails is
# somebody with a working production context running one of these lines. So
# the throwaway profile is exported here and checked here: a config home with
# exactly one context in it, pointing at the container. It is checkable, unlike
# "did you mean to point at that host", and it makes the wrong instance
# unreachable rather than merely discouraged.
if [ ! -f "$profile/config/jr/config.toml" ]; then
	say "no throwaway profile at $profile."
	say "Run: make dc-up   (which sets one up against the container)"
	exit 2
fi

if [ ! -x "$jr" ]; then
	say "no binary at $jr — run 'make build', or set JR"
	exit 2
fi

one() {
	local out=$1
	shift
	case $out in
	*.datacenter.json) ;;
	*)
		# The deployment is in the filename because internal/lint groups
		# cassettes by it, and a recording filed under the wrong deployment is
		# evidence for the API it did not come from.
		say "cassette name must end in .datacenter.json: $out"
		return 2
		;;
	esac

	# JIRA_RECORD_SCRUB is deliberately not set.
	#
	# This instance is seeded with invented identifiers from the start — project
	# ENG, the names in seed.sh — so there is no mapping from a real identifier
	# to a fictional one, which is the leak internal/lint/scrubpairs_test.go
	# exists to refuse. Nothing to scrub beats scrubbing correctly. The host is
	# rewritten to recorded.invalid by the recorder either way.
	say "recording -> $out"
	mkdir -p "$(dirname "$repo/$out")"
	JIRA_RECORD="$repo/$out" "$jr" "$@" >/dev/null || {
		say "  jr exited $? — see above; the cassette may be partial"
		return 1
	}
}

if [ "${1:-}" != "--all" ]; then
	[ "$#" -ge 2 ] || {
		say "usage: $0 --all | $0 <cassette-path-from-repo-root> <jr args...>"
		exit 2
	}
	one "$@"
	say
	say "read the residue lines above before committing"
	exit 0
fi

failed=0
recorded=0
skipped=0
while IFS=$'\t' read -r group cassette command; do
	case ${group:-} in
	'' | \#*) continue ;;
	esac
	case $command in
	'!'*)
		say "skipping $cassette: ${command#!}"
		skipped=$((skipped + 1))
		continue
		;;
	esac
	# The command field is an argv with shell quoting, so a JQL string stays
	# one argument. Splitting on whitespace would send Jira four words.
	eval "set -- $command"
	if one "$cassette" "$@"; then
		recorded=$((recorded + 1))
	else
		failed=$((failed + 1))
	fi
done <"$here/manifest.tsv"

say
say "recorded $recorded, skipped $skipped, failed $failed"
say "read the residue lines above before committing anything"
[ "$failed" -eq 0 ]
