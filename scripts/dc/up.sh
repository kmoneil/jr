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

# The images, resolved by compose from .env and the file's own defaults, and
# printed because nothing else does. .env is gitignored and outlives the
# session that wrote it, so a JIRA_VERSION left in it from a 9.12 pass is a
# 9.12 instance that every message below describes as if it were the default,
# and one card had its Data Center half measured that way before serverInfo
# was asked.
say "starting containers: $("${compose[@]}" config --images | tr '\n' ' ')"
"${compose[@]}" up -d

"$here/setup.sh"
"$here/seed.sh"

base=$(jira_base)
require_jr || {
	say
	say "the instance is up. Build a binary this machine can run, then"
	say "'make dc-up' again to log the throwaway profile in; nothing else"
	say "is repeated."
	exit 1
}

use_profile

# The context is recreated, not reused. `auth login` creates the first
# context when there are none and leaves an existing one alone, which is
# right for a person's profile and wrong for this one: the only thing the
# context holds is the container's address, and the address is whichever
# subnet Docker had free when `up -d` created the network. After a `dc-down
# && dc-up` the old one serves nothing, or serves an unrelated stack's
# Postgres. Deleting the config makes this login the one that decides, every
# time. The credential store and the cache are keyed by site and an entry for
# a site nobody asks about again is inert, so they stay.
#
# A production context in the default profile is one command away from being
# recorded into a fixture, which is why record.sh insists on this profile
# rather than on a flag.
rm -f "$profile/config/jr/config.toml"
say "logging the throwaway profile in at $base"
"$jr" auth login --site "$base" --token-file "$profile/token" >/dev/null

# The guard every recording script runs, run here as the post-condition:
# dc-up is finished when the profile it leaves would pass it.
require_rig >/dev/null

say
say "ready. $base, administrator ${ADMIN_USER:-ada}"
say "the licence expires three hours from setup; 'make dc-down && make dc-up'"
say "buys another three, because the expiry runs from install and not a date."
say
say "record with: make dc-record"
