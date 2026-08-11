#!/usr/bin/env bash
#
# Bring up a licensed, seeded Jira Data Center and point a throwaway jr profile
# at it. Idempotent: every step checks before it acts.
#
# Cold, this takes about eight minutes, nearly all of it Jira's first boot.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

compose=(docker compose -f "$here/docker-compose.yml")

if [ ! -f "$here/.env" ]; then
	cp "$here/.env.example" "$here/.env"
	say "wrote $here/.env from the example"
fi

say "starting containers"
"${compose[@]}" up -d

"$here/setup.sh"
"$here/seed.sh"

base=$(jira_base)
jr=${JR:-$repo/bin/jr}
[ -x "$jr" ] || {
	say
	say "no binary at $jr. Run 'make build', then 'make dc-up' again to log in."
	exit 0
}

export XDG_CONFIG_HOME=$here/profile/config
export XDG_STATE_HOME=$here/profile/state
export XDG_CACHE_HOME=$here/profile/cache

# The throwaway profile, and the reason record.sh insists on it: a production
# context in the default profile is one command away from being recorded into a
# fixture. `auth login` creates the first context when there are none, so this
# one command configures the whole profile.
say "logging the throwaway profile in at $base"
"$jr" auth login --site "$base" --token-file "$here/profile/token" >/dev/null

say
say "ready. $base, administrator ${ADMIN_USER:-ada}"
say "the licence expires three hours from setup; 'make dc-down && make dc-up'"
say "buys another three, because the expiry runs from install and not a date."
say
say "record with: make dc-record"
