#!/usr/bin/env bash
#
# Drive the Jira setup wizard, including the licence.
#
# The wizard is the one step that used to need a human, and the reason this
# rig sat unrun. It is scriptable: the UI is JS-rendered with no server-side
# form inputs, but the .jspa actions behind it take ordinary form posts.
#
# The XSRF token rotates on every request, so each POST is preceded by a GET of
# the page it posts to and the token is read out of the cookie jar. Posting a
# stale one is a 403 with no explanation, which is the failure this comment
# exists to save you from re-deriving.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
cd "$here"

# shellcheck disable=SC1091
. "$here/common.sh"

jar=$(mktemp)
page=$(mktemp)
trap 'rm -f "$jar" "$page"' EXIT

admin_user=${ADMIN_USER:-ada}
admin_password=${ADMIN_PASSWORD:-fixtures-only}
admin_fullname=${ADMIN_FULLNAME:-Ada Lovelace}
admin_email=${ADMIN_EMAIL:-ada@recorded.invalid}

licence() {
	if [ -s licence.txt ]; then
		say "licence: scripts/dc/licence.txt"
		tr -d '[:space:]' <licence.txt
		return
	fi
	say "licence: fetching the published Data Center timebomb key"
	python3 ./licence.py
}

# token prints the current XSRF token from the cookie jar.
token() {
	awk '$6 == "atlassian.xsrf.token" { print $7 }' "$jar" | tail -1
}

# stepname reduces a wizard URL to the action behind it.
stepname() {
	printf '%s\n' "$1" | sed -E 's#.*/([A-Za-z]+)(!default)?\.jspa.*#\1#'
}

# first_step asks the instance which step it is on.
#
# Read out of the form the page renders, never out of the URL. During setup
# every wizard URL serves the step the instance is actually on — asking for
# SetupAdminAccount after the account exists renders the mail step — while `/`
# keeps redirecting to the application-properties URL long after that step is
# saved. Trusting the URL replays finished steps, and the admin-account step is
# not idempotent: the second attempt fails because the user already exists.
first_step() {
	# --max-time is generous because the wizard's first render is slow on a
	# cold instance: a 20-second ceiling returned an empty body here, and an
	# empty body has no setup form in it, which read as "the wizard finished".
	curl -sS -L --max-time 120 -c "$jar" -b "$jar" -o "$page" "$base/" || true
	rendered_step
}

# rendered_step names the wizard step whose form is on the page just fetched.
#
# "Done" when the page carries no setup form at all, which is what finishing
# looks like: the last step answers with the dashboard, whose first form is a
# search box.
rendered_step() {
	local action
	action=$(sed -nE 's#.*<form[^>]*action="(Setup[A-Za-z]*)\.jspa".*#\1#p' "$page" | head -1)
	if [ -n "$action" ]; then
		printf '%s\n' "$action"
		return
	fi
	# No setup form has two meanings and they are opposite: the wizard is
	# finished, or the page never arrived. Distinguish them by the size of what
	# came back rather than by its absence, because a timeout and a redirect to
	# the dashboard both leave no form behind.
	if [ ! -s "$page" ] || [ "$(wc -c <"$page")" -lt 2048 ]; then
		printf 'Unready\n'
		return
	fi
	printf 'Done\n'
}

# post sends one wizard step and prints the step Jira redirected to.
#
# The GET first is not redundant: it is what mints the token the POST carries.
post() {
	local action=$1
	shift
	curl -sS -c "$jar" -b "$jar" -o /dev/null "$base/secure/$action!default.jspa"
	local out
	# `next` is the submit button and `nextStep` is a hidden field the wizard's
	# JavaScript fills in. Without both, the step answers 200 and re-renders
	# itself with the submitted values echoed back into the inputs — no error
	# message, no redirect, nothing that distinguishes it from success except
	# that you are still on the same step. `nextStep` is the one that matters.
	out=$(curl -sS -c "$jar" -b "$jar" -o "$page" -w '%{http_code} %{redirect_url}' \
		-X POST "$base/secure/$action.jspa" \
		--data-urlencode "atl_token=$(token)" \
		--data-urlencode "next=Next" \
		--data-urlencode "finish=Finish" \
		--data-urlencode "nextStep=true" "$@")
	say "  $action -> $out"

	local code=${out%% *} landed=${out#* }
	case $code in
	403)
		say "403 from $action: the token rotated between the GET and the POST,"
		say "or this Jira version renamed the action."
		exit 1
		;;
	esac

	# Two ways forward and both are ordinary. Some steps redirect to the next
	# one; some answer 200 with the next step's page in the body. The step is
	# whichever form came back, so read the form rather than the status —
	# treating 200 as failure calls a completed admin-account step a rejection.
	local next
	if [ -n "$landed" ]; then
		next=$(stepname "$landed")
	else
		next=$(rendered_step)
	fi

	if [ "$next" = "$action" ]; then
		say "$action was rejected:"
		sed -nE 's#.*class="error"[^>]*>([^<]+)<.*#  \1#p' "$page" | head -3 >&2
	fi
	if [ -z "$next" ] || [ "$next" = "$action" ]; then
		say "$action did not advance: the wizard answered with its own form again,"
		say "which is how it reports a rejected value. Nothing was saved."
		exit 1
	fi
	printf '%s\n' "$next"
}

say "waiting for Jira to answer"
base=""
for _ in $(seq 1 120); do
	if base=$(jira_base 2>/dev/null); then
		break
	fi
	sleep 10
done
[ -n "$base" ] || {
	jira_base
	exit 1
}
say "Jira is answering at $base, state $(jira_state "$base")"

current=$(first_step)
for _ in $(seq 1 40); do
	say "step: $current"
	case $current in
	SetupDatabase)
		# Only reached if the compose environment did not configure the
		# database. It normally does, through ATL_JDBC_*.
		current=$(post SetupDatabase \
			--data-urlencode "databaseOption=external" \
			--data-urlencode "databaseType=postgres72" \
			--data-urlencode "jdbcHostname=db" \
			--data-urlencode "jdbcPort=5432" \
			--data-urlencode "jdbcDatabase=${DB_NAME:-jira}" \
			--data-urlencode "jdbcUsername=${DB_USER:-jira}" \
			--data-urlencode "jdbcPassword=${DB_PASSWORD:-jira}" \
			--data-urlencode "schemaName=public")
		;;
	SetupApplicationProperties | SetupMode)
		current=$(post SetupApplicationProperties \
			--data-urlencode "title=jr fixtures" \
			--data-urlencode "mode=private" \
			--data-urlencode "baseURL=$base")
		;;
	SetupLicense)
		key=$(licence)
		[ -n "$key" ] || {
			say "no licence key"
			exit 1
		}
		say "  licence: ${#key} characters"
		current=$(post SetupLicense --data-urlencode "setupLicenseKey=$key")
		;;
	SetupAdminAccount)
		current=$(post SetupAdminAccount \
			--data-urlencode "username=$admin_user" \
			--data-urlencode "fullname=$admin_fullname" \
			--data-urlencode "email=$admin_email" \
			--data-urlencode "password=$admin_password" \
			--data-urlencode "confirm=$admin_password")
		;;
	SetupMailNotifications)
		current=$(post SetupMailNotifications --data-urlencode "noemail=true")
		;;
	Unready)
		# The instance answered /status but has not rendered the wizard yet.
		# Waiting is the whole remedy; concluding anything here is how a
		# half-set-up instance reports itself finished and fails at the first
		# REST call with a 503.
		say "  no wizard page yet, state $(jira_state "$base"); waiting"
		sleep 15
		current=$(first_step)
		;;
	Done | SetupComplete | Dashboard | MyJiraHome | *)
		if [ "$(jira_state "$base")" != "RUNNING" ]; then
			say "  no setup form, but the instance is still $(jira_state "$base")"
			sleep 15
			current=$(first_step)
			continue
		fi
		say "setup complete: $base"
		say "  administrator: $admin_user / $admin_password"
		exit 0
		;;
	esac
done

say "the wizard did not finish; last step was $current"
exit 1
