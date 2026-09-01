#!/usr/bin/env bash
#
# Record internal/transport/testdata/serverinfo.datacenter.json.
#
# This one cassette needs its own script because the contract test plays four
# exchanges (the deployment probe, the account, a 404 for a missing issue, and
# a POST that must fail on the summary) and no single jr invocation produces
# all four. A cassette carrying anything else fails `Unplayed()`, and one
# missing an exchange fails with FIXTURE_MISS.
#
# So three invocations are recorded and their interactions concatenated in the
# order the test asks for them. Every exchange is real; only the assembly is
# ours, which is what keeps `source: recorded` honest.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

out=internal/transport/testdata/serverinfo.datacenter.json

require_rig >/dev/null || exit 2

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# --refresh is what puts the probe in the recording: without it the deployment
# is answered from the cache and the first interaction never happens.
say "probe and account"
JIRA_RECORD="$work/1.json" "$jr" user me --refresh >/dev/null

# Exit 5 is the point of this one, so a non-zero status is success here.
say "a missing issue"
JIRA_RECORD="$work/2.json" "$jr" issue get ENG-9999 >/dev/null 2>&1 || true

# 300 characters against Jira's 255-character limit: the shortest real request
# that produces a field error naming `summary`.
say "a field error"
summary=$(printf 'x%.0s' $(seq 1 300))
JIRA_RECORD="$work/3.json" "$jr" issue create \
	--project "${SEED_PROJECT:-ENG}" --type Task --summary "$summary" >/dev/null 2>&1 || true

python3 - "$work" "$repo/$out" <<'PY'
import json, sys

work, target = sys.argv[1], sys.argv[2]
parts = [json.load(open(f"{work}/{n}.json")) for n in (1, 2, 3)]
interactions = [i for part in parts for i in part["interactions"]]

want = [
    ("GET", "/rest/api/2/serverInfo"),
    ("GET", "/rest/api/2/myself"),
    ("GET", "/rest/api/2/issue/ENG-9999"),
    ("POST", "/rest/api/2/issue"),
]
got = [(i["request"].get("method", "GET"), i["request"]["path"]) for i in interactions]
if got != want:
    sys.exit(f"recorded {got}\nwanted   {want}\nthe cassette would fail the contract test")

json.dump(
    {
        "deployment": "datacenter",
        "source": "recorded",
        "note": (
            "Recorded against a local Jira Software Data Center, licensed with the "
            "timebomb key Atlassian publishes for running a Data Center product "
            "without the SDK, and seeded with invented identifiers. Four real "
            "exchanges, assembled in the order this test plays them because no "
            "single jr invocation produces all four. Regenerate with "
            "scripts/dc/record-transport.sh."
        ),
        "interactions": interactions,
    },
    open(target, "w"),
    indent=2,
)
open(target, "a").write("\n")
print(f"wrote {len(interactions)} interactions to {target}")
PY

say
say "read the residue lines above. serverInfo carries Jira's own build sha in"
say "scmInfo, which the identifier check flags and which is Atlassian's, not yours."
