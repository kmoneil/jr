#!/usr/bin/env bash
#
# Mutation testing, held to a baseline that only goes down.
#
# A mutation score is not a number to chase. What it is good for is the
# question a coverage number cannot ask: for this change to this line, does any
# test notice? `internal/jql` answers "no" sixteen times while sitting at 100%
# statement coverage, which is the whole argument for running this at all.
#
# So this is a ratchet and not a threshold. Each package carries the number of
# mutants that survive today; more than that fails, fewer than that fails too,
# with a message asking for the baseline to be lowered in the same change. A
# baseline nobody has to update stops describing the tree, which is the same
# rule the unenforced ledger and the unrecorded ledger already follow.
#
# The timeout coefficient is not a tuning knob, it is a correctness fix. See
# TIMEOUT_COEFFICIENT below.
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# MUTATION_BASELINE is an override so the exit codes can be driven against a
# one-package file rather than a five-minute sweep. Every default here is the
# real one.
baseline=${MUTATION_BASELINE:-$repo/scripts/mutation-baseline.tsv}
gremlins=${GREMLINS:-$(go env GOPATH)/bin/gremlins}

# The coefficient multiplies the measured suite time to get a per-mutant
# timeout, and the default of 2 is catastrophic here rather than merely tight:
# `internal/jql` runs in 15 milliseconds, so the timeout lands below the compile
# and 194 of 216 mutants are abandoned unrun. Gremlins then reports
# "Test efficacy: 100.00%" over the 25 that finished. A tool whose default
# answer is a perfect score for a tenth of the work is worse than no tool, so
# the number lives here and not in whoever's shell history.
TIMEOUT_COEFFICIENT=${TIMEOUT_COEFFICIENT:-60}

if [ ! -x "$gremlins" ]; then
	echo "no gremlins at $gremlins. Run 'make tools', or set GREMLINS." >&2
	exit 1
fi
if [ ! -f "$baseline" ]; then
	echo "no baseline at $baseline" >&2
	exit 1
fi

status=0
printf '%-24s %8s %8s %s\n' package lived baseline verdict

while IFS=$'\t' read -r pkg want _note; do
	case "$pkg" in '' | '#'*) continue ;; esac

	out=$("$gremlins" unleash \
		--timeout-coefficient "$TIMEOUT_COEFFICIENT" \
		"./$pkg/" 2>&1) || true

	# The summary line, rather than the JSON, because the count is the whole
	# result and a parser is a second thing to keep right. A run that printed no
	# summary at all is a failure and not a zero: that is how a tool that could
	# not build its own mutants reports itself.
	got=$(printf '%s\n' "$out" | sed -n 's/.*Lived: \([0-9]*\).*/\1/p' | tail -1)
	if [ -z "$got" ]; then
		printf '%-24s %8s %8s %s\n' "$pkg" "?" "$want" "no summary: the run failed"
		printf '%s\n' "$out" | tail -20 >&2
		status=1
		continue
	fi

	verdict=ok
	if [ "$got" -gt "$want" ]; then
		verdict="REGRESSED: $((got - want)) more mutant(s) survive"
		status=1
	elif [ "$got" -lt "$want" ]; then
		verdict="IMPROVED: lower the baseline to $got in this change"
		status=1
	fi
	printf '%-24s %8s %8s %s\n' "$pkg" "$got" "$want" "$verdict"

	# The survivors themselves, because a count says a test is missing and
	# these say where to write it.
	if [ "$got" -gt 0 ]; then
		printf '%s\n' "$out" | sed -n 's/^ *LIVED /    lived: /p'
	fi
done <"$baseline"

exit "$status"
