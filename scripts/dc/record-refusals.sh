#!/usr/bin/env bash
#
# Record the refusals a real Data Center can be made to produce.
#
# The failure fixtures in this tree were the last hand-written Data Center
# cassettes, and most of them have to stay that way: a healthy server does not
# answer 503 on request, and a cassette holding an interaction that must never
# be played cannot be a recording of anything. Two can be recorded, and are:
#
#   create-twice   two identical creates, both accepted. The point of the test
#                  is that the second really is sent, so it is two invocations
#                  concatenated: every exchange real, only the assembly ours.
#   delete-parent  deleting an issue that has subtasks, refused by Jira in
#                  prose that this tool turns into a remedy naming --subtasks.
#
# Each mutation lands on issues this script creates.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

project=${SEED_PROJECT:-ENG}
dir=internal/resource/issue/testdata

require_rig >/dev/null || exit 2
[ -z "${CONTEXT_PATH:-}" ] || {
	say "refusing: these are the root cassettes and this instance is served"
	say "under ${CONTEXT_PATH}."
	exit 2
}

field() { sed -n "s/^$1\t//p" | head -1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# 1. Two identical creates.
#
# The summary is the one the existing test uses, so the recorded conversation
# is comparable with the constructed one it stands beside.
say "two identical creates"
JIRA_RECORD="$work/1.json" "$jr" issue create \
	--project "$project" --type Bug --summary "a summary" >/dev/null
JIRA_RECORD="$work/2.json" "$jr" issue create \
	--project "$project" --type Bug --summary "a summary" >/dev/null 2>"$work/warn"

python3 - "$work" "$repo/$dir/create-twice-recorded.datacenter.json" <<'PY'
import json, sys

work, target = sys.argv[1], sys.argv[2]
parts = [json.load(open(f"{work}/{n}.json")) for n in (1, 2)]
interactions = [i for part in parts for i in part["interactions"]]
paths = [(i["request"].get("method", "GET"), i["request"]["path"]) for i in interactions]
if paths != [("POST", "/rest/api/2/issue")] * 2:
    sys.exit(f"recorded {paths}, wanted two creates and nothing else")
if len({json.dumps(i["request"].get("body"), sort_keys=True) for i in interactions}) != 1:
    sys.exit("the two creates did not send the same body, so they are not a duplicate")

json.dump(
    {
        "deployment": "datacenter",
        "source": "recorded",
        "note": (
            "Two identical creates against Jira Software Data Center 10.4.0, "
            "each a real invocation, concatenated because JIRA_RECORD writes one "
            "cassette per run and the point of the test is that the second "
            "request is really sent. Regenerate with scripts/dc/record-refusals.sh."
        ),
        "interactions": interactions,
    },
    open(target, "w"),
    indent=2,
)
print(f"wrote {len(interactions)} interactions to {target}")
PY

# 2. An issue Jira will not delete, because it has a subtask.
say "a parent with a subtask"
parent=$("$jr" issue create --project "$project" --type Task \
	--summary "Parent of a subtask" --format tsv | field @key)
[ -n "$parent" ] || {
	say "no parent key"
	exit 1
}
"$jr" issue create --project "$project" --type Sub-task --parent "$parent" \
	--summary "The subtask that blocks the delete" --format tsv >/dev/null
say "  $parent"

# Exit 2 is the recording: Jira refuses, and this tool turns its prose into a
# remedy naming the flag that would have worked.
say "recording -> $dir/delete-parent-recorded.datacenter.json"
JIRA_RECORD="$repo/$dir/delete-parent-recorded.datacenter.json" \
	"$jr" issue delete "$parent" --yes >/dev/null 2>&1 || true

python3 - "$repo/$dir/delete-parent-recorded.datacenter.json" <<'PY'
import json, sys

path = sys.argv[1]
cassette = json.load(open(path))
statuses = [i["response"]["status"] for i in cassette["interactions"]]
if statuses != [400]:
    sys.exit(f"recorded {statuses}, wanted a single 400 refusing the delete")
cassette["note"] = (
    "Deleting an issue that has a subtask, refused by Jira Software Data Center "
    "10.4.0 in prose. The cassette beside it, delete-subtasks.datacenter.json, "
    "carries a second interaction this cannot: a delete that is refused even "
    "with deleteSubtasks=true, which a healthy server does not do. Regenerate "
    "with scripts/dc/record-refusals.sh."
)
json.dump(cassette, open(path, "w"), indent=2)
open(path, "a").write("\n")
print(f"wrote {len(cassette['interactions'])} interaction to {path}")
PY

say
say "read the residue lines above before committing anything"
