#!/usr/bin/env bash
#
# Record the Data Center write verbs, in order.
#
# These are one script rather than manifest rows because they are a sequence
# and not a set. Each step needs the instance in the state the step before it
# left: a comment cannot be deleted before it is added, and its id is not
# knowable until the add answers. Recording them individually would mean
# putting the instance into a specific state by hand each time, which is the
# work this file exists to remove.
#
# Every mutation lands on issues this script creates, so a re-run against a
# freshly seeded instance produces the same conversation with different ids —
# and the ids are what the cassettes carry, so a re-run is a re-record rather
# than a no-op.
set -euo pipefail

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
[ -z "${CONTEXT_PATH:-}" ] || {
	say "refusing: this instance is served under ${CONTEXT_PATH}, and these"
	say "cassettes are the root ones. Clear CONTEXT_PATH and re-create it."
	exit 2
}

dir=internal/resource/issue/testdata

# record <cassette> <jr args...> — writes the cassette and prints jr's stdout,
# so a step can read the key or id it just created.
record() {
	local out=$dir/$1
	shift
	say "recording -> $out"
	JIRA_RECORD="$repo/$out" "$jr" "$@"
}

# field reads one value out of a record document.
#
# TSV for a single record is a field/value table rather than a row of columns —
# `@key<TAB>ENG-6` — so a column index reads the field *name* out of the second
# row and hands back "@key". That is how the first run of this script sent
# Jira the literal string "@key" as an issue key.
field() { sed -n "s/^$1\t//p" | head -1; }

say "creating the issue every other step writes to"
subject=$(record create-recorded.datacenter.json issue create \
	--project "$project" --type Task \
	--summary "Recorded write path" \
	--description "Created by scripts/dc/record-writes.sh" \
	--format tsv | field @key)
[ -n "$subject" ] || {
	say "issue create printed no key"
	exit 1
}
say "  $subject"

record edit-recorded.datacenter.json issue edit "$subject" \
	--summary "Recorded write path, edited" >/dev/null

record assign-recorded.datacenter.json issue assign "$subject" \
	"${SEED_USER:-grace}" >/dev/null

record watch-recorded.datacenter.json issue watch "$subject" >/dev/null
record unwatch-recorded.datacenter.json issue watch "$subject" --remove >/dev/null

comment=$(record comment-add-recorded.datacenter.json issue comment add \
	"$subject" "Recorded by the fixture rig" --format tsv | field @id)
[ -n "$comment" ] || {
	say "comment add printed no id"
	exit 1
}
record comment-delete-recorded.datacenter.json issue comment delete \
	"$subject" "$comment" --yes >/dev/null

# --started, rather than letting it default to now. The body carries the
# timestamp, the replayer matches on the body, and a body stamped with the
# clock can never be replayed: the recording is of a request nobody can build
# twice. Any fixed instant does, and this one is the project's own fictional
# date.
worklog=$(record worklog-add-recorded.datacenter.json issue worklog add \
	"$subject" 1h --started 2026-01-02T09:00:00Z --format tsv | field @id)
[ -n "$worklog" ] || {
	say "worklog add printed no id"
	exit 1
}
record worklog-delete-recorded.datacenter.json issue worklog delete \
	"$subject" "$worklog" --yes >/dev/null

# "relates" alone is refused: it names a link type and reads the same in both
# directions, so the direction has to be spelled. That refusal is this tool's,
# not Jira's, and it is worth meeting here rather than in a hand-written test.
record link-add-recorded.datacenter.json issue link add \
	"$subject" "relates to" "$project-2" >/dev/null

# The transition name is the site's, not this tool's, so it is read rather than
# assumed: a workflow renames these freely and "In Progress" is only the
# default scheme's spelling.
#
# The first offered transition is not automatically the right one to record.
# Jira's default workflow offers "To Do" to an issue already in To Do, and a
# move that changes nothing is a weak recording of a verb whose whole purpose
# is changing something — so the one picked is the first whose destination
# differs from where the issue is now.
status=$("$jr" issue get "$subject" --format tsv | field @status)
transition=$("$jr" meta transitions "$subject" --format tsv |
	awk -F'\t' -v now="$status" 'NR > 1 && $3 != now { print $2; exit }')
[ -n "$transition" ] || {
	say "no transition offered for $subject"
	exit 1
}
say "transition: $transition"
record move-recorded.datacenter.json issue move "$subject" "$transition" >/dev/null

# Cloning last but one, and deleting the clone rather than the subject, so a
# failure part way through leaves the recorded issue intact for inspection.
clone=$(record clone-recorded.datacenter.json issue clone "$subject" \
	--summary "Recorded write path, cloned" --format tsv | field @key)
[ -n "$clone" ] || {
	say "clone printed no key"
	exit 1
}
record delete-recorded.datacenter.json issue delete "$clone" --yes >/dev/null

say
say "recorded the write sequence against $subject"
say "read the residue lines above before committing anything"
