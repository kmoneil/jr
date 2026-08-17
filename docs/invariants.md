# Invariants

These are the rules `jr` cannot break without breaking a test. Each one states
what must be true, why it exists, and names the test that enforces it.

The naming is the point. An invariant that no test names is a rule enforced by
whoever remembers it, and this project has now watched that fail twice: once
where `--order` was declared, bound, documented, and dropped by a branch that
only read `--sort`, and once where `--reporter` skipped the user resolution
every other filter did. Both passed every gate, because every gate read a
declaration rather than running the thing.

So `internal/lint/invariants_test.go` reads this document and requires every
invariant below to name a test function that exists in the tree. An invariant
with nothing behind it goes in that test's `unenforced` ledger with the reason,
and the ledger only shrinks: an entry naming an invariant that has since grown a
test fails, so paying the debt forces the ledger to be updated rather than
leaving a row that reads as outstanding forever.

Breaking one should fail `make test`. If you find a way to break one that tests
do not catch, add the test in the same change and cite it here.

## Output and the contract

- **The output shape is a public API.** Golden files in
  `internal/render/testdata/` and `internal/cli/testdata/` are the contract. A
  diff in a golden file requires a schema version bump for that kind in the same
  commit, which `make golden` enforces rather than asking you to remember: it
  refuses to overwrite `testdata/kinds/<kind>.v<N>.xml` with a different shape.
  Regenerate with `make golden`, never by hand.
  **Enforced by:** `TestEveryKindHasAShapeGolden`, `TestGoldenOutput`.
- **Every build's output is goldened, not one reference build's.** A kind's
  shape is pinned once per version under `internal/cli/testdata/kinds/`; what
  differs between profiles, the command surface, the tag list, and the kinds a
  build emits, is recorded once per profile under
  `internal/cli/testdata/<profile>/`. A golden assertion that skips is an
  assertion that ran nothing.
  **Enforced by:** `TestEveryShippedProfileHasAGoldenSet`,
  `TestEveryKindEveryProfileEmitsHasAShapeGolden`.
- **`complete="false"` or exit 3.** A truncated result set is never reported as
  complete, in any format. TSV has no envelope, so truncation there is a
  structured stderr warning plus exit 3.
  **Enforced by:** `TestOnlyOnePlaceDecidesWhetherAResultIsComplete`,
  `TestStreamedTruncationStillExitsPartial`.
- **Exit codes never change meaning.** New conditions get new codes. The table
  is frozen in `internal/exitcode/`.
  **Enforced by:** `TestCodesAreFrozen`.
- **stdout is data only.** Never a spinner, a warning, or a progress line. A
  failing command writes nothing at all to stdout.
  **Enforced by:** `TestErrorsGoToStderrAndLeaveStdoutClean`,
  `TestStdoutOwnersEmitNothingElse`.
- **Collections stream in TSV and buffer in everything else.** A streamed result
  is byte-identical to a buffered one. A command writes rows to a
  `render.Stream` and never branches on format. Declare `CollectionName` and
  `Columns` on the command, because the header goes out before the first page
  arrives.
  **Enforced by:** `TestStreamedTSVIsByteIdenticalToBuffered`,
  `TestStructuredFormatsMatchBuffered`.
- **Progress only when stderr is a terminal.** On a pipe, nothing. That is what
  keeps "stderr is always structured" true while still telling a human that a
  hundred-request run is moving.
  **Enforced by:** `TestProgressIsSilentOnAPipe`.
- **A command that owns stdout emits no result document.** `OwnsStdout` marks a
  protocol server; rendering a document alongside its stream puts a frame on the
  wire the peer cannot parse.
  **Enforced by:** `TestStdoutOwnerEmitsNoDocument`,
  `TestACommandThatOwnsStdoutIsNotATool`.
- **A data column never carries an escape sequence.** `--url` emits a bare URL
  and not an OSC 8 terminal hyperlink, because stdout is data only and `cut -f6`
  has to yield something a browser can open. Terminals linkify a bare URL
  anyway, so the clickable string and the parseable string are the same string.
  **Enforced by:** `TestTheURLColumnIsABareURL`.
- **Every error code in `docs/output-contract.md` produces the exit documented
  beside it, and only that one.** A code with two exits gets split, because
  `retryable` on a refusal advertises a retry that cannot work.
  **Enforced by:** `TestTheDocumentedErrorCodesAreTheOnesTheBinaryProduces`.
- **Every schema version printed in the docs is the one the binary emits.** A
  document whose first instruction is "branch on `v`" cannot print a `v` no
  build ever emitted.
  **Enforced by:** `TestTheDocumentedSchemaVersionsAreCurrent`.
- **The profile counts and the "Gates today" column in `docs/build-profiles.md`
  are asserted.** A number in a document that nothing checks is a number that
  was true once.
  **Enforced by:** `TestTheProfileTableMatchesTheBinaries`,
  `TestTheGatesTodayColumnMatchesTheBinaries`.

## Queries and pagination

- **No offset flag.** `--limit`, `--page-size`, `--page-token`. Never
  `--paginate`, `--offset`, `--start-at`. The page token is opaque and carries
  the deployment it was minted against; a token from one is refused against the
  other rather than read as offset zero.
  **Enforced by:** `TestNoBannedFlags`.
- **Every query carries an `ORDER BY`.** Default `issuekey DESC`; a caller's
  `--sort` keeps the key as a tiebreaker. An unordered query depends on the
  server's undocumented default, which is not guaranteed stable between two
  requests, so a paged result could interleave two orderings unnoticed.
  **Enforced by:** `TestEveryQueryCarriesAnOrderBy`.
- **Prefer keyset pagination over offsets.** Data Center resumes with
  `issuekey < <last>`, not `startAt`. An offset shifts when a row is inserted
  above it, so a long run silently skips or repeats while reporting itself
  complete. Keyset needs the key ordering; anything else falls back, and the
  result records which was used.
  **Enforced by:** `TestSortsByKeyIsTheKeysetPrecondition`.
- **Issue keys never sort as text.** `IDO-999` is below `IDO-1000` as an issue
  and above it as a string. Use `issue.ParseKey` and `Key.Compare`, and keep the
  check that verifies the server agrees, because the failure mode otherwise is a
  silently short result.
  **Enforced by:** `TestKeyCompareIsTotalAndNumeric`, `FuzzKeyCompareIsATotalOrder`.
- **The deployment is detected, never declared.** Probe
  `/rest/api/2/serverInfo` and cache it. An unrecognized `deploymentType` is
  refused, because guessing Cloud sends v3 to a v2 server and guessing Data
  Center uses offset pagination against a cursor API.
  **Enforced by:** `TestProbeIdentifiesBothDeployments`, `TestUnknownDeploymentIsRefused`.
- **JQL is built, never concatenated.** `internal/jql` owns the only place a
  string is quoted, `quote()` in `render.go`, and nothing else. `Raw()` output is
  always parenthesized. JQL is never inspected with a regex: use `jql.Fields` or
  `jql.ReferencesField`, which tokenize. The quoting is tested exhaustively; that
  no other package quotes is held by the import graph and by review.
  **Enforced by:** `TestQuoteEscapesEveryControlCharacter`, `TestTheFullyCoveredPackagesAre`.
- **Wrapping a fragment contains it only if the fragment balances.** `a) OR (1=1`
  escapes the parentheses `Raw()` puts around it and drops the surrounding
  scope. Every command declaring a `--jql` flag calls `jql.Validate` in
  `Command.Validate`, never in the builder, which for a streaming command runs
  after the header is on stdout. An exemption needs its reason written into
  `jqlReportsRatherThanRefuses`.
  **Enforced by:** `TestRawJQLIsRefusedWhereverItIsAccepted`.
- **A `WAS` or `CHANGED` predicate is built, never written.** `Clause.Predicates`
  carries `BY`, `AFTER`, `BEFORE` and the rest as a closed keyword set, with the
  values going through the same `quote()` as any other value. Never `Raw()`.
  **Enforced by:** `TestPredicatesRender`, `TestPredicateValuesAreQuotedLikeAnyOther`.
- **`internal/jql` stays at 100% statement coverage** and keeps its fuzzers
  green. Run `make fuzz` after touching anything that quotes, escapes, or
  parses. When a fuzzer finds a crasher, add the input as an `f.Add` seed
  alongside the fix, so the regression is visible in the test source rather than
  only in a corpus file.
  **Enforced by:** `TestTheFullyCoveredPackagesAre`.
- **Every flag on a query command narrows the result set or says why it does
  not.** `constrainingFlags` offers the way out of a sweep refusal and
  `QueryOptions.Constrained` decides; a filter in the first but not the second
  turns `--watcher x --limit all` into a full-instance sweep. A flag that is not
  a filter goes in `notAFilter` with its reason.
  **Enforced by:** `TestEveryFlagIsAFilterOrIsNot`, `TestAnyFilterSatisfiesTheGuard`.
- **A filter that names a person resolves that person.** All nine user-valued
  flags on `issue list` go through `site.Metadata.ResolveUser`, spelled
  `meta.ResolveUser` at the call sites: `--assignee`, `--reporter`, `--creator`,
  `--involving`, `--watcher`, `--voter`, `--worklog-author`, `--was-assignee`,
  and `--changed-by`. An unresolved display name matches nothing on Cloud and
  returns complete, empty, exit 0, which is indistinguishable from an honest
  answer. The sentinels `unassigned` and `empty` are honoured on `--assignee`
  only, because `creator IS EMPTY` matches nothing and `CHANGED BY EMPTY` is not
  JQL. The sweep takes its subjects from `userFilterFlags`, the list the command
  itself loops over, so a tenth user-valued flag is covered the day it is added.
  **Enforced by:** `TestEveryUserFilterResolvesThePersonItNames`,
  `TestOnlyTheAssigneeTakesTheSentinelWords`.
- **Retries count against `--max-requests`.** A retry is another request from
  the server's side; a budget that ignored them would bound nothing.
  **Enforced by:** `TestRetriesCountAgainstTheBudget`.

## Flags and commands

- **No `--reverse`.** Sorting is `--sort <field>` plus `--order asc|desc`.
  **Enforced by:** `TestNoBannedFlags`.
- **No single-letter flag whose letter is not in its own name**, and no letter
  reused across commands with a different meaning.
  **Enforced by:** `TestShortFlagLettersAppearInTheirNames`,
  `TestShortFlagsAreNotReusedWithDifferentMeanings`.
- **A flag that cannot do what it says is not shipped.** `--field` fetched a
  field and then discarded it, and replacing the default set made every row
  render as unassigned with an unknown status. A flag either affects the output
  or does not exist.
  **Enforced by:** `TestFieldNamesResolveThroughTheCommand`, `TestFieldsResolveByIdAndName`.
- **A flag's effect is asserted, not reviewed.** The sweep drives every flag on
  and off, each deployment, and the requests, the columns, the document, or the
  error has to differ. An exemption goes in `flagWithNoObservableEffect` or
  `commandNotSwept`, with its reason.
  **Enforced by:** `TestEveryFlagChangesWhatTheCommandDoes`,
  `TestTheFlagSweepAccountsForEveryFlag`.
- **A command validates its own flags in `Command.Validate`.** A streaming
  command writes its header before its body runs, so a rejection from inside the
  command arrives after bytes are on stdout.
  **Enforced by:** `TestValidateRunsBeforeAnyOutput`.
- **Every command declares what it emits.** A command that returns a kind it did
  not declare is rejected at runtime and by the contract tests.
  **Enforced by:** `TestCommandsDeclareTheirOutput`, `TestEveryKindDeclaresItsShape`.
- **Every mutating command** declares the `write` tag, accepts `--dry-run`, and
  declares exit 10. Every destructive one requires `--yes`.
  **Enforced by:** `TestMutatingCommandsAreSafeByConstruction`.
- **`LocalState` is not `Mutating`.** `Mutating` means "changes Jira" and pulls
  in the write tag, `--dry-run`, exit 10, and the read-only gate. `LocalState`
  means "writes local config or credentials" and must work in every build,
  because a reader binary that could not create a context could not be
  configured at all.
  **Enforced by:** `TestLocalStateCommandsExistInEveryBuild`, `TestReaderBuildCannotMutate`.

## Data fidelity

- **A value is never silently altered to make it representable.** Invalid UTF-8
  is refused with `INVALID_ENCODING`, not replaced with U+FFFD. The same rule
  applies anywhere else a lossy conversion is possible.
  **Enforced by:** `TestInvalidUTF8IsRefusedNotReplaced`, `TestInvalidUTF8IsRefusedNotSubstituted`.
- **A field the server did not send is absent, not defaulted.** A `bool` cannot
  hold "not said", so `hasScreen` and `isPrivate` are `*bool` and the attribute
  is written only when the server sent one. An absence needs a documented
  meaning that is true on both deployments. Nothing compiles a comment.
  **Enforced by:** `TestAnUnsentHasScreenStaysUnsent`.
- **Anything a parser accepts is safe as a URL path segment, unescaped.**
  `issue.ParseKey` once let `../../admin-1` through as a project. Escaping is the
  second layer, never the only one, and a parser whose output reaches a path
  carries a fuzz target seeded with the inputs that used to get through.
  **Enforced by:** `FuzzParseKeyProducesASafePathSegment`.
- **A fuzz target behind a build tag is a target the sweep cannot see.** `make
  fuzz` builds with the full tag set, because `go test -list` under no tags
  reported no targets in `internal/workflow` and swept happily over code it could
  not compile. A green sweep that ran nothing is worse than no sweep.
  **Enforced by:** `TestTheDocumentedFuzzCountsAreTheOnesInTheTree`.
- **A scheduled sweep reports what it measured, not what it was built to
  find.** `if: failure()` fires for a finding, for a tool that could not run,
  and for a runner that was reclaimed. The weekly mutation sweep's first
  scheduled run was the third of those, and it filed an issue saying the
  baseline had moved against a run that compared nothing to anything. Its sweep
  step also piped `make mutate` into `tee` under a shell with no `pipefail`, so
  a real finding would have gone green. `scripts/mutate.sh` now gives a moved
  count and an unmeasurable one different exit codes, and the workflow titles
  the issue from the verdict the sweep recorded rather than from the fact that
  the job went red.
  **Enforced by:** `TestTheMutationSweepExitCodesSayWhichFailureItWas`,
  `TestTheMutationWorkflowKeepsTheSweepsOwnStatus`,
  `TestTheMutationReportNamesWhatActuallyFailed`.
- **A mutant runs under a memory bound, because some of them do not
  terminate.** Four mutants of `internal/jql/token.go` turn its scan loop into
  an unbounded append, and gremlins' per-mutant timeout is the coefficient times
  a measured suite time that includes a cold build: 1.74s on a runner against
  79.77ms locally, so the same mutant gets 104 seconds there rather than 4.8 and
  takes the machine. `scripts/mutate.sh` caps the address space of `go` through
  a PATH shim, not of its own shell, because capping the shell caps gremlins,
  which sizes itself from `NumCPU` and then dies copying the source tree per
  worker. The bound is on the children that run away.
  **Enforced by:** `TestTheMutationSweepBoundsARunawayMutant`.

## Credentials and safety

- **Never log an Authorization, Cookie, or Proxy-Authorization header.**
  Redaction happens when a `transport.Event` is built, not when it is formatted,
  so a credential never reaches a Tracer and no future formatter can leak one.
  URLs count: userinfo and credential-shaped query params are redacted too, and a
  `*url.Error` is never printed raw.
  **Enforced by:** `TestTokenNeverReachesDebugOutput`.
- **A non-idempotent request is not replayed after an upstream error.** A POST
  that got a 503 may have been processed before the failure; retrying it is how
  one `issue create` becomes two issues. Only a 429, refused before processing,
  or an explicit `Replayable`, meaning the caller holds an idempotency key,
  allows a POST retry.
  **Enforced by:** `TestPostIsNotReplayedAfterAnUpstreamError`, `TestPostIsReplayedAfterRateLimiting`.
- **A retry this client declines to make is traced as a decision.** The rule
  above was enforced by a test that counts requests, which passes whether or not
  anything says why the count is one. It was one: `shouldRetry` built the reason
  on every non-idempotent 5xx and `settle` traced it only when the request was
  still retryable, which is exactly when that reason is not the one. So the
  decision that stops one `issue create` becoming two left a `--debug` trace
  identical to a run where the policy was never consulted. It holds on both
  paths that end an exchange, a status that will not be replayed and a
  connection that dropped, because `traceNotRetried` is one function and two
  copies of it would be two places for the rule to stop being true. A plain 4xx
  and a cancelled context still trace nothing, because neither is a decision
  this client made.
  **Enforced by:** `TestTheRefusalToReplayAPostIsTraced`,
  `TestTheRefusalToReplayAPostAfterADroppedConnectionIsTraced`,
  `TestAPlain4xxTracesNoRetryReason`.
- **A wait this client shortens says what it was asked for.** `Retry-After` is
  capped at 30 seconds so one command cannot become an hour-long hang, which
  means a server asking for an hour is retried 120 times sooner than it
  instructed, which keeps a throttle alive and spends `--max-requests` doing it.
  The cap stays; being silent about it does not. The trace carries the server's
  number beside the honoured one whenever the two differ, and only then.
  **Enforced by:** `TestACappedRetryAfterSaysWhatTheServerAsked`,
  `TestAnHonouredRetryAfterIsNotReportedTwice`, `TestRetryAfterIsCapped`.
- **A request path is relative, never absolute.** An absolute path would let a
  server-supplied value redirect the request, and the credential, to another
  host.
  **Enforced by:** `TestAbsolutePathIsRefused`, `TestRelativeDoesNotEchoTheURL`.
- **A credential never reaches the config file.** `config.toml` holds a
  credential reference; the store is a separate file under the state directory
  at mode 0600, and is refused on read if it is wider. The config is meant to be
  hand-edited and kept in a dotfiles repository.
  **Enforced by:** `TestConfigNeverHoldsACredential`, `TestConfigFileNeverContainsACredential`,
  `TestOverlyOpenStoreIsRefused`.
- **`auth.Secret` does not stringify.** It has `String` and `Format` methods so
  `%v`, `%s`, and `%q` all print `REDACTED`. `Reveal()` is the only way out, and
  it is greppable, so every place a credential can escape is one search away.
  **Enforced by:** `TestSecretDoesNotPrintItself`, `TestAuthStatusNeverRevealsTheToken`.
- **A token is read from stdin or a file, never a flag value.** An argument
  lands in the shell history and the process list. `JIRA_API_TOKEN` and `.netrc`
  work with no login step at all.
  **Enforced by:** `TestLoginRejectsEmptyStdin`, `TestEnvProvider`, `TestNetrcFormats`.
- **Nothing ever blocks on input silently, and nothing blocks in a build with no
  human.** Only the `prompt` tag may ask, with echo off; the agent, reader, and
  ci builds have no prompt compiled in and refuse with the alternatives.
  `--token-stdin`, `--token-file`, and the environment always work.
  **Enforced by:** `TestLoginPromptsOnATerminal`, `TestTokenStdinAtATerminalAlsoPrompts`.
- **Input a command accepted is never quietly forgotten.** `auth login --site X`
  creates the first context, because storing a credential for a site and then
  reporting "no site configured" is the tool ignoring what it was told. Act only
  when the choice is unambiguous, meaning zero contexts, and leave an existing
  setup alone.
  **Enforced by:** `TestLoginCreatesTheFirstContext`.
- **Read-only is a one-way latch, within an invocation.** `jctx.Resolve` ORs
  `--readonly`, `JIRA_READONLY`, and the context's own flag, so
  `JIRA_READONLY=0` does not clear it; to write, use a context that permits it.
  `context edit --unset readonly` is the deliberate edit that does.
  **Enforced by:** `TestReadOnlyIsAOneWayLatch`, `TestReadOnlyEnvIsGenerous`.
- **A refusal the declaration drives lives with the declaration.**
  `registry.Gate` is the only place `Destructive` becomes "needs `--yes`" and
  `Mutating` becomes "refused in read-only", and every caller of `Command.Run`
  calls it, the CLI and `internal/mcp` alike.
  **Enforced by:** `TestEveryCallerOfACommandGatesIt`, `TestAToolCallHonoursTheReadOnlyLatch`.
- **A gate test asserts the request that must not happen, not the code that says
  it did not.** A refusal arriving after the DELETE carries the same `READ_ONLY`
  as one arriving before it, so the MCP gate tests count `Session.Connect`,
  require zero, and require `Validate` never ran.
  **Enforced by:** `TestAToolCallHonoursTheConfirmationGate`,
  `TestReadOnlyIsNotRelaxedForADryRunOverMCP`.

## Package structure

- **Only `internal/render` encodes output.** Nothing else imports
  `encoding/xml`, `encoding/csv`, or a YAML package.
  **Enforced by:** `TestRenderIsTheOnlyWriterOfOutput`.
- **Only `internal/transport` speaks HTTP.** Nothing else imports `net/http`,
  including for a method constant. Use `transport.MethodGet` and friends.
  **Enforced by:** `TestTransportOwnsHTTP`.
- **Nothing imports `internal/resource/*`** except `cmd`, `internal/mcp`,
  `internal/workflow`, and `internal/commands`, which exists only to
  blank-import resources so their init functions run. Resources never import
  each other.
  **Enforced by:** `TestOnlyTheEdgesImportResources`, `TestResourcesDoNotImportEachOther`.
- **A resource reaches Jira through `registry.Session`**, never by building a
  client. That is what lets it be tested against a recorded fixture with no
  auth, no config, and no network.
  **Enforced by:** `TestEveryPackageThatReachesJiraHasAConversation`.

## Keeping this current

Update this document in the same change whenever you:

- add an invariant, which means adding the test that enforces it and citing the
  test here;
- rename a test this document cites, because the gate resolves every name
  against the tree and a rename is what it is built to catch;
- retire an invariant, which means deleting the bullet rather than leaving a
  rule nothing enforces;
- pay off an entry in `unenforced`, which means deleting that entry in the same
  commit as the new citation.

`internal/lint/invariants_test.go` is the gate. It does not check that an
invariant is worth having, or that the test it names is a good one.
