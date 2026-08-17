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
#
# Two failures live here and they are not the same failure, so they do not share
# an exit code:
#
#   0  every package matched its baseline
#   1  a count disagreed with the baseline, in either direction
#   2  no count could be produced, so nothing was compared
#
# The distinction is for whoever reads the report. A 1 is a finding about the
# tests and somebody writes one or lowers a number. A 2 is a finding about the
# run, and saying anything about the tests on the strength of it would be this
# script inventing a result it does not have.
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
	exit 2
fi
if [ ! -f "$baseline" ]; then
	echo "no baseline at $baseline" >&2
	exit 2
fi

# A mutant that does not terminate is not hypothetical. Four of them live in
# internal/jql/token.go, whose scan loop carries no post statement, so every
# case advances `i` itself. INCREMENT_DECREMENT flips one of those `i++` to
# `i--`, it oscillates against the unmutated `i++` in a neighbouring case, and
# `out = append(out, ...)` grows on every cycle forever.
#
# Gremlins handles those correctly on its own terms: the per-mutant timeout
# fires and the mutant is recorded as timed out. What it cannot do is survive
# the wait. That timeout is the coefficient times the measured suite time, and
# the measurement is of a cold build as much as of a suite: 1.74 seconds on a CI
# runner against 79.77 milliseconds here. The same mutant therefore gets about
# 104 seconds there rather than 4.8, which is enough to take sixteen gigabytes
# and the machine with them. It killed three scheduled sweeps, and every arm of
# a probe that tried to fix it by lowering the worker count, one worker
# included: a single runaway is sufficient.
#
# So the bound belongs on the process and not on the concurrency. Measured on
# arm64 and amd64 alike, and the floor is sharp because thread stacks count
# against this limit: below 3 GiB the Go runtime cannot start, dying with
# "pthread_create failed: Resource temporarily unavailable" before any mutant
# runs. At 3 GiB a runner completed the sweep for the first time, reporting
# Killed: 203, Lived: 16, Timed out: 0 and never dropping below 11.8 GB free.
# 4 GiB reports exactly the same counts and bottoms out at 8.0 GB, so 3 GiB is
# the same answer with twice the headroom.
#
# The baseline does not move: `lived` is what it records, and a runaway that
# used to exhaust its timeout now dies at the cap and is counted as killed.
# The cap goes on `go` and not on this shell, which is the difference between a
# fix and a second bug. Capping the shell caps gremlins too, and gremlins sizes
# itself from NumCPU: at 3 GiB a four-core runner is fine and a twelve-core
# machine dies inside `filepath.Walk`, copying the source tree per worker, while
# below 3 GiB gremlins cannot start at all and aborts with
# "pthread_create failed: Resource temporarily unavailable" because thread
# stacks count against this limit. None of that is about the mutants. The
# processes that run away are the `go test` children, so they are what to bound,
# and with the cap in the right place gremlins is free to size itself however
# the machine warrants.
#
# gremlins resolves `go` from PATH (`exec.CommandContext(ctx, "go", ...)`), so a
# shim ahead of it reaches every child and nothing else. This script calls
# gremlins by absolute path and is unaffected by its own shim.
#
# 3 GiB rather than less: a four-core runner completes the full sweep at 2 GiB
# too, never touching swap and bottoming out at 14.1 GB free against 12.2 GB at
# 3 GiB, so the lower value is not wrong. 3 GiB is what has also been run
# against twelve workers here, and it leaves room for a package larger than the
# three swept today without anybody having to rediscover this comment.
MUTATION_ADDRESS_SPACE_KIB=${MUTATION_ADDRESS_SPACE_KIB:-3145728}
realgo=$(command -v go) || {
	echo "no go on PATH" >&2
	exit 2
}
shim=$(mktemp -d)
trap 'rm -rf "$shim"' EXIT
cat >"$shim/go" <<EOF
#!/bin/sh
# Bound the address space a mutated test may claim, then hand over to the real
# toolchain. macOS does not implement this limit, so the failure is ignored: a
# sweep that refuses to run is worse than one that can still be taken down by a
# mutant, and the machines this protects are the CI ones.
ulimit -v $MUTATION_ADDRESS_SPACE_KIB 2>/dev/null || true
exec $realgo "\$@"
EOF
chmod +x "$shim/go"
PATH="$shim:$PATH"
export PATH

moved=0
broken=0
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
		printf '%-24s %8s %8s %s\n' "$pkg" "?" "$want" "NO COUNT: the run produced no summary"
		printf '%s\n' "$out" | tail -20 >&2
		broken=1
		continue
	fi

	verdict=ok
	if [ "$got" -gt "$want" ]; then
		verdict="REGRESSED: $((got - want)) more mutant(s) survive"
		moved=1
	elif [ "$got" -lt "$want" ]; then
		verdict="IMPROVED: lower the baseline to $got in this change"
		moved=1
	fi
	printf '%-24s %8s %8s %s\n' "$pkg" "$got" "$want" "$verdict"

	# The survivors themselves, because a count says a test is missing and
	# these say where to write it.
	if [ "$got" -gt 0 ]; then
		printf '%s\n' "$out" | sed -n 's/^ *LIVED /    lived: /p'
	fi
done <"$baseline"

# A sweep that could not measure a package outranks one that measured a move,
# because the headline it earns is different: the counts this run does have are
# a partial sweep's counts, and the first thing to fix is that it ran at all.
# Both are in the table either way.
if [ "$broken" -eq 1 ]; then
	exit 2
fi
exit "$moved"
