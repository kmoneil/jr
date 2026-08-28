# Recipes

Worked examples for the things people actually do. Every one is copy-pasteable
once you have finished [getting-started.md](getting-started.md).

Commands are shown without `--project`, on the assumption your context has one.
Add `--project ENG` to any of them to override it for a single run.

- [Finding issues](#finding-issues)
- [Finding your own work](#finding-your-own-work)
- [Reading an issue](#reading-an-issue)
- [Creating and editing](#creating-and-editing)
- [Moving issues through a workflow](#moving-issues-through-a-workflow)
- [Comments, worklogs, and links](#comments-worklogs-and-links)
- [Sprints, epics, and boards](#sprints-epics-and-boards)
- [Custom fields](#custom-fields)
- [Exporting and piping](#exporting-and-piping)
- [Bulk operations](#bulk-operations)
- [Working safely](#working-safely)
- [Scripting and CI](#scripting-and-ci)
- [Agents and MCP](#agents-and-mcp)
  - [Installing the skill](#installing-the-skill)

## Finding issues

```console
# Everything open in your default project
$ jr issue list --status 'To Do' --status 'In Progress'

# By type, by label
$ jr issue list --type Bug --label regression

# Excluding a label
$ jr issue list --type Bug --not-label wontfix

# Excluding statuses: everything still live
$ jr issue list --not-status Done --not-status Closed

# Excluding types: no sub-tasks in the listing
$ jr issue list --not-type Sub-task

# Created or updated in a window; offsets are relative to now
$ jr issue list --created-after -30d
$ jr issue list --updated-after 2026-01-01 --updated-before 2026-02-01

# Sorted, with the issue key as a stable tiebreaker you get for free
$ jr issue list --sort updated --order desc

# What moved today, newest first: the date filter narrows, --sort orders
$ jr issue list --updated-after -1d --sort updated --order desc
```

Repeatable flags OR together, so `--status 'To Do' --status 'In Progress'` means
either. Different flags AND together. Every list filter has a negative twin —
`--not-status`, `--not-type`, `--not-label` — which is a `NOT IN` and ANDs with
the rest. A repeated flag is also the only way to name several values: nothing
splits on commas, so `--status 'To Do,In Progress'` asks for one status with a
comma in its name.

A mistyped value fails differently by field, and it is worth knowing which one
is quiet. Jira validates a status and an issue type, so `--status Dnoe` comes
back as an error naming the value. It does not validate a label, so
`--label regresion` is a legal query with a truthful, empty answer, the same
answer a correct label gets on a day nothing carries it. `jr` checks the label
itself and says so on stderr, without refusing the query:

```console
$ jr issue list --label regresion
<warning v="1">
  <code>UNKNOWN_LABEL</code>
  <message>no issue on this site carries the label "regresion"</message>
</warning>
key	status	assignee	updated	summary
```

The rows still go to stdout, the exit is still 0, and a script that reads only
stdout is unaffected. The check is site-wide: it catches a label nothing
anywhere carries, and stays quiet about one that exists in a project this query
is not looking at.

**A filter never orders anything.** Without `--sort`, results come back by issue
key descending — near enough to creation order to be mistaken for "most recent",
which is why `--updated-after` above is paired with a `--sort`. `--order` on its
own turns that key ordering around rather than doing nothing.

**Dates are evaluated in the Jira account's timezone, not yours.** An offset
like `-30d` is relative to now and unaffected, but anything with a day boundary
in it — `startOfDay()`, `2026-01-01`, `startOfWeek()` — means that boundary
_there_. `jr user me` reports the zone:

```console
$ jr user me | grep timezone
```

If it is not your zone, `--created-after startOfDay()` is not your midnight. For
an account on `America/Chicago`, it is 05:00Z, so "created today" starts five
hours late and still reports itself complete. To mean your own day, convert it:

```console
$ start=$(TZ=Pacific/Auckland date -d "today 00:00" +%s)
$ jr issue list --created-after \
    "$(TZ=America/Chicago date -d @$start '+%Y-%m-%d %H:%M')"
```

Timestamps coming _back_ are always RFC 3339 in UTC, whichever way the query
went in.

### When the flags do not cover it

Pass JQL whole. It is combined with the other filters and always parenthesized,
so an `OR` inside it cannot widen the scope you set:

```console
$ jr issue list --jql 'priority = Highest AND resolution IS EMPTY'

# Free-text search across summary and description
$ jr issue list --jql 'text ~ "connection reset"'

# Combine: the JQL narrows, your flags still apply
$ jr issue list --type Bug --jql 'summary ~ "timeout" OR summary ~ "deadline"'
```

Check a query without running it:

```console
$ jr jql validate --jql 'project = ENG AND status = Open'
$ jr jql explain --jql 'assignee = currentUser() AND sprint IN openSprints()'
```

A date that does not parse is refused with the reason, rather than silently
matching nothing:

```console
$ jr issue list --created-after 2020-13-45
# exit 2: month 13 is out of range
```

## Finding your own work

There is a real difference between issues that _belong_ to you and issues you
_touched_, and mixing them up is how you get a plausible, wrong answer.

```console
# Assigned to me right now
$ jr issue list --assignee currentUser

# I filed it (cannot be changed afterwards)
$ jr issue list --creator currentUser

# It is on me and somebody updated it this week — note: not "I worked on it"
$ jr issue list --assignee currentUser --updated-after -7d
```

That last one reads like "what I did this week" and is not. `updated` means
somebody updated the issue: a bot relabelling your ticket on Tuesday puts it in
the result, and everything you did to somebody else's ticket stays out.

For the second question:

```console
# One person across assignee, reporter, creator, and worklogAuthor
$ jr issue list --involving currentUser --updated-after -7d

# I changed the status of it
$ jr issue list --changed-by currentUser --changed-after -1w

# I changed some other field
$ jr issue list --changed-by currentUser --changed-field assignee --changed-after -1w

# I logged time against it
$ jr issue list --worklog-author currentUser --worklog-after -7d

# It was mine at some point, whoever has it now
$ jr issue list --was-assignee currentUser
```

Two limits worth knowing rather than discovering:

- **Comment authorship is not searchable.** JQL has no field for it, so nothing
  here answers "issues I commented on". `--involving` says so rather than
  quietly approximating it. What you can do is fetch the threads and look, in
  the request the listing already costs — see below.
- **`CHANGED` names one field at a time.** There is no way to ask whether _any_
  field changed, so `--changed-field` defaults to `status` and anything else has
  to be named.

Every user-valued flag takes a display name, an email, an account id, or the
word `currentUser`. A name that resolves to nobody is refused rather than sent —
because `assignee = "Ada Lovelace"` against Cloud matches nothing and comes back
complete, empty, and successful, which is indistinguishable from a real answer.

### Building the whole picture of somebody's week

Three sources, and none of them subsumes the others. What is mine, what I
changed, and what I said.

```console
# What changed on an issue, and who changed it
$ jr issue history ENG-101

# Just the transitions, which is usually the question
$ jr issue history ENG-101 --changed-field status

# The issues, with every comment thread, in one request per page
$ jr issue list --involving ada --updated-after -7d --with-comments --format json
```

**Reach for `--changed-field` before reading a bare `issue history`.** A
changelog carries every description and Acceptance Criteria edit as a full
before-and-after body on a single row, so one issue with a few revisions can
cost more to read than every other question about it put together. Repeat the
flag for several fields, and pass the name the changelog records or the field's
id. A field the issue never changed warns and names the ones it did, so a typo
is not another empty answer to read.

`--with-comments` is the one to understand before relying on it, because the two
deployments answer differently and the row says which you got:

```console
$ jr issue list --project ENG --with-comments
key      status  assignee  updated               summary       comments  comments-total
ENG-3    To Do             2026-08-12T16:32:09Z  Second story  20        25
```

`20` of `25` means the server did not send the rest. Data Center inlines the
whole thread; **Cloud caps it at twenty and sends the _newest_ twenty**, so the
five oldest comments on that issue are not in the response, the structured
formats carry `start-at="5"` saying so, and the run exits 3. Read that one
thread properly with `jr issue comment list ENG-3 --limit all`.

So a scan for "issues this person commented on" is exact on Data Center and, on
Cloud, exact only for issues with twenty comments or fewer. The counts are in
the output precisely so a script can tell which rows it has to check the hard
way:

```console
$ jr issue list --project ENG --with-comments --format tsv \
    | awk -F'\t' 'NR>1 && $6 != $7 { print $1 }'
ENG-3
```

**`comments-total` is empty when the server sent no count at all.** That is not
a thread of length zero, it is this tool saying it does not know how long the
thread is, and the run exits 3 for it exactly as a thread known to be clipped
does. The `awk` above already does the right thing with it: an empty cell is not
equal to the count, so the row is one to read the hard way, which it is.

Comment authorship is still not searchable — this fetches threads and looks
rather than asking Jira a question it cannot answer. What it saves is the
request per issue, not the reading.

### Or ask for the events directly

`issue activity` merges all of it into one time-ordered feed, newest first, in
the request the page already costs:

```console
$ jr issue activity --since -7d --user ada
at                    issue   kind        author         field   time-spent  from         to        body
2026-08-11T14:02:11Z  ENG-412 comment     Ada Lovelace                                              rolled the runner back
2026-08-11T13:58:04Z  ENG-412 transition  Ada Lovelace   status              In Progress  Blocked
2026-08-10T09:14:00Z  ENG-388 worklog     Ada Lovelace           2h
```

`--kind` narrows it, and narrows the request with it, so a transition feed does
not pay for comment bodies:

```console
$ jr issue activity --since -1d --kind transition --kind field
```

Two things it will not pretend about:

- **The feed is bounded by the issues `--since` matched.** Somebody who
  commented on an issue they never otherwise touched is not in it, because JQL
  cannot search comment authorship and no number of requests changes that.
- **Some of it may be clipped**, and then the run exits 3 rather than looking
  whole. Cloud sends the newest twenty comments of a longer thread; both
  deployments send the *oldest* twenty worklogs, which for a feed about recent
  work is the wrong twenty, so an issue with more than that costs one extra
  request to read properly and gets it. Cloud also caps the changelog it inlines
  at forty saves, and this feed does not yet fetch the rest, so a heavily edited
  issue is reported clipped instead.

### Polling for what changed

`issue activity` answers "what happened lately" once. `issue changes` answers it
repeatedly without gaps or repeats, which is a different problem: each answer
carries the cursor the next one starts from.

```console
$ jr issue changes --since -1h --format json
```

```json
{
  "kind": "issue.changes",
  "v": 1,
  "complete": true,
  "count": 2,
  "changes": [ … ],
  "next-since-token": "eyJkIjoiY2xvdWQiLCJ0IjoiMjAyNi0wOC0xN1QxNDozMDowMFoifQ"
}
```

A poller keeps that token and passes it back:

```bash
token=$(jr issue changes --since -1h --format json | jq -r '."next-since-token"')
while sleep 60; do
  out=$(jr issue changes --since "$token" --format json) || continue
  echo "$out" | jq -c '.changes[]'
  # Only advance when the answer was whole. An empty token means this poll did
  # not cover its window, and the next one has to ask for the same window again.
  next=$(echo "$out" | jq -r '."next-since-token" // empty')
  [ -n "$next" ] && token=$next
done
```

Three things worth knowing:

- **Do not build the cursor yourself.** `updated >= <the last timestamp>` is the
  obvious poll and it is wrong on both deployments: JQL cannot express a bound
  finer than a minute and neither operator can bisect one, so that query either
  repeats a minute of changes every poll or skips whatever landed inside the
  minute it rounded past. The token names a window instead, and two consecutive
  windows cover every instant exactly once.
- **A poll wants a structured format.** The cursor is in the envelope and TSV has
  none.
- **No token means poll again with the same `--since`.** A run cut short by
  `--limit`, by the request budget, or by a clipped changelog exits 3 and issues
  no cursor, because advancing past a window it did not fully report is how a
  feed loses a change quietly.

## Reading an issue

```console
$ jr issue get ENG-101

# With the comment thread
$ jr issue get ENG-101 --with-comments

# With a link you can click or pipe to a browser
$ jr issue get ENG-101 --url

# Reading, not parsing (needs the render tag; make build has it)
$ jr issue get ENG-101 --format markdown
```

`--age` adds a column saying how long since each issue was updated, in words,
without touching the timestamp it is derived from:

```console
$ jr issue list --age
key      status       assignee       updated               summary          age
ENG-101  In Progress  Ada Lovelace   2026-08-10T14:30:31Z  Retry drops...   3 hours
```

Both forms are in the row, so a script keeps parsing `updated` while you read
`age`. It is coarse — one unit, no "ago" — and it stops at days, because a month
has no fixed length and this tool will not pick one for you.

`--url` adds a bare browse URL, on `issue get` and `issue list` alike. It is a
plain string rather than a terminal escape sequence, so it is both clickable in
most terminals and usable in a pipe:

```console
$ jr issue list --assignee currentUser --url | cut -f6 | xargs open
```

## Creating and editing

```console
# Preview first — this sends nothing
$ jr issue create --type Bug --summary 'Retry drops the last error' --dry-run

# Then for real
$ jr issue create --type Bug --summary 'Retry drops the last error'

# With more of it filled in
$ jr issue create \
    --type Story \
    --summary 'Add keyset pagination' \
    --description 'Offsets shift when a row is inserted above them.' \
    --assignee currentUser \
    --priority High \
    --label transport
```

`--description` is sent as **plain text** by default: `**bold**` reaches Jira as
six characters. To have markup interpreted, ask for it:

```console
$ jr issue create --type Bug --summary Probe \
    --description '## Repro

1. Start the server
2. Kill it mid-request' \
    --body-format markdown
```

`--body-format markdown` is Cloud only, and refuses by name anything the
supported subset cannot hold rather than approximating it.

Editing works the same way:

```console
$ jr issue edit ENG-101 --summary 'A better summary'
$ jr issue edit ENG-101 --priority Highest --assignee ada@company.com

# Labels: --label replaces the whole set
$ jr issue edit ENG-101 --label transport --label retry

# ...or adjust it without knowing what is there
$ jr issue edit ENG-101 --add-label flaky --remove-label wontfix
```

Combining `--label` with `--add-label` is refused, because one replaces the set
and the other adjusts it, and doing both has no single right answer.

### Making a retry safe

A `create` that gets a 503 may or may not have created the issue. `jr` never
replays a POST after an upstream error for exactly that reason — but if you want
a retry to be safe, hold an idempotency key:

```console
$ jr issue create --type Task --summary 'Ship release 42' \
    --idempotency-key release-42
```

Run it twice and the second returns the first issue instead of making another.

The same flag is on `issue move`, where it matters more: a transition is not
idempotent, so a retry that guesses wrong either fails confusingly or does the
work again.

```console
$ jr issue move ENG-101 'Close Issue' --idempotency-key release-42
```

The second run answers from the ledger without asking Jira anything, and marks
the result `replayed="true"`. Use a new key for a different issue or a different
transition — the same one is refused rather than answered with the first move's
result.

## Moving issues through a workflow

Ask what an issue can do before telling it to do something:

```console
$ jr meta transitions ENG-101
```

Then move it. The transition is named as Jira names it — which is the transition
name, not the destination status:

```console
$ jr issue move ENG-101 'Start Progress'
$ jr issue move ENG-101 'Close Issue' --resolution Fixed
$ jr issue move ENG-101 'Done' --comment 'Fixed by the retry rework'

# Preview it
$ jr issue move ENG-101 Done --dry-run
```

A transition the issue does not offer _right now_ is refused with the list of
the ones it does, each with its id and destination.

Assigning is its own verb:

```console
$ jr issue assign ENG-101 ada@company.com
$ jr issue assign ENG-101 currentUser
$ jr issue assign ENG-101 unassigned
```

## Comments, worklogs, and links

```console
# Comments
$ jr issue comment list ENG-101
$ jr issue comment add ENG-101 'Reproduced on 9.4.2'
$ jr issue comment add ENG-101 '**Fixed**' --body-format markdown

# Worklogs: <key> <time>, in Jira's duration format
$ jr issue worklog list ENG-101
$ jr issue worklog add ENG-101 2h --comment 'Pairing on the retry path'
$ jr issue worklog add ENG-101 '1d 4h' --started 2026-08-01T09:00:00Z
```

The duration is one argument, so quote it if it has a space in it.

A duration is a count of `w`, `d`, `h`, or `m`, largest first. Nothing is
converted between them, because how long a working day is is a site setting.

```console
# Links: <from> <relationship> <to>
$ jr issue link list ENG-101
$ jr issue link add ENG-101 blocks ENG-102
$ jr issue link add ENG-101 'is blocked by' ENG-99
$ jr issue link add ENG-101 'relates to' ENG-102
```

Link wording is customizable per site, so an unknown phrase is refused with
every phrase your site does offer. The relationship is a phrase and not a type
name: `jr issue link add ENG-101 relates ENG-102` is refused with
`AMBIGUOUS_LINK_DIRECTION`, because `Relates` is the type and `relates to` is
the phrase, and a type name says nothing about which way the link runs.
`blocks` works because it is the outward phrase of the `Blocks` type as well as
its name — a coincidence rather than a rule.

```console
# Attachments
$ jr issue attachment list ENG-101
$ jr issue attachment upload ENG-101 ./trace.log
$ jr issue attachment download ENG-101 10042
$ jr issue attachment download ENG-101 10042 --output - > trace.log
```

## Sprints, epics, and boards

```console
$ jr board list
$ jr sprint list --board 42
$ jr sprint list --state active
$ jr sprint get 1001

# Move issues into a sprint, or into an epic
$ jr sprint add 1001 ENG-101 ENG-102 ENG-103
$ jr epic add ENG-1 ENG-101 ENG-102
$ jr epic remove ENG-101

# What is in the active sprint
$ jr issue list --jql 'sprint IN openSprints()' --limit all
```

### Running a sprint end to end

Plan it, fill it, start it, close it. The id you need at every step after the
first is the one `sprint create` reports.

```console
$ jr sprint create "Sprint 14" --board 42 \
    --start 2026-08-17T09:00:00Z --end 2026-08-31T09:00:00Z \
    --goal "Ship the importer"
# → <sprint id="1002" state="future" …>

$ jr sprint add 1002 ENG-101 ENG-102
$ jr sprint start 1002
$ jr sprint close 1002 --yes
```

Dates are RFC 3339, and a bare `2026-08-17` is refused: it names no time and no
zone, and `jr` will not choose one for you.

**The window belongs to the sprint, not to the command.** Because the sprint
above was created with both dates, `sprint start` needs neither flag. A sprint
planned without them wants them at the point it starts:

```console
$ jr sprint create "Sprint 15" --board 42
$ jr sprint start 1003 --start 2026-08-31T09:00:00Z --end 2026-09-14T09:00:00Z
```

Scripting it, where the id is the only thing you need out of the create. A
record in TSV is a field/value table rather than a row, so the id is a lookup by
name and not a column:

```console
$ id=$(jr sprint create "Sprint 14" --board 42 --format tsv |
       awk -F'\t' '$1 == "@id" { print $2 }')
$ jr sprint start "$id" --start 2026-08-17T09:00:00Z --end 2026-08-31T09:00:00Z
```

`sprint close` is the one to be careful with: every unfinished issue returns to
the backlog, no API reopens a closed sprint, and it needs `--yes` and a build
carrying the `admin` tag. Creating and starting need only `write`, so an agent
build can plan an iteration and begin one and cannot end one.

**`sprint = <id>` is not a test of current membership.** Jira's Sprint field
records every sprint an issue has ever been in, so a finished iteration still
answers with its whole contents:

```console
# Everything that was ever in it, including what was carried out at close
$ jr issue list --jql 'sprint = 1002' --limit all
```

Set a default board on your context so you stop passing `--board`:

```console
$ jr context edit work --board 42
```

## Custom fields

Find the field first. Names are what you see in the UI; ids are what the API
wants, and `jr` accepts either:

```console
$ jr field list
$ jr field list | grep -i story
```

Then ask for it. `--field` **adds** to the default columns rather than replacing
them, and what you ask for reaches the output:

```console
$ jr issue list --field 'Story Points'
$ jr issue list --field customfield_10042 --field Sprint
$ jr issue get ENG-101 --field 'Story Points'
```

A name that matches nothing is refused locally, with the near misses and their
ids — rather than sent for Jira to reject opaquely. A field the server did not
return comes back present and empty, so "no value" stays distinguishable from
"you asked for something that does not exist".

**It is not only for custom fields.** The default columns are five, and an issue
carries more than that, so `--field` is also how you widen a listing with the
ones already being fetched:

```console
$ jr issue list --field created --field reporter --field priority
$ jr issue list --field labels        # a list flattens into one cell, comma-joined
```

Naming a field that is already a column — `summary`, `status`, `assignee`,
`updated` — does nothing, because it is already there. Everything else gets a
column, headed the way the document names it: `--field issuetype` is a `type`
column, `--field fixVersions` a `fix-versions` one.

If your team has fields that belong on every issue, store them on the context:

```console
$ jr context edit work --field 'Story Points' --field Team

$ jr issue get ENG-250            # both included
$ jr issue list --field Sprint    # both, plus Sprint
$ jr issue list --no-context-fields --field Sprint   # just Sprint, this once
```

Two things to know before storing a set: every read then resolves it, so a field
renamed in Jira fails `issue get` and `issue list` until the context is
corrected; and every read consults the field catalogue, which is one request per
cache TTL rather than per command, but is not free on a cold cache.

### Writing one

On a write `--field` takes `id=value` and sets the field. It is the same word
in both places and the `=` is what separates the senses: `--field 'Story Points'`
on a read asks for a column, `--field 'Story Points=5'` on a write sets a value.
A bare name on a write is refused rather than read as the other one.

```console
$ jr issue edit ENG-101 --field 'Story Points=5'
$ jr issue create --type Story --summary Retry --field customfield_10140=5
```

The value is typed from your site's own catalogue, so a number field refuses a
value that is not a number before anything is sent:

```console
$ jr issue edit ENG-101 --field 'Story Points=about five'
FIELD_NOT_A_NUMBER: customfield_10140 (Story Points) takes a number
```

Repeat one id to build an array. Nothing is split on commas, because a comma is
a character a value may contain:

```console
$ jr issue edit ENG-101 --field components=api --field components=transport
```

**Some fields have no type worth guessing at.** Jira reports Epic Link, Rank,
Team, Parent Link, and most plugin fields as type `any`, which says nothing
about what to send. Those are refused by name, and `--field-json` sends the
value exactly as written:

```console
$ jr issue edit ENG-101 --field 'Epic Link=ENG-42'
FIELD_TYPE_UNSUPPORTED: customfield_11350 (Epic Link) has type "any"
  detail: Jira calls it com.pyxis.greenhopper.jira:gh-epic-link
  remedy: send the value as written: --field-json customfield_11350='"..."'

$ jr issue edit ENG-101 --field-json customfield_11350='"ENG-42"'
```

The quoting is JSON quoting inside shell quoting, so a string carries its own
double quotes. `jr field list --format json` shows both the type and the type
key, which is what tells two `any` fields apart.

Everything else a write does still applies. `--dry-run` prints the body,
`--if-unchanged` still refuses a stale write, and a field a typed flag already
owns is refused naming that flag:

```console
$ jr issue edit ENG-101 --field 'Story Points=5' --dry-run
$ jr issue edit ENG-101 --field 'Story Points=5' --if-unchanged "$precondition"
$ jr issue edit ENG-101 --field summary=hello
FIELD_HAS_A_FLAG: summary is written by --summary, not by --field
```

## Exporting and piping

TSV is the default for lists precisely so this works:

> **`--limit all` needs a filter.** With no project and no other filter it is
> refused (`UNCONSTRAINED_QUERY`, exit 2), because it would page until it had
> every issue in every project your credential can see. A context project counts
> as a filter, which is why these examples work. To genuinely mean the whole
> instance, say so: `--all-projects`.

```console
# Straight to a file
$ jr issue list --limit all > issues.tsv

# Pick columns
$ jr issue list --limit all | cut -f1,5

# Skip the header
$ jr issue list --limit all | tail -n +2

# Line it up for reading
$ jr issue list | column -t -s $'\t'

# Count by status
$ jr issue list --limit all | tail -n +2 | cut -f2 | sort | uniq -c | sort -rn
```

To CSV, if something downstream insists on it:

```console
$ jr issue list --limit all | tr '\t' ',' > issues.csv
```

That is only safe because TSV values never contain a raw tab, newline, or
backslash — they are escaped. It is _not_ safe if a summary contains a comma, so
prefer a real converter for anything that matters:

```console
$ jr issue list --limit all --format json | jq -r '.issues[] | [.key, .summary] | @csv'
```

With `jq`:

```console
$ jr issue list --format json | jq -r '.issues[].key'
$ jr issue get ENG-101 --format json | jq -r '.issue.status.text'
$ jr issue list --format json --limit all |
      jq '[.issues[] | select(.status.text == "Done")] | length'
```

Watch the envelope while you are there — it tells you whether you got
everything:

```console
$ jr issue list --limit 50 --format json | jq '{count, complete}'
{
  "count": 50,
  "complete": false
}
```

## Bulk operations

There is no bulk verb, deliberately: a half-completed bulk edit is worse than a
loop you can see. Loop in the shell, and let the exit codes stop you.

```console
# Transition everything matching a query
$ jr issue list --jql 'status = "In Review" AND updated < -30d' \
      --limit all | tail -n +2 | cut -f1 |
  while read -r key; do
      jr issue move "$key" 'Close Issue' --resolution 'Won'"'"'t Do' || break
  done
```

Rehearse it first. Swap the mutating command for `--dry-run`, or just look:

```console
$ jr issue list --jql 'status = "In Review" AND updated < -30d' --limit all
```

A safer shape, which stops on the first failure and tells you where it got to:

```console
$ set -e
$ jr issue list --label needs-triage --limit all | tail -n +2 | cut -f1 |
  while read -r key; do
      echo "labelling $key" >&2
      jr issue edit "$key" --add-label triaged
  done
```

Note `--limit all`. Without it you get the first 50 and **exit 3**, and a bulk
loop that silently processed the first page would be the exact failure this tool
exists to prevent.

### Resuming a truncated run

If a run is cut short, the stderr warning carries a token:

```console
$ jr issue list --limit 50 --project ENG
# exit 3, and stderr names a next-page-token

$ jr issue list --limit 50 --project ENG --page-token <token>
```

The token is opaque and carries the deployment it was minted against, so one
from Cloud is refused against Data Center rather than read as offset zero.

## Working safely

**Preview every write.** Every mutating command takes `--dry-run` and prints the
exact request it would send, body included:

```console
$ jr issue delete ENG-101 --dry-run
```

**Destructive commands require `--yes`.** Deleting is not something you do by
typo:

```console
$ jr issue delete ENG-101 --yes
```

**Use a read-only context for anything exploratory:**

```console
$ jr context create audit --site your-company.atlassian.net --readonly
$ jr context use audit
```

Or for one invocation:

```console
$ jr issue list --readonly
$ JIRA_READONLY=1 ./some-script.sh
```

Read-only is a one-way latch within an invocation: nothing a command does turns
it off, and `JIRA_READONLY=0` will not clear a context that was created
read-only. Make the context writable again deliberately:

```console
$ jr context edit audit --unset readonly
```

**Or use a binary that cannot write at all.** `make build-reader` produces a
`jr` with no mutating commands compiled in — not refused at runtime, absent:

```console
$ bin/jr-reader schema --limit all | grep -c .
```

**Bound the damage of a runaway query:**

```console
$ jr issue list --limit all --max-requests 20
```

Exceeding the budget truncates and exits 3 rather than running forever.

## Scripting and CI

Set everything from the environment; no login step, no config file:

```bash
export JIRA_SITE=your-company.atlassian.net
export JIRA_EMAIL=ci@company.com
export JIRA_API_TOKEN="$JIRA_TOKEN"   # from your secret store
export JIRA_FORMAT=json
export JIRA_READONLY=1                # if the job only reads
```

On Data Center, leave `JIRA_EMAIL` unset. A token with a user beside it is sent
as HTTP Basic and a token on its own is sent as a bearer token, and a default
Jira 11 refuses Basic outright — `AUTH_SCHEME_REFUSED`, exit 4, on the first
request, before the job does anything. The variable that makes it work is the
one you leave out.

Then branch on the exit code, not on the output:

```bash
#!/usr/bin/env bash
set -euo pipefail

if ! out=$(jr issue list --jql 'labels = release-blocker' --limit all); then
    status=$?
    case $status in
        3) echo "truncated; widen --limit" >&2 ;;
        4) echo "credentials rejected" >&2 ;;
        *) echo "jr failed with $status" >&2 ;;
    esac
    exit $status
fi

count=$(printf '%s' "$out" | jq '.count')
[ "$count" -eq 0 ] || { echo "$count release blockers open" >&2; exit 1; }
```

`set -e` alone is not enough here: exit 3 means _success, truncated_, and you
have to decide whether that is acceptable for your job rather than have it
inherited as a generic failure.

Use the smallest build that does the job. `make build-ci` is query-only and the
smallest of the four; a token it holds cannot write even if the script is wrong.

### Handing the credential to another tool

`jr auth token --header` prints one line, `Authorization: <value>`, so the
obvious capture is the correct one:

```bash
curl -H "$(jr auth token --header)" \
     "https://$JIRA_SITE/rest/api/3/myself"
```

Do not capture `jr auth token` without the flag and use it as a header. Every
other output of that command is a *document*, which is the whole design, and a
document interpolated into a header is a malformed header. curl answers status
000, which is what it also answers for a network failure, so the mistake does
not look like one.

If you want the value on its own rather than the whole header, take the field:

```bash
header=$(jr auth token --format tsv | awk -F'\t' '$1=="authorization"{print $2}')
```

The command reveals a secret on purpose and is the only one that does. It goes
to stdout like any other result, so redirect it deliberately and keep it out of
anything that logs its arguments.

## Agents and MCP

### Installing the skill

`jr skill` prints the instructions an agent needs that the command declarations
cannot carry: which of five near-synonymous filters answers the question asked,
what to do with each exit code, and that a refusal is information rather than an
obstacle to route around. It writes Markdown to stdout and nothing else.

```console
$ jr skill                 # the skill
$ jr skill workflows       # one reference: workflows, failures, or gotchas
```

**Symlink it, if you have the repository.** `skills/jr/` is generated by
`make skill` and checked in, so it is already current:

```console
$ mkdir -p ~/.claude/skills
$ ln -s "$PWD/skills/jr" ~/.claude/skills/jr        # personal, every project
$ ln -s "$PWD/skills/jr" .claude/skills/jr          # this project only
```

A symlink means `git pull` updates the skill. A copy does not.

**Copy it, if you only have the binary.** The skill is four documents, so copy
them in a loop rather than one at a time:

```console
$ dest=~/.claude/skills/jr
$ mkdir -p "$dest/references"
$ jr skill > "$dest/SKILL.md"
$ for ref in workflows failures gotchas; do
      jr skill "$ref" > "$dest/references/$ref.md"
  done
```

**Generate it from the binary the agent will actually run.** The skill carries a
command inventory taken from the registry of whichever binary printed it, so it
describes that build and not the project:

```console
$ bin/jr-reader skill | grep 'commands, profile'
41 commands, profile `reader`, tags `mcp`.
```

A reader build's skill lists no mutating commands, because a reader build holds
none. Handing a model the full build's skill and the reader binary would
describe 21 commands it cannot call.

**For a host that reads `AGENTS.md`** rather than a skill directory, the same
document works as-is; it is Markdown with a YAML header that other hosts ignore:

```console
$ jr skill >> AGENTS.md
```

`make skill` regenerates the checked-in copy, and
`internal/lint/skill_test.go` fails when it is stale, so the version in the
repository cannot drift from the binary.

**The skill is not reachable over MCP.** It writes Markdown to stdout with no
envelope, which is the stream `jr mcp serve` speaks on, so it is not offered as
a tool and calling it by name is refused rather than corrupting the session.
Install it alongside the server by one of the means above; a client that speaks
only MCP has no way to fetch it until it is served as a resource.

### MCP

Every command in the build is also an MCP tool, generated from the same registry,
so the tool list cannot drift from the CLI:

```console
$ jr mcp serve
```

Point an MCP client at that. Two properties worth knowing:

- **The tool list is the truth about the binary.** A reader build advertises no
  mutating tools because it does not contain any. An agent introspecting the
  server sees what is there, not a list of tools that will refuse.
- **There is no exit code in a tool reply**, so a truncated result carries its
  warning in the content instead. It is never reported as complete.

Serving a reader or agent build is the straightforward way to give a model Jira
access it cannot misuse:

```console
$ make build-reader && bin/jr-reader mcp serve
```

For feeding results to a model directly, TSV is much the cheapest: a hundred
issues cost about 2,930 tokens as TSV against 12,755 as XML. The numbers and the
method are in
[output-contract.md](output-contract.md#what-the-defaults-cost).
