<!--
The subject of the squashed commit comes from this PR's title, so write it as a
Conventional Commit: `<type>(<scope>): <subject>`, imperative, under 72
characters, no trailing period. The body below becomes the commit message, and
it matters more than the title — say what was wrong and why the fix is shaped
the way it is.
-->

## What was wrong

## Why the fix is shaped this way

## Checks

<!--
Delete the rows that do not apply. The ones left should be true, and CI checks
most of them — this list is for the two or three it cannot.
-->

- [ ] `make ci` passes locally, or CI is green.
- [ ] **Output contract.** If a golden under `internal/cli/testdata/kinds/`
      changed, the kind's schema version is bumped in the same commit, and the
      commit is marked `!` with a `BREAKING CHANGE:` footer naming the kind and
      its new version.
- [ ] **Source-of-truth docs updated in the same change**, if this touched what
      they cover: `docs/output-contract.md` (a kind, an exit code, an error
      code, the envelope, escaping), `docs/build-profiles.md` (a tag, a profile,
      what a tag gates), `docs/architecture.md` (a package, an import rule).
- [ ] `docs/commands.md` regenerated with `make docs`, if a command or a flag
      changed. Never hand-edited.
- [ ] `make fuzz` run, if this touched anything that quotes, escapes, or parses,
      and any crasher Go wrote to `testdata/fuzz` is committed with the fix.
- [ ] **A new invariant has a test.** If this fixes a bug a gate should have
      caught, the gate is extended in the same change — that is the rule this
      project actually runs on.
