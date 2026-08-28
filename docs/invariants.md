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
- **A feed issues no cursor for an answer that was not whole.** `issue changes`
  reports a window and hands back the boundary the next poll starts from, so a
  cursor issued after a run cut short by `--limit`, by the request budget, or by
  a changelog the server would not send in full would move the next poll past
  changes this one never reported. Truncation is already visible three ways; this
  is the fourth thing it has to do, and it is the one that would lose data rather
  than merely understate it.
  **Enforced by:** `TestTheFeedIssuesNoCursorWhenItWasCutShort`,
  `TestTheFeedIssuesNoCursorWhenTheChangelogWasClipped`.
- **Exit codes never change meaning.** New conditions get new codes. The table
  is frozen in `internal/exitcode/`.
  **Enforced by:** `TestCodesAreFrozen`.
- **A diagnostic exits on whether it ran, never on what it found.** `jr doctor`
  reports eight checks and exits 0 whenever all of them were reached, however
  many failed. Making a finding non-zero would collapse "the diagnostic ran" and
  "this configuration is healthy" into one signal, which is the distinction the
  command exists to draw, and it would suppress the document that carries the
  verdicts. Every check is present in every run for the same reason: a
  diagnostic that printed only problems could not be told apart from one whose
  checks never ran.
  **Enforced by:** `TestDoctorAlwaysExitsZeroAndReportsEveryCheck`.
- **stdout is data only.** Never a spinner, a warning, or a progress line. A
  failing command writes nothing at all to stdout, and there are exactly two
  exceptions, both where the alternative is a caller misled about something that
  already happened. A half-applied mutation writes its result document and then
  exits non-zero, because "nothing happened" is the dangerous assumption to leave
  somebody with. A streamed TSV collection that fails after its first row keeps
  the rows it had already written, because TSV puts a row on the wire the moment
  it arrives and no failure can unwrite it; the error on stderr says how many are
  out there. Both are named in `docs/output-contract.md`. The second is a split
  along `--format`, because the same collection refused for the same reason
  writes nothing at all under XML, JSON, and YAML, and it is stated rather than
  closed: closing it means buffering every format, which is what streaming exists
  not to do.
  **Enforced by:** `TestErrorsGoToStderrAndLeaveStdoutClean`,
  `TestStdoutOwnersEmitNothingElse`,
  `TestAStreamedFailureLeavesTheRowsItAlreadyWroteAndSaysSo`,
  `TestAFailureBeforeTheFirstRowStillWritesNothing`,
  `TestAHalfAppliedWriteReachesStdoutAndStillFails`,
  `TestAnOrdinaryFailureStillWritesNothingToStdout`.
- **A refusal of a name the caller typed names the near misses, from one rule.**
  A mistyped field, flag, verb, and command name are the same mistake, and this
  tool had three different ideas of "close" plus one refusal that offered
  nothing: edit distance for a field, substring for a command name, cobra's
  suggester for a subcommand, and silence for a flag. `internal/nearest` is the
  rule now, the candidates go in `detail` beside the remedy rather than
  replacing it, and a refusal with nothing close says nothing rather than
  guessing.
  **Enforced by:** `TestARefusalOfANameTheCallerTypedNamesTheNearMisses`,
  `TestARefusalSaysNothingRatherThanGuessing`,
  `TestSuggestionsComeFromThisCommandsOwnFlags`.
- **A refusal names the record it refused.** A validation path is built from
  element names, so it is the same string for every row in a collection: without
  this a caller is told which field was refused and has to bisect `--limit` to
  find which of a few hundred records holds it. The identity goes in the error's
  `detail`, from the item's `key`, `id`, or `name`, and from every attribute it
  carries where a kind has none of those.
  **Enforced by:** `TestARefusalNamesTheRecordItRefused`,
  `TestAnIdentityNoFormatCanCarryIsNotCopiedIntoTheError`,
  `TestEveryCollectionKindCanNameItsRows`.
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
- **Every command and flag in a worked example is one the build has.** The
  generated documents are regenerated and compared; the hand-written on-ramp is
  1,890 lines of commands somebody will paste, and `CLAUDE.md` asked for it to
  be grepped by hand on every change that alters what an invocation does. That
  is a rule with a half-life, and it had expired: the README illustrated date
  validation with `--created 2020-13-45`, which is not a flag. The real ones are
  `--created-after` and `--created-before`, so the line a reader pastes answers
  `unknown flag: --created` rather than the `month 13 is out of range` the
  sentence beside it promises. It demonstrated the opposite of its own point.
  Flags are checked as a union across every command, because a flag named in
  prose often belongs to a different command than the example beside it; a
  command is checked where it is invoked. Another tool's flag, and one of this
  tool's deliberate absences, go in `notAJrFlag` with the reason.
  **Enforced by:** `TestEveryFlagInAWorkedExampleExists`,
  `TestEveryCommandInAWorkedExampleExists`.

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
- **One field has one spelling on a write.** `--field` reaches any field a typed
  flag does not, and refuses every field one does, naming that flag. Two
  spellings of one write is a last-one-wins whose winner depends on map
  iteration order. The list of owned fields cannot be derived, because only the
  request builders know that `--priority` writes `priority`. So the guard builds
  a request with every typed flag set and requires each field id it produces to
  be on the list.
  **Enforced by:** `TestEveryTypedFlagOwnsItsField`, `TestFieldWriteRefusesBeforeAnythingIsSent`.
- **A write refuses a type rather than guessing at it.** `--field` encodes from
  the schema type the site reported. A type it cannot encode unambiguously is
  refused by name, pointing at `--field-json`, which sends the value verbatim.
  Jira reports `any` for Epic Link, Rank, Team, Parent Link, and most plugin
  fields. That is five of the thirteen custom fields on a stock Data Center, so
  it is the common path and not the edge.
  **Enforced by:** `TestFieldWriteEncodesFromTheSitesOwnTypes`, `TestFieldJSONIsSentAsWritten`.
- **A flag's effect is asserted, not reviewed.** The sweep drives every flag on
  and off, each deployment, and the requests, the columns, the document, or the
  error has to differ. An exemption goes in `flagWithNoObservableEffect` or
  `commandNotSwept`, with its reason.
  **Enforced by:** `TestEveryFlagChangesWhatTheCommandDoes`,
  `TestTheFlagSweepAccountsForEveryFlag`.
- **Every flag the binary accepts is described by the registry, inherited ones
  included.** A command's own flags and the `Global Flags:` section of its
  `--help` are both compared against the declaration, in both directions. The
  second half was missing until 2026-08-28 and its absence was written down as a
  design note rather than noticed as a gap: the parser skipped that section
  because the sweep had been built against `--limit`, a per-command flag, and
  the exclusion drawn to make that sweep pass left thirteen inherited flags
  unchecked. `--project` was one of them, it filters `issue activity`, and it
  appeared in no `jr schema` output anywhere.
  **Enforced by:** `TestEveryBoundFlagIsDeclared`, `TestTheFlagSurfaceSweepCanFail`.
- **A command's `ScopedBy` is what it actually reads.** `jr schema` reports each
  inherited global with an `affects` attribute, and `affects="result"` comes
  from `Command.ScopedBy`. Every command is driven against a recording session
  and what it asks for is compared against what it declares, both ways round, so
  a command that reads the context's project without declaring it fails and so
  does one that declares a scope it never reads. The declaration is not derived
  from which files mention `inv.Jira.Project()`: a helper shared by four
  commands is one call site and four different answers.
  **Enforced by:** `TestScopedByMatchesWhatTheCommandReads`, `TestTheScopeSweepCanFail`.
- **The envelope names the scope the answer was computed over, and it is the
  scope the command read.** `complete="true"` says no bound cut the result
  short; it has never said "this is the answer", and until the `project` and
  `board` attributes existed there was no field in which the difference could be
  stated. They report what the command asked the session for, through
  `registry.ScopeWatcher`, rather than a second read of the context: a second
  read answers for `--all-projects`, which consults the context for nothing and
  would be stamped with a scope its rows did not come from.
  **Enforced by:** `TestTheEnvelopeNamesTheScopeTheAnswerWasComputedOver`,
  `TestTheScopeAttributeMatchesTheQueryThatWasSent`.
- **A query whose `--jql` names a project the scope excludes says so.** The
  fragment and the context scope are ANDed into a query that cannot match by
  construction, and the answer is a complete, empty, exit-0 result that is the
  same bytes as an honest "nothing matched". It fires only on positive
  selection, reads the fragment as tokens so a project key inside a string
  value is a value, and leaves the exit code alone.
  **Enforced by:** `TestAnOutOfScopeJQLSaysSo`,
  `TestTheScopeWarningStaysQuietOnQueriesThatAreFine`,
  `TestProjectsSelectedIsNotAFieldScan`.
- **`issue history --changed-field` matches the changelog's own names, and says
  so when it matches nothing.** It compares against the field name and id the
  changelog carries, never through the site's field catalogue: Jira 9.12 Data
  Center sends no field id in a changelog at all, so resolving a friendly name
  to a `customfield_` id would match nothing on that deployment. A filter that
  removed every row on an issue that has changes warns and names what the issue
  does hold, because a mistyped field and a field nobody touched otherwise
  produce the same zero rows at exit 0. Filtering never changes completeness:
  that is computed from the saves the server sent, not from the rows written.
  **Enforced by:** `TestHistoryFiltersByChangedField`,
  `TestHistoryFilterMatchesTheChangelogsOwnNames`,
  `TestAnUnmatchedChangedFieldSaysWhatTheIssueHolds`,
  `TestAMatchedChangedFieldIsSilent`.
- **An empty scope is refused, not reinterpreted.** `--project ""` falls back to
  the context's project rather than lifting the scope, so accepting it means
  running a query against a project the caller did not name and returning a
  complete, empty, exit-0 result.
  **Enforced by:** `TestAnEmptyScopeIsRefused`.
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
- **The set of markdown this converter refuses does not move unnoticed.**
  `FuzzMarkdownRoundTrips` returns early on a refusal, so refusing more always
  looks greener and a converter that refused everything would leave it
  permanently, silently happy. Two refusal regressions have shipped under it:
  `flanks` once refused 25 inputs the converter had written correctly the day
  before, through two 600-second sweeps that said nothing, and a three-line
  change to `after` moved 95 verdicts with the whole suite green. The verdict for
  every corpus input is a golden, so a refusal moving in either direction is a
  reviewable line in a pull request rather than a number nobody computed.
  **Enforced by:** `TestTheRefusalSetIsPinned`.
- **A document the converter writes reads back as the document it came from.**
  `FuzzMarkdownRoundTrips` permits the first conversion to change the text, for
  a real reason: emphasis has two spellings, so `_**x**_` and `***x***` are one
  document written two ways. The allowance is not restricted to spelling, and a
  first pass that moves a mark across a boundary, drops a node, or changes a
  table's shape converges exactly as readily. Sixty-five inputs already in that
  fuzzer's own corpus lose something on the first pass and it is green on all of
  them. The comparison is a projection, which marks each non-whitespace
  character carries inside which blocks, because mark order and a mark on
  whitespace are not content. Losses are a golden over the adversarial corpus
  and a named list with a reason over the documents Jira really stored.
  **Enforced by:** `TestTheDocumentSurvivesTheWriter`,
  `TestEveryRealDocumentSurvivesTheWriter`.
- **A table row is never written narrower than it is.** `table` takes its width
  from the header row and `tableLine` writes exactly that many cells, so a body
  row holding more had everything past the width dropped, with `err` nil and
  exit 0: three cells of content under a one-cell header came out as one.
  `tableLine`'s own doc comment reasons about the short row, which it pads; the
  long row went through the same loop and fell off the end of it. The parse side
  was the same rule from the other direction and hid it, because `FromMarkdown`
  builds each row from its own pipe count with no reference to the header, so it
  kept a cell GFM discards and the writer dropped it again on the way out. One
  refusal covers both, through the self-check `FromMarkdown` already ends with.
  The short row is still padded: empty cells invent nothing a reader sees.
  **Enforced by:** `TestARowWiderThanItsHeaderIsRefusedRatherThanTruncated`.
- **A strike span is never written narrower than the mark it came from.** `~~`
  has no flanking rules, so nothing beside it can make it inert and the only
  thing that changes what it means is another `~~` flush against it, which a
  reader takes for four literal tildes. A cut leaves the rest of the mark to
  open its own span at the cut with nothing in between, so for this mark the
  cut is not a retry, it is the corruption. Refusing it is not refusing the
  document: `renderChoices` opens the span with the next mark instead, and only
  a span whose first node carries nothing else is refused. Three fuzz finds in
  two days ended in those tildes, two of them the writer wrong about a
  collision that was not there and one with nothing wrong upstream at all. No
  markdown produces the documents that reach it, so `FuzzMarkdownRoundTrips`
  cannot see the class and the guard is asserted from ADF instead. That was one
  hand-written shape until 2026-08-20; `FuzzMarkedParagraphConverts` now fuzzes
  the class it belongs to, building paragraphs from mark sets rather than from
  markdown, which is the only input space in which these documents exist.
  **Enforced by:** `TestAStrikeSpanIsNeverCut`, `FuzzMarkedParagraphConverts`.
- **A run of emphasis spans is spelled as a run, not one span at a time.** Each
  span writes itself expecting the next to open with an asterisk, because
  `opensWith` names one on the neighbour's behalf before the neighbour has
  chosen anything, so a span with an emphasis neighbour takes the underscore and
  leaves the asterisk for it. That is worth doing: an underscore is inert
  between word characters, so a neighbour closing in front of one has only the
  asterisk. It is also unsatisfiable at three spans against each other, where the
  middle one has an underscore on its left and a predicted asterisk on its right
  and may take neither. Every document of that shape was refused, `_a_**b**_c_`
  among them, which CommonMark reads back as exactly the three spans it came
  from. So the prediction is treated as the preference it is and given up:
  `inlineList` writes the run a second time without it, and only after the first
  attempt found no spelling for some span in it, which is why nothing written
  today is written differently and 62 corpus inputs that were refused now
  convert. The nightly sweep of 2026-08-19 found it as this package being unable
  to read ``0 ~~*0*~~__\!__*\!*``, which it had written itself one conversion
  earlier.
  **Enforced by:** `TestTheAsteriskIsYieldedUntilThereIsNoRoom`.
- **The markdown a document converts to is a fixed point.** Reading it back and
  writing it again gives the same characters. One conversion was not: a mark on
  whitespace is dropped when that whitespace lands at the edge of a span, which
  is deliberate, but which span an edge belongs to is decided while writing.
  Two mark runs that overlap without nesting force a cut, the cut can leave a
  marked space at the head of what is left, and only one such space lands there
  per conversion, so a document with two of them took three conversions to stop
  moving and `FuzzMarkdownRoundTrips` allows two. Widening that allowance is not
  the answer, because n marked spaces need n conversions. `ToMarkdown` settles
  instead, and only through a document it reads back as the one it was given:
  settling looked free on every corpus here, all of which are markdown-shaped,
  and a text node holding a newline is not. The reader joins the lines with a
  space the way a soft break does, and an unanchored settle adopted the join and
  lost the newline. `contentKey` is the definition of "the same document" that
  the anchor needs, and it is the same function the survival golden projects
  through, so the writer's judgement and the golden's cannot drift apart.
  **Enforced by:** `TestTheTextIsAFixedPoint`.
- **A span the writer cannot follow is reconsidered, not refused.**
  `renderChoices` lists the ways to open the span at one position and takes the
  first that can be written, and nothing went back, so a locally correct span
  could leave the rest of the run with no spelling and the document was refused
  over a choice made three nodes earlier. The strong span over the last two
  nodes of one fuzz find has no spelling, is cut to one and written `***0***`,
  and that leaves an asterisk where the next span needs one: `**` merges into a
  run of three and `__` cannot close between two word characters. Opening the
  `em` instead writes `_**0**_` and the next span takes `**`, which is the text
  this converter's own reader had just built the document from. The run is
  searched rather than walked, in the same order, so a document that converts
  today is unaffected: the walk makes 1.00 attempts per span position at the
  median of both corpora and 1.55 at the worst, and the search is only reached
  when the walk has refused. `searchBudget` bounds it, because a backtracking
  walk is exponential in the worst case and the linear guarantee above is not
  negotiable.

  Three things are chosen while writing a run and all three are searched: which
  mark opens a span, how far it reaches, and which of the two characters an
  emphasis span is written with. The third hides, because it is correct about
  the span that makes it and wrong about the span around it: writing the strong
  in `*_00_0 __0__*` as `**0**` is right, and it leaves the em's content ending
  in a live asterisk where neither of the em's spellings can be read back. So
  the search yields every way of writing a span's content rather than the first,
  and it does that in a second pass, because reaching for a second character
  before another position has tried its first returns a different member of the
  same set and moves text that was stable.

  **Enforced by:** `TestASpanIsReconsideredWhenTheRestOfTheRunCannotBeWritten`,
  `TestANestedSpanTakesTheCharacterItsParentNeedsToLeave`.
- **A spelling the writer's own rules refuse is offered to the reader before it
  is given up on.** `merges` holds two checks that are approximations rather
  than rules: `insideLive` refuses a `**` span whose content holds a live
  asterisk, and the flush test refuses one whose content starts or ends with the
  delimiter on one side only. Both are right about a span delimited by a single
  character and conservative about one delimited by two, because a live asterisk
  inside is only a collision when it does not pair with something else inside.
  `**a*b*c**` is fine and `**a*bc**` is not, and what decides is the reader's
  delimiter pairing. There is no rule to add that is not that algorithm written
  a second time, so the last pass of the search drops both checks and reads
  every candidate it then generates back, comparing it against the nodes it was
  written from through `contentKey`. A candidate the reader does not agree with
  is not written. This took the emphasis refusals to **zero** across the corpus,
  from 75 before 2026-08-19, and it is affordable only because of where it sits:
  the greedy walk answers almost every document, the search runs when the walk
  refused, and this pass runs when every strict spelling in the search has been
  tried. Relaxing the generator without the check would emit spellings nothing
  verifies, and adding the check without relaxing would never see a candidate;
  they are one change.
  **Enforced by:** `TestTheWriterAsksTheReaderWhenItsOwnRulesRefuse`.
- **A scheduled sweep reports what it measured, not what it was built to
  find.** `if: failure()` fires for a finding, for a tool that could not run,
  and for a runner that was reclaimed. The weekly mutation sweep's first
  scheduled run was the third of those, and it filed an issue saying the
  baseline had moved against a run that compared nothing to anything. Its sweep
  step also piped `make mutate` into `tee` under a shell with no `pipefail`, so
  a real finding would have gone green. `scripts/mutate.sh` now gives a moved
  count and an unmeasurable one different exit codes, and the workflow titles
  the issue from the verdict the sweep recorded rather than from the fact that
  the job went red. The nightly fuzz sweep had the same shape and could not use
  the same answer: `fuzz` is a matrix, and every leg writes to one job-output
  namespace, so it reads the run's own jobs back and reports which legs failed
  at their `fuzz` step and which never reached it.
  **Enforced by:** `TestTheMutationSweepExitCodesSayWhichFailureItWas`,
  `TestTheMutationWorkflowKeepsTheSweepsOwnStatus`,
  `TestTheMutationReportNamesWhatActuallyFailed`,
  `TestAScheduledSweepReportsWhatItMeasured`.
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

- **Certificate verification cannot be turned off, by any surface.** A private
  CA is trusted with `--ca-bundle` and mTLS is answered with a client
  certificate; skipping verification is not one of the legitimate needs, and
  every tool that has shipped an `--insecure` has it in a wiki page somewhere as
  the standard fix for a certificate problem. None of the header redaction, the
  off-site URL refusal, or the relative-path rule survives a connection nobody
  verified. Checked three ways, because there are three ways to reintroduce it:
  a flag, an environment variable, and one line in a `tls.Config` literal.
  **Enforced by:** `TestNothingCanDisableCertificateVerification`.
- **A TLS setting that was named is used or refused, never ignored.** A CA
  bundle that cannot be read fails the invocation rather than falling back to
  the system roots, because the fallback produces the same verification error
  the caller was trying to fix with nothing saying the file was never read. The
  same holds for the recorder, which is built before the site's TLS settings are
  known and is re-pointed at the configured chain rather than dialling around
  it.
  **Enforced by:** `TestABundleThatCannotBeReadIsRefusedRatherThanIgnored`,
  `TestARecordingRunStillGoesThroughTheConfiguredChain`.
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
- **A file no full-tags build compiles imports nothing outside `internal/`.**
  `make vuln` scans once, at `TAGS_FULL`, and the comment justifying that said
  there are no negated build constraints in the tree. There are four, three of
  them shipped, so one pass is not four. The scan is still right, because those
  three are a const, a refusal, and a no-op that reach no dependency and no
  standard-library package, and that is the property this asserts rather than
  the sentence that was wrong. The check also fails if the set empties, because
  then the corrected comment is stale in the other direction.
  **Enforced by:** `TestWhatAFullTagsScanCannotSeeImportsNothing`.

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
