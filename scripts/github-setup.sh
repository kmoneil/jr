#!/usr/bin/env bash
#
# Apply this project's GitHub settings: branch protection, the checks a pull
# request has to pass, and the security features that are off by default.
#
# Idempotent, and it prints what it is about to do. Run it after the repository
# exists and `gh auth status` is happy:
#
#   ./scripts/github-setup.sh
#   ./scripts/github-setup.sh --dry-run
#
# It never creates the repository and never pushes. Those are deliberate acts
# and belong to a human at a terminal.
set -euo pipefail

REPO=${REPO:-kmoneil/jr}
DRY=${DRY:-}
[ "${1:-}" = "--dry-run" ] && DRY=1

say() { printf '%s\n' "$*" >&2; }

run() {
	if [ -n "$DRY" ]; then
		say "  would run: $*"
		return 0
	fi
	"$@"
}

command -v gh >/dev/null || {
	say "gh is not installed: https://cli.github.com"
	exit 2
}
gh auth status >/dev/null 2>&1 || {
	say "gh is not authenticated. Run: gh auth login"
	exit 2
}
gh repo view "$REPO" >/dev/null 2>&1 || {
	say "$REPO does not exist or is not visible to this account."
	say "Create it first — that is a deliberate act and this script will not do it:"
	say "  gh repo create $REPO --public --source . --remote origin --push"
	exit 2
}

# The status checks a pull request has to pass. These are job *names* from
# .github/workflows/ci.yml, and a name that does not match a job silently
# protects nothing — GitHub waits for a check that will never report. Keep this
# list and that file in step.
# The contexts a pull request has to report before it can merge. Every name
# here is a job name in .github/workflows/ci.yml, and
# internal/lint/requiredchecks_test.go holds the two lists together: a required
# check no job reports sits pending forever and makes every pull request
# unmergeable, including the one that would fix it.
#
# "fuzz" is the aggregate job rather than the sweep itself. The sweep is a
# matrix over the packages holding fuzz targets, so its contexts are named
# "fuzz (internal/jql)" and the set of them changes whenever a package gains
# its first target. The aggregate has a fixed name and fails unless every leg
# passed.
CHECKS=(
	"format, vet, lint"
	"vulnerability scan"
	"output contract is unchanged"
	"fuzz"
	"build profiles and size budget"
	"test (tags=none)"
	"test (tags=mcp)"
	"test (tags=mcp,write)"
	"test (tags=tui,prompt,render,browser,clipboard,mcp,write,admin)"
)

say "== ruleset on main =="
# A ruleset rather than the older branch-protection API: it is what GitHub
# develops now, and it can be read back as one object.
checks_json=$(printf '%s\n' "${CHECKS[@]}" | jq -R . | jq -s 'map({context: .})')
ruleset=$(
	jq -n --argjson checks "$checks_json" '{
    name: "main",
    target: "branch",
    enforcement: "active",
    conditions: {ref_name: {include: ["~DEFAULT_BRANCH"], exclude: []}},
    rules: [
      # No direct pushes: everything arrives by pull request.
      #
      # Zero approvals required, decided 2026-08-12 and not an oversight:
      # GitHub does not let an author approve their own pull request, so on a
      # single-maintainer repository a requirement of 1 locks the only
      # maintainer out of their own main branch. The pull request itself is
      # what is being required here, because it is what makes the checks run
      # and the change readable before it lands. Raise this to 1 on the day
      # there is a second maintainer, not before.
      {type: "pull_request", parameters: {
        required_approving_review_count: 0,
        dismiss_stale_reviews_on_push: true,
        require_code_owner_review: false,
        require_last_push_approval: false,
        required_review_thread_resolution: true
      }},
      {type: "required_status_checks", parameters: {
        strict_required_status_checks_policy: true,
        required_status_checks: $checks
      }},
      # The history is a list of changes worth reading, so no merge commits.
      {type: "required_linear_history"},
      {type: "non_fast_forward"},
      {type: "deletion"}
    ]
  }'
)
if [ -n "$DRY" ]; then
	say "  would apply this ruleset:"
	printf '%s\n' "$ruleset" | sed 's/^/    /' >&2
else
	existing=$(gh api "repos/$REPO/rulesets" --jq '.[] | select(.name=="main") | .id' 2>/dev/null || true)
	if [ -n "$existing" ]; then
		say "  updating ruleset $existing"
		printf '%s' "$ruleset" | gh api -X PUT "repos/$REPO/rulesets/$existing" --input - >/dev/null
	else
		say "  creating it"
		printf '%s' "$ruleset" | gh api -X POST "repos/$REPO/rulesets" --input - >/dev/null
	fi
fi

say "== merge behaviour =="
# Squash only, and the commit message comes from the pull request title and body
# rather than from a list of "fix review" commits.
run gh api -X PATCH "repos/$REPO" \
	-F allow_squash_merge=true \
	-F allow_merge_commit=false \
	-F allow_rebase_merge=true \
	-F delete_branch_on_merge=true \
	-F squash_merge_commit_title=PR_TITLE \
	-F squash_merge_commit_message=PR_BODY \
	>/dev/null

say "== security features =="
# Dependabot alerts and secret scanning are off by default on a new repository.
# Push protection is the one that matters most here: this tree's whole
# credential discipline is undone by one token in a commit.
# Nested fields go in as JSON: `gh api -F a[b]=c` does not build an object, and
# the brackets are a glob to the shell besides.
security=$(jq -n '{
  security_and_analysis: {
    secret_scanning: {status: "enabled"},
    secret_scanning_push_protection: {status: "enabled"}
  }
}')
if [ -n "$DRY" ]; then
	say "  would enable secret scanning and push protection"
else
	printf '%s' "$security" | gh api -X PATCH "repos/$REPO" --input - >/dev/null
fi
run gh api -X PUT "repos/$REPO/vulnerability-alerts" >/dev/null
run gh api -X PUT "repos/$REPO/automated-security-fixes" >/dev/null

say "== done =="
say "Verify with:  gh api repos/$REPO/rulesets --jq '.[].name'"
