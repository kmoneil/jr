# Troubleshooting

When something fails, `jr` tells you what happened in a structured error with a
machine-stable `code` and, where there is anything useful to say, a `remedy`.
**Read that first.** This page is for when it is not enough, or when you want to
know why the tool is refusing something it could have guessed at.

- [How to read an error](#how-to-read-an-error)
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
- **`detail`** carries the candidates — the near-miss field names, the
  transitions the issue actually offers, the phrases your site uses for links.
  A refusal that lists alternatives is usually a typo you can fix by reading it.
- **`retryable`** is `true` only for `RATE_LIMIT` and `REMOTE`. Everything else
  will fail identically if you run it again.

Errors go to **stderr**, in whatever `--format` you asked for, and a failing
command writes **nothing at all** to stdout. So this is always safe:

```console
$ jr issue list --project ENG > out.tsv 2> err.xml
```

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
  the credential you just stored. `jr auth status` names the source.
- **Basic auth is disabled.** Many Data Center instances require a personal
  access token and reject username/password. Use `--scheme bearer`.

### Exit 6 — authenticated but not allowed

The credential is fine; the account cannot do that. Worth knowing:
`--watcher` and `--voter` are allowed **for yourself only** unless your account
can manage watchers or view voters, which is why they are deliberately not part
of `--involving`.

## "It returned nothing"

### Exit 0 with an empty result

That is an honest "nothing matched" — `jr` goes to some trouble to make sure an
empty result is never a silent failure. A user-valued filter that resolved to
nobody is refused (`UNKNOWN_USER`, exit 2) rather than sent, precisely because
`assignee = "Ada Lovelace"` against Cloud matches nothing and returns
successfully.

So if a query is empty and you expected rows, the query is wrong, not the
lookup. Check it:

```console
$ jr jql explain --jql 'your query here'
```

Remember that repeated flags OR together and different flags AND together:
`--status Done --type Bug` means Done **and** a Bug.

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

### `INVALID_KEY`

A malformed issue key is rejected before any request at all, because a 404 for a
typo reads like a missing issue. Keys look like `ENG-123`.

```console
$ jr issue get foo
# exit 2: "foo" is not an issue key
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

### `SPRINTS_REFUSED`

Jira refused a sprint listing for that board. Usually the board is a kanban
board, and only scrum boards have sprints. `detail` keeps the server's own
message.

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

Retrying with the *same* precondition will fail the same way every time, which
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

The error `code` is the thing to search for. Every code is listed with its exit
status and meaning in
[output-contract.md](output-contract.md#errors), and every exit code a given
command can produce is listed in its entry in [commands.md](commands.md).
