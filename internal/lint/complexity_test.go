package lint_test

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// complexityLimit is the cognitive-complexity score above which a function has
// to justify itself.
//
// 15 is the number every complexity finding in this project has been measured
// against since the first one. It was, until this test existed, a number in
// planning documents and in the habit of running gocognit by hand: nothing in
// the build read it, so "we keep functions under 15" was a claim about what
// somebody would remember to check. Thirty-four functions were over it.
const complexityLimit = 15

// exemption is a function allowed to sit above the limit, with the reason and
// the score it was allowed at.
//
// The score matters as much as the reason. An exemption with no ceiling is a
// function that can grow forever behind an argument made when it was smaller,
// and the argument below is specifically about what is left after a split, not
// about the function being hard.
type exemption struct {
	max int
	why string
}

// exempt is every function over the limit, keyed by file path and name.
//
// Adding an entry is a deliberate act and needs the same thing this one has:
// the score decomposed into what it is made of, and a reason the parts cannot
// go. "It is complicated" is not one. The alternative to an exemption is not
// hiding the complexity in a helper nobody calls twice; a flat dispatch over N
// constructs is not improved by moving N somewhere else.
var exempt = map[string]exemption{
	"internal/adf/inline.go:scanInline": {
		max: 20,
		why: "measured, not estimated, by removing each component and reading " +
			"the difference: 12 points are four `if err != nil` at nesting two, " +
			"5 are the multi-byte guards defining which bytes open which " +
			"construct, 2 the switch, 1 the loop. The 12 is Go's error " +
			"propagation written where the errors happen, and folding the four " +
			"checks together would hide which construct refused; the 5 is " +
			"CommonMark's list of openers, not ours. Split once already, from " +
			"40. See _plans/backlog/done/scaninline-complexity.md",
	},
}

// TestNothingIsMoreComplexThanItsReason holds every function in the tree to the
// limit, and every exception to a written argument.
//
// It runs gocognit over the whole tree rather than per package, and gocognit
// reads source without applying build constraints, so tagged code is included.
// That is deliberate: `make fuzz` once swept past internal/workflow entirely
// because the untagged build reported no targets there, and a sweep that
// cannot see part of the tree passes for it.
func TestNothingIsMoreComplexThanItsReason(t *testing.T) {
	over := runGocognit(t)

	seen := map[string]bool{}
	for _, f := range over {
		if strings.HasSuffix(f.Pos.Filename, "_test.go") {
			// A table-driven test is a list, and its score is the length of the
			// list. Holding one to a limit meant for branching code buys a
			// worse test, not a simpler one.
			continue
		}
		key := f.Pos.Filename + ":" + f.FuncName
		seen[key] = true

		allowed, ok := exempt[key]
		switch {
		case !ok:
			t.Errorf("%s:%d: %s is cognitive %d, over the limit of %d.\n"+
				"    Split it, or add it to exempt in this file with the score "+
				"decomposed and a reason the parts cannot go.",
				f.Pos.Filename, f.Pos.Line, f.FuncName, f.Complexity, complexityLimit)
		case f.Complexity > allowed.max:
			t.Errorf("%s:%d: %s is cognitive %d, above the %d it is exempt at.\n"+
				"    Its exemption says: %s\n"+
				"    Either bring it back under %d, or re-measure and say what "+
				"the new points are made of.",
				f.Pos.Filename, f.Pos.Line, f.FuncName, f.Complexity,
				allowed.max, allowed.why, allowed.max)
		}
	}

	// An exemption for a function that is now under the limit is a stale
	// argument, and a reader who finds one has no way to tell it from a live
	// one. Deleting it is the point of noticing.
	var stale []string
	for key := range exempt {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("%s is exempt from the complexity limit and is no longer over "+
			"it. Delete the entry.", key)
	}
}

// gocognitFunc is one entry of `gocognit -json`.
type gocognitFunc struct {
	PkgName    string
	FuncName   string
	Complexity int
	Pos        struct {
		Filename string
		Line     int
	}
}

// runGocognit reports every function over the limit.
//
// It fails rather than skips when the tool is absent. A gate that quietly does
// not run is worse than no gate, because it reads as coverage: the untagged
// dead-code pass shipped in exactly that state, loading the same eight tags it
// was meant to drop and reporting clean.
func runGocognit(t *testing.T) []gocognitFunc {
	t.Helper()

	cmd := exec.Command("gocognit",
		"-over", strconv.Itoa(complexityLimit), "-json", "./internal", "./cmd")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		// gocognit exits 1 when -over matched something, which is the normal
		// case here and not a failure. Anything it could not do goes to stderr,
		// and an unreadable stdout is caught by the decode below.
		exit, isExit := errors.AsType[*exec.ExitError](err)
		if !isExit {
			t.Fatalf("cannot run gocognit: %v\n"+
				"    install it with: go install "+
				"github.com/uudashr/gocognit/cmd/gocognit@latest\n"+
				"    this gate fails closed rather than skipping, because a "+
				"complexity check that did not run reads exactly like one that passed.",
				err)
		}
		if len(exit.Stderr) > 0 {
			t.Fatalf("gocognit failed: %v\n%s", err, exit.Stderr)
		}
	}

	var funcs []gocognitFunc
	if err := json.Unmarshal(out, &funcs); err != nil {
		t.Fatalf("cannot read gocognit's output: %v\n%s", err, out)
	}
	for i, f := range funcs {
		// Reported relative to the working directory, which is the module root.
		funcs[i].Pos.Filename = path.Clean(f.Pos.Filename)
	}
	return funcs
}
