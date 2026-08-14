# Gotchas

Everything here returns **exit 0 and a result that reports itself complete**.
None of it produces an error. These are the cases where a confident wrong answer
is available, which makes them the ones worth knowing before you need them.

## Contents

- [Dates are evaluated in Jira's timezone, not yours](#dates-are-evaluated-in-jiras-timezone-not-yours)
- [`-1M` is one minute, not one month](#-1m-is-one-minute-not-one-month)
- [A filter never orders anything](#a-filter-never-orders-anything)
- [Issue keys do not sort as text](#issue-keys-do-not-sort-as-text)
- [`sprint = <id>` is not current membership](#sprint--id-is-not-current-membership)
- [Questions JQL cannot answer](#questions-jql-cannot-answer)
- [Nothing splits on commas](#nothing-splits-on-commas)
- [A record in TSV is not a row](#a-record-in-tsv-is-not-a-row)
- [The default limit is 50](#the-default-limit-is-50)

## Dates are evaluated in Jira's timezone, not yours

Every date you send is evaluated in the timezone on **the Jira account's
profile**. Not UTC, and not your machine's clock, which the server never learns.

```console
jr user me        # <timezone>America/Chicago</timezone>
```

For an account on `America/Chicago` in August, `startOfDay()` is 05:00Z, so
"created today" quietly starts five hours late and still reports
`complete="true"` at exit 0. The same applies to a bare literal:
`--created-after "2026-08-10 00:00"` is midnight *there*.

Relative offsets like `-30d` are relative to now and unaffected. Anything with a
day boundary in it is not.

To mean your own day, convert it and send an absolute literal:

```bash
start=$(TZ=Pacific/Auckland date -d "today 00:00" +%s)
jr issue list --created-after "$(TZ=America/Chicago date -d @$start '+%Y-%m-%d %H:%M')"
```

`startOfWeek()` and friends are passed through rather than computed locally on
purpose: they carry Jira's own notion of when a week begins, which a converted
instant does not.

## `-1M` is one minute, not one month

Jira's period units are case-insensitive on a date field, so `M` and `m` are one
unit and that unit is minutes. `--updated-after -1M` asks for the last minute
and answers exit 0, `complete="true"`, and usually empty.

There is no month unit on a field. The units are `m` `h` `d` `w`, either case,
and a compound sums its components with the sign on the front: `-4w 2d` is
thirty days. For a month, say a month:

```bash
jr issue list --updated-after -30d               # thirty days
jr issue list --updated-after 'startOfMonth()'   # this calendar month
jr issue list --updated-after 'endOfDay(-1M)'    # a month ago
```

The third one is a **different grammar**, and it is the reason to read this
twice: inside a date function the units are `y M w d h m`, they are
case-sensitive, `M` means months, and there is no compound form.

## A filter never orders anything

Without `--sort`, results come back **by issue key descending**. On a busy
project that is near enough to creation order to be mistaken for "most recent",
which is exactly how it gets misread.

```console
# Wrong: filters to the last day, then returns them in key order
jr issue list --updated-after -1d

# Right: the date filter narrows, --sort orders
jr issue list --updated-after -1d --sort updated --order desc
```

`--order` on its own turns the key ordering around rather than doing nothing.

Every query carries an `ORDER BY`, and a caller's `--sort` keeps the key as a
tiebreaker. An unordered query would depend on the server's undocumented default,
which is not guaranteed stable between two requests, so a paged result could
interleave two orderings unnoticed.

## Issue keys do not sort as text

`IDO-999` is below `IDO-1000` as an issue and above it as a string. If you sort
keys yourself, sort by project then by numeric part. Sorting a column of keys
with `sort` gives you the wrong order and no indication that it did.

## `sprint = <id>` is not current membership

Jira's Sprint field records **every sprint an issue has ever been in**. A
finished sprint answers with everything that was ever in it, including what was
carried out at close.

If you want what is in a sprint now, that is a different question than
`--jql 'sprint = 1002'` answers.

## Questions JQL cannot answer

Two limits are worth knowing rather than discovering, because in both cases the
plausible substitute silently answers a different question:

- **Comment authorship is not searchable.** JQL has no field for it, so nothing
  answers "issues I commented on." `--involving` says so rather than
  approximating it. Do not substitute `--involving` and describe the result as
  comment activity.
- **`CHANGED` names one field at a time.** There is no way to ask whether *any*
  field changed. `--changed-field` defaults to `status`; anything else has to be
  named explicitly.

Related, and the most common misreading: **`--updated-after` means somebody
updated it**, not that the named person did. `--assignee me --updated-after -7d`
answers "assigned to me and touched by anyone this week."

The five who-touched-it flags are genuinely different questions:

| Question | Flag |
| --- | --- |
| Who owns it now | `--assignee` |
| Who filed it | `--reporter` |
| Who touched it at all | `--involving` |
| Who used to own it | `--was-assignee` |
| Who logged time on it | `--worklog-author` |
| Who changed its status | `--changed-by` |

Every user-valued flag takes a display name, an email, an account id, or
`currentUser`, and resolves it against the site before sending. A name matching
nobody is refused rather than sent. The sentinel words `unassigned` and `empty`
are honoured on `--assignee` only: `creator IS EMPTY` matches nothing and
`CHANGED BY EMPTY` is not JQL.

## Nothing splits on commas

A status or a label may contain a comma, so nothing is ever split on one.

```console
# One status, named "To Do,In Progress". No project has it. Exit 0, empty.
jr issue list --status 'To Do,In Progress'

# Two statuses
jr issue list --status 'To Do' --status 'In Progress'
```

Repeated flags OR together. Different flags AND together. Every list filter has a
negative twin (`--not-status`, `--not-type`, `--not-label`) which is a `NOT IN`
and ANDs with the rest.

## A record in TSV is not a row

A collection in TSV is a header row plus data rows. A **single record** in TSV is
a field/value table, so a value is a lookup by name and not a column:

```bash
id=$(jr sprint create "Sprint 14" --board 42 --format tsv |
     awk -F'\t' '$1 == "@id" { print $2 }')
```

If you are parsing one record, XML or JSON is usually the better choice.

## The default limit is 50

Worth repeating here because it is the most common way to get a wrong answer that
looks right: a query with more than 50 matches returns 50 rows, warns on stderr,
and exits 3. In a pipeline the rows have already flowed downstream by the time
the exit code arrives.

Before reporting a count or acting on every result, confirm completeness: exit 0,
`complete="true"`, or the absence of a `RESULT_TRUNCATED` warning.
