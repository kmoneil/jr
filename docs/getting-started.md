# Getting started

From nothing to your first query. About five minutes, most of it spent creating
an API token.

This guide assumes you have used a terminal and have a Jira account. It assumes
nothing about Jira's API, JQL, or how `jr` is put together.

- [1. Build it](#1-build-it)
- [2. Get a token](#2-get-a-token)
- [3. Log in](#3-log-in)
- [4. Prove it works](#4-prove-it-works)
- [5. Set a default project](#5-set-a-default-project)
- [6. Your first real query](#6-your-first-real-query)
- [7. Read the output](#7-read-the-output)
- [Where to go next](#where-to-go-next)

## 1. Build it

There is no release binary yet, so build from source. You need Go 1.26.

```console
$ git clone https://github.com/kmoneil/jr && cd jr
$ make build
```

That writes `bin/jr`. Put it on your `PATH`, or type `bin/jr` everywhere this
guide says `jr`.

```console
$ jr version
<result kind="version" v="1">
  <version app="jr" release="0.0.0-untagged+960aa0c" profile="full" ...>
    <display>jr 0.0.0-untagged+960aa0c (full; tags=tui,prompt,render,...)</display>
```

If that prints a document naming a release and a profile, you are ready. The
release reads `0.0.0-untagged` until there is a tagged build; that is expected.

> **Which build?** `make build` gives you everything. There are smaller ones —
> `make build-reader` produces a binary that physically cannot change anything
> in Jira, which is a useful thing to hand to a script you are not sure about.
> See [build-profiles.md](build-profiles.md). Start with the full build.

## 2. Get a token

`jr` never asks for your password interactively and never takes a token as a
flag value. You need a token first. Which kind depends on which Jira you have,
and the two are genuinely different:

**Jira Cloud** — your site looks like `your-company.atlassian.net`.

Create an API token at
<https://id.atlassian.com/manage-profile/security/api-tokens>, then copy it. On
Cloud a token is only half a credential: it is paired with the **email address**
of your Atlassian account.

**Jira Data Center or Server** — your site is hosted by your company, something
like `jira.company.com`, possibly with a path like `jira.company.com/jira`.

Create a personal access token from your profile menu → **Personal Access
Tokens** → **Create token**. This exists in Jira 8.14 and later. A personal
access token stands alone: there is no email to pair it with.

Get the token even if you know your password. **Jira Data Center 11 disables
HTTP Basic by default**, so a username and password — and `.netrc`, which is
the same scheme — is refused on the first request any run makes, the deployment
probe, with `AUTH_SCHEME_REFUSED` and exit 4. Nothing about the account is
wrong and no permission change helps. Username and password is for an instance
older than 8.14, which has no personal access tokens to offer, or one that has
Basic switched back on: `--user` rather than `--email` in the next step.

Now get it into this shell without typing it as an argument. Let the shell read
it — `-s` means it does not echo, and what you type at a `read` prompt does not
enter your shell history:

```console
$ printf 'API token: '; read -rs TOKEN; echo
API token:
```

Paste the token, press enter, and nothing appears on screen. That is correct.

The rest of this guide uses `$TOKEN`. If you would rather keep the token in a
file or a password manager, that works too — see [step 3](#3-log-in).

## 3. Log in

Two flags matter: `--site` is your Jira host, and the token arrives on **stdin**.

**Cloud:**

```console
$ printf '%s' "$TOKEN" | jr auth login \
    --site your-company.atlassian.net \
    --email you@company.com \
    --token-stdin
```

**Data Center, with a personal access token:**

```console
$ printf '%s' "$TOKEN" | jr auth login \
    --site jira.company.com \
    --token-stdin
```

**Data Center, with a username and password** — only against an instance that
still accepts it:

```console
$ printf '%s' "$PASSWORD" | jr auth login \
    --site jira.company.com \
    --user your.username \
    --token-stdin
```

If that comes back `AUTH_SCHEME_REFUSED`, exit 4, the instance is a Jira 11 with
HTTP Basic switched off. It fails here rather than three commands later because
`auth login` checks the credential against the site before storing it, which is
what that check is for. Go back and create a personal access token: the same
command with `--user` dropped stores it as a bearer token, because the scheme is
inferred from whether a user was given.

If your Jira lives under a path, include it: `--site jira.company.com/jira`.

When you are done, `unset TOKEN` — the shell has no further use for it, and the
credential is now in the store.

### Other ways to supply the token

`--token-stdin` reads whatever is piped at it, so anything that can print a
token works:

```console
$ pass show jira/token | jr auth login --site ... --token-stdin
$ jr auth login --site ... --token-file ~/.secrets/jira
```

**`jr auth login` never prompts.** If stdin is a terminal it refuses and lists
the alternatives rather than waiting — a command that waits with no prompt and
no output is indistinguishable from a hang, and a headless build has no human to
wait for. That is why the token is read by *the shell* above and piped in, which
gets you the interactive login without the tool ever blocking on input.

Three things happen, and it is worth knowing all three:

1. **The credential is checked before it is stored.** `jr` probes the site and
   fetches your account. A typo in the host, a missing path, or a bad token is
   refused here rather than surfacing three commands later as something that
   looks unrelated. `--no-verify` skips it, for preparing a config offline.
2. **The token goes to the credential store**, a separate file under your state
   directory at mode 0600 — never into `config.toml`, which is meant to be
   hand-edited and kept in a dotfiles repo.
3. **A context is created for you**, if you had none. That is the thing that
   gives the next command somewhere to point.

### Or skip logging in entirely

Set the environment and every command works with no login step:

```console
$ export JIRA_SITE=your-company.atlassian.net
$ export JIRA_API_TOKEN=...
$ export JIRA_EMAIL=you@company.com     # Cloud only
```

This is usually what you want in CI. `jr` also reads `.netrc`, shared with
`curl` and `git`. Sources are tried in order: environment, then the credential
store, then `.netrc` — the environment first so CI can override a disk config
without editing it.

## 4. Prove it works

```console
$ jr user me
```

This is the cheapest command that proves a credential is good, and it prints the
**id every command that takes a user actually wants** — an accountId on Cloud, a
username on Data Center. The two are not interchangeable, and it is the one
thing a token cannot tell you by looking at it.

If it fails, [troubleshooting.md](troubleshooting.md) maps the error codes to
fixes. The error is structured and names a remedy; read it before anything else.

## 5. Set a default project

Contexts are kubectl-style: a named site, with defaults. `auth login` made one
for you. Give it a project so you stop typing `--project` every time:

```console
$ jr context list
name              current  site                              project  board  readonly
your-company      true     https://your-company.atlassian.net

$ jr context edit your-company --project ENG
$ jr context show
```

A project is always a **default, never a requirement** — any command takes
`--project` to override it for one invocation.

You can have as many contexts as you like, which is how you work against two
sites, or against one site in two modes:

```console
$ jr context create audit --site your-company.atlassian.net --readonly
$ jr context use audit         # everything from here is refused if it would write
$ jr context use your-company  # back
```

`--readonly` is a one-way latch **within an invocation**: nothing a command does
turns it off, and `JIRA_READONLY=0` will not clear it. To make a read-only
context writable again, edit the context itself: `jr context edit audit --unset
readonly`.

## 6. Your first real query

```console
$ jr issue list --assignee currentUser
```

`currentUser` is the word that means you, everywhere a user is accepted. You can
also pass a display name, an email, or an account id.

A few more, to get the shape of it:

```console
# What is on my plate right now
$ jr issue list --assignee currentUser --status 'In Progress'

# What I actually touched this week — not the same question
$ jr issue list --involving currentUser --updated-after -7d

# Everything in one project, however many pages that takes
$ jr issue list --project ENG --limit all

# Read one issue
$ jr issue get ENG-101
```

If you know JQL, pass it whole. It is combined with the other filters and always
parenthesized, so an `OR` inside it cannot escape your project scope:

```console
$ jr issue list --jql 'labels IN (retry, transport) AND priority = High'
```

If you do not know JQL, you never need it: the flags cover the common queries.
`jr issue list --help` lists all of them.

## 7. Read the output

### Lists are TSV, records are XML

```console
$ jr issue list --limit 2
key      status       assignee      updated               summary
ENG-101  In Progress  Ada Lovelace  2026-08-04T11:32:07Z  Retry logic drops...
ENG-102  To Do                      2026-08-04T09:00:00Z  Tabs and newlines...
```

Tab-separated, with a header row. It pipes into `cut`, `awk`, and `column`
without ceremony, and it is by far the cheapest format to feed to an LLM.

One issue comes back as XML, because a record is not rectangular — a description
full of newlines and code fences is exactly the mixed content that TSV cannot
hold:

```console
$ jr issue get ENG-101
<result kind="issue.get" v="7">
  <issue key="ENG-101" type="Story" priority="High" project="ENG">
    <summary>Retry logic drops the last error</summary>
    <status category="in-progress">In Progress</status>
```

Every result carries a `kind` and a schema version `v`. If you are writing a
script that parses this, that pair is what you pin against — see
[output-contract.md](output-contract.md).

### Ask for another format any time

```console
$ jr issue list --format json
$ jr issue get ENG-101 --format yaml
$ jr issue list --format xml
```

`tsv`, `xml`, `json`, and `yaml` work on every command. Set `JIRA_FORMAT` to
change the default globally.

There is a fifth, `markdown`, for reading rather than parsing:

```console
$ jr issue get ENG-101 --format markdown
```

It carries no schema version and may change in any release. Do not parse it.

### Exit codes are the part people miss

```console
$ jr issue list --limit 2; echo "exit $?"
...
exit 3
```

**Exit 3 is not an error.** It means the result was cut short — there were more
issues than your limit. The rows on stdout are real and complete as far as they
go, and the warning went to stderr with a token to resume from.

This is the tool's central promise: **a truncated result is never reported as
complete.** If you only check for exit 0, a `--limit 50` that quietly returned
the first 50 of 400 would look identical to a complete answer. Here it does not.

The codes you will meet most:

| Exit | Meaning                                                     |
| ---- | ----------------------------------------------------------- |
| `0`  | Success, and the result is complete                         |
| `2`  | You made a mistake — bad flag, unknown field, malformed key |
| `3`  | Success, but truncated                                      |
| `4`  | Credentials missing, invalid, or expired                    |
| `5`  | The issue, project, or board does not exist                 |
| `6`  | You are authenticated but not allowed to do that            |

The full table is in [output-contract.md](output-contract.md#exit-codes), and
every code that can come back from a command is listed in that command's entry
in [commands.md](commands.md).

### stdout is data, stderr is everything else

Nothing but the result ever reaches stdout — no spinners, no warnings, no
"Fetching…". A command that fails writes **nothing at all** to stdout. So this
is always safe:

```console
$ jr issue list --project ENG --limit all > issues.tsv
```

Either `issues.tsv` holds a complete result and the exit code was 0, or the exit
code told you otherwise.

## Where to go next

- **[recipes.md](recipes.md)** — worked examples for the things people actually
  do: bulk transitions, exports, sprint reports, finding what you worked on.
- **[commands.md](commands.md)** — every command, every flag, generated from the
  same registry that builds the tool.
- **[troubleshooting.md](troubleshooting.md)** — when something fails, start
  here.
- **[output-contract.md](output-contract.md)** — read this before writing a
  script that parses the output.
- `jr <command> --help` — the same reference, offline, always current.

Two habits worth forming early:

**Preview a write before you make it.** Every mutating command takes
`--dry-run`, which prints the exact request it would send and sends nothing:

```console
$ jr issue move ENG-101 Done --dry-run
```

**Check the exit code, not the output.** `jr` is built so that you never have to
read the result to find out whether it is trustworthy.
