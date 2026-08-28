---
name: jr
description: Query and modify Jira from the command line with `jr`, a client whose output is a versioned, parseable contract. Use this whenever the task touches Jira in any form - finding, reading, filing, editing, transitioning, or reporting on issues, sprints, epics, boards, projects, comments, worklogs, attachments, or JQL - and whenever a repository contains a `jr` binary or a `.jr` context. Use it even when the user does not say "jira" but names an issue key like ENG-4821, asks what someone is working on, asks what shipped in a sprint, or asks for a ticket to be filed or moved. Read this before the first `jr` invocation, because the failure protocol and the cost rules are what separate a correct answer from a plausible one.
---

# jr

`jr` is a client for Jira, built for programs first. Its output is a versioned
contract, and its central promise is inverted from most tools:

**When `jr` cannot honor a request exactly, it fails. It never approximates,
never truncates silently, and never guesses.**

Everything below follows from that. The tool is doing work on your behalf when
it refuses, and treating a refusal as an obstacle to route around is how you
produce a confident wrong answer.

## A refusal is a finding

This is the one rule that matters most, because the reflex it overrides is
strong. When `jr` refuses, the refusal is the most reliable information you
have. Report it or act on what it tells you. Do not reach for a way past it.

| You see | Do not | Do |
| --- | --- | --- |
| `UNCONSTRAINED_QUERY` | Add `--jql 'project is not empty'` to satisfy the filter check | Scope it (`--project`, `--status`, a date bound) or pass `--all-projects` if a whole-instance sweep is genuinely intended |
| Exit 3, `RESULT_TRUNCATED` | Report the rows you got as the answer | Resume with `--page-token`, or say the result is partial and how much you saw |
| `UNKNOWN_USER`, `UNKNOWN_FIELD`, `UNKNOWN_TRANSITION` | Guess another spelling and retry | Read `detail`. It lists the real candidates with their ids. Pass an id |
| `INVALID_USAGE`, `UNKNOWN_COMMAND` | Re-read the help output and guess again | Read `detail`. A mistyped flag, verb, or command name carries the near misses; an empty `detail` means nothing is close, so check `jr schema` rather than trying another spelling |
| `READ_ONLY` (exit 10) | Look for another command that writes | Stop. The caller chose a read-only context. Tell them |
| `CONFIRMATION_REQUIRED` (exit 10) | Append `--yes` and rerun | `--yes` is the user's decision. Ask for it, once, for the specific action |
| `STALE_WRITE` (exit 7) | Drop `--if-unchanged` and force it | Re-read, recompute the change against what is there now, retry |

Adding `--yes` on your own initiative is the one that does real damage. It exists
so a human authorizes destruction. An agent that supplies it reflexively has
removed the only gate.

## Orient before you invoke

Never guess a flag. The binary describes itself, offline, in about a kilobyte:

```console
jr schema              # every command: name, summary, mutating, destructive, kind
jr schema issue.list   # one command in full: flags, types, args, exits, prose
jr contract            # every output kind, its schema version, and its emitters
```

`jr schema` describes **this build**, not the project. Features are compiled in
or out, so a reader build contains no mutating commands at all and lists none.
What `jr schema` shows is what exists. If a command is absent, it is absent from
the binary, and no flag will summon it.

`jr schema <command>` costs one local call and settles every question about
flags, defaults, and exit codes. Reach for it instead of `--help` parsing or
recall, and instead of guessing that a flag you know from another CLI exists
here.

**A command has two sets of flags and `jr schema <command>` reports both.**
`<flags>` is what the command declares. `<global-flags>` is what it inherits
from the root, and each of those carries an `affects` attribute saying what it
reaches **on that command**:

| `affects` | What it means for you |
| --- | --- |
| `result` | It narrows what you get back, and nothing in the result says so. `--project` is one. Read these before you trust a count. |
| `provenance` | It decides which Jira answers. The envelope's `site` attribute tells you which one did. |
| `invocation` | It changes how the command runs or prints, not what the answer is. |

`affects="result"` is the one to check. A scoped query is complete within a
frame `complete="true"` says nothing about: `jr issue list --jql 'key = OPS-1'`
against a context whose project is `ENG` returns zero rows, `complete="true"`,
and exit 0, which is the same answer as "nothing matched".

**The envelope names the frame.** Every structured format carries the scope the
answer was actually computed over, beside the site:

```xml
<result kind="issue.list" v="7" site="https://jira.example" project="ENG">
```

Absent means the command asked for no scope, which is what `--all-projects`
produces and what a command like `version` always produces. **TSV has no
envelope**, so a pipeline that needs to know its scope asks for `--format json`,
exactly as it does for `site`. `--all-projects` lifts the scope where the
command has it, and `jr context show` says what the scope currently is.

**A `--jql` naming a project outside the scope warns rather than going quiet.**
`SCOPE_MISMATCH` on stderr, exit 0, rows unchanged. It fires only on positive
selection, so `project != OPS` says nothing, and it reads the fragment as
tokens, so a key inside a string value is a value.

An empty value does not lift the scope. `--project ""` is refused as
`EMPTY_SCOPE`, because it used to fall back to the context and produce exactly
the empty success above.

## Reading what comes back

- **stdout is data only.** Never a warning, never a progress line. A failed
  command writes nothing at all to stdout, with one exception to handle: a TSV
  collection streams, so a command that fails partway has already written the
  rows before the failure. A non-zero exit means the rows you have are not the
  answer, however many of them arrived.
- **stderr is structured.** Errors and truncation warnings, in the format you
  asked for.
- **Default formats**: TSV for lists, XML for single records. `--format` takes
  `tsv|xml|json|yaml|markdown`.
- **`complete="true"` is the only proof you have everything.** It appears in the
  envelope of every format that has one. TSV has no envelope, which is why exit 3
  and the stderr warning exist. Check one of the three, every time.

An error is always shaped the same way and always carries a machine-stable
`code`, plus `retryable` and `exit`:

```xml
<error v="1">
  <code>JQL_SYNTAX</code>
  <message>Unclosed quote in --jql at position 34</message>
  <remedy>Quote the whole expression in single quotes, or escape inner double quotes.</remedy>
  <retryable>false</retryable>
  <exit>2</exit>
  <exit-name>USAGE</exit-name>
</error>
```

Branch on `code`, never on `message`. `retryable` is `true` only for
`RATE_LIMIT` and `REMOTE`; retrying anything else burns budget on a verdict that
will not change.

## Exit codes are your control flow

| Exit | Name | What to do |
| --- | --- | --- |
| 0 | `OK` | Result is complete. Use it |
| 2 | `USAGE` | You built the command wrong. Read `remedy`, fix, retry once |
| 3 | `PARTIAL` | Truncated. Resume with `--page-token` or report it as partial |
| 4 | `AUTH` | Credentials missing or expired. Stop and tell the user |
| 5 | `NOT_FOUND` | The thing does not exist. Do not search for a near match unless asked |
| 6 | `PERMISSION` | Authenticated but not allowed. Stop and report |
| 7 | `CONFLICT` | Stale write or invalid transition. Re-read, then retry |
| 8, 9 | `RATE_LIMIT`, `REMOTE` | Transient. `retryable` is true. Back off and retry |
| 10 | `BLOCKED` | Local policy refused. Stop. Do not work around it |

Codes never change meaning. New conditions get new codes.

## Cost

Feeding results to a model is the common case and format choice dominates it.

- **TSV is roughly 4x cheaper than XML** on tabular results. Use it for lists you
  are going to summarize or count.
- **XML earns its cost on prose.** Descriptions and comments are mixed content
  full of newlines, quotes, and fenced code. XML carries them as text; JSON turns
  them into an escape-sequence minefield you pay for twice, once in tokens and
  once in unescaping.
- `--limit` defaults to 50 and takes `all`. `--max-requests N` bounds an
  invocation's HTTP calls; exceeding it exits 3 with a resume token rather than
  running for an hour.
- Ask for the columns you need. Fetching fields you will not read costs tokens on
  the way out and requests on the way in.

## Writing

Mutations are gated on purpose, and the gates are cheap to satisfy honestly.

1. **`--dry-run` first.** It prints the exact HTTP requests the command would
   send, method, path, query, and body. It sends nothing. For any multi-issue or
   irreversible change, run it and read it before the real invocation.
2. **`--idempotency-key <k>` on creates.** A retried `issue create` without one
   is how a single request becomes two issues. With one, the repeat returns the
   original result and says `replayed="true"`.
3. **`--if-unchanged <precondition>` on edits you based on a read.** It refuses a
   changed issue with `STALE_WRITE` at exit 7, having sent nothing. It is a
   read-compare with a one-round-trip window, not an atomic swap, and it says so
   in its own output.
4. **`--field id=value` for anything without a flag of its own.** Story points,
   acceptance criteria, and every other custom field are reachable:
   `jr issue edit ENG-1 --field 'Story Points=5'`. The id or the name both work
   and the value is typed from the site's catalogue, so a bad value is refused
   before anything is sent. Where the type is one `jr` will not guess at, and
   Jira reports Epic Link and most plugin fields as `any`, the refusal names
   `--field-json`, which sends the value verbatim:
   `--field-json customfield_11350='"ENG-42"'`. Do not leave `jr` for `curl` to
   set a field; you lose the dry run, the precondition, and the validation.

## Picking the right command

Several commands answer questions that sound identical in English and are not.
Getting this wrong returns a complete, empty, exit-0 result, which is
indistinguishable from an honest "nothing matched".

| The question | The flag |
| --- | --- |
| Who owns it now | `--assignee` |
| Who filed it | `--reporter` |
| Who touched it at all | `--involving` |
| Who used to own it | `--was-assignee` |
| Who logged time on it | `--worklog-author` |
| Who changed its status | `--changed-by` (defaults to the status field; `--changed-field` picks another) |

`--updated-after` means *somebody* updated it, not that the named person did.
Pairing `--assignee me --updated-after -7d` answers "assigned to me and touched
by anyone this week", which is usually not the question asked.

**A filter never orders anything.** Without `--sort`, results come back by issue
key descending, which on a busy project looks enough like creation order to be
mistaken for "most recent". If you want recency, say so: `--sort updated --order
desc`.

**Repeated flags OR, different flags AND, and nothing splits on commas.**
`--status 'To Do,In Progress'` asks for one status with a comma in its name.
Repeat the flag instead.

## What this build contains

Generated from the registry of the binary that printed this, so it is the truth
about that binary and not about the project. A reader build lists no mutating
commands because it contains none.

67 commands, profile `full`, tags `prompt, render, mcp, write, admin`.

| Command | | Does |
| --- | --- | --- |
| `auth login` |  | Store a credential for a site |
| `auth logout` | `D` | Remove a stored credential |
| `auth status` |  | Report which credential a site would use, and where it comes from |
| `auth token` |  | Print the credential for a site, for use in another tool |
| `board get` |  | Fetch one board |
| `board list` |  | List the boards this credential can see |
| `completion` |  | Print a shell completion script |
| `context create` |  | Create or replace a named site and project pairing |
| `context delete` | `D` | Delete a context |
| `context edit` |  | Change one setting of a context, leaving the rest alone |
| `context list` |  | List every configured context |
| `context show` |  | Show one context, or the effective settings for this invocation |
| `context use` |  | Select the context every command uses by default |
| `contract` |  | Dump the machine-readable output contract for every kind |
| `doctor` |  | Explain, layer by layer, why this tool will not work here |
| `epic add` | `M` | Move issues into an epic |
| `epic get` |  | Fetch one epic |
| `epic list` |  | List a board's epics |
| `epic remove` | `M` | Take issues out of their epic |
| `field list` |  | List every field this site has |
| `issue activity` |  | List what happened across issues, as events rather than rows |
| `issue assign` | `M` | Set or clear an issue's assignee |
| `issue attachment download` |  | Download an attachment |
| `issue attachment list` |  | List the files attached to an issue |
| `issue attachment upload` | `M` | Attach a file to an issue |
| `issue changes` |  | Report what changed since the last poll, and where to resume |
| `issue clone` | `M` | Create a copy of an issue |
| `issue comment add` | `M` | Add a comment to an issue |
| `issue comment delete` | `M D` | Delete a comment |
| `issue comment edit` | `M` | Replace the text of a comment |
| `issue comment list` |  | List an issue's comments |
| `issue create` | `M` | Create an issue |
| `issue delete` | `M D` | Delete an issue |
| `issue edit` | `M` | Change fields on an issue |
| `issue get` |  | Fetch one issue in full |
| `issue history` |  | List what changed on an issue, and who changed it |
| `issue link add` | `M` | Link two issues |
| `issue link list` |  | List the issues linked to one issue |
| `issue link remove` | `M D` | Remove a link between two issues |
| `issue list` |  | List issues matching a query |
| `issue move` | `M` | Transition an issue to another status |
| `issue watch` | `M` | Start or stop watching an issue |
| `issue worklog add` | `M` | Log work against an issue |
| `issue worklog delete` | `M D` | Delete a worklog entry |
| `issue worklog list` |  | List the work logged against an issue |
| `jql explain` |  | Show the query this tool would send |
| `jql validate` |  | Ask Jira whether a query parses, without running it |
| `mcp serve` |  | Serve this build's commands as MCP tools over stdio |
| `meta createmeta` |  | List the fields a new issue of one type requires |
| `meta transitions` |  | List the transitions available on an issue right now |
| `project components` |  | List a project's components |
| `project get` |  | Fetch one project |
| `project list` |  | List the projects this credential can see |
| `project statuses` |  | List the statuses each issue type can be in |
| `project versions` |  | List a project's versions |
| `schema` |  | Describe every command this build contains |
| `skill` |  | Print the agent skill for this build |
| `sprint add` | `M` | Move issues into a sprint |
| `sprint close` | `M D` | Close an active sprint |
| `sprint create` | `M` | Create a future sprint on a board |
| `sprint get` |  | Fetch one sprint |
| `sprint list` |  | List a board's sprints |
| `sprint start` | `M` | Start a future sprint |
| `user get` |  | Fetch one user by id |
| `user list` |  | Search for users |
| `user me` |  | Show who this credential authenticates as |
| `version` |  | Print the build identity and its compiled-in capabilities |

`M` marks a command that changes Jira and is refused in read-only mode. `D`
marks one that requires `--yes`. They are independent: `auth logout` is `D` and
not `M`, because it removes a stored credential and never touches Jira.

## Deeper references

Load these when the task reaches them, not before.

- `references/workflows.md` - multi-step procedures: paging a large result set to
  completion, bulk transitions, running a sprint, resuming an interrupted run.
- `references/failures.md` - every error code, what causes it, and the recovery.
  Read it when you hit a code this file does not list.
- `references/gotchas.md` - the traps that return a plausible wrong answer rather
  than an error: timezone boundaries, ordering, key sorting, sprint membership.

Each is also printable from the binary, which is where they come from:

```console
jr skill                 # this file
jr skill workflows       # one reference
```

For the tool's own documentation, the repository carries `docs/recipes.md`,
`docs/troubleshooting.md`, and the generated `docs/commands.md`. Prefer
`jr schema` over all three for questions about flags: it cannot drift.
