# Troubleshooting

When something fails, `jr` tells you what happened in a structured error with a
machine-stable `code` and, where there is anything useful to say, a `remedy`.
**Read that first.** This page is for when it is not enough, or when you want to
know why the tool is refusing something it could have guessed at.

- [How to read an error](#how-to-read-an-error)
- [Start with `jr doctor`](#start-with-jr-doctor)
- [Nothing works yet](#nothing-works-yet)
- [Authentication](#authentication)
- ["It returned nothing"](#it-returned-nothing)
- [Refusals that look like bugs](#refusals-that-look-like-bugs)
- [Writes](#writes)
- [When it is the network or the server](#when-it-is-the-network-or-the-server)
- [Digging deeper](#digging-deeper)

## How to read an error

```console
$ jr issue get foo
<?xml version="1.0" encoding="UTF-8"?>
<error v="1">
  <code>INVALID_KEY</code>
  <message>...</message>
  <remedy>...</remedy>
  <retryable>false</retryable>
  <exit>2</exit>
  <exit-name>USAGE</exit-name>
</error>
```

Four fields do most of the work:

- **`code`** is stable forever. Branch on it in a script; never parse `message`.
- **`remedy`** is what to do about it, when there is a single sensible answer.
- **`detail`** carries the candidates: the near-miss field names, the flag or
  the verb you probably meant, the transitions the issue actually offers, the
  phrases your site uses for links. A refusal that lists alternatives is usually
  a typo you can fix by reading it, and one that lists none is saying it has
  nothing close rather than that it did not look.
- **`retryable`** is `true` only for `RATE_LIMIT` and `REMOTE`. Everything else
  will fail identically if you run it again.

Errors go to **stderr**, in whatever `--format` you asked for, and a failing
command writes **nothing at all** to stdout. So this is always safe:

```console
$ jr issue list --project ENG > out.tsv 2> err.xml
```

## Start with `jr doctor`

One error can only describe the request that failed, and the interesting
failures are the ones where the first request fails and the reason is three
layers below it. A 401 from a Data Center under a context path reads exactly
like a 401 from an expired token, a 401 from a proxy that stripped the header,
and a 401 from a site URL that lost its `/jira` and reached a different
application.

`jr doctor` takes those layers apart. It reports eight checks, in the order they
stack, and every one of them whether it passed or not:

```console
$ jr doctor --format tsv
field   value
@status failed
@checks 8
@failed 1
@skipped        0
config/@status  ok
...
account/@status failed
account/@code   UNAUTHORIZED
```

It needs no credential and no reachable site, and it is in every build,
including `reader` and `ci`. **It exits 0 whenever it ran, whatever it found**,
so branch on the document rather than on `$?`: a non-zero exit means the
diagnostic itself could not run, never that it found something.

Three verdicts:

- **`ok`** means that layer is working. Every check reports one, including the
  ones with nothing to say, because a diagnostic that printed only problems
  could not tell a healthy configuration from a check that never ran.
- **`failed`** means that layer is broken, and the check carries the same
  `code`, `detail`, and `remedy` an ordinary command would have failed with.
  Every code on this page answers a `failed` check too.
- **`skipped`** means the check could not run, and it names the check it was
  waiting on. Eight checks in a stack fail together, so read down the document
  and act on the **first** `failed`: the ones below it are usually the same
  cause counted again.

Worth knowing what each check can tell you that no other command will:

- **site** reports the URL a request actually resolves to, built by the same
  code that builds a request. A base and an endpoint that disagree about a
  context path is the failure that produces an unexplainable 401.
- **transport** reports the proxy in effect, which nobody configured here and
  which comes from `HTTPS_PROXY` and `NO_PROXY`, along with the CA bundle and
  client certificate and where each came from. Once a response has arrived it
  also reports what the connection did: `tls-version`, and `verified-against`
  as `system` or `bundle`. A bundle that is configured and never needed is a
  setting doing nothing, and the summary says so.
- **deployment** says whether the answer came from the probe, from its cache, or
  from `--api-version`. A stale cached answer and a fresh one are different
  claims. `--refresh` forces the probe.
- **account** is the only check that proves the credential works. The probe
  answers anonymously on most instances, so a reachable site says nothing about
  the token.

### `CLOCK_SKEW` — this machine and the site disagree about the time

The one code only `jr doctor` produces. It compares this machine's clock against
the site's own and fails the check at a minute or more apart.

A minute, because a minute is the finest bound JQL has: no operator this tool
can send bisects one. So a machine a minute out asks Jira for a different window
than the one it computed, and the damage is silent. `--since` cursors, `created
>= "-1d"`, and every worklog and changelog window are all evaluated against a
clock that is not this one.

Fix the clock, with NTP or whatever your platform uses, or run from a host whose
clock is right. Nothing in `jr` can compensate for it: a tool that corrected for
skew would be guessing at an offset that changes while it runs.

## Nothing works yet

### `NO_SITE` — no Jira site configured

You have not told `jr` where your Jira is. If you are setting up for the first
time, this is the one to run, because it fixes the credential at the same time:

```console
$ printf '%s' "$TOKEN" | jr auth login \
      --site your-company.atlassian.net \
      --email you@company.com --token-stdin
```

That stores the credential _and_ creates your first context. Setting only a site
gets you past this error and straight into `NO_CREDENTIALS`, which is why the
remedy leads with logging in.

If you already have a credential — `JIRA_API_TOKEN` exported, say — you just
need a site:

```console
$ export JIRA_SITE=your-company.atlassian.net
$ jr issue list --site your-company.atlassian.net
$ jr context create work --site your-company.atlassian.net
```

If a credential is in the store but no context points at it, the error says so
and names the host it already knows about.

### `NO_CREDENTIALS` — nothing to authenticate with

The `detail` lists every place that was looked in. The order is environment,
then the credential store, then `.netrc`.

```console
$ printf '%s' "$TOKEN" | jr auth login --site your-company.atlassian.net \
      --email you@company.com --token-stdin
```

### `INCOMPLETE_CREDENTIAL` — half a credential

Almost always Cloud with a token and no email. On Cloud a token is paired with
the account email; on Data Center a personal access token stands alone.

```console
$ export JIRA_EMAIL=you@company.com          # Cloud
$ export JIRA_AUTH_SCHEME=bearer             # Data Center PAT, if it is being
                                             # read as basic auth
```

### `STORE_PERMISSIONS` — the credential file is readable by others

The store holds a secret and is refused if anyone but you can read it. The
remedy names the exact path:

```console
$ chmod 600 <path from the error>
```

### `UNKNOWN_DEPLOYMENT` — the probe did not recognise the server

`jr` detects Cloud vs Data Center from `/rest/api/2/serverInfo` rather than
letting you declare it, because a wrong guess sends v3 requests to a v2 server
or uses offset pagination against a cursor API. An unrecognised answer is
refused rather than assumed.

Usually this means the URL is not actually a Jira root — a reverse proxy, or a
missing context path. Check what the site resolves to:

```console
$ jr context show
$ curl -s https://<your-site>/rest/api/2/serverInfo | head
```

If your Jira lives under a path, include it in the site:
`--site jira.company.com/jira`.

As a last resort you can skip the probe, though you are then asserting something
the tool would rather verify:

```console
$ jr issue list --api-version 2     # Data Center
$ jr issue list --api-version 3     # Cloud
```

## Authentication

### Exit 4 — credentials missing, invalid, or expired

Find out what `jr` would actually use, and where it came from:

```console
$ jr auth status
```

Then prove the credential works. This is the cheapest real check, because the
deployment probe answers anonymously on most instances and so proves nothing
about your token:

```console
$ jr user me
```

Common causes, in the order they are worth checking:

- **The token expired.** Cloud API tokens can be given an expiry; Data Center
  personal access tokens usually have one by default.
- **Email and token belong to different accounts.** On Cloud they must match.
- **A stale environment variable is winning.** The environment is tried _before_
  the credential store, deliberately, so CI can override a disk config. That
  also means a forgotten `export JIRA_API_TOKEN=` in your shell silently beats
  the credential you just stored — and because the environment is not tied to a
  site, it is sent to whichever one you point at. The failure says so itself
  now: the detail names where the credential came from, and the remedy names
  the precedence. `jr auth status` answers the same question before you hit it.
- **`--email` against Data Center.** Pairing an email with a personal access
  token makes `jr` send Basic, and Data Center wants a bearer token. The token
  is fine; the scheme is wrong. `auth login` now says so when it happens, rather
  than reporting a credential Jira rejected and suggesting it expired.
- **Basic auth is disabled.** Jira Data Center 11 turns HTTP Basic off by
  default and refuses the first request of every run. It has its own code —
  `AUTH_SCHEME_REFUSED`, below — and its own fix, a personal access token.
  `--scheme bearer` names the scheme for a token you already hold; it cannot
  turn a password into one.

### `AUTH_SCHEME_REFUSED` — the instance does not take that kind of credential

```
code: AUTH_SCHEME_REFUSED
detail: GET https://jira.example.com/rest/api/2/serverInfo;
        Basic Authentication has been disabled on this instance.
```

**Jira Data Center 11 disables HTTP Basic by default.** A username and password,
a `.netrc` entry, or `JIRA_API_TOKEN` alongside `JIRA_EMAIL` all authenticate as
Basic, and a default 11.x instance refuses every one of them on the first
request — the deployment probe — before anything else is tried.

Nothing about the account is wrong and no permission change helps. Create a
personal access token instead:

1. In Jira: your avatar → **Profile** → **Personal Access Tokens** → **Create
   token**.
2. Store it as a bearer credential:

```console
$ printf '%s' "$TOKEN" | jr auth login --site https://jira.example.com --token-stdin
```

A token supplied on its own is used as a bearer token; the scheme only needs
naming when a user is also present. `jr auth status` shows which one is in play.

### Exit 6 — authenticated but not allowed

The credential is fine; the account cannot do that. Worth knowing:
`--watcher` and `--voter` are allowed **for yourself only** unless your account
can manage watchers or view voters, which is why they are deliberately not part
of `--involving`.

## "It returned nothing"

### Exit 0 with an empty result

Usually that is an honest "nothing matched" — `jr` goes to some trouble to make
sure an empty result is never a silent failure. A user-valued filter that
resolved to nobody is refused (`UNKNOWN_USER`, exit 2) rather than sent,
precisely because `assignee = "Ada Lovelace"` against Cloud matches nothing and
returns successfully.

**Check the scope first, because the answer is complete within it.** A query is
combined with the context's project, so a `--jql` naming an issue in another
project cannot match:

```console
$ jr context show                  # what project am I scoped to?
$ jr jql explain --jql 'your query here'
```

`jql explain` prints the query that would actually be sent, including the
project scope it would be combined with, which is the fact the result document
used to leave out. Two things now say it without being asked:

- Every structured result carries the scope in its envelope:
  `<result kind="issue.list" … project="ENG">`. Absent means no scope was
  applied. TSV has no envelope, so use `--format json` when you need this in a
  pipeline.
- A `--jql` naming a project the scope excludes produces `SCOPE_MISMATCH` on
  stderr at exit 0. See below.

If neither fires and the result is still empty, the query is wrong, not the
lookup. Remember that repeated flags OR together and different flags AND
together: `--status Done --type Bug` means Done **and** a Bug.

### `SCOPE_MISMATCH` — the query names a project the scope excludes

```console
$ jr issue list --jql 'key = OPS-208'      # context project is ENG
```

```xml
<warning v="1">
  <code>SCOPE_MISMATCH</code>
  <message>--jql selects project OPS, and the scope is ENG, so those rows cannot be returned; use --all-projects or --project to widen it</message>
</warning>
```

Exit 0, and the rows that did match still come back. The query is legal: `jr`
ANDs your fragment with the context's project, so `project = ENG AND (key =
OPS-208)` is a query that cannot match by construction. Three ways out:

- `--all-projects`, on the commands that have it, lifts the scope entirely.
- `--project OPS` names the scope you meant for one invocation.
- `jr context edit <name> --project OPS`, or `--unset project`, changes it for
  good.

It fires only on positive selection. `project != OPS` names a project without
asking for it and stays quiet, and so does a project key inside a string value,
because the fragment is read as tokens rather than searched.

One case it gets wrong: a **renamed** project key still resolves on the server,
so `key = OLD-1` against a scope of `NEW` warns and the query would in fact have
matched. Closing that would cost a request on every `--jql`, so it is a warning
you can ignore rather than a refusal you cannot.

### `JQL_NOT_UNDERSTOOD`: Jira does not understand the query

```console
$ jr issue list --jql 'nosuchfield = 1'
code: JQL_NOT_UNDERSTOOD
message: Jira does not understand this query, and would answer it with no rows
detail: Field 'nosuchfield' does not exist or you do not have permission to view it.
```

The `detail` is Jira's own message, unedited, because it names the field or the
value and carries the position.

This refusal exists because Cloud's search endpoint answers a query it knows is
meaningless with HTTP 200 and no rows. Without the check, a typo inside `--jql`
came back complete, exit 0, and empty, which reads exactly like an honest
"nothing matched". So the query is checked before the command runs.

**A warning refuses too.** `--jql 'assignee = "nobody-xyz"'` is valid JQL naming
a user who does not exist; Jira reports that as a warning and would answer with
no rows. It is refused for the same reason `--assignee nobody-xyz` is refused,
so both spellings of the same mistake behave the same way.

If the query is right and you want the answer anyway, the check is the only
thing between you and it: fix the value, or ask a question that does not name
something the site does not have. To see the verdict on its own:

```console
$ jr jql validate --jql 'your query here'
```

One thing this does not catch, on Cloud only: the operand of a `WAS`,
`CHANGED TO`, or `CHANGED FROM` predicate. `--jql 'status was "NoSuchStatus"'`
is checked by neither of Cloud's endpoints and still returns an empty result
there. Data Center refuses it.

### `UNKNOWN_LABEL`, a warning rather than an error

Labels are the one filter value nothing on the server validates. A status or an
issue type that does not exist comes back as a 400 naming it; a label that does
not exist is a legal query, and the answer is an honest, empty, complete result
with exit 0. That is indistinguishable from asking for a real label on a day
nothing carries it, so `jr` checks the label and says so:

```console
$ jr issue list --label regresion
<warning v="1">
  <code>UNKNOWN_LABEL</code>
  <message>no issue on this site carries the label "regresion"</message>
</warning>
key	status	assignee	updated	summary
```

Nothing is refused: the query runs, the exit is 0, and stdout is unchanged.

Two things it does not mean. **Silence is not a promise that the label will
match here**, because the check is site-wide, and a label alive in a project
this query does not cover produces no warning and no rows. And **a site that cannot answer
the check gets no warning either**: where the route is absent, the site reports
no labels at all, or the request fails, `jr` says nothing rather than guessing.
If you want to know why a label query is empty and no warning appeared, look
at the scope first: `--project`, and any `--status` or date filter alongside
it.

### Exit 3 — a truncated result

**Not an error.** The rows you got are real; there were more. The default limit
is 50.

```console
$ jr issue list --project ENG --limit all      # everything
$ jr issue list --project ENG --limit 500      # a bigger bound
$ jr issue list --page-token <token from the warning>   # resume
```

If a script inherited a failure from this, it is checking `$?` without treating
3 as a success — see [recipes.md](recipes.md#scripting-and-ci).

### `UNCONSTRAINED_QUERY` — `--limit all` with no filter

Refused, because it would page until it had every issue in every project your
credential can see, which on a Data Center instance with a long-lived account is
a great deal more than people expect. The default bound of 50 is what makes an
unfiltered query harmless, so only the pairing is refused.

```console
$ jr issue list --limit all --project ENG      # scope it
$ jr issue list --limit all --all-projects     # or mean it
```

### "Today" is not my today

Nothing is broken and the result is complete. Jira evaluates every date you send
in the timezone on **the Jira account's profile** — not UTC, and not your
machine's clock, which the server never learns.

```console
$ jr user me
# <timezone>America/Chicago</timezone>
```

If that is not your zone, `--created-after startOfDay()` does not mean your
midnight. For an account on `America/Chicago` in August, `startOfDay()` is
05:00Z, so "created today" quietly starts five hours late and still reports
`complete="true"` at exit 0.

The same applies to a bare literal: `--created-after "2026-08-10 00:00"` is
midnight _there_, not here.

To mean your own day, convert it and send an absolute literal:

```console
# midnight where you are, expressed in the account's zone
$ start=$(TZ=Pacific/Auckland date -d "today 00:00" +%s)
$ jr issue list --created-after "$(TZ=America/Chicago date -d @$start '+%Y-%m-%d %H:%M')"
```

`startOfWeek()` and friends are passed through rather than computed here on
purpose — they carry Jira's own notion of when a week begins, which a converted
instant does not. The exception is `jr issue activity`, which compares dates
itself and therefore refuses a function rather than passing it through: see
`UNBOUNDABLE_DATE` below.

## Refusals that look like bugs

Most of these are the tool declining to guess. The pattern is the same
throughout: if a request cannot be honored exactly, it fails.

### `UNKNOWN_FIELD` / `AMBIGUOUS_FIELD`

The name is resolved against your site's field catalogue before the request is
built. `detail` lists near misses with their ids. For an ambiguous name, pass
the id:

```console
$ jr field list | grep -i points
$ jr issue list --field customfield_10042
```

If a field was renamed in Jira and you stored it on a context, every read fails
until the context is corrected:

```console
$ jr context edit work --unset field
```

### `UNKNOWN_TRANSITION`

The issue does not offer that move **right now** — transitions depend on the
current status. `detail` lists the ones it does offer.

```console
$ jr meta transitions ENG-101
```

Note the name is the _transition's_ name, which is often not the destination
status: `Start Progress` rather than `In Progress`.

### `UNKNOWN_USER` / `AMBIGUOUS_USER`

`detail` lists the near misses with their ids, and flags whether an account is
inactive or is an app rather than a person. Pass an id or an email to be
unambiguous, or `currentUser` for yourself. `jr user me` prints your own id.

### `UNKNOWN_LINK_TYPE` / `AMBIGUOUS_LINK_DIRECTION`

Link wording is customizable per site, so `detail` lists every phrase yours
offers. `"Blocks"` on its own is ambiguous because it reads in both directions;
say `blocks` or `is blocked by`.

### `INVALID_KEY` / `INVALID_PROJECT_KEY`

A malformed issue key is rejected before any request at all, because a 404 for a
typo reads like a missing issue. Keys look like `ENG-123`.

```console
$ jr issue get foo
# exit 2: "foo" is not an issue key
```

A project key is checked the same way, on `project get`, `project components`,
`project versions`, and `project statuses`. A key starts with a letter and
continues with letters, digits, or underscores — which is wider than Jira's own
default of two or more uppercase letters, deliberately: a site can widen the
pattern, and refusing a key some site genuinely uses would be worse than the
round trip the check saves.

```console
$ jr project get ../etc
# exit 2: "../etc" is not a project key
```

This holds against an unreachable site and on a cold cache. It did not always:
the key was parsed inside the client, past the deployment probe, so a typo came
back as `NETWORK` at exit 9 and advertised itself as retryable. The same is true
of `--page-size` and `--page-token`, and of a board, sprint, epic, or comment id.

A `--page-token` minted against the other deployment is the one paging refusal
that still costs a request, because which server issued it is not a question the
token can answer by itself.

### `INVALID_ENCODING` / `UNRENDERABLE_VALUE`

Text that is not valid UTF-8. `INVALID_ENCODING` (exit 2) is something _you_
supplied and can correct. `UNRENDERABLE_VALUE` (exit 1) came back from Jira and
names the field; the value holds a character no output format can carry, and it
is refused rather than replaced with U+FFFD, which would put something in your
output that nobody wrote.

One bad value fails the whole command, so a single comment holding one stray
byte can make every query that reaches back far enough exit 1. `detail` names
the record it is in, which is the record to go and correct in Jira:

```console
$ jr issue comment list ENG-101 > out.tsv
<?xml version="1.0" encoding="UTF-8"?>
<error v="1">
  <code>UNRENDERABLE_VALUE</code>
  <message>the text of issue.comment.list/comments/comment/body holds a character no output format can carry</message>
  <detail>U+001B at byte 5; in comment id=10234</detail>
  <remedy>this is what Jira returned; the field has to be corrected there. 2 rows of this collection reached stdout before it failed: TSV streams, so a row is bytes the moment it is written. What is there is the answer up to the failure and not a complete one.</remedy>
  <retryable>false</retryable>
  <exit>1</exit>
  <exit-name>ERROR</exit-name>
</error>
```

Note what that `remedy` says about `out.tsv`. Under `tsv` the rows before the
refused one are already on stdout, because a TSV collection streams, and there
is no envelope to mark them incomplete: the exit code is the only thing that
says so. The other three formats buffer until the last page lands, so a refusal
there leaves stdout empty. Either way the fix is the same, and it is in Jira
rather than here.

### `SPRINTS_REFUSED`

Jira refused a sprint listing for that board. Usually the board is a kanban
board, and only scrum boards have sprints. `detail` keeps the server's own
message.

### `SPRINT_HAS_NO_DATES` / `INVALID_SPRINT_DATE`

`jr sprint start` refused because the sprint has no window and you did not pass
one. Jira will not run a sprint that has no dates; this says so without spending
the round trip.

### `UNBOUNDABLE_DATE`

`jr issue activity --since startOfWeek()`, or any other date function. Every
other date flag is a clause the server evaluates, so a function is passed
through. This one is not: comments are not searchable in JQL, so `issue
activity` matches most of its events in this process, and `--since` has to bound
the events and not only the issues the query found.

Computing `startOfWeek()` here means choosing which day a week starts on, and
that choice is Jira's. So the combination is refused. Use an absolute date or a
relative offset:

```console
$ jr issue activity --since -7d
$ jr issue activity --since 2026-08-10
```

The version that shipped before did neither: the function reached the query, the
events were compared against nothing, and the feed reported `complete="true"` at
exit 0 with events from outside the window in it.

The same refusal reaches `jr issue changes` for the same reason: its window is
compared here, so a boundary only Jira can compute cannot be one of its ends.

### `INVALID_SINCE_TOKEN`

`jr issue changes --since <token>` was given something that is not a cursor this
tool issued, or one issued against the other deployment. A cursor names a window
of one site's changelog, and replaying it against a different site would report
somebody else's history as this one's.

Pass the `next-since-token` from the previous answer, which is in the envelope
of any structured format:

```console
$ jr issue changes --since -1h --format json | jq -r '."next-since-token"'
```

To start over, pass a date or an offset instead. `--since -1h` and
`--since 2026-08-10` are both first polls.

### `SINCE_AFTER_NOW`

The cursor names a time the site's clock has not reached. It is not rounded away,
because the two ways to get here are both worth knowing about: the token came
from a different site, or the site's clock moved backwards. Either way this tool
cannot say what happened in between, so it says that instead of guessing.

### `NO_SERVER_TIME`

`jr issue changes` bounds every poll with the site's own clock, read from
`/rest/api/2/serverInfo`, because the timestamps it compares were written by the
server. A client running behind the site would otherwise claim to have reported
through an instant the site had not reached, and anything written in between
would never be reported and never asked for again. This says the site answered
that endpoint without a `serverTime`. There is no fallback to the local clock,
which would be the guess the whole design exists to avoid.

### `INVALID_CA_BUNDLE`

The site is behind a TLS-intercepting proxy and the bundle you pointed at cannot
be read, or holds no certificate this tool recognises.

```console
$ jr --ca-bundle /etc/ssl/corporate-root.pem issue list
$ jr context edit onprem --ca-bundle /etc/ssl/corporate-root.pem
$ export JIRA_CA_BUNDLE=/etc/ssl/corporate-root.pem
```

The file has to be PEM, the `-----BEGIN CERTIFICATE-----` form, rather than DER
or a key. A bundle that cannot be read fails the invocation rather than falling
back to the system roots, because that fallback produces the same verification
error you were trying to fix with nothing saying the file was never read.

**The bundle is added to the system roots, not swapped for them.** Trusting one
more CA and trusting only one CA are different things, and the second breaks
every public site on the same context.

### `INCOMPLETE_CLIENT_CERT` / `INVALID_CLIENT_CERT`

A client certificate is two files that have to agree. The first says you passed
one and not the other; the second says the key does not belong to the
certificate, or one of them is not PEM.

```console
$ jr context edit onprem --client-cert /etc/ssl/jira.pem --client-key /etc/ssl/jira.key
```

Both are refused here rather than at the handshake, where the error names the
server and nothing names the file.

### There is no `--insecure`

Deliberately, and it is not coming. A private CA is trusted with `--ca-bundle`
and mutual TLS is answered with a client certificate; those are the real needs
behind corporate TLS. Turning verification off sends your credential to whoever
answers the connection, and none of this tool's other guarantees — no credential
in a log, no off-site URL, no absolute request path — mean anything on a
connection nobody verified.

If a certificate error is blocking you, the thing to find is the internal root
your organisation signs with. It is usually already on the machine: on Linux,
somewhere under `/etc/ssl/certs` or `/etc/pki`; on macOS, exportable from
Keychain Access.

### `NO_ACCOUNT_TIMEZONE` / `UNKNOWN_ACCOUNT_TIMEZONE`

An absolute `--since` on `jr issue activity` is a wall clock, and reading it
means knowing the account's zone, which is one request to `/myself`. These say
the site did not report a zone, or reported one that is not a zone.

`jr issue changes` needs the zone on **every** poll, not only for an absolute
date: it mints an absolute JQL bound from its window so that every page of one
walk is filtered by the same instant, and a literal is read in the account's
zone.

Both deployments send one, so this is unusual. Reading the literal as UTC anyway
would put the bound five or nine hours off with nothing in the output to say so.
A relative offset needs no zone and always works:

```console
$ jr issue activity --since -7d
```

```console
$ jr sprint start 5
# SPRINT_HAS_NO_DATES: sprint 5 has no dates, and Jira will not run a sprint
# without them

$ jr sprint start 5 --start 2026-08-17T09:00:00Z --end 2026-08-31T09:00:00Z
```

**A sprint that already has both dates needs neither flag**, because the window
belongs to the sprint and not to the request. If you passed dates to
`jr sprint create`, `jr sprint start <id>` on its own is enough. The refusal
names only the half that is missing, so being asked for `--end` alone means the
start date is already set.

`INVALID_SPRINT_DATE` means the value is not RFC 3339. A bare `2026-08-17` names
no time and no zone, and `jr` will not pick one for you — that would decide when
your iteration begins. An offset is fine and is normalized to UTC:
`2026-08-17T11:00:00+02:00` is sent, and reported, as `2026-08-17T09:00:00Z`.

## Writes

### `READ_ONLY` (exit 10)

Something turned read-only mode on: `--readonly`, `JIRA_READONLY`, or a context
created with `--readonly`. It is a one-way latch within an invocation —
`JIRA_READONLY=0` does not clear it, and omitting the flag does not either.

```console
$ jr context show                        # what is actually in effect
$ jr context use work                    # switch to a writable context
$ jr context edit audit --unset readonly # or make this one writable
```

If the command is missing entirely rather than refused, you are running a reader
or ci build, which does not contain mutating commands at all:

```console
$ jr version          # profile= tells you which build this is
$ jr schema --limit all | grep create
```

### `CONFIRMATION_REQUIRED` (exit 10)

A destructive command needs `--yes`. It is not raised for `--dry-run`, because a
preview is what you look at in order to decide.

### `IDEMPOTENCY_KEY_REUSED` (exit 7)

The key was already used for a _different_ operation. Answering one with the
other's result would be worse than refusing. Use a new key.

### `STALE_WRITE` (exit 7)

Somebody else changed the issue between the read your `--if-unchanged`
precondition came from and this write, so nothing was sent. This is not a
failure to fix; it is the flag doing its job.

The remedy is a loop, not a retry. Read the issue again, decide again against
what it says now — the change you were about to make may no longer be the one
you want — and write with the precondition from the second read:

```console
$ jr issue get ENG-101 --format json | jq -r '.issue.precondition'
eyJkIjoiY2xvdWQiLCJrIjoiRU5HLTEwMSIsInUiOiIyMDI2LTA4LTA0VDExOjQxOjU1LjAwOFoifQ
$ jr issue edit ENG-101 --priority High --if-unchanged eyJkIjoiY2xvdWQi...
```

Retrying with the _same_ precondition will fail the same way every time, which
is the point: it describes a version of the issue that no longer exists.

`updated` moves for any change, including a comment somebody added, so this can
refuse a write that would not actually have collided. That is deliberate — the
alternative is deciding which changes count, and getting that wrong loses an
edit silently.

### `INVALID_PRECONDITION` (exit 2)

The value passed to `--if-unchanged` is not one this tool issued: not a token at
all, one describing a different issue, or one minted against your other site.
It comes from the `precondition` attribute of `jr issue get`, and nowhere else —
it is deliberately opaque, so there is nothing to assemble by hand.

Refused rather than compared, because comparing a value from somewhere else
would report "the issue changed", which is a claim about your issue that nobody
checked.

### Setting a field `jr` has no flag for

`issue create` and `issue edit` name a handful of fields directly. Everything
else, which on most instances means every custom field, goes through `--field`:

```console
$ jr issue edit ENG-101 --field 'Story Points=5'
$ jr issue create --type Story --summary Retry --field customfield_10140=5
```

The name or the id both work, and the value is typed from your site's own
catalogue: a number field refuses a value that is not a number before anything
is sent. Repeat the flag with one id to build an array; nothing is split on
commas, because a comma is a character a value may contain.

### `FIELD_NOT_KV` (exit 2)

`--field` means two different things and this is the one that says so. On a
write it *sets* a field and takes `id=value`; on `jr issue get` and
`jr issue list` it *selects a column* and takes a bare name. A bare name on a
write is refused rather than read as the other one:

```console
$ jr issue edit ENG-101 --field 'Story Points'          # refused
$ jr issue edit ENG-101 --field 'Story Points=5'        # sets it
$ jr issue get ENG-101 --field 'Story Points'           # shows it
```

### `FIELD_HAS_A_FLAG` (exit 2)

The field has a typed flag of its own, and that flag is the answer:
`--summary`, `--description`, `--priority`, `--label`, `--assignee`,
`--parent`, `--type`. Naming one field twice has no single right answer, so it
is refused rather than resolved to whichever the implementation applied last.

### `FIELD_TYPE_UNSUPPORTED` (exit 2)

The field exists and `jr` will not guess at its schema type. This is most often
type `any`, which is what Jira reports for Epic Link, Rank, Team, Parent Link,
and anything a plugin added. `detail` carries Jira's own type key where the
site reported one, and the remedy is to say exactly what to send:

```console
$ jr issue edit ENG-101 --field-json customfield_11350='"ENG-42"'
```

`--field-json` sends the value verbatim, so the quoting is JSON quoting: a
string needs its own quotes inside the shell's.

Run `jr field list --format json` to see a field's type and type key.

### `DUPLICATE_FIELD` (exit 2)

One field was named twice. On an array field repeating `--field` is how you
build the array, so this only fires when the field is not an array, or when
`--field` and `--field-json` both name it.

### A write failed and you do not know whether it happened

`jr` never replays a non-idempotent request after an upstream error — a POST
that got a 503 may have been processed before the failure, and retrying is how
one `issue create` becomes two issues. So a failed create was probably not
applied, but "probably" is not good enough to act on: check.

```console
$ jr issue list --creator currentUser --created-after -10m
```

To make retries safe in the first place, hold a key:

```console
$ jr issue create --type Task --summary Ship --idempotency-key release-42
```

## When it is the network or the server

`NO_SUCH_ENDPOINT`, `NETWORK`, `TIMEOUT`, `MALFORMED_SERVER_INFO`,
`UNKNOWN_DEPLOYMENT`, and `OFF_SITE_URL` all carry **where the site came from**
in their `detail` — `the site came from context "work"`, `from --site`, or
`from JIRA_SITE` — because three things can supply a site and which one won is
otherwise invisible.

### Exit 8 (`RATE_LIMIT`) and exit 9 (`REMOTE`)

These are the two retryable failures, and `jr` has already retried before you
see them. Retries count against `--max-requests`, because a retry is another
request from the server's side.

```console
$ jr issue list --retries 5
$ jr issue list --limit all --max-requests 200   # bound a long run
```

### Cached site metadata is stale

The deployment probe and the field catalogue are cached on disk with a TTL. If
your Jira was upgraded, or a field was just created:

```console
$ jr issue list --refresh
```

## Digging deeper

### See the requests

```console
$ jr issue list --debug
```

Traces every HTTP request to stderr. **Credentials are redacted inside the
transport**, when the trace event is built rather than when it is printed, so a
debug trace cannot leak a token — including in URLs, which are scrubbed of
userinfo and credential-shaped query parameters.

The trace also says what `jr` decided and why, which is usually what you are
looking for when the request count is not what you expected:

```
[http] retry attempt=1 GET https://jira.example.com/rest/api/3/search status=429 wait=30s asked=1h0m0s request-id=c1db85 reason="rate limited"
[http] failure attempt=1 POST https://jira.example.com/rest/api/3/issue status=503 request-id=ab2ca8 reason="non-idempotent request not replayed after an upstream error"
```

`asked=` appears only when it differs from `wait=`: a single wait is capped at
30 seconds, so a server asking for longer gets retried sooner than it said. If
you are still being throttled, that gap is why. Raising `--retries` will not
help; wait out the server's interval instead.

A `failure` line carrying a reason and a single attempt is `jr` refusing to
replay a request that may already have been processed. That is deliberate, and
re-running it by hand is a decision only you can make.

### See what a command would do

```console
$ jr issue move ENG-101 Done --dry-run     # the exact request, sent nowhere
$ jr issue list --describe                 # the command's full schema
$ jr context show                          # the settings this invocation resolves to
```

`--dry-run` prints the real request — method, path, query, and body — rather
than a paraphrase, so it is something you can read and compare against Jira's
API docs.

### Check the tool's own view of itself

```console
$ jr version                  # release, profile, and compiled-in tags
$ jr schema --limit all       # every command this build contains
$ jr contract                 # every output kind and its schema version
```

If a command you expected is missing, it was compiled out. See
[build-profiles.md](build-profiles.md).

### Still stuck

The error `code` is the thing to search for.
[output-contract.md](output-contract.md#errors) explains the codes where the
message alone is not enough — the resolution failures, the refusals a server
sends, the idempotency ledger — with the exit each one carries, and every exit
code a given command can produce is listed in its entry in
[commands.md](commands.md). A code in neither is not undocumented: the error
itself carries `exit`, `exit-name`, and a `remedy`, and a code is stable
forever, so a script can branch on one no page here names.
