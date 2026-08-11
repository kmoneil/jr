# Shared settings for the Data Center recording rig. Sourced, never run.
#
# shellcheck shell=bash

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)

# shellcheck disable=SC1091
[ -f "$here/.env" ] && . "$here/.env"

jira_container=${JIRA_CONTAINER:-jr-fixtures-jira-1}

say() { printf '%s\n' "$*" >&2; }

# jira_base prints a URL that actually reaches this rig's Jira, or fails.
#
# Two answers are possible and both are ordinary. On a machine running Docker
# directly, the published port on loopback is it — which is what the compose
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
