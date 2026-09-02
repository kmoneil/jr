# Getting started

From nothing to your first query. About five minutes, most of it spent creating
an API token.

This guide assumes you have used a terminal and have a Jira account. It assumes
nothing about Jira's API, JQL, or how `jr` is put together.

- [1. Get it](#1-get-it)
- [2. Get a token](#2-get-a-token)
- [3. Log in](#3-log-in)
- [4. Prove it works](#4-prove-it-works)
- [5. Set a default project](#5-set-a-default-project)
- [6. Your first real query](#6-your-first-real-query)
- [7. Read the output](#7-read-the-output)
- [Where to go next](#where-to-go-next)

## 1. Get it

```console
$ brew install kmoneil/tap/jr
```

That is the whole step on macOS and Linux, and it brings the shell completions
with it. If you would rather not use Homebrew, download a release archive:

```console
$ gh release download --repo kmoneil/jr --pattern 'jr-full_*_darwin_arm64.tar.gz'
$ tar xzf jr-full_*_darwin_arm64.tar.gz
$ install jr-full_*/jr ~/.local/bin/jr
```

Substitute `linux` or `amd64` as needed, and see
[the README](../README.md#install) for verifying the download.

Fetch it with `gh` or `curl` rather than through a browser. On macOS a browser
marks the file with `com.apple.quarantine`, and Gatekeeper then refuses to run a
binary that is not signed with an Apple Developer ID, which released `jr`
binaries are not. Homebrew and `gh` both sidestep that.
[It will not start](troubleshooting.md#it-will-not-start) has the fix if you have
already hit it.

Or build from source, which needs Go 1.26:

```console
$ git clone https://github.com/kmoneil/jr && cd jr
$ make build
```

That writes `bin/jr`. Put it on your `PATH`, or type `bin/jr` everywhere this
guide says `jr`.

```console
$ jr version
<result kind="version" v="1">
  <version app="jr" release="0.0.0-untagged+2acd8a4" profile="full" ...>
    <display>jr 0.0.0-untagged+2acd8a4 (full; tags=prompt,render,mcp,write,admin)</display>
```

That is what a clone prints: `scripts/version.sh` produces
`0.0.0-untagged+<sha>` when `git describe` has nothing to describe, and a clone
has no tag. An archive downloaded from the releases page names its release
there instead. Either way, if it prints a document naming a version and a
profile, you are ready.

> **Which build?** The full build gives you everything. There are smaller ones —
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

Keep it on the clipboard for a moment; the next step asks for it.

## 3. Log in

One flag matters: `--site` is your Jira host. `jr` asks for the token.

**Cloud:**

```console
$ jr auth login --site your-company.atlassian.net --email you@company.com
API token for your-company.atlassian.net:
```

Paste the token and press enter. Nothing appears on screen — the prompt does not
echo, and what you type at it does not enter your shell history either, which is
why the token is never a flag value.

**Data Center, with a personal access token:**

```console
$ jr auth login --site jira.company.com
API token for jira.company.com:
```

**Data Center, with a username and password** — only against an instance that
still accepts it:

```console
$ jr auth login --site jira.company.com --user your.username
API token for jira.company.com:
```

If that comes back `AUTH_SCHEME_REFUSED`, exit 4, the instance is a Jira 11 with
HTTP Basic switched off. It fails here rather than three commands later because
`auth login` checks the credential against the site before storing it, which is
what that check is for. Go back and create a personal access token: the same
command with `--user` dropped stores it as a bearer token, because the scheme is
inferred from whether a user was given.

If your Jira lives under a path, include it: `--site jira.company.com/jira`.

### Other ways to supply the token

Nothing about the prompt is compulsory, and a script never sees one. `jr` takes
a token from a pipe or a file, and those are what to use anywhere a person is
not sitting there:

```console
$ pass show jira/token | jr auth login --site ... --token-stdin
$ jr auth login --site ... --token-file ~/.secrets/jira
```

Or skip logging in altogether: set `JIRA_API_TOKEN`, plus `JIRA_EMAIL` on Cloud,
and every command uses it with no stored credential at all.

**The agent, reader, and ci builds do not prompt.** They have no interactive
prompt compiled in, so a terminal on stdin is refused with the alternatives
listed rather than waited on — there is nobody there to answer, and a command
waiting with no reader is indistinguishable from a hang. Use a pipe or a file
in those builds.

## 4. Prove it works

```console
$ jr user me
```

This is the cheapest command that proves a credential is good, and it prints the
**id every command that takes a user actually wants** — an accountId on Cloud, a
username on Data Center. The two are not interchangeable, and it is the one
thing a token cannot tell you by looking at it.

If it fails, the error is structured and names a remedy; read it before anything
else. When the remedy is not enough, `jr doctor` takes the whole stack apart and
reports every layer between this binary and an answer: the config file, the
credential, the site URL and its context path, the proxy, the deployment probe,
the clock, and the account. It needs no credential and no reachable site, and it
exits 0 whatever it finds, so read the document rather than `$?`.
[troubleshooting.md](troubleshooting.md) maps the codes either of them produces
to fixes.

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
<result kind="issue.get" v="9" site="https://your-company.atlassian.net">
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
