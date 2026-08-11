# A Data Center to record against

Every `*.datacenter.json` cassette in this tree used to be hand-written. A
hand-written cassette proves a response is *handled*; it cannot prove the
request was *accepted*, and that is where the Data Center bugs were —
`validateQuery` sent as a string where the server takes a boolean, `createmeta`
on a route removed in Jira 9.0, `project list` never expanding the lead. Each
one encoded its author's belief and passed for months.

This directory is the instance that replaces the belief with a recording.

```sh
make build      # the binary the recordings are made with
make dc-up      # container, licence, seed, throwaway profile — about 8 minutes cold
make dc-record  # every cassette in manifest.tsv
make dc-down    # destroy it, including the licence
```

## The licence, which is the part that nearly stopped this

Atlassian's self-serve Data Center trials ended on **30 March 2026**, and Data
Center is end-of-life: no new-customer purchase at any price, everything
read-only on 28 March 2029. The free "Data Center developer licence" is a perk
of being a billing contact on an active *paid* commercial licence. There is no
evaluation key to get.

What still works is the **timebomb licence** Atlassian publishes for running a
Data Center product without the SDK:

<https://developer.atlassian.com/platform/marketplace/timebomb-licenses-for-testing-server-apps/>
→ *Data Center host product licenses* → **10 user Jira Software Data Center
license, expires in 3 hours**.

`licence.py` fetches that page, decodes every key on it, and picks the one that
is a Jira Software Data Center licence with a relative expiry. Three properties
make it a rig rather than a stunt:

- **`P3H` runs from install, not from a date.** The key was minted in 2018 and
  licensed a 2025 build today. A fresh container buys another three hours,
  indefinitely — which is why `dc-down && dc-up` is the answer to an expired
  instance and there is nothing to ration.
- **The embedded `ServerID` does not have to match.** It names somebody else's
  instance and Jira takes it anyway.
- **There is no online activation**, so the rig works with no Atlassian account
  and offline.

**The key is fetched, never committed.** It carries "Do not distribute this to
customers", and a key in a repository is distribution. Fetching also means the
day the page goes away is a loud failure naming the URL rather than a stale
copy failing inside a setup wizard. If the fetch fails, save the key by hand as
`licence.txt` — gitignored — and every script here uses it.

The framing is worth knowing rather than glossing: the page is written for
developers testing Marketplace **apps**, and `jr` is not one, though the section
publishing this key is written for running a Data Center product without the
SDK. The key says not-for-production, which a throwaway local container plainly
is. Nobody at Atlassian has said either way.

## What `make dc-up` does

1. `docker compose up -d` — `atlassian/jira-software` plus Postgres. Jira is
   published on **loopback only**: this instance holds a licence and a personal
   access token, and a Jira with a known admin password reachable from the
   network is a different thing from a local fixture rig.
2. `setup.sh` drives the wizard over HTTP. Three things about it cost an
   afternoon and are worth not re-deriving:
   - the XSRF token rotates on **every** request, so each POST is preceded by a
     GET of the page it posts to and reads `atlassian.xsrf.token` back out of
     the cookie jar;
   - the submit button `next` **and** the hidden `nextStep` must both be sent.
     Without them the step answers `200` and re-renders itself with the values
     echoed back — no error, no redirect, nothing that distinguishes it from
     success except that you are still on the same step;
   - the step is whichever form the server renders, never the URL. `/` keeps
     redirecting to the application-properties step long after it is saved, and
     following that replays finished steps — which matters because the
     admin-account step is not idempotent.
3. `seed.sh` creates project `ENG`, a scrum board, a sprint, five issues across
   four types, a component, a version, a second user, and a personal access
   token. Everything is idempotent.
4. `jr auth login` against a throwaway profile under `profile/`, using the token
   from step 3. Data Center takes it as a bearer token, which is what `jr`
   sends.

**Use fictional identifiers from the first keystroke.** That is the cheap half
of the scrubbing problem: if nothing real ever enters the instance, no mapping
from a real identifier to a fictional one has to exist — and a mapping is
exactly what `internal/lint/scrubpairs_test.go` refuses to let into the
repository. Nothing to scrub beats scrubbing correctly. `ENG` is the key because
it is already the fictional project in these cassettes.

## What `make dc-record` does

`record.sh --all` walks `manifest.tsv`, which is a tab-separated
`group → cassette → command`. `internal/lint/dcmanifest_test.go` reads the same
file and requires that every Data Center recording in the tree is named by it,
so a recording that nobody can reproduce fails the build rather than quietly
rotting the next time a request changes.

`record-transport.sh` is separate because
`internal/transport/testdata/serverinfo.datacenter.json` has to hold exactly
four exchanges — the probe, the account, a 404 for a missing issue, and a POST
that fails on the summary — and no single `jr` invocation produces all four. It
records three invocations and concatenates their interactions. Every exchange is
real; only the assembly is ours.

`JIRA_RECORD_SCRUB` is deliberately never set. Read the residue lines anyway.
`serverInfo` carries Jira's own build SHA in `scmInfo`, which trips the
identifier check and is Atlassian's, not yours.

## Recording against another version

`JIRA_VERSION` in `.env`. The default is 10.4, which is post-9.0 and therefore
records the *replacement* createmeta endpoint. The two other passes worth doing:

- **9.12** — the last line before the createmeta removal and the last with an
  embedded H2 database. Records the shape a customer on 9.x LTS still serves.
- **11.3** — the current line. **Refuses HTTP Basic entirely**
  (`403 {"message":"Basic Authentication has been disabled on this instance."}`)
  so a personal access token is the only way in, and adds `maxResultWindow` to
  search responses.

Changing the version means `make dc-down` first: the volume holds the old
cluster.

## The context path is a second, separate run

`CONTEXT_PATH` is empty by default and should stay that way for the cassettes
here, because the recorder stores `req.URL.Path` verbatim: a context path lands
inside the fixture and no existing cassette carries one.

A `/jira` run is worth doing afterwards, deliberately, as its own evidence. It
is the shape that produced `Relative` returning early before the host check, the
context path applied twice, and `underPath` reading `/jiraxyz` as inside
`/jira` — all reasoned about against documentation, never observed. Jira's base
URL has to be changed to match in ⚙ → System → General configuration, or every
self link disagrees with how it was reached.

## Running this where Docker is not the local machine

`common.sh` probes for a URL that answers `/status`: the published loopback
port first, then the container's own bridge address. Inside a dev container that
shares only the daemon, the published port lands on the *host's* loopback and
the bridge address is the way through. Nothing needs configuring for either
case, and guessing wrong would fail at the first POST of a wizard that then has
to be restarted from scratch.
