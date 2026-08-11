#!/usr/bin/env bash
#
# Record a Data Center refusing basic authentication.
#
# Jira Data Center 11 disables HTTP Basic by default. Every REST call answers
# `403 {"message":"Basic Authentication has been disabled on this instance."}`
# with no header saying so, and jr reported that as a permission problem — exit
# 6, remedy "check the project permissions for this account" — for a credential
# the instance will never accept.
#
# This needs its own script for two reasons the manifest cannot express: the
# instance has to be an 11.x, and the credential has to be a *basic* one, which
# no seeded profile holds because the seeded profile holds a token that works.
#
# The credential is stored with --no-verify, since verification is the thing
# being refused.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

jr=${JR:-$repo/bin/jr}
out=internal/transport/testdata/basic-refused.datacenter.json

[ -x "$jr" ] || {
	say "no binary at $jr — run 'make build', or set JR"
	exit 2
}

base=$(jira_base)

# The refusal is version-specific, so check for it rather than assuming it.
body=$(curl -sS -u "${ADMIN_USER:-ada}:${ADMIN_PASSWORD:-fixtures-only}" \
	"$base/rest/api/2/serverInfo" || true)
case $body in
*"Basic Authentication has been disabled"*) ;;
*)
	say "this instance accepts basic authentication, so there is no refusal to"
	say "record. Set JIRA_VERSION=11.3.5 in .env and re-create it:"
	say "  make dc-down && make dc-up"
	exit 2
	;;
esac

# A profile of its own, thrown away afterwards. It holds a credential that does
# not work, which is the point, and it must not be the one the other
# recordings use.
profile=$(mktemp -d)
trap 'rm -rf "$profile"' EXIT

export XDG_CONFIG_HOME=$profile/config
export XDG_STATE_HOME=$profile/state
export XDG_CACHE_HOME=$profile/cache

printf '%s' "${ADMIN_PASSWORD:-fixtures-only}" |
	"$jr" auth login --site "$base" --user "${ADMIN_USER:-ada}" \
		--token-stdin --no-verify >/dev/null

say "recording -> $out"
mkdir -p "$(dirname "$repo/$out")"
# Exit 4 is the recording, so a non-zero status here is success.
JIRA_RECORD="$repo/$out" "$jr" user me >/dev/null 2>&1 || true

python3 - "$repo/$out" <<'PY'
import json, sys

path = sys.argv[1]
cassette = json.load(open(path))
statuses = [i["response"]["status"] for i in cassette["interactions"]]
if statuses != [403]:
    sys.exit(f"recorded {statuses}, wanted a single 403 — nothing else should "
             "have been reachable with a credential the instance refuses")
cassette["note"] = (
    "Recorded against Jira Software Data Center 11.3.5, which disables HTTP "
    "Basic by default: the deployment probe is the first request any run makes "
    "and it is refused, so this is the whole conversation. Regenerate with "
    "scripts/dc/record-auth-refusal.sh."
)
json.dump(cassette, open(path, "w"), indent=2)
open(path, "a").write("\n")
print(f"wrote {len(cassette['interactions'])} interaction to {path}")
PY
