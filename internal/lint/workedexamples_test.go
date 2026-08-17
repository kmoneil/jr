package lint_test

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The hand-written documents are full of commands somebody will paste, and
// nothing checked that any of them still parse.
//
// The generated ones are gated: TestTheCommandReferenceIsCurrent rewrites
// docs/commands.md from the registry, TestTheShippedSkillIsCurrent rewrites
// skills/jr from the binary. The hand-written ones carry the on-ramp instead,
// which is 1,890 lines of worked examples, and CLAUDE.md asks for them to be
// grepped by hand on every change that alters what an invocation does. That is
// a rule with a half-life, and it had already expired: the README illustrated
// date validation with `--created 2020-13-45`, a flag that has never existed.
// The real flags are --created-after and --created-before, so the example a
// reader pastes answers "unknown flag: --created" rather than the "month 13 is
// out of range" the sentence beside it promises. It demonstrated the opposite
// of its own point.
//
// This is the same shape as the profile counts, the error-code table, the fuzz
// counts, the kind versions, and the README surface block: the thing a reader
// consults is the thing nothing asserted.

// exampleDocs are the hand-written documents to hold to the binary.
//
// The skill assets are globbed rather than listed. They are the prose a model
// reads, they grow a file at a time, and a list here would be a second place to
// update, which is the defect this whole package exists to catch.
func exampleDocs(t *testing.T) []string {
	t.Helper()

	docs := []string{
		"README.md",
		"docs/getting-started.md",
		"docs/recipes.md",
		"docs/troubleshooting.md",
	}
	for _, pattern := range []string{
		"internal/cli/skillassets/*.md",
		"internal/cli/skillassets/references/*.md",
	} {
		found, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(found) == 0 {
			t.Fatalf("%s matched nothing. Either the skill assets moved or this "+
				"gate stopped reading them, and a gate that reads nothing passes.",
				pattern)
		}
		for _, path := range found {
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				t.Fatalf("rel %s: %v", path, relErr)
			}
			docs = append(docs, filepath.ToSlash(rel))
		}
	}
	return docs
}

// notAJrFlag names a `--flag` that appears in the documents and is not one of
// this tool's, with the reason.
//
// Every entry is a decision rather than a suppression, which is why it carries
// prose. Two kinds so far, and both are legitimate things for a document to
// contain: another tool's flag inside a worked example, and one of this tool's
// deliberate absences, which a reader has to be told about precisely because
// they would otherwise reach for it.
var notAJrFlag = map[string]string{
	"--repo": "gh release download and gh attestation verify, in the install " +
		"steps in README.md and docs/getting-started.md",
	"--pattern": "gh release download, in the same install steps",
	"--no-color": "README.md names it as a flag this tool deliberately does " +
		"not have, because nothing emits ANSI and it would do nothing",
	"--token": "README.md names it as the flag that cannot exist, because a " +
		"token in a flag value reaches the shell history and the process list",
	"--insecure": "docs/troubleshooting.md names it as the flag this tool " +
		"refuses to have, under a heading saying so, because somebody hitting " +
		"a certificate error will look for it first and has to be told what to " +
		"reach for instead. TestNothingCanDisableCertificateVerification is " +
		"what keeps the absence true",
}

// inlineLink strips a markdown link target, which is not prose and is where a
// heading anchor lives.
//
// `[sprint = <id> is not current membership](#sprint--id-is-not-current-membership)`
// holds a run of two hyphens that reads exactly like a flag and is not one. The
// anchor is generated from the heading, so the false positive comes back every
// time somebody links to a heading with a hyphen in it, and exempting each one
// by name would grow a list nobody can prune.
var inlineLink = regexp.MustCompile(`\]\([^)]*\)`)

// flagPattern is a long flag as these documents spell one.
var flagPattern = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// TestEveryFlagInAWorkedExampleExists holds the hand-written documents to the
// flags the full build actually has.
//
// The union across every command, rather than the flags of the command the
// example invokes. A flag named in a sentence beside an example often belongs
// to a different command, and a gate that guessed which would be wrong often
// enough to be turned off. The weaker question still catches the failure that
// matters, which is a flag that no longer exists anywhere.
func TestEveryFlagInAWorkedExampleExists(t *testing.T) {
	have := flagsInTheFullBuild(t)

	seen := map[string]bool{}
	for _, doc := range exampleDocs(t) {
		for i, line := range readLines(t, filepath.Join(repoRoot, doc)) {
			for _, flag := range flagPattern.FindAllString(inlineLink.ReplaceAllString(line, ""), -1) {
				seen[flag] = true
				if have[flag] {
					continue
				}
				if reason, excused := notAJrFlag[flag]; excused {
					if reason == "" {
						t.Errorf("%s is excused with no reason; an exemption "+
							"with no argument is the drift it exists to prevent", flag)
					}
					continue
				}
				t.Errorf("%s:%d names %s and no command in the full build has "+
					"it. A reader pastes this. Fix the example, or add %q to "+
					"notAJrFlag with the reason it is not ours.",
					doc, i+1, flag, flag)
			}
		}
	}

	// The exemptions can only shrink, the same way every ledger in this package
	// does. A row for a flag no document mentions any more reads as an
	// outstanding decision and is not one.
	for _, flag := range slices.Sorted(maps(notAJrFlag)) {
		if !seen[flag] {
			t.Errorf("notAJrFlag names %s and no document mentions it; delete "+
				"the row", flag)
		}
	}

	// A run that matched nothing reports what a clean tree reports.
	if len(seen) < 50 {
		t.Fatalf("found only %d flags across %d documents, so this read the "+
			"wrong files or the pattern stopped matching", len(seen), len(exampleDocs(t)))
	}
}

// TestEveryCommandInAWorkedExampleExists is the other half, and the worse one
// to get wrong: a reader types a command.
//
// Only lines that are a command, which means a fenced block's `jr ...` or a
// `$ jr ...` prompt. A `jr issue list` inside a sentence is prose about the
// command and is not something anybody pastes, and reading prose as a command
// line is how the README surface parser learned its grammar the hard way.
func TestEveryCommandInAWorkedExampleExists(t *testing.T) {
	profiles := profilesFromMakefile(t)
	i := slices.IndexFunc(profiles, func(p profile) bool { return p.name == "full" })
	if i < 0 {
		t.Fatalf("the Makefile ships %v; this test needs the full profile, "+
			"because the documents describe every command", profiles)
	}
	tree := surfaceTreeOf(commandsIn(t, t.TempDir(), profiles[i]))

	checked := 0
	for _, doc := range exampleDocs(t) {
		for i, line := range readLines(t, filepath.Join(repoRoot, doc)) {
			words, ok := commandWords(line)
			if !ok {
				continue
			}
			checked++
			if resolves(tree, words) {
				continue
			}
			t.Errorf("%s:%d invokes `jr %s` and the full build has no such "+
				"command. A reader types this one.",
				doc, i+1, strings.Join(words, " "))
		}
	}

	if checked < 30 {
		t.Fatalf("found only %d command lines across the documents, so this "+
			"read the wrong files or the shape changed", checked)
	}
}

// commandWords returns the nouns and verbs of a `jr` command line, up to its
// first flag, and reports whether the line is one at all.
//
// A placeholder ends it. `jr <command> --debug` is a template rather than an
// invocation, and resolving `<command>` against the tree would fail on a line
// nobody can paste as written.
func commandWords(line string) ([]string, bool) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "$ "))
	if len(fields) < 2 || fields[0] != "jr" {
		return nil, false
	}

	var words []string
	for _, w := range fields[1:] {
		if strings.HasPrefix(w, "-") {
			break
		}
		if strings.ContainsAny(w, "<>|$'\"`") {
			break
		}
		words = append(words, w)
	}
	return words, len(words) > 0
}

// resolves reports whether some prefix of words is a command.
//
// A prefix rather than the whole run, because everything after the command is
// an argument: `jr issue get ENG-101` and `jr skill workflows` both end in one,
// and neither is a deeper command. The longest match is not needed, only that
// one exists.
func resolves(tree surfaceTree, words []string) bool {
	for n := len(words); n > 0; n-- {
		if tree.leaf[strings.Join(words[:n], ".")] {
			return true
		}
	}
	return false
}

// flagsInTheFullBuild is every long flag the full build accepts: the persistent
// ones from the root command, and every command's own.
//
// Read from the binary rather than from docs/commands.md. The reference is
// generated from the same registry, so comparing prose against it would compare
// two derivations and pass whenever both moved together.
func flagsInTheFullBuild(t *testing.T) map[string]bool {
	t.Helper()

	profiles := profilesFromMakefile(t)
	i := slices.IndexFunc(profiles, func(p profile) bool { return p.name == "full" })
	if i < 0 {
		t.Fatalf("the Makefile ships %v; this test needs the full profile", profiles)
	}
	p := profiles[i]
	bin := buildProfile(t, t.TempDir(), p)

	out := map[string]bool{}
	// The persistent flags, which no command's own description repeats.
	for _, flag := range flagPattern.FindAllString(askBinary(t, bin, p, "--help"), -1) {
		out[flag] = true
	}
	for _, name := range commandsIn(t, t.TempDir(), p) {
		path := strings.Split(name, ".")
		args := append(append([]string{}, path...), "--describe", "--format", "tsv")
		for line := range strings.SplitSeq(askBinary(t, bin, p, args...), "\n") {
			// flags/flag[3]/@name<TAB>page-size
			label, value, found := strings.Cut(line, "\t")
			if found && strings.HasSuffix(label, "/@name") &&
				strings.HasPrefix(label, "flags/") {
				out["--"+value] = true
			}
		}
	}

	if len(out) < 50 {
		t.Fatalf("the full build reported only %d flags, so --describe changed "+
			"shape and this gate is reading nothing", len(out))
	}
	return out
}
