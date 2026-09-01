# Failures

Every error carries a machine-stable `code`. Branch on the code, never on the
message. `retryable` is `true` only for `RATE_LIMIT` and `REMOTE`.

The pattern throughout: **if a request cannot be honored exactly, it fails.**
Most of what looks like a bug is the tool declining to guess.

## Contents

- [`jr doctor` when the layer is not obvious](#jr-doctor-when-the-layer-is-not-obvious)
- [Nothing is configured yet](#nothing-is-configured-yet)
- [Authentication](#authentication)
- [Empty and truncated results](#empty-and-truncated-results)
- [Resolution refusals](#resolution-refusals)
- [Write refusals](#write-refusals)
- [Network and server](#network-and-server)
- [When a write failed and you cannot tell whether it landed](#when-a-write-failed-and-you-cannot-tell-whether-it-landed)

## `jr doctor` when the layer is not obvious

One error describes the request that failed. When the reason is below it, run
`jr doctor`: eight checks, in the order the layers stack, each `ok`, `failed`,
or `skipped`.

| Check | Answers |
| --- | --- |
| `config` | which context, from which file, and whether read-only is latched |
| `credential` | which credential a request would carry, from where, and under which scheme |
| `site` | the URL a request resolves to, and the context path it is under |
| `transport` | the proxy in effect, the CA bundle, the client certificate, and once a response has arrived the negotiated TLS version and whether the chain needed the bundle (`verified-against`: `system` or `bundle`) |
| `deployment` | Cloud or Data Center, and whether that came from the probe, its cache, or `--api-version` |
| `clock` | this machine against the site's, failing at a minute apart |
| `account` | who the credential is, which is the only proof the credential works |
| `limits` | whatever the site discloses about throttling |

**It exits 0 whenever it ran, whatever it found.** Read the document, not `$?`.
A non-zero exit means the diagnostic could not run at all.

A `failed` check carries the same `code`, `detail`, and `remedy` an ordinary
command would have failed with, so every code below answers one. A `skipped`
check names the check it was waiting on: read down and act on the **first**
`failed`, because the rest are usually the same cause counted again.

It needs no credential and no reachable site, and it is in every build. One code
is its own, `CLOCK_SKEW`: this machine and the site are a minute or more apart,
which silently moves every `--since`, every relative date, and every window this
tool computes, because a minute is the finest bound JQL has.

## Nothing is configured yet

| Code | Exit | Cause and recovery |
| --- | --- | --- |
| `NO_SITE` | 2 | No site configured. The `remedy` names all three ways: `jr auth login --site <host>`, `--site`, or `JIRA_SITE` |
| `NO_CREDENTIALS` | 4 | A site is known, nothing authenticates against it |
| `INCOMPLETE_CREDENTIAL` | 4 | Half a credential. Usually an email with no token, or the reverse |
| `STORE_PERMISSIONS` | 4 | The credential file is readable by others. It is refused above mode 0600 |
| `UNKNOWN_DEPLOYMENT` | - | The `serverInfo` probe returned a `deploymentType` neither Cloud nor Data Center. Refused rather than guessed: guessing Cloud sends v3 to a v2 server, and guessing Data Center uses offset pagination against a cursor API |

`jr schema`, `jr contract`, and `jr --describe` all work with no credential and
no network. Use them to orient before authenticating.

## Authentication

**Exit 4** means missing, invalid, or expired credentials. Stop and tell the
user; there is nothing to retry.

`AUTH_SCHEME_REFUSED` is the one worth recognizing on sight. A token with a user
beside it is sent as HTTP Basic; a token alone is sent as a bearer token. A
default Jira 11 Data Center refuses Basic outright, on the first request, before
anything else happens.

On Data Center, leave `JIRA_EMAIL` unset. The variable that makes it work is the
one you leave out.

**Exit 6** (`PERMISSION`) means authenticated but not authorized. Also stop: no
retry and no alternate command will change it.

## Empty and truncated results

**Exit 0 with an empty result is an honest "nothing matched."** `jr` goes to some
trouble to guarantee this. A user-valued filter that resolved to nobody is
refused with `UNKNOWN_USER` at exit 2 rather than sent, precisely because
`assignee = "Ada Lovelace"` against Cloud matches nothing and returns
successfully, which is indistinguishable from a real answer.

So an unexpectedly empty result means the query is wrong, not the lookup. Check
what was actually sent:

```console
jr jql explain --jql 'your query here'
```

Remember repeated flags OR and different flags AND: `--status Done --type Bug`
means Done **and** a Bug.

**Exit 3 is not an error.** The rows are real; there were more. See
[workflows.md](workflows.md#resuming-an-interrupted-run).

`UNCONSTRAINED_QUERY` refuses `--limit all` with no filter, because it would page
until it had every issue in every project the credential can see. The default
bound of 50 is what makes an unfiltered query harmless, so only the pairing is
refused. Scope it, or pass `--all-projects` to mean it.

## Resolution refusals

Anything naming something on the server is resolved against the site before the
request is built, never sent for Jira to reject. Every refusal carries the
candidates in `detail`, because an error that only says "unknown" leaves you
reading a catalogue to find your typo.

| Code | Exit | What `detail` gives you |
| --- | --- | --- |
| `UNKNOWN_FIELD` | 2 | Near misses, each with its id |
| `AMBIGUOUS_FIELD` | 2 | Every candidate with its id. Pass the id |
| `UNKNOWN_TRANSITION` | 2 | Every transition the issue offers **right now**, with id and destination |
| `AMBIGUOUS_TRANSITION` | 2 | Both transitions, where two names lead to different statuses |
| `UNKNOWN_ISSUE_TYPE` | 2 | The types the project does offer |
| `UNKNOWN_USER` | 2 | Near misses with ids, or absent when nothing shares a word with what you typed |
| `AMBIGUOUS_USER` | 2 | Every candidate with its id, whether the account is inactive, and whether it is an app rather than a person |
| `UNKNOWN_LINK_TYPE` | 2 | Every link phrase this site offers |
| `AMBIGUOUS_LINK_DIRECTION` | 2 | `"Blocks"` reads both ways. Say `blocks` or `is blocked by` |
| `UNKNOWN_PROJECT` | 5 | The project does not exist, or this credential may not create in it |
| `INVALID_KEY` / `INVALID_PROJECT_KEY` | 2 | A malformed key, rejected before any request, because a 404 for a typo reads like a missing issue |

Read `detail` and pick from it. Do not guess another spelling and retry.

Two specifics worth knowing:

- A transition's name is often **not** the destination status: `Start Progress`
  rather than `In Progress`.
- `UNKNOWN_TRANSITION` lists the whole available set rather than near matches,
  because a move missing from it is far more often blocked from the current
  status than misspelled. Transitions are never cached for the same reason.

## Write refusals

| Code | Exit | Cause and recovery |
| --- | --- | --- |
| `READ_ONLY` | 10 | `--readonly`, `JIRA_READONLY`, or a context created read-only. A one-way latch within an invocation; `JIRA_READONLY=0` does not clear it. Stop and tell the user. Making a context writable again is a deliberate `jr context edit <name> --unset readonly` |
| `CONFIRMATION_REQUIRED` | 10 | A destructive command with no `--yes`. Ask the user for authorization for that specific action. Do not supply it yourself |
| `STALE_WRITE` | 7 | The issue changed since the read your precondition came from. Nothing was sent. Re-read for a fresh precondition, decide again, retry |
| `INVALID_PRECONDITION` | 2 | The `--if-unchanged` value is not one this tool issued, or describes another issue or another site. It comes from the `precondition` attribute of `jr issue get` and nowhere else |
| `IDEMPOTENCY_KEY_REUSED` | 7 | The key was already used for a *different* operation. Answering one with the other's result would be worse than refusing. Use a new key |
| `SPRINT_HAS_NO_DATES` | - | Jira will not run a sprint with no window. The refusal names only the missing half, so being asked for `--end` alone means the start is already set |
| `INVALID_SPRINT_DATE` | - | Not RFC 3339. A bare `2026-08-17` names no time and no zone. An offset is fine and normalizes to UTC |
| `SPRINTS_REFUSED` | - | Usually a kanban board. Only scrum boards have sprints. `detail` keeps the server's own message |

## Network and server

`RATE_LIMIT` (8) and `REMOTE` (9) are the only codes with `retryable` true. `jr`
already retried per `--retries` (default 3) before reporting either, so back off
before trying again rather than looping immediately.

A non-idempotent request is **not** replayed after an upstream error. A POST that
got a 503 may have been processed before the failure, and retrying it is how one
`issue create` becomes two issues. Only a 429, which is a refusal before
processing, or an explicit idempotency key allows a POST retry.

`INVALID_ENCODING` (2) is text you supplied that is not valid UTF-8; correct it.
`UNRENDERABLE_VALUE` (1) came back from Jira and names the field: the value holds
a character no output format can carry, and it is refused rather than replaced
with U+FFFD, which would put something in the output nobody wrote. One such value
fails the whole command, so a single bad comment can deny a whole project's
comment feed. `detail` names the record holding it, as `in comment id=10234`, and
that record is the one to correct in Jira. Under `tsv` the rows before it are
already on stdout and `remedy` says how many; they are the answer up to the
failure and not a complete one.

## When a write failed and you cannot tell whether it landed

Read before you retry. A create with an idempotency key can be retried safely;
one without cannot. Check the current state first:

```console
jr issue get <key>
jr issue list --jql 'summary ~ "..."' --created-after -10m
```

See the requests themselves when you need to know exactly what went out:

```console
jr <command> --debug        # traces HTTP to stderr; credentials are redacted
jr <command> --dry-run      # prints what it would send, and sends nothing
```
