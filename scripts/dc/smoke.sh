#!/usr/bin/env bash
#
# Run every read verb, and the writes that leave nothing behind, against the
# rig — and report what each one did.
#
# This is the version-compatibility check. The recordings pin what one Data
# Center answered; this asks whether another one answers at all. Jira 11
# already produced a defect that no fixture could have found, because it
# refuses the credential shape before any command runs, and 9.12 is the line a
# lot of customers are still on.
#
# It prints one row per command: the exit code, and the error code when there
# is one. Nothing is asserted — a sweep that decided what "wrong" means would
# need to know which refusals are correct, and several here are.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

profile=$here/profile
jr=${JR:-$repo/bin/jr}
project=${SEED_PROJECT:-ENG}

export XDG_CONFIG_HOME=$profile/config
export XDG_STATE_HOME=$profile/state
export XDG_CACHE_HOME=$profile/cache

[ -f "$profile/config/jr/config.toml" ] || {
	say "no throwaway profile at $profile. Run: make dc-up"
	exit 2
}
[ -x "$jr" ] || {
	say "no binary at $jr — run 'make build', or set JR"
	exit 2
}

version=$(curl -sS -u "${ADMIN_USER:-ada}:${ADMIN_PASSWORD:-fixtures-only}" \
	"$(jira_base)/rest/api/2/serverInfo" 2>/dev/null |
	sed -E 's/.*"version" *: *"([^"]+)".*/\1/')
say "sweeping Jira ${version:-unknown} at $(jira_base)"
say

fail=0
run() {
	local label=$1
	shift
	local out code error
	# The exit status has to come from jr and not from whatever formats its
	# output, which is why nothing is piped here. `EXIT=$?` after a pipe reads
	# the last command in it, and a refusal that correctly exits 7 then looks
	# like a success.
	out=$("$jr" "$@" 2>&1)
	code=$?
	error=$(printf '%s' "$out" | sed -nE 's#.*<code>([A-Z_]+)</code>.*#\1#p' | head -1)
	if [ "$code" -ne 0 ] && [ -z "$error" ]; then
		error=$(printf '%s' "$out" | head -1 | cut -c1-60)
	fi
	printf '%-34s exit=%-3s %s\n' "$label" "$code" "$error"
	[ "$code" -eq 0 ] || fail=$((fail + 1))
}

# Reads. Every one of these should exit 0 against a healthy instance.
run "user me" user me
run "user list" user list a
run "project list" project list
run "project get" project get "$project"
run "project components" project components "$project"
run "project versions" project versions "$project"
run "project statuses" project statuses "$project"
run "issue list" issue list --project "$project" --limit 5
run "issue get" issue get "$project-1"
run "issue get --with-comments" issue get "$project-1" --with-comments
run "issue comment list" issue comment list "$project-1"
run "issue link list" issue link list "$project-1"
run "issue worklog list" issue worklog list "$project-1"
run "issue attachment list" issue attachment list "$project-4"
run "board list" board list
run "board get" board get 1
run "sprint list" sprint list --board 1
run "epic list" epic list --board 1
run "field list" field list
run "meta transitions" meta transitions "$project-1"
run "meta createmeta" meta createmeta --project "$project" --type Task --refresh
run "jql validate" jql validate --jql "project = $project ORDER BY key DESC"
run "jql explain" jql explain --jql "project = $project"

say
say "$fail command(s) did not exit 0. Several refusals are correct — read them."
