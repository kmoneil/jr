# Shared settings for the Data Center recording rig. Sourced, never run.
#
# shellcheck shell=bash

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)

# shellcheck disable=SC1091
[ -f "$here/.env" ] && . "$here/.env"

jira_container=${JIRA_CONTAINER:-jr-fixtures-jira-1}

# The throwaway profile, and the binary the recordings are made with. Every
# script here uses both, and up.sh is the one that creates the first.
profile=$here/profile
jr=${JR:-$repo/bin/jr}

say() { printf '%s\n' "$*" >&2; }

# jira_base prints a URL that actually reaches this rig's Jira, or fails.
#
# Two answers are possible and both are ordinary. On a machine running Docker
# directly, the published port on loopback is it, which is what the compose
# file binds, deliberately, so the instance is not on the network. Inside a
# container that only shares the daemon, that publish lands on the *host's*
# loopback and not on this one, and the way through is the container's own
# address on the bridge.
#
# Probed rather than configured, because guessing wrong produces a connection
# refused at the first POST of a wizard that has to be restarted from scratch.
jira_base() {
	local candidate
	for candidate in \
		"http://127.0.0.1:${JIRA_PORT:-9493}${CONTEXT_PATH:-}" \
		"http://$(container_ip):8080${CONTEXT_PATH:-}"; do
		case $candidate in
		*"://:"*) continue ;;
		esac
		if curl -sS --max-time 5 "$candidate/status" 2>/dev/null | grep -q '"state"'; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done
	say "nothing answered /status."
	say "  tried http://127.0.0.1:${JIRA_PORT:-9493}${CONTEXT_PATH:-}"
	say "  tried the $jira_container bridge address"
	say "Is it up? docker compose -f $here/docker-compose.yml logs -f jira"
	return 1
}

container_ip() {
	docker inspect -f \
		'{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' \
		"$jira_container" 2>/dev/null
}

# jira_state prints FIRST_RUN, RUNNING, or nothing at all.
jira_state() {
	local base=$1
	curl -sS --max-time 5 "$base/status" 2>/dev/null |
		sed -E 's/.*"state" *: *"([A-Z_]+)".*/\1/'
}

# jira_version prints the version the instance reports, or nothing.
#
# Over the profile's token, because the instance is set up in private mode and
# answers serverInfo anonymously with an empty error, and because an 11.x
# refuses basic authentication outright. The token is what jr sends, so this
# asks the same way jr does.
jira_version() {
	local base=$1
	[ -s "$profile/token" ] || return 0
	curl -sS --max-time 5 -H "Authorization: Bearer $(cat "$profile/token")" \
		"$base/rest/api/2/serverInfo" 2>/dev/null |
		sed -nE 's/.*"version" *: *"([^"]+)".*/\1/p'
}

# use_profile points jr at the throwaway profile for the rest of the script.
#
# The paths are absolute because $here is. A relative XDG_CONFIG_HOME is
# refused by Go's os.UserConfigDir, and jr then reads its default profile
# instead: `XDG_CONFIG_HOME=scripts/dc/profile/config jr context list` reports
# no contexts at all, against a config.toml that plainly holds one.
use_profile() {
	export XDG_CONFIG_HOME=$profile/config
	export XDG_STATE_HOME=$profile/state
	export XDG_CACHE_HOME=$profile/cache
}

# require_jr checks that the binary exists and runs on this machine.
#
# -x is not that check. bin/jr is whatever `make build` last wrote, and a build
# made on the host is a Mach-O binary that a Linux dev container sharing the
# daemon cannot exec. It is executable by mode, so the old check passed, and
# the run reached the login at the very end of an eight-minute dc-up before
# answering `cannot execute binary file: Exec format error`. So ask it to run,
# with the one command that needs no site and no credential.
require_jr() {
	[ -x "$jr" ] || {
		say "no binary at $jr. Run 'make build', or set JR."
		return 2
	}
	local reply
	if ! reply=$("$jr" version --format tsv 2>&1); then
		say "the binary at $jr does not run on this machine:"
		say "  $(printf '%s\n' "$reply" | head -1)"
		say "Build one for it, or set JR to one that runs here."
		return 2
	fi
}

# require_rig is the guard every script that talks to the rig runs first. It
# prints the address it verified, for the caller to use.
#
# Four things, in the order they fail: the throwaway profile exists, the binary
# runs, the instance answers, and the profile's current context names the
# address the instance answers at. The last is the one this rig went without
# for a fortnight. `make dc-up` recreates the compose network, Docker hands it
# the first free subnet, so the container's address depends on what else is
# running that day; `auth login` leaves an existing context alone by design;
# and the first command against the stale address is a TIMEOUT that reads as
# a network failure, after `auth status` has said authenticated, which it
# does without contacting Jira because a credential for that site is stored.
# Every one of those answers is correct. None of them says the address is
# stale. This one does, with both values side by side, and it refuses rather
# than repointing because a recording script has no business editing the
# profile: that is up.sh's job, and up.sh runs this last as its own
# post-condition.
require_rig() {
	use_profile
	[ -f "$profile/config/jr/config.toml" ] || {
		say "no throwaway profile at $profile."
		say "Run: make dc-up   (which sets one up against the container)"
		return 2
	}
	require_jr || return 2

	local base
	base=$(jira_base) || return 2

	# The current context, read through jr rather than out of the TOML,
	# because jr owns the file and the record document names the site it
	# would actually send to.
	local stored
	stored=$("$jr" context show --format tsv 2>/dev/null |
		awk -F'\t' '$1 == "@site" { print $2 }')
	[ -n "$stored" ] || {
		say "the throwaway profile at $profile has no current context."
		say "Run: make dc-up"
		return 2
	}

	if [ "${stored%/}" != "${base%/}" ]; then
		say "refusing: the throwaway profile names a Jira this rig is not serving."
		say "  profile context  $stored"
		say "  rig answers at   $base"
		say "The container came back on a different address, or CONTEXT_PATH"
		say "changed. 'make dc-up' logs the profile in again at the address it"
		say "finds; nothing else is repeated."
		return 2
	fi

	local version
	version=$(jira_version "$base")
	say "rig: Jira ${version:-(version unknown)} at $base"
	printf '%s\n' "$base"
}
