#!/usr/bin/env bash
#
# Seed the rig with the fictional identifiers the fixtures already use.
#
# Every name in here is invented, from the first keystroke. That is the cheap
# half of the scrubbing problem: if nothing real ever enters the instance, no
# mapping from a real identifier to a fictional one has to exist, and a mapping
# is what internal/lint/scrubpairs_test.go refuses to let into the repository.
# Nothing to scrub beats scrubbing correctly.
#
# ENG is the key because it is already the fictional project in the Data Center
# cassettes, so a recording lands beside the constructed fixture it replaces
# without a rename.
#
# Idempotent: every step checks for what it would create and says "exists"
# instead. Re-running after a failure half way through is the normal case.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

# shellcheck disable=SC1091
. "$here/common.sh"

base=$(jira_base)
admin_user=${ADMIN_USER:-ada}
admin_password=${ADMIN_PASSWORD:-fixtures-only}

project=${SEED_PROJECT:-ENG}
second_user=${SEED_USER:-grace}
second_fullname=${SEED_USER_FULLNAME:-Grace Hopper}

# How this script authenticates, which is not a given any more.
#
# Jira Data Center 11 refuses HTTP Basic outright — a fresh 11.3.5 answers
# `403 {"message":"Basic Authentication has been disabled on this instance."}`
# to every REST call — and refuses /rest/auth/1/session for two-step
# verification as well. 10.4 still accepts Basic. So the seeding logs in the
# way a browser does when Basic is gone, mints a personal access token through
# the session, and sends that as a bearer token: the same credential jr uses.
auth_args=(-u "$admin_user:$admin_password")
bearer=""
session=$(mktemp)
trap 'rm -f "$session"' EXIT

setup_auth() {
	local code
	if [ -n "${SEED_TOKEN:-}" ]; then
		bearer=$SEED_TOKEN
		auth_args=(-H "Authorization: Bearer $bearer")
		say "seeding over the token in SEED_TOKEN"
		return
	fi
	code=$(curl -sS -o /dev/null -w '%{http_code}' \
		-u "$admin_user:$admin_password" "$base/rest/api/2/myself")
	if [ "$code" = "200" ]; then
		say "seeding over basic auth"
		return
	fi
	say "basic auth answered $code; logging in for a session instead"

	# The login form, not the REST session endpoint: /rest/auth/1/session is
	# refused on 11.x too, for two-step verification.
	curl -sS -c "$session" -b "$session" -o /dev/null "$base/login.jsp"
	local token
	token=$(awk '$6 == "atlassian.xsrf.token" { print $7 }' "$session" | tail -1)
	curl -sS -c "$session" -b "$session" -o /dev/null -X POST "$base/login.jsp" \
		--data-urlencode "os_username=$admin_user" \
		--data-urlencode "os_password=$admin_password" \
		--data-urlencode "os_destination=/secure/" \
		--data-urlencode "atl_token=$token" \
		--data-urlencode "login=Log In"

	bearer=$(curl -sS -c "$session" -b "$session" -X POST \
		"$base/rest/pat/latest/tokens" \
		-H 'Content-Type: application/json' \
		-H 'X-Atlassian-Token: no-check' \
		-d '{"name":"jr fixture seeding","expirationDuration":1}' |
		jq -r 'if type == "object" then .rawToken // empty else empty end' 2>/dev/null || true)
	[ -n "$bearer" ] || {
		say
		say "This instance takes neither basic auth nor a scripted login, so the"
		say "seeding has no way in."
		say
		say "Jira 11 disables basic auth, refuses /rest/auth/1/session for"
		say "two-step verification, and renders its login form in JavaScript, so"
		say "there is no form to post either. Every route a script has is closed."
		say
		say "Two ways forward:"
		say "  - seed on 10.4 instead: JIRA_VERSION=10.4.0 in .env, then"
		say "    make dc-down && make dc-up"
		say "  - or make a token by hand at $base — log in as"
		say "    ${ADMIN_USER:-ada}, avatar → Profile → Personal Access Tokens —"
		say "    and re-run with SEED_TOKEN=<token> ./scripts/dc/seed.sh"
		say
		say "The refusal itself needs none of this:"
		say "  ./scripts/dc/record-auth-refusal.sh"
		exit 1
	}
	auth_args=(-H "Authorization: Bearer $bearer")
	say "seeding over a personal access token"
}

# api METHOD PATH [JSON] — prints the response body, fails loudly on an error
# status. Every seed step goes through it so a failure names the call.
api() {
	local method=$1 path=$2 body=${3:-}
	local out status
	if [ -n "$body" ]; then
		out=$(curl -sS "${auth_args[@]}" -X "$method" "$base$path" \
			-H 'Content-Type: application/json' \
			-d "$body" -w '\n%{http_code}')
	else
		out=$(curl -sS "${auth_args[@]}" -X "$method" "$base$path" -w '\n%{http_code}')
	fi
	status=${out##*$'\n'}
	body=${out%$'\n'*}
	case $status in
	2*)
		printf '%s' "$body"
		;;
	*)
		say "$method $path -> $status"
		say "$body"
		return 1
		;;
	esac
}

exists() {
	curl -sS -o /dev/null -w '%{http_code}' "${auth_args[@]}" "$base$1"
}

say "seeding $base as $admin_user"
setup_auth

# 1. The project. Scrum rather than Kanban: the sprint verbs need a board with
#    sprints on it, and a Kanban board has none.
if [ "$(exists "/rest/api/2/project/$project")" = "200" ]; then
	say "project $project exists"
else
	api POST /rest/api/2/project "$(
		cat <<JSON
{"key":"$project","name":"Engineering","projectTypeKey":"software",
 "projectTemplateKey":"com.pyxis.greenhopper.jira:gh-scrum-template",
 "lead":"$admin_user","assigneeType":"PROJECT_LEAD",
 "description":"Fixture project for jr's Data Center recordings"}
JSON
	)" >/dev/null
	say "project $project created"
fi

# 2. A second user, so `user list` and `user get` return more than yourself.
if [ "$(exists "/rest/api/2/user?username=$second_user")" = "200" ]; then
	say "user $second_user exists"
else
	api POST /rest/api/2/user "$(
		cat <<JSON
{"name":"$second_user","password":"fixtures-only",
 "emailAddress":"$second_user@recorded.invalid","displayName":"$second_fullname"}
JSON
	)" >/dev/null
	say "user $second_user created"
fi

# 3. A component and a version, so `project components` and `project versions`
#    record something other than an empty collection. Both already have an
#    empty-case cassette; what is missing is the populated one.
if api GET "/rest/api/2/project/$project/components" | grep -q '"name":"api"'; then
	say "component api exists"
else
	api POST /rest/api/2/component \
		"{\"name\":\"api\",\"project\":\"$project\",\"description\":\"REST surface\"}" >/dev/null
	say "component api created"
fi

if api GET "/rest/api/2/project/$project/versions" | grep -q '"name":"1.0"'; then
	say "version 1.0 exists"
else
	api POST /rest/api/2/version \
		"{\"name\":\"1.0\",\"project\":\"$project\",\"description\":\"First release\"}" >/dev/null
	say "version 1.0 created"
fi

# 4. Issues. Mixed types, and one of everything a default column reads: an
#    assignee, a label, a component, a fix version.
#
# The Epic needs its name in the Epic Name custom field, which is a different
# field id on every instance, so it is looked up rather than assumed.
epic_field=$(api GET /rest/api/2/field |
	jq -r '.[] | select(.name == "Epic Name") | .id' | head -1)
say "epic name field: ${epic_field:-<none>}"

issue_count=$(api GET "/rest/api/2/search?jql=project%20%3D%20$project&maxResults=0" | jq -r .total)
if [ "$issue_count" -ge 5 ]; then
	say "$issue_count issues exist"
else
	create_issue() {
		local type=$1 summary=$2 extra=$3
		api POST /rest/api/2/issue "$(
			cat <<JSON
{"fields":{"project":{"key":"$project"},"issuetype":{"name":"$type"},
 "summary":"$summary"$extra}}
JSON
		)" | jq -r .key
	}

	epic_extra=""
	[ -n "$epic_field" ] && epic_extra=",\"$epic_field\":\"Recording the API\""
	epic=$(create_issue Epic "Record every Data Center conversation" "$epic_extra")
	say "epic $epic"

	create_issue Story "Page a search by keyset" \
		",\"assignee\":{\"name\":\"$admin_user\"},\"labels\":[\"pagination\"]" >/dev/null
	create_issue Task "Quote a JQL value exactly once" \
		",\"assignee\":{\"name\":\"$second_user\"},\"components\":[{\"name\":\"api\"}]" >/dev/null
	create_issue Bug "A truncated result reports itself complete" \
		",\"fixVersions\":[{\"name\":\"1.0\"}],\"labels\":[\"contract\",\"truncation\"]" >/dev/null
	create_issue Task "Refuse an unrecognised deployment type" "" >/dev/null
	say "issues created"
fi

# 4b. An attachment, because it is the one thing that makes the server hand
#     back a URL this tool then has to follow. `issue attachment download`
#     resolves the `content` link through transport.Relative, which is the only
#     place a server-supplied URL becomes a request — and under a context path
#     it is the code that has to not repeat the prefix or wander off the site.
if api GET "/rest/api/2/issue/${SEED_ISSUE:-ENG-4}?fields=attachment" |
	grep -q '"filename"'; then
	say "attachment exists"
else
	tmp=$(mktemp -d)
	printf 'summary,status\nENG-1,To Do\n' >"$tmp/rows.csv"
	curl -sS "${auth_args[@]}" -X POST \
		-H 'X-Atlassian-Token: no-check' \
		-F "file=@$tmp/rows.csv" \
		"$base/rest/api/2/issue/${SEED_ISSUE:-ENG-4}/attachments" >/dev/null
	rm -rf "$tmp"
	say "attachment added to ${SEED_ISSUE:-ENG-4}"
fi

# 5. The board the scrum template made, and a sprint on it.
board=$(api GET "/rest/agile/1.0/board?projectKeyOrId=$project" | jq -r '.values[0].id // empty')
[ -n "$board" ] || {
	say "no board for $project — the scrum template did not create one"
	exit 1
}
say "board $board"

sprints=$(api GET "/rest/agile/1.0/board/$board/sprint" | jq -r '.values | length')
if [ "$sprints" -ge 1 ]; then
	say "$sprints sprint(s) exist"
else
	api POST /rest/agile/1.0/sprint \
		"{\"name\":\"$project Sprint 1\",\"originBoardId\":$board}" >/dev/null
	say "sprint created"
fi

# 6. A personal access token, which is what jr sends to Data Center. Basic auth
#    is what this script uses and is not what a fixture should record: Jira
#    11.x refuses it outright, so a rig that only ever proved Basic would be
#    recording a conversation the current line cannot have.
token_file=$here/profile/token
mkdir -p "$(dirname "$token_file")"
if [ -s "$token_file" ]; then
	say "token exists: $token_file"
elif [ -n "$bearer" ]; then
	# Already minted one to do the seeding with, on an instance that refuses
	# Basic. Making a second would leave the first behind with nothing holding
	# it.
	printf '%s' "$bearer" >"$token_file"
	say "token written: $token_file"
else
	api POST /rest/pat/latest/tokens \
		'{"name":"jr fixture recording","expirationDuration":1}' |
		jq -r .rawToken >"$token_file"
	[ -s "$token_file" ] || {
		say "no token came back"
		exit 1
	}
	say "token written: $token_file"
fi

say
say "seeded. board $board, project $project, users $admin_user and $second_user"
