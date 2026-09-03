# Workflows

Multi-step procedures. Each one is written as the sequence to run, with the
failure that makes the sequence necessary.

## Contents

- [Getting a complete result set](#getting-a-complete-result-set)
- [Resuming an interrupted run](#resuming-an-interrupted-run)
- [Bulk changes across a query](#bulk-changes-across-a-query)
- [Transitioning an issue](#transitioning-an-issue)
- [Running a sprint](#running-a-sprint)
- [Filing an issue safely](#filing-an-issue-safely)
- [Editing something you read first](#editing-something-you-read-first)
- [Working from a script or CI](#working-from-a-script-or-ci)

## Getting a complete result set

`--limit` defaults to 50. A query with more matches than that returns 50 rows,
warns on stderr, and **exits 3**. The rows look like a complete answer.

```console
jr issue list --project ENG --status 'In Progress' --limit all --max-requests 50
```

`--limit all` exhausts the set. `--max-requests` bounds how long that can take;
exceeding it truncates and exits 3 with a resume token rather than running for an
hour against a 40,000-issue project.

`--limit all` with no filter at all is refused with `UNCONSTRAINED_QUERY`. Scope
it, or pass `--all-projects` if a whole-instance sweep is what you mean.

Before reporting a count, confirm one of the three truncation signals says you
have everything: exit 0, `complete="true"` in the envelope, or no
`RESULT_TRUNCATED` warning on stderr.

## Resuming an interrupted run

Exit 3 always carries an opaque `next-page-token` in the stderr warning.

```console
jr issue list --project ENG --limit 50
# exit 3; stderr carries RESULT_TRUNCATED and a token

jr issue list --project ENG --limit 50 --page-token <token>
```

The token encodes the deployment it was minted against, so a Cloud token is
refused against Data Center rather than silently read as offset zero. Do not
construct one, and do not try to convert it to an offset: there is no offset
flag, by design, because an offset shifts when a row is inserted above it and a
long run then skips or repeats while reporting itself complete.

## Bulk changes across a set

`issue edit` takes several keys only through a plan, and that is the only path
to a bulk write. `--plan-out <file>` sends nothing and writes a document: one
row per issue, each carrying its own baseline and an idempotency key, with the
change written once because a plan applies one change to many issues. Read it,
then `--apply <file>`.

```console
jr issue edit ENG-101 ENG-102 ENG-103 --add-label triaged --plan-out plan.xml
jr issue edit --apply plan.xml
```

Every row is attempted. Each is reported `applied`, `skipped` or `failed` with
its own error code, and a row somebody changed since the plan was built is
refused with nothing sent while the rest still go through. The exit is whatever
stopped the rows that failed, so exit 7 means somebody moved a ticket and exit 8
means you are being throttled.

Running the same apply again is the resume, and there is no flag for it: a row
that already landed is skipped, because that is what its idempotency key means.
An interrupted run is finished by running it again.

The cap is fifty rows, which is about how much a person reads rather than what
the API can carry, and a plan applies one change to every row. A different
change per row is a second plan.

## Bulk changes a plan cannot express

For a set a plan does not fit, loop, and let the exit codes stop you.

Rehearse first. Read the set before changing it:

```console
jr issue list --jql 'status = "In Review" AND updated < -30d' --limit all
```

Then run the loop, stopping on the first failure:

```bash
set -euo pipefail
jr issue list --label needs-triage --limit all | tail -n +2 | cut -f1 |
  while read -r key; do
      echo "labelling $key" >&2
      jr issue edit "$key" --add-label triaged
  done
```

`--limit all` is load-bearing. Without it the loop processes the first 50 and the
exit 3 arrives from a command whose output already flowed into the pipe.

For anything irreversible, swap the mutating command for its `--dry-run` and read
what it would send before running it for real.

## Transitioning an issue

Transition names are per-issue and per-status. What an issue offers depends on
where it is right now, which is why transitions are never cached.

```console
jr meta transitions ENG-101          # what this issue can do, right now
jr issue move ENG-101 'Close Issue' --resolution 'Done' --dry-run
jr issue move ENG-101 'Close Issue' --resolution 'Done'
```

`UNKNOWN_TRANSITION` lists every transition the issue actually offers, with ids
and destinations. A move missing from that list is far more often blocked from
the current status than misspelled, so read the list before assuming a typo.

## Running a sprint

The id you need at every step is the one `sprint create` reports.

```console
jr sprint create "Sprint 14" --board 42 \
    --start 2026-08-17T09:00:00Z --end 2026-08-31T09:00:00Z \
    --goal "Ship the importer"
jr sprint add 1002 ENG-101 ENG-102
jr sprint start 1002
jr sprint close 1002 --yes
```

Dates are RFC 3339. A bare `2026-08-17` is refused: it names no time and no zone,
and `jr` will not choose one.

Scripting it, where the id is the only thing you need out of the create. A single
record in TSV is a field/value table rather than a row, so the id is a lookup by
name and not a column:

```bash
id=$(jr sprint create "Sprint 14" --board 42 --format tsv |
     awk -F'\t' '$1 == "@id" { print $2 }')
```

`sprint close` returns every unfinished issue to the backlog and no API reopens a
closed sprint. It needs `--yes` and a build carrying the `admin` tag.

**`sprint = <id>` is not a test of current membership.** Jira's Sprint field
records every sprint an issue has ever been in, so a finished sprint answers with
everything that was ever in it, including what was carried out at close.

## Filing an issue safely

```console
jr issue create --type Bug --summary "..." --dry-run
jr issue create --type Bug --summary "..." --idempotency-key "$(uuidgen)"
```

`--dry-run` prints the exact request, body included, and sends nothing.

`--idempotency-key` is what makes a retry safe. Without one, a create that timed
out after Jira processed it becomes two issues when you retry. With one, the
repeat returns the original result marked `replayed="true"` and exit 0. Generate
the key before the first attempt and reuse it on every retry of that same
logical create.

## Editing something you read first

If your edit is computed from a read, the issue may have moved between the two.

The precondition token comes from the `precondition` attribute of `jr issue get`
and from nowhere else. It is opaque, so there is nothing to assemble by hand.

```console
jr issue get ENG-101 --format json | jq -r '.issue.precondition'
jr issue edit ENG-101 --priority High --if-unchanged eyJkIjoiY2xvdWQi...
```

`--if-unchanged` reads the issue, compares, and refuses a changed one with
`STALE_WRITE` at exit 7 having sent nothing. It is a read-compare with a window
one round trip wide, not an atomic compare-and-swap, and its output says
`method="read-compare"` rather than implying the stronger promise.

On exit 7 the remedy is a loop, not a retry. Read the issue **again**, get a
fresh precondition, decide again against what it says now (the change you were
about to make may no longer be the one you want), and write with the second
token. Retrying with the same precondition fails identically every time, because
it describes a version of the issue that no longer exists.

`updated` moves for any change, including a comment somebody added, so this can
refuse a write that would not actually have collided. That is deliberate: the
alternative is deciding which changes count, and getting that wrong loses an edit
silently.

Do not drop the flag to force the write through.

## Working from a script or CI

Everything comes from the environment. No login step, no config file:

```bash
export JIRA_SITE=your-company.atlassian.net
export JIRA_EMAIL=ci@company.com          # Cloud only
export JIRA_API_TOKEN="$JIRA_TOKEN"
export JIRA_FORMAT=json
export JIRA_READONLY=1                    # if the job only reads
```

On Data Center, **leave `JIRA_EMAIL` unset**. A token with a user beside it is
sent as HTTP Basic; a token alone is sent as a bearer token. A default Jira 11
refuses Basic outright with `AUTH_SCHEME_REFUSED` at exit 4 on the first request.
The variable that makes it work is the one you leave out.

Branch on the exit code, not on the output:

```bash
if ! out=$(jr issue list --jql 'labels = release-blocker' --limit all); then
    status=$?
    case $status in
        3) echo "truncated; widen --limit" >&2 ;;
        4) echo "credentials rejected" >&2 ;;
        *) echo "jr failed with $status" >&2 ;;
    esac
    exit $status
fi
```

`set -e` alone is not enough: exit 3 means *success, truncated*, and whether that
is acceptable is a decision for the job rather than a generic failure to inherit.
