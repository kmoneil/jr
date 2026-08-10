#!/bin/sh
# Classify one `go test -fuzz` run: pass, flake, or fail.
#
# Reads the run's combined output on stdin and takes its exit status as $1.
# Prints exactly one word on stdout.
#
# Why this exists
#
# Go 1.26 has a race in the fuzzing coordinator's shutdown. When -fuzztime
# elapses, `internal/fuzz.CoordinateFuzzing` wakes on the parent context's Done
# channel and calls stop(ctx.Err()), and stop suppresses the deadline error by
# comparing it against a *child* context:
#
#     if err == fuzzCtx.Err() || isInterruptError(err) { err = nil }
#
# `cancelCtx.cancel` stores the error, closes its own done channel, and only
# then cancels its children — and `cancelCtx.Err()` is a lock-free atomic load,
# so a coordinator woken between those two steps reads fuzzCtx.Err() as nil,
# fails to suppress, and returns context.DeadlineExceeded as the target's
# result. The target ran its whole budget and found nothing; the run reports
# FAIL anyway.
#
# Upstream: golang/go#75804, fixed by https://go.dev/cl/774140 with
# `err == ctx.Err() ||` added to the same condition, and backported to
# release-branch.go1.27 by https://go.dev/cl/804900. Neither is in any 1.26.x,
# and the backport landed after go1.27rc2 was cut, so the earliest build that
# has it is go1.27.0. TestTheFuzzFlakeWorkaroundIsStillNeeded deletes this
# script's reason for existing the moment the toolchain catches up.
#
# What must not happen
#
# A real crasher must never be classified as a flake. Every real failure the
# fuzzer finds writes the input to testdata and says so, so the absence of
# "Failing input written to" is the discriminator, and a seed-corpus failure —
# which fails before any input is written — is named separately. The two other
# spurious-failure modes upstream knows about are deliberately *not* absorbed
# here: a hung worker (golang/go#56238) is a different bug and this script has
# no evidence about it, so it stays a failure.
#
# The sweep prints the flaked run's full output either way. Nothing is hidden;
# the verdict decides only whether the sweep's exit status blames the target.

set -eu

status="${1:?usage: fuzz-verdict.sh <exit-status> < output}"
out=$(cat)

if [ "$status" -eq 0 ]; then
	echo pass
	exit 0
fi

# The whole failure detail is a bare "context deadline exceeded" on its own
# line. A target whose own t.Fatalf happens to contain that phrase reads
# differently, because its line carries the message around it.
if ! printf '%s\n' "$out" | grep -qE '^[[:space:]]*context deadline exceeded[[:space:]]*$'; then
	echo fail
	exit 0
fi

# Anything that names a real finding, however it got there.
for marker in \
	'Failing input written to' \
	'failure while testing seed corpus entry' \
	'fuzzing process hung or terminated unexpectedly' \
	'fuzzing process exited unexpectedly'; do
	if printf '%s\n' "$out" | grep -qF "$marker"; then
		echo fail
		exit 0
	fi
done

echo flake
