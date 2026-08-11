# Security

`jr` holds a credential to somebody's Jira and does what a script tells it to.
Two things follow from that, and this document is about both: the credential
must not leak, and a result must never claim more than it can back up — a
script that acts on a partial answer it believes is complete is a security
problem wearing a correctness costume.

## Reporting a vulnerability

Email **kevin@oneil.xyz**. Please do not open a public issue for a
vulnerability.

You will get an acknowledgement within 3 working days. Fixed issues are
disclosed publicly within **90 days** of the report, sooner if a fix ships
earlier, later only by agreement with the reporter.

Include the invocation and, if you can, the response that triggers it. A
recorded exchange is worth more than a description — cassettes are how this
project keeps evidence, and yours may end up as one.

## Supported versions

Pre-1.0. Until the v1.0 tag, only the tip of `main` receives fixes.

## Threat model

**Assumed hostile: everything the server sends.** A response is parsed, never
trusted. It cannot redirect a request to another host, name a file outside the
working directory, put an escape sequence in a data column, or turn a bounded
run unbounded. A Jira instance can be compromised, be somebody else's, or simply
be a proxy that lies.

**Assumed hostile: values that flow into a query.** A label, a status, a display
name, or a `--jql` fragment may come from a ticket, a pipeline variable, or a
model. None of them may change what a query _scopes_.

**Assumed trusted: the caller.** Flags, arguments, the config file, and the
context are the operator's own instructions. `jr issue delete ENG-1 --yes` does
what it says. The tool defends the operator from mistakes with a confirmation
gate and a read-only latch; it does not defend the machine from its own user.

**Assumed trusted: the credential store, once written.** File modes are checked;
the filesystem is not otherwise second-guessed.

**Out of scope:** side channels, an attacker who already has your token, an
attacker with write access to your config or your `$PATH`, and the contents of
the Jira instance itself. If someone can edit `config.toml`, they can point you
at their server.

## What is not possible, by construction

Absences, not options. There is no flag that enables these and no hook to wire
one to.

- **No process is ever started.** No shelling out to a browser, a clipboard, or
  a keyring helper — a child process would inherit the environment, which is
  where `JIRA_API_TOKEN` lives. `os/exec` appears in the test tree and nowhere
  else, held there by `TestNothingShippedExecutesAProcess`.
- **No cgo.** Pure Go, standard library plus four dependencies.
- **No shell is involved in building a request.** Nothing is interpolated into a
  command line, because there is no command line.
- **Only `internal/transport` speaks HTTP.** Nothing else may import
  `net/http`, so header redaction cannot be bypassed by a package that builds
  its own client. `TestTransportOwnsHTTP`.
- **A reader build cannot mutate Jira.** Not "refuses to": the mutating verbs
  are behind the `write` build tag and are not in the binary.
  `TestReaderBuildCannotMutate`, and `internal/lint/profiles_test.go` builds
  each profile and counts what it contains.
- **Only `internal/render` encodes output**, so an escaping rule holds
  everywhere or nowhere. `TestRenderIsTheOnlyWriterOfOutput`.

## Credentials

A credential reaches `jr` from the environment, the credential store, or
`.netrc`, in that order. It never arrives as a flag value: an argument lands in
the shell history and in the process list, where every other user on the machine
can read it. `TestTokenIsNotAcceptedOnTheCommandLine`.

- **`config.toml` holds a reference, never a secret.** The config is meant to be
  hand-edited and kept in a dotfiles repository. The secret lives under the
  state directory at mode 0600, and is _refused on read_ if it is wider.
  `TestConfigFileNeverContainsACredential`, `TestCredentialFileIsNotWorldReadable`.
- **Redaction happens where the event is built, not where it is printed.** A
  `transport.Event` is scrubbed inside the transport, so no present or future
  formatter is ever handed a token. URLs count: userinfo, credential-shaped
  query parameters, and the URL inside a `*url.Error` are all scrubbed.
  `TestTokenNeverReachesDebugOutput` asserts the literal token appears nowhere
  in debug output; `internal/lint/scrubpairs_test.go` holds the redaction table
  to itself.
- **`auth.Secret` does not stringify.** `%v`, `%s`, and `%q` all print
  `REDACTED`. `Reveal()` is the only way out and is greppable, so every place a
  credential can escape is one search away.
- **`jr auth token` is the single command whose output is a secret**, and it
  exists so that piping one into another tool does not require reading the store
  by hand. `TestAuthStatusNeverRevealsTheToken` covers the neighbouring command
  that must not.
- **A dry run carries no credential.** `TestDryRunNeverCarriesACredential`.
- **Nothing ever blocks on input.** A command that would read from a terminal
  refuses and lists the alternatives, so a headless runner cannot hang holding a
  credential half-written.

## Talking to Jira

- **A request path is relative, always.** An absolute path would let a
  server-supplied value redirect the request — and the `Authorization` header —
  to another host. `FuzzRelativeStaysOnTheSite`.
- **A server-supplied URL is not followed off-site.** An attachment whose
  content URL points somewhere else is `OFF_SITE_URL`, refused rather than
  fetched. `TestAnOffSiteContentURLIsRefused`.
- **A server-supplied filename is not written blindly.** `issue attachment
download` refuses a name that is not a plain filename — absolute, containing a
  separator, a parent reference, or a Windows device name — with
  `UNSAFE_FILENAME`, rather than reducing it to something it guesses was meant.
  `--output` is how the caller names a destination. A download never replaces an
  existing file without `--force`.
- **A non-idempotent request is not replayed after an upstream error.** A POST
  that got a 503 may have been processed; retrying it is how one `issue create`
  becomes two issues. Only a 429 — refused before processing — or a caller
  holding an idempotency key allows a POST retry.
  `TestPostIsNotReplayedAfterAnUpstreamError`.
- **Retries count against `--max-requests`.** A budget that ignored them would
  bound nothing. `TestRetriesCountAgainstTheBudget`, `TestBudgetStopsALongRun`.
- **The deployment is detected, never declared.** An unrecognised
  `deploymentType` is refused, because guessing sends v3 payloads to a v2 server.

## Input this tool does not trust

- **JQL is built, never concatenated.** `internal/jql` owns the only place a
  value is quoted. A caller's `--jql` fragment is always parenthesized, and a
  fragment whose own parentheses do not balance is refused before it is sent —
  `a) OR (1=1` would otherwise close the wrapper, escape the project scope, and
  return a wider result set that reports itself complete.
  `TestRawJQLCannotEscapeTheProjectScope`,
  `TestAnUnbalancedFragmentIsRefusedBeforeItIsSent`, and
  `TestRawJQLIsRefusedWhereverItIsAccepted`, which holds every command that
  accepts a fragment to the same verdict by the same code. The package is kept
  at 100% statement coverage and four fuzzers back it up.
- **Anything a parser accepts is safe as a URL path segment, unescaped.**
  `issue.ParseKey` once accepted `../../admin-1`, which a caller that
  concatenated turned into a request to another endpoint. Escaping is the second
  layer, never the only one, and every parser whose output reaches a path
  carries a fuzz target: `FuzzParseKeyProducesASafePathSegment`,
  `FuzzEpicRefIsSafeInAPath`, `FuzzAValidProjectKeyIsASafePathSegment`,
  `FuzzBrowseURLStaysUnderBrowse`.
- **A value is never altered to make it representable.** Invalid UTF-8 is
  refused with `INVALID_ENCODING`, not replaced with U+FFFD — a query for
  something other than what was asked for is a wrong answer that looks like a
  right one. `TestInvalidUTF8IsRefusedNotReplaced`.
- **A data column never carries an escape sequence.** `--url` emits a bare URL
  rather than an OSC 8 terminal hyperlink, because stdout is data and a terminal
  is a program that interprets bytes. Nothing this tool prints to a data column
  can move a cursor or set a title.
- **Cache and state paths stay under their roots.**
  `FuzzSiteCacheStaysUnderTheCacheRoot`, `TestCacheKeyCannotEscapeTheDirectory`.

## Refusing, confirming, and the MCP surface

- **Read-only is a one-way latch within an invocation.** `--readonly`,
  `JIRA_READONLY`, or a context created read-only turns it on; nothing a command
  does turns it off, and `JIRA_READONLY=0` does not clear it. Changing what a
  context is _for_ is a deliberate edit — `context edit --unset readonly` — and
  not something an invocation can do to itself. `TestReadOnlyIsAOneWayLatch`,
  `TestReadOnlyIsNotRelaxedForADryRun`.
- **Destructive commands require `--yes`**, and a dry run is allowed without it,
  because you preview in order to decide whether to confirm.
- **Both rules live with the declaration, not with the caller.**
  `registry.Gate` is the only place `Destructive` becomes "needs `--yes`" and
  `Mutating` becomes "refused in read-only". This was a real bug: enforcement
  used to sit in the CLI, and `jr mcp serve` calls `Run` directly, so a
  read-only context sent a real DELETE and replied `isError: false`.
  `TestEveryCallerOfACommandGatesIt` fails the build if a new caller forgets.
- **The MCP gate tests assert the request that must not happen**, not the error
  that says it did not: they count `Session.Connect` and require zero, because a
  refusal that arrives after the DELETE carries the same error code as one that
  arrives before it. `TestAToolCallHonoursTheReadOnlyLatch`,
  `TestAToolCallHonoursTheConfirmationGate`,
  `TestReadOnlyIsNotRelaxedForADryRunOverMCP`.
- **An MCP server advertises what the binary contains.** A reader build lists no
  mutating tools, so an agent introspecting it sees the truth rather than a list
  of tools that would refuse.

## Truncation is a security property

A result set cut short is never reported as complete, in any format: the
envelope says `complete="false"`, TSV has no envelope so it is a structured
stderr warning plus exit 3, and both carry a token to resume from. The reason
this is here rather than only in the output contract is that the failure is
silent by nature — a nightly job that reads fifty rows as the whole project
makes decisions on a subset and reports success, and nothing downstream can tell.

`stdout` is data and nothing else. A failing command writes nothing at all to
it, so a partially-written document can never be parsed as a whole one.
`TestErrorsGoToStderrAndLeaveStdoutClean`,
`TestAnOrdinaryFailureStillWritesNothingToStdout`,
`TestStreamedTSVIsByteIdenticalToBuffered`.

## Supply chain

- **Four direct dependencies**, listed with their licences in [NOTICE](NOTICE).
  Nothing is vendored; `go.sum` pins every module.
- **`make vuln` runs govulncheck** over the full tag set and fails closed. It is
  part of `make ci`.
- **The test suite never touches the network.** Every host in a test uses a
  reserved TLD — `.invalid`, `.test`, `.example` — enforced by
  `internal/lint/hosts_test.go`. This was learned the hard way: when `auth
login` grew credential verification, the suite began sending test tokens to a
  plausible-looking domain that turned out to exist. Nothing in the tests had
  changed; a behaviour change had turned an inert string into a real request.
- **Fixtures are recordings.** Each cassette records whether it was recorded or
  hand-written, and a recording no manifest can remake fails the build.

## What a hostile Jira can still do

Stated plainly, because a threat model that only lists wins is not one.

It can lie about the data. Wrong issues, wrong statuses, a `baseUrl` pointing at
a phishing host — `--url` prints what the server reports about itself, which is
the same string its own notification emails use. It can withhold rows: a page
that claims to be the last one is believed, because there is no second source to
check it against. It can make requests slow or expensive, bounded only by
`--max-requests` and by whatever timeout the caller sets. And it can serve an
attachment whose _contents_ are hostile; `jr` writes bytes to the path you named
and never opens them.

What it cannot do is get the credential sent anywhere else, get a file written
outside the working directory, get a process started, or get a result that was
cut short reported as complete.

## Keeping this current

Update this file in the same change that alters what the tool treats as hostile,
adds a way for a credential or a request to leave the process, changes the
read-only or confirmation gates, or changes the disclosure process. Every claim
above names the test that holds it — if you move or rename one, this document is
part of the change.
