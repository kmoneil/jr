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
- [Wiki markup is checked, not parsed](#wiki-markup-is-checked-not-parsed)
- [Questions JQL cannot answer](#questions-jql-cannot-answer)
- [`text ~` is stemmed, unranked, and silent about stop words](#text--is-stemmed-unranked-and-silent-about-stop-words)
- [Nothing splits on commas](#nothing-splits-on-commas)
- [`--field` reads a column and writes a value](#--field-reads-a-column-and-writes-a-value)
- [A record in TSV is not a row](#a-record-in-tsv-is-not-a-row)
- [`auth token` is a document, not a header](#auth-token-is-a-document-not-a-header)
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

**And a sprint id you looked up earlier goes stale.** Sprints roll over. An id
resolved at the start of a long session can name a closed sprint by the end of
it, and `sprint add` into a closed one is refused with `SPRINT_CLOSED` and moves
nothing. Re-derive it near the write that uses it rather than carrying it:

```console
jr sprint current                             # the board's one active sprint
jr sprint list --state active --state future  # everything that will take issues
```

`sprint current` refuses when the answer is not singular: `NO_ACTIVE_SPRINT`
(exit 5) when nothing is running, `AMBIGUOUS_SPRINT` (exit 2, naming every
candidate) when the board runs several at once.

The same applies to any id with a lifecycle, such as a version or a board that
gets archived. Re-derive; do not remember.

## Wiki markup is checked, not parsed

Data Center stores wiki markup and `jr` sends it through untouched. Some
constructs have more than one reading, and you have no way to see the result,
so `jr` warns on the ones it can be certain about. Exit stays 0 and the write
happens: nothing here is known to be wrong.

```console
jr issue create --type Task --summary x --description '{{/subjects/{subject\}}}'
# → AMBIGUOUS_WIKI_MARKUP: "}}}" is three or more braces in a row ...
```

Three cases: a run of three or more braces, an unclosed `{code}`, `{noformat}`,
`{quote}`, `{panel}` or `{color}`, and an unequal number of `{{` and `}}`
outside a code block. Text inside a fence is literal and is left alone.

**If you want braces rendered literally, put them in `{code}` or `{noformat}`.**
That is unambiguous and is the fix for the warning as well as for the text.

Nothing else about your markup is checked. A silent run does not mean the markup
is right, only that it holds none of these three. Cloud never warns, because a
body there becomes an ADF document where a brace is a brace.

## `text ~` is stemmed, unranked, and silent about stop words

Measured against Jira 10.4.0 Data Center.

| You write | What happens |
| --- | --- |
| `text ~ "truncate"` | matches a stored "truncated"; the index stems |
| `text ~ "a" AND text ~ "b"` | the intersection, including across fields |
| `text ~ "the"` | **nothing, exit 0**, because the index discards common words |

**There is no relevance ranking.** Every query carries `ORDER BY issuekey DESC`,
so the "top" of a text search is the highest issue key that matched and nothing
more. Reading the first five rows as the five best matches is wrong. Ordering by
relevance is not available: `--jql` carrying its own `ORDER BY` is refused,
because the fragment is parenthesised and JQL does not allow one there.

When a text search disappoints, check in this order: try each word alone, in
case one is a stop word; consider what the word stems to; and do not read a
key-ordered list as a ranked one.

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
updated it**, not that the named person did.
`--assignee currentUser --updated-after -7d` answers "assigned to me and touched
by anyone this week."

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

## `--field` reads a column and writes a value

One word, two jobs, and the `=` is what separates them.

```console
# On a read it selects a column, and takes a bare name.
jr issue get ENG-101 --field 'Story Points'

# On a write it sets the field, and takes id=value.
jr issue edit ENG-101 --field 'Story Points=5'
```

A bare name on a write is refused with `FIELD_NOT_KV` rather than read as the
other sense. Repeat one id on a write to build an array; nothing is split on
commas here either.

A field with a typed flag of its own (summary, description, priority, labels,
assignee, parent, type) is refused with `FIELD_HAS_A_FLAG` naming that flag.
Use the flag: it validates the value and resolves a user, which `--field`
cannot.

`FIELD_TYPE_UNSUPPORTED` means the field exists and its schema type says nothing
about what to send. Jira reports Epic Link, Rank, Team, and most plugin fields
as `any`. The remedy names `--field-json`, whose value is JSON sent verbatim, so
a string carries its own quotes inside the shell's:

```console
jr issue edit ENG-101 --field-json customfield_11350='"ENG-42"'
```

`jr field list --format json` carries both the schema type and Jira's own type
key, which is the only thing that tells two `any` fields apart.

**A requested field says whether it has a value.** An empty element used to be
unreadable: a field with no value, a field holding an empty string, and a field
the server never sent all looked the same.

```console
jr issue get ENG-101 --field 'Story Points'
# → <customfield_10042 name="Story Points" set="false"/>
```

`set="false"` appears only when the issue has no value, so its absence means the
text is the value. The `name` is your site's catalogue name, not what you typed,
so asking by name and by id give identical bytes. The TSV header stays the id.

**`--field Sprint` is structured on both deployments.** Data Center sends that
field as Greenhopper's Java `toString`, one dump per sprint the issue has been
through, so on a long-lived issue the raw value runs to thousands of characters.
`jr` reads it into sprints, which is what makes the live one cheap to find:

```console
jr issue get ENG-101 --field Sprint
# → <sprint id="12346" state="active">ENG Sprint 4</sprint>
```

`state` is `future`, `active`, or `closed`. In TSV the column holds the names,
comma-joined. A value that does not parse as a sprint is passed through
unchanged, so check for the element rather than assuming it.

## A record in TSV is not a row

A collection in TSV is a header row plus data rows. A **single record** in TSV is
a field/value table, so a value is a lookup by name and not a column:

```bash
id=$(jr sprint create "Sprint 14" --board 42 --format tsv |
     awk -F'\t' '$1 == "@id" { print $2 }')
```

If you are parsing one record, XML or JSON is usually the better choice.

## `auth token` is a document, not a header

Every result this tool produces is a document, and `auth token` is no exception.
Capturing it whole and using it as a header produces a malformed header, and
curl reports that as status `000`, which is what it also reports for a network
failure, so the mistake does not look like one.

```console
# Wrong in every format: $TOKEN is a whole document.
TOKEN=$(jr auth token); curl -H "Authorization: $TOKEN" ...

# Right: the flag that prints a header and nothing else.
curl -H "$(jr auth token --header)" ...

# Or take the one field.
header=$(jr auth token --format tsv | awk -F'\t' '$1=="authorization"{print $2}')
```

`--header` with an explicit `--format` is `HEADER_AND_FORMAT`, exit 2: they name
two different outputs.

## The default limit is 50

Worth repeating here because it is the most common way to get a wrong answer that
looks right: a query with more than 50 matches returns 50 rows, warns on stderr,
and exits 3. In a pipeline the rows have already flowed downstream by the time
the exit code arrives.

Before reporting a count or acting on every result, confirm completeness: exit 0,
`complete="true"`, or the absence of a `RESULT_TRUNCATED` warning.
