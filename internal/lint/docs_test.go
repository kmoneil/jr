package lint_test

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/registry"

	// Every resource, so registry.Default holds them. The blank import is how
	// the other lint tests reach it too; cli.Registry adds the built-ins on top.
	_ "github.com/kmoneil/jr/internal/commands"
)

// commandReference is the generated browsable reference for every command.
const commandReference = "../../docs/commands.md"

// updateDocs rewrites the reference instead of comparing against it. `make
// docs` passes it, under the full tag set.
var updateDocs = flag.Bool("update-docs", false,
	"rewrite docs/commands.md from the registry instead of comparing against it")

// TestTheCommandReferenceIsCurrent holds docs/commands.md to the registry.
//
// The reference is generated rather than written, because the registry is
// already the single description of every command — it drives the cobra tree,
// `jr schema`, and the MCP tool list, and a hand-written fourth copy would be
// the "second place to update" that CLAUDE.md calls a bug. A reference nobody
// regenerates documents the surface as it was, which is worse than no reference
// at all: `--help` is at least never stale.
//
// What it asserts depends on what was compiled, because the document describes
// the full build and a reduced profile genuinely holds fewer commands:
//
//   - Under the full tag set, exact equality. This is the comparison that
//     catches a new flag, a reworded summary, or a bumped output version.
//   - Under any other tag set, every command in this build must still be
//     documented. That is weaker, and it is deliberately not a skip: `make
//     test` runs untagged, and an assertion that skips is an assertion that
//     ran nothing.
func TestTheCommandReferenceIsCurrent(t *testing.T) {
	// cli.Registry rather than registry.All: the built-ins — auth, context,
	// version, schema, contract, and the tag-gated ones — are added when the
	// app assembles its registry, not at package init. Reading the default
	// registry alone documented 45 of the 60 commands and looked complete.
	commands := cli.Registry().All()
	if len(commands) == 0 {
		t.Fatal("the registry reports no commands at all")
	}

	if buildinfo.Profile() == "full" {
		checkReferenceIsExact(t, commands)
		return
	}
	checkReferenceCoversThisBuild(t, commands)
}

// checkReferenceIsExact compares the file against a freshly rendered reference,
// or rewrites it under -update-docs.
func checkReferenceIsExact(t *testing.T, commands []*registry.Command) {
	t.Helper()

	want := renderCommandReference(commands)
	if *updateDocs {
		if err := os.WriteFile(commandReference, []byte(want), 0o644); err != nil {
			t.Fatalf("rewriting the reference: %v", err)
		}
		t.Logf("rewrote %s from %d commands", commandReference, len(commands))
		return
	}

	got, err := os.ReadFile(commandReference)
	if err != nil {
		t.Fatalf("reading the reference: %v; run `make docs`", err)
	}
	if string(got) == want {
		return
	}
	t.Errorf("docs/commands.md is stale: %s.\nRun `make docs` to regenerate it.",
		firstReferenceDifference(string(got), want))
}

// checkReferenceCoversThisBuild asserts that every command this profile
// contains appears in the reference, which is the part of the claim that still
// holds when the full surface is not compiled.
func checkReferenceCoversThisBuild(t *testing.T, commands []*registry.Command) {
	t.Helper()

	body, err := os.ReadFile(commandReference)
	if err != nil {
		t.Fatalf("reading the reference: %v; run `make docs`", err)
	}
	text := string(body)

	documented := 0
	for _, c := range commands {
		if strings.Contains(text, "\n### `jr "+c.UseLine()+"`\n") {
			documented++
			continue
		}
		t.Errorf("`jr %s` is in the %s build and not in docs/commands.md; run `make docs`",
			c.UseLine(), buildinfo.Profile())
	}
	if documented == 0 {
		t.Error("the reference documented none of this build's commands, so the " +
			"heading format probably changed and this test asserted nothing")
	}
}

// firstReferenceDifference names the first line that differs, so the failure
// says which command moved rather than that two long strings are unequal.
func firstReferenceDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		g, w := lineAt(gotLines, i), lineAt(wantLines, i)
		if g == w {
			continue
		}
		return fmt.Sprintf("line %d is %q and the registry says %q", i+1, g, w)
	}
	return "the two differ in trailing content"
}

// lineAt returns the i-th line, or a marker for past the end.
func lineAt(lines []string, i int) string {
	if i >= len(lines) {
		return "(end of file)"
	}
	return lines[i]
}

// renderCommandReference builds the whole document.
func renderCommandReference(commands []*registry.Command) string {
	var b strings.Builder
	writeReferencePreamble(&b)
	writeReferenceContents(&b, commands)
	for _, group := range groupByNoun(commands) {
		fmt.Fprintf(&b, "\n## %s\n", group.noun)
		for _, c := range group.commands {
			writeCommand(&b, c)
		}
	}
	writeReferenceFooter(&b)
	return b.String()
}

// nounGroup is one top-level noun and the commands under it.
type nounGroup struct {
	noun     string
	commands []*registry.Command
}

// groupByNoun buckets commands by their first path element, keeping
// registry.All's ordering within each bucket and across them.
func groupByNoun(commands []*registry.Command) []nounGroup {
	var out []nounGroup
	index := map[string]int{}
	for _, c := range commands {
		noun := c.Path[0]
		i, seen := index[noun]
		if !seen {
			index[noun] = len(out)
			out = append(out, nounGroup{noun: noun})
			i = len(out) - 1
		}
		out[i].commands = append(out[i].commands, c)
	}
	return out
}

// writeReferencePreamble writes the header and the reading instructions.
//
// The lines are interpreted strings rather than one raw literal because the
// document is full of backticks, and a raw literal ends at the first one.
func writeReferencePreamble(b *strings.Builder) {
	writeLines(
		b,
		"# Command reference",
		"",
		"Every command in the full build, generated from the registry that also builds",
		"the command tree, `jr schema`, and the MCP tool list. The same text is",
		"available as `jr <command> --help`.",
		"",
		"<!-- Generated by `make docs`. Do not edit by hand: internal/lint/docs_test.go",
		"     regenerates this file from the registry and fails when it is stale. -->",
		"",
		"New here? Start with [getting-started.md](getting-started.md), then",
		"[recipes.md](recipes.md) for worked examples of common tasks. This page is the",
		"exhaustive list, which is the wrong thing to read first.",
		"",
		"**Reading an entry.** Each command lists its flags, its positional arguments,",
		"the output `kind` and schema version it emits, and every exit code it can",
		"produce. A command marked **mutating** changes Jira: it accepts `--dry-run`, is",
		"refused in read-only mode, and is absent from the reader and ci builds. A",
		"command marked **destructive** additionally requires `--yes`.",
		"",
		"**Global flags** apply to every command and are not repeated in each entry.",
		"See [output-contract.md](output-contract.md) for what the formats guarantee",
		"and [build-profiles.md](build-profiles.md) for which build contains what.",
		"",
	)
	writeGlobalFlagTable(b)
}

// writeGlobalFlagTable renders registry.GlobalFlags, and what each one reaches.
//
// It is generated because the hand-written version was wrong. This paragraph
// used to list the globals inline and listed twelve of the thirteen — --ca-bundle
// had been added to the root and never to the sentence — which is the third
// copy of a list that now has one source. And "apply to every command" was the
// part worth replacing rather than correcting: --project applies to `issue
// activity` by filtering its results and to `auth token` not at all, and a
// reader who cannot tell those apart is the reader this table is for.
//
// The per-command answer is in `jr schema <command>`, where each inherited
// global carries an affects attribute. This table is the summary.
func writeGlobalFlagTable(b *strings.Builder) {
	writeLines(b,
		"**What a global reaches** depends on the command, so `jr schema <command>`",
		"reports it per command as an `affects` attribute on each inherited flag.",
		"The three values:",
		"",
		"| `affects` | Means |",
		"| --- | --- |",
	)
	for _, e := range []registry.Effect{
		registry.EffectResult, registry.EffectProvenance, registry.EffectInvocation,
	} {
		fmt.Fprintf(b, "| `%s` | %s |\n", e, registry.EffectDescriptions[e])
	}
	writeLines(b,
		"",
		"The flags themselves:",
		"",
		"| Flag | Type | Default | Usage |",
		"| --- | --- | --- | --- |",
	)
	for _, f := range registry.GlobalFlags() {
		fmt.Fprintf(b, "| `--%s` | `%s` | %s | %s |\n",
			f.Name, f.Type, defaultCell(f.Default), escapePipes(f.Usage))
	}
	b.WriteString("\n")
}

// defaultCell renders a flag's default, or an em-space-free placeholder for a
// flag that has none.
func defaultCell(def string) string {
	if def == "" {
		return "—"
	}
	return "`" + def + "`"
}

// escapePipes keeps a usage string containing a pipe — `tsv|xml|json|yaml` —
// from ending its table cell early.
func escapePipes(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

// writeLines writes each argument as its own line.
func writeLines(b *strings.Builder, lines ...string) {
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
}

// writeReferenceContents writes the table of contents.
func writeReferenceContents(b *strings.Builder, commands []*registry.Command) {
	b.WriteString("\n## Contents\n\n")
	for _, group := range groupByNoun(commands) {
		names := make([]string, 0, len(group.commands))
		for _, c := range group.commands {
			names = append(names, fmt.Sprintf("[`%s`](#%s)",
				c.UseLine(), anchor("jr "+c.UseLine())))
		}
		fmt.Fprintf(b, "- **[%s](#%s)** — %s\n",
			group.noun, anchor(group.noun), strings.Join(names, ", "))
	}
}

// writeReferenceFooter writes the closing pointers.
func writeReferenceFooter(b *strings.Builder) {
	writeLines(
		b,
		"",
		"## Keeping this current",
		"",
		"This file is generated. `make docs` rewrites it from the registry, and",
		"`internal/lint/docs_test.go` fails the suite when it no longer matches — so a",
		"new command, a new flag, or a reworded summary is a regeneration rather than an",
		"edit. Under a reduced build profile the same test asserts that every command",
		"present is documented, because `make test` runs untagged.",
	)
}

// writeCommand writes one command's entry.
func writeCommand(b *strings.Builder, c *registry.Command) {
	fmt.Fprintf(b, "\n### `jr %s`\n\n%s\n", c.UseLine(), c.Summary)

	if badges := commandBadges(c); badges != "" {
		fmt.Fprintf(b, "\n%s\n", badges)
	}

	fmt.Fprintf(b, "\n```\njr %s\n```\n", strings.TrimSpace(c.UseLine()+" "+usageTail(c)))

	if e := strings.TrimSpace(c.Example); e != "" {
		fmt.Fprintf(b, "\nExamples:\n\n```console\n%s\n```\n", e)
	}

	writeArgs(b, c)
	writeFlags(b, c)
	writeOutputs(b, c)
	writeExits(b, c)

	// The prose comes last, for the reason `--help` prints it last: a reader
	// arrives at a command's entry to look something up, and the tables are
	// what they came for. `issue list` carries 527 words, which put its flag
	// table 54 lines below the heading in this document and its first flag on
	// line 60 of `jr issue list --help`. The two surfaces are generated from
	// one declaration and now read in one order.
	if d := strings.TrimSpace(c.Description); d != "" {
		fmt.Fprintf(b, "\n%s\n", escapeMarkdownText(d))
	}
}

// commandBadges renders the properties a caller has to know before running it.
func commandBadges(c *registry.Command) string {
	var out []string
	if c.Mutating {
		out = append(out, "**mutating** — changes Jira; accepts `--dry-run`, refused in read-only mode")
	}
	if c.Destructive {
		out = append(out, "**destructive** — requires `--yes`")
	}
	if c.LocalState {
		out = append(out, "**writes local state** — changes config or stored credentials, never Jira")
	}
	if c.Paginated {
		out = append(out, "**paginated** — bounded by `--limit`; a truncated result exits 3")
	}
	if len(c.RequiresTags) > 0 {
		out = append(out, fmt.Sprintf("**build tags** — needs `%s`",
			strings.Join(c.RequiresTags, "`, `")))
	}
	if len(out) == 0 {
		return ""
	}
	return "- " + strings.Join(out, "\n- ")
}

// usageTail renders the argument spec for the usage line.
func usageTail(c *registry.Command) string {
	spec := c.ArgSpec()
	if len(c.AllFlags()) == 0 {
		return spec
	}
	return strings.TrimSpace(spec + " [flags]")
}

// writeArgs writes the positional-argument table.
func writeArgs(b *strings.Builder, c *registry.Command) {
	if len(c.Args) == 0 {
		return
	}
	b.WriteString("\n| Argument | Required | Description |\n| --- | --- | --- |\n")
	for _, a := range c.Args {
		name := a.Name
		if a.Variadic {
			name += "..."
		}
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", name, yesNo(a.Required), cell(a.Usage))
	}
}

// writeFlags writes the flag table.
//
// It reads AllFlags rather than Flags, so a paginated command's --limit appears
// in the table. It used to read Flags, which is how the reference printed
// "bounded by --limit" in the pagination bullet directly above a table that did
// not list --limit, on all nine paginated commands.
func writeFlags(b *strings.Builder, c *registry.Command) {
	all := c.AllFlags()
	if len(all) == 0 {
		return
	}
	b.WriteString("\n| Flag | Type | Default | Description |\n| --- | --- | --- | --- |\n")
	for _, f := range all {
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n",
			flagName(f), flagType(f), flagDefault(f), flagUsage(f))
	}
}

// flagName renders the flag's spelling, including its short form.
func flagName(f registry.Flag) string {
	if f.Short != "" {
		return fmt.Sprintf("`--%s`, `-%s`", f.Name, f.Short)
	}
	return fmt.Sprintf("`--%s`", f.Name)
}

// flagType renders the type, spelling out an enum's members.
func flagType(f registry.Flag) string {
	if f.Type == registry.TypeEnum && len(f.Enum) > 0 {
		return "`" + strings.Join(f.Enum, "\\|") + "`"
	}
	return "`" + string(f.Type) + "`"
}

// flagDefault renders the default value, or a dash.
func flagDefault(f registry.Flag) string {
	if f.Default == "" {
		return "—"
	}
	return "`" + f.Default + "`"
}

// flagUsage renders the usage text plus the qualifiers that change how the flag
// is called.
func flagUsage(f registry.Flag) string {
	out := cell(f.Usage)
	var notes []string
	if f.Required {
		notes = append(notes, "required")
	}
	if f.Repeatable {
		notes = append(notes, "repeatable")
	}
	if len(notes) > 0 {
		out += " (" + strings.Join(notes, ", ") + ")"
	}
	return out
}

// writeOutputs writes the kinds this command can emit.
func writeOutputs(b *strings.Builder, c *registry.Command) {
	if len(c.Outputs) == 0 {
		if c.OwnsStdout {
			b.WriteString("\nEmits no result document: this command owns stdout.\n")
		}
		return
	}
	b.WriteString("\n| Emits | Schema | When |\n| --- | --- | --- |\n")
	for _, o := range c.Outputs {
		when := o.When
		if when == "" {
			when = "always"
		}
		fmt.Fprintf(b, "| `%s` | v%d | %s |\n", o.Kind, o.Version, cell(when))
	}
	if len(c.Columns) > 0 {
		headers := make([]string, 0, len(c.Columns))
		for _, col := range c.Columns {
			headers = append(headers, col.Header)
		}
		fmt.Fprintf(b, "\nDefault TSV columns: `%s`\n", strings.Join(headers, "`, `"))
	}
}

// writeExits writes every exit code this command can produce.
//
// AllExitCodes rather than the declared list, and labelled without a
// qualifier: a command may declare a universal code redundantly — `issue link
// add` declares Usage — and "beyond 0, 1, and 2" printed above a list
// containing 2 is a caption arguing with its own table.
func writeExits(b *strings.Builder, c *registry.Command) {
	codes := c.AllExitCodes()
	if len(codes) == 0 {
		return
	}
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("`%d` %s", code.Int(), code.Name()))
	}
	fmt.Fprintf(b, "\nExit codes: %s\n", strings.Join(parts, ", "))
}

// placeholder matches the `<key>` spelling a help text uses for an argument.
var placeholder = regexp.MustCompile(`<([a-zA-Z/][a-zA-Z0-9._-]*)>`)

// escapeMarkdownText makes plain help text safe to render as markdown.
//
// A description is written for a terminal, where `<key>` names an argument. A
// markdown renderer reads it as an HTML tag and drops it, so "takes <key> and
// <id>" renders as "takes  and " — the placeholders vanish from the one
// sentence that needed them. Indented lines are left alone: markdown already
// treats them as code, and an escape there would render as literal `&lt;`.
func escapeMarkdownText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t") {
			continue
		}
		lines[i] = placeholder.ReplaceAllString(l, "&lt;$1>")
	}
	return strings.Join(lines, "\n")
}

// cell escapes what would otherwise break out of a markdown table cell, or be
// eaten by it.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = placeholder.ReplaceAllString(s, "&lt;$1>")
	return strings.Join(strings.Fields(s), " ")
}

// yesNo renders a boolean for a table.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// anchor is the GitHub heading slug for a heading, which is what the contents
// links have to match.
func anchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}
