package lint_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// invariantsDoc is the tracked list of rules this project holds itself to.
//
// It is read rather than compiled because the list is prose: the rule, the
// failure that produced it, and the test that enforces it. Only the last of
// those three can be checked mechanically, and this file checks it.
const invariantsDoc = repoRoot + "/docs/invariants.md"

// invariantFloor is the number of invariants below which this gate assumes it
// read the wrong document rather than that the list shrank.
//
// The list was 51 entries when this was written. A parser that silently matched
// nothing would pass every assertion below, which for a guard is worse than
// failing.
const invariantFloor = 40

// unenforced names the invariants nothing asserts yet, with the reason.
//
// The list only shrinks. An entry naming an invariant that now cites a test is
// a failure rather than a no-op, so paying the debt forces the ledger to be
// updated in the same commit instead of leaving a row that reads as
// outstanding forever. That is the rule `unrecorded` follows in
// evidence_test.go, for the same reason: a ledger nobody has to maintain stops
// describing the tree.
var unenforced = map[string]string{
	"A filter that names a person resolves that person.": "nothing drives a " +
		"user-valued flag with a display name and reads the outbound JQL back. " +
		"Both deployments were measured by hand on 2026-08-16 and all six flags " +
		"resolve, so this is a missing assertion and not a live defect. See " +
		"_plans/backlog/no-test-asserts-a-user-valued-filter-resolves.md",
}

// enforcedBy marks the sentence naming the tests behind an invariant.
const enforcedBy = "**Enforced by:**"

// noTest is what an invariant says when nothing enforces it yet. It is written
// out rather than left off so that a rule with nothing behind it is
// distinguishable from a citation somebody forgot.
const noTest = "nothing yet"

// invariant is one bullet from the document: the rule's own words, and the
// tests it claims enforce it.
//
// cited records whether the bullet carried an "Enforced by" sentence at all. A
// bullet that did not is reported by the parser, and a bullet that did and
// named nothing is reported by the checker, and they are different mistakes.
type invariant struct {
	title string
	tests []string
	line  int
	cited bool
}

// TestEveryInvariantNamesATestThatExists is the gate the invariant list did not
// have.
//
// The list has always said its entries are enforced by test rather than by
// review, and nothing checked that claim, so it was true of most entries and
// false of at least one at any time. `--order` was declared, bound, documented,
// and dropped by a branch that only read `--sort`; `--reporter` skipped the
// user resolution every other filter did. Both were found by running the
// command, which is not a thing that happens on a schedule.
//
// This is deliberately a weak check. It cannot tell whether a test asserts what
// the invariant says, only whether the name resolves to a test that exists. The
// failures it catches are the cheap ones: a rule that names nothing, and a rule
// whose test was renamed or deleted underneath it.
func TestEveryInvariantNamesATestThatExists(t *testing.T) {
	doc := readFile(t, invariantsDoc)
	invariants, problems := parseInvariants(doc)

	// Every bullet is accounted for, not merely enough of them. The first draft
	// of this parser required a bullet's bold title to close on its own line,
	// so the five invariants whose titles wrap were dropped without a word, and
	// the floor below was happy at 46 of 51. A count that only has to clear a
	// floor cannot see a parser that stopped matching.
	if got, want := len(invariants), len(bulletStart.FindAllString(doc, -1)); got != want {
		t.Fatalf("%s holds %d invariant bullets and the parser read %d of them, "+
			"so it is skipping some silently", invariantsDoc, want, got)
	}
	if len(invariants) < invariantFloor {
		t.Fatalf("found %d invariants in %s and expected at least %d, so this "+
			"read the wrong document", len(invariants), invariantsDoc, invariantFloor)
	}

	problems = append(problems, checkInvariants(invariants, declaredTests(t), unenforced)...)
	for _, p := range problems {
		t.Errorf("%s:%d: %s", invariantsDoc, p.line, p.why)
	}

	t.Logf("checked %d invariants, %d excused with a written reason",
		len(invariants), len(unenforced))
}

// TestNoInvariantIsWrittenTwice keeps the ledger addressable.
//
// Both this gate and the ledger key on the rule's own words, so two invariants
// sharing a title would make one of them unexcusable and the other excused by
// an entry that does not describe it.
func TestNoInvariantIsWrittenTwice(t *testing.T) {
	invariants, _ := parseInvariants(readFile(t, invariantsDoc))

	seen := map[string]int{}
	for _, inv := range invariants {
		if first, dup := seen[inv.title]; dup {
			t.Errorf("%s:%d: %q also appears at line %d; two invariants with one "+
				"title cannot be excused or cited apart",
				invariantsDoc, inv.line, inv.title, first)
			continue
		}
		seen[inv.title] = inv.line
	}
}

// TestTheInvariantGateCanFail drives the parser and the checker over documents
// built to break each rule, because a gate nobody has watched fail is a gate
// nobody has watched.
//
// Every case here is a real edit somebody will make: renaming a test without
// touching the document, adding an invariant and forgetting the citation,
// writing the rule down with nothing behind it, and paying off a ledger entry
// without deleting it.
func TestTheInvariantGateCanFail(t *testing.T) {
	known := map[string]bool{"TestSomethingReal": true}
	ledger := map[string]string{"An unenforced rule.": "nothing asserts it yet"}

	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "a citation naming no test",
			doc:  "- **A rule.** Prose. **Enforced by:** `TestLongSinceRenamed`.",
			want: "not a test function",
		},
		{
			name: "an invariant with no citation line at all",
			doc:  "- **A rule.** Prose, and nothing about what enforces it.",
			want: "carries no",
		},
		{
			name: "an unenforced invariant that is not in the ledger",
			doc:  "- **A rule.** Prose. **Enforced by:** nothing yet.",
			want: "names no test",
		},
		{
			name: "a ledger entry that was paid off and left standing",
			doc:  "- **An unenforced rule.** Prose. **Enforced by:** `TestSomethingReal`.",
			want: "delete its entry",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invariants, problems := parseInvariants(tc.doc)
			problems = append(problems, checkInvariants(invariants, known, ledger)...)

			var found bool
			for _, p := range problems {
				found = found || strings.Contains(p.why, tc.want)
			}
			if !found {
				t.Errorf("this document should have been refused for %q and %d "+
					"problems came back: %v", tc.want, len(problems), problems)
			}
		})
	}

	// The control: a document that breaks nothing must produce nothing, or
	// every case above would pass for the wrong reason.
	good := "- **A rule.** Prose. **Enforced by:** `TestSomethingReal`.\n" +
		"- **An unenforced rule.** Prose. **Enforced by:** nothing yet."
	invariants, problems := parseInvariants(good)
	if problems = append(problems, checkInvariants(invariants, known, ledger)...); len(problems) > 0 {
		t.Errorf("a document that breaks no rule was refused: %v", problems)
	}
}

// problem is one thing wrong with the document, and the line to say it about.
type problem struct {
	line int
	why  string
}

func (p problem) String() string { return fmt.Sprintf("line %d: %s", p.line, p.why) }

// checkInvariants holds each invariant to naming a test that exists, and the
// ledger to naming invariants that still need it.
func checkInvariants(invariants []invariant, known map[string]bool, ledger map[string]string) []problem {
	var out []problem

	titles := map[string]bool{}
	for _, inv := range invariants {
		titles[inv.title] = true
		reason, excused := ledger[inv.title]

		switch {
		case !inv.cited:
			// The parser already said this bullet names nothing at all, and
			// saying it twice about one line reads as two defects.
		case len(inv.tests) > 0 && excused:
			out = append(out, problem{inv.line, fmt.Sprintf(
				"%q cites %s now, so delete its entry from unenforced, or the "+
					"ledger reads as debt that was already paid",
				inv.title, strings.Join(inv.tests, ", "))})
		case len(inv.tests) == 0 && !excused:
			out = append(out, problem{inv.line, fmt.Sprintf(
				"%q names no test. Add the test and cite it, or add the title "+
					"to unenforced with the reason you cannot", inv.title)})
		case len(inv.tests) == 0 && strings.TrimSpace(reason) == "":
			out = append(out, problem{inv.line, fmt.Sprintf(
				"%q is excused with an empty reason; an exemption with no "+
					"argument is the drift it exists to prevent", inv.title)})
		}

		for _, name := range inv.tests {
			if !known[name] {
				out = append(out, problem{inv.line, fmt.Sprintf(
					"%q cites %s, which is not a test function in this tree. A "+
						"rename is what this gate is for: cite the new name, or "+
						"say what replaced the assertion", inv.title, name)})
			}
		}
	}

	for _, title := range slices.Sorted(maps(ledger)) {
		if !titles[title] {
			out = append(out, problem{0, fmt.Sprintf(
				"unenforced names %q and no invariant has that title, so it was "+
					"reworded or removed", title)})
		}
	}

	return out
}

// bulletStart matches the line an invariant begins on, and nothing about what
// it says. The title is matched separately, against the joined block, because a
// title long enough to wrap closes its bold on the next line: requiring the
// closing pair here dropped five invariants silently, which is the failure this
// whole document exists to complain about.
var bulletStart = regexp.MustCompile(`(?m)^- \*\*`)

// bulletTitle reads the rule's own words out of a joined block.
var bulletTitle = regexp.MustCompile(`^- \*\*(.+?)\*\*`)

// citedInvariantTest matches a backticked test or fuzz function name.
//
// The backticks are required here and not in securityclaims_test.go because
// this document names types and functions in the same sentence as its
// citations: `Clause.Predicates` is prose and `TestPredicatesRender` is
// evidence.
var citedInvariantTest = regexp.MustCompile("`((?:Test|Fuzz)[A-Za-z0-9_]*)`")

// parseInvariants reads the document into one entry per bullet, and reports the
// bullets that are not usable as entries.
//
// A bullet runs from its own line to the next bullet or heading, because the
// prose wraps and the citation is usually on the last line of it.
func parseInvariants(doc string) ([]invariant, []problem) {
	var (
		found    []invariant
		problems []problem
		block    []string
		startAt  int
	)

	flush := func() {
		if len(block) == 0 {
			return
		}
		inv, p := parseBullet(strings.Join(block, " "), startAt)
		found = append(found, inv)
		problems = append(problems, p...)
		block = nil
	}

	for i, line := range strings.Split(doc, "\n") {
		switch {
		case bulletStart.MatchString(line):
			flush()
			block = []string{strings.TrimSpace(line)}
			startAt = i + 1
		case strings.HasPrefix(line, "#"), strings.HasPrefix(line, "- "):
			// A heading ends the bullet, and so does a list item that is not an
			// invariant: the "Keeping this current" list is prose about the
			// document rather than a rule the tree has to hold to.
			flush()
		case len(block) > 0:
			block = append(block, strings.TrimSpace(line))
		}
	}
	flush()

	return found, problems
}

// parseBullet turns one joined bullet into an invariant, or says why it is not
// one.
//
// A bullet with no "Enforced by" sentence is a problem here rather than a
// silent skip, because forgetting the sentence and having nothing to cite are
// the same thing to a reader and different things to this gate.
func parseBullet(block string, line int) (invariant, []problem) {
	inv := invariant{title: strings.TrimSpace(block), line: line}
	if title := bulletTitle.FindStringSubmatch(block); title != nil {
		inv.title = strings.TrimSpace(title[1])
	} else {
		return inv, []problem{{line, fmt.Sprintf(
			"%.60q opens an invariant whose bold title never closes, so it has "+
				"no words to cite or excuse it by", block)}}
	}

	_, claim, found := strings.Cut(block, enforcedBy)
	if !found {
		return inv, []problem{{line, fmt.Sprintf(
			"%q carries no %q sentence. Every invariant names its test, or says "+
				"%q and goes in the unenforced ledger", inv.title, enforcedBy, noTest)}}
	}

	inv.cited = true
	if strings.Contains(claim, noTest) {
		return inv, nil
	}
	for _, m := range citedInvariantTest.FindAllStringSubmatch(claim, -1) {
		inv.tests = append(inv.tests, m[1])
	}
	return inv, nil
}

// The declarations themselves come from declaredTests in
// securityclaims_test.go, which already walks the tree for exactly this and
// carries its own floor. Two walks with two floors would be two things to keep
// in step, and the second to drift would be the one nobody reads.
