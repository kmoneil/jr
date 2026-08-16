package lint_test

import (
	"slices"
	"strings"
	"testing"
)

// The README states its command surface twice: as a count, and as the block of
// commands under it. The count has been asserted since
// TestTheReadmeSurfaceCountMatchesTheBinaries, and the block was not, so on
// 2026-08-16 the sentence said 65 and the block listed 62. `issue history` had
// shipped four days earlier, `issue activity` after it, and `completion` had
// never been there at all. Two releases went out in between.
//
// That is the profile counts, the error-code table, the fuzz counts and the
// kind versions for the fifth time, and the shape is the same every time: a
// number in a published document is asserted and the list beside it is not. The
// count is the easy half to check and the useless half to be right about,
// because a reader does not consult the number to find out whether a command
// exists.
//
// Both directions are checked. A command missing from the block is what
// happened here; a command in the block that no longer exists is worse, because
// a reader types it.

// readmeSurfaceHeading is the section the block belongs to. The block is the
// first fenced one under it.
const readmeSurfaceHeading = "## What works today"

// surfaceBlock returns the lines inside the fence under readmeSurfaceHeading.
//
// It fails rather than returning nothing when the section or the fence moves.
// A gate that reads a document by shape has exactly one way to be useless,
// which is to match nothing and pass, and every gate in this package that has
// gone quiet went quiet that way.
func surfaceBlock(t *testing.T) []string {
	t.Helper()

	lines := readLines(t, readmePath)
	start := slices.Index(lines, readmeSurfaceHeading)
	if start < 0 {
		t.Fatalf("%s: no %q heading; this test reads the block under it, so a "+
			"renamed section silently asserts nothing", readmePath, readmeSurfaceHeading)
	}

	open := -1
	for i := start + 1; i < len(lines); i++ {
		switch {
		case strings.HasPrefix(lines[i], "```") && open < 0:
			open = i
		case strings.HasPrefix(lines[i], "```"):
			if open+1 == i {
				t.Fatalf("%s: the fenced block under %q is empty", readmePath, readmeSurfaceHeading)
			}
			// The block is identified by what is in it rather than by being
			// first, so deleting it reports that instead of reporting the next
			// block's first line as unparseable. That is what happened when
			// this was checked by deletion: the console block under it became
			// the surface block and the failure named `$ jr schema`.
			if first := lines[open+1]; !strings.HasPrefix(first, "jr ") {
				t.Fatalf("%s: the first fenced block under %q starts %q, which is "+
					"not the command surface. Either the block moved or it is gone, "+
					"and either way nothing here is being checked.",
					readmePath, readmeSurfaceHeading, first)
			}
			return lines[open+1 : i]
		case strings.HasPrefix(lines[i], "## ") && open < 0:
			t.Fatalf("%s: %q ends at %q before any fenced block",
				readmePath, readmeSurfaceHeading, lines[i])
		}
	}
	t.Fatalf("%s: the fenced block under %q never closes", readmePath, readmeSurfaceHeading)
	return nil
}

// surfaceTree is the built binary's command surface, in the two shapes reading
// the block needs: which dotted names are commands, and which are the prefix of
// one.
//
// Both come from the binary rather than from a list in this file. A hand-kept
// set of sub-nouns is a second place to update, which is the defect this test
// exists to catch, one layer down.
type surfaceTree struct {
	leaf  map[string]bool
	group map[string]bool
}

func surfaceTreeOf(commands []string) surfaceTree {
	s := surfaceTree{leaf: map[string]bool{}, group: map[string]bool{}}
	for _, name := range commands {
		s.leaf[name] = true
		parts := strings.Split(name, ".")
		for i := 1; i < len(parts); i++ {
			s.group[strings.Join(parts[:i], ".")] = true
		}
	}
	return s
}

// resolve reads one word of a line, given the noun the line opened with and the
// prefix words are currently attaching to. It returns the command the word
// names, or the new prefix it opens, and reports which.
//
// The order matters and is the block's own grammar: a word is a sub-noun of
// what is open, then a command under it, then a sub-noun of the line's noun,
// then a command under that. `jr issue link list add remove | worklog list add
// delete` needs the third case, because `worklog` follows commands of
// `issue.link` and belongs to `issue`.
func (s surfaceTree) resolve(noun, prefix, word string) (name, opened string, ok bool) {
	for _, base := range []string{prefix, noun} {
		if s.group[base+"."+word] {
			return "", base + "." + word, true
		}
		if s.leaf[base+"."+word] {
			return base + "." + word, "", true
		}
	}
	return "", "", false
}

// parseLine turns one line of the block into the commands it claims.
func (s surfaceTree) parseLine(t *testing.T, line string) []string {
	t.Helper()

	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "jr" {
		t.Fatalf("%s: cannot read %q as a command line; every line of the block "+
			"is `jr <noun> <verbs>` or `jr <command> | <command>`", readmePath, line)
	}

	var out []string
	noun, prefix := "", ""
	for _, word := range fields[1:] {
		switch {
		case word == "|":
			prefix = noun
		case s.leaf[word] && noun == "":
			// A top-level command, which is the last line of the block. It ends
			// the line's noun rather than opening one, so a following `|` does
			// not attach the next word to anything.
			out = append(out, word)
		case s.group[word] && noun == "":
			noun, prefix = word, word
		default:
			out = append(out, s.wordUnderNoun(t, line, noun, &prefix, word))
		}
	}
	return out
}

// wordUnderNoun resolves a word that has to belong to the line's noun, and
// fails naming the word rather than dropping it.
func (s surfaceTree) wordUnderNoun(t *testing.T, line, noun string, prefix *string, word string) string {
	t.Helper()

	if noun == "" {
		t.Fatalf("%s: %q in %q is not a command this build has, and no noun is "+
			"open for it to belong to", readmePath, word, line)
	}
	name, opened, ok := s.resolve(noun, *prefix, word)
	if !ok {
		t.Fatalf("%s: %q in %q resolves to no command under %q. Either the "+
			"README names something this build does not have, or the block's "+
			"shape changed and this parser has to learn it.",
			readmePath, word, line, noun)
	}
	if opened != "" {
		*prefix = opened
		return ""
	}
	return name
}

// TestTheReadmeSurfaceBlockListsEveryCommand holds the block to the binary the
// sentence above it is already held to.
func TestTheReadmeSurfaceBlockListsEveryCommand(t *testing.T) {
	profiles := profilesFromMakefile(t)
	i := slices.IndexFunc(profiles, func(p profile) bool { return p.name == "full" })
	if i < 0 {
		t.Fatalf("the Makefile ships %v; this test needs the full profile, "+
			"because the block claims to be every command", profiles)
	}

	commands := commandsIn(t, t.TempDir(), profiles[i])
	tree := surfaceTreeOf(commands)

	var claimed []string
	for _, line := range surfaceBlock(t) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, name := range tree.parseLine(t, line) {
			if name != "" {
				claimed = append(claimed, name)
			}
		}
	}
	slices.Sort(claimed)
	claimed = slices.Compact(claimed)

	for _, name := range commands {
		if !slices.Contains(claimed, name) {
			t.Errorf("%s: the full build has %s and the block under %q does not "+
				"list it. A reader consults the block, not the count above it.",
				readmePath, name, readmeSurfaceHeading)
		}
	}
	for _, name := range claimed {
		if !slices.Contains(commands, name) {
			t.Errorf("%s: the block lists %s and the full build has no such "+
				"command. This is the worse direction: a reader types it.",
				readmePath, name)
		}
	}
}
