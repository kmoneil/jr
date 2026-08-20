package lint_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/buildinfo"
)

// internalPrefix is the only import path a file invisible to the full-tags
// build may name. Everything else, third-party and standard library alike, is
// something a scanner would want to see.
const internalPrefix = module + "/internal/"

// negatedFileImports excuses a file the full-tags build cannot compile from
// importing only internal packages, with the reason.
//
// It is empty, and that is the resting state rather than an achievement.
// Paying for an entry here means deciding that `make vuln` scans one build and
// cannot see this import, which is a decision and not a formality: the
// alternative is to loop the four tag sets in that target, at four times the
// slowest thing in `make ci` and the only one that needs the network.
//
// An entry whose file stops being invisible fails the test rather than sitting
// here as an excuse for something that no longer exists, which is the rule
// notYetGating and writeGatedNonMutations already follow in tags_test.go.
var negatedFileImports = map[string]string{}

// TestWhatAFullTagsScanCannotSeeImportsNothing is the assertion behind a
// sentence in the Makefile that was wrong for as long as anybody had read it.
//
// `make vuln` scans once, at TAGS_FULL, and the comment justifying that said
// there are no negated build constraints in this tree, so the full tag set is a
// superset of every shipped profile and one pass covers all four. There are
// four such files and three of them ship: presentational_absent.go behind
// `!render`, prompt_absent.go behind `!prompt`, partial_noop.go behind
// `!write`. No full-tags build compiles any of them, so one pass is not four.
//
// The scan is nevertheless right, and this is the property that makes it right:
// those files are a const, a refusal, and a no-op, and between them they import
// `internal/errs` and `internal/registry` and nothing else. A vulnerability
// lives in a dependency or in the standard library, and code that reaches
// neither cannot carry one.
//
// That was true when the comment was rewritten and nothing kept it true. It is
// the same failure the original sentence had, one layer down, which is why the
// correction is not worth having without this.
//
// It asks the toolchain which files a build contains rather than grepping for
// `//go:build !`, for the reason filesPerTag already gives: a constraint can be
// written several ways, and a file can become invisible to the full build
// without a `!` anywhere in it.
func TestWhatAFullTagsScanCannotSeeImportsNothing(t *testing.T) {
	full := goFiles(t, buildinfo.KnownTags)

	// The union over the shipped profiles, read from the Makefile so that a
	// profile added there is covered without a second list to keep in step.
	invisible := map[string]bool{}
	for _, p := range profilesFromMakefile(t) {
		var tags []string
		if p.tags != "" {
			tags = strings.Split(p.tags, ",")
		}
		for file := range goFiles(t, tags) {
			if !full[file] {
				invisible[file] = true
			}
		}
	}

	// Not a formality. A test that finds nothing to check passes, and this one
	// would then be reporting that `make vuln` is right for a reason that has
	// stopped applying. If the set empties, the full tag set really is a
	// superset again and the Makefile comment saying there are four such files
	// is wrong in the other direction, which is the state this whole card
	// exists to get out of.
	if len(invisible) == 0 {
		t.Fatal("no shipped file is invisible to the full-tags build, so the " +
			"vuln target's comment describing three of them is stale; one pass " +
			"now genuinely covers four and the comment should say so")
	}

	for _, file := range sorted(invisible) {
		reason, excused := negatedFileImports[file]
		if excused {
			if reason == "" {
				t.Errorf("%s is excused with no reason", file)
			}
			continue
		}
		for _, path := range importsOf(t, file) {
			if !strings.HasPrefix(path, internalPrefix) {
				t.Errorf("%s imports %q and no full-tags build compiles it, so "+
					"`make vuln` scans a tree that does not contain this import; "+
					"move the code, add the file to negatedFileImports with a "+
					"reason, or loop the tag sets in that target", file, path)
			}
		}
	}

	for file := range negatedFileImports {
		if !invisible[file] {
			t.Errorf("negatedFileImports excuses %s, which the full-tags build "+
				"now compiles; the scan sees it and the entry is stale", file)
		}
	}
}

// importsOf returns the import paths of one module-relative file.
func importsOf(t *testing.T, file string) []string {
	t.Helper()

	path := filepath.Join(repoRoot, filepath.FromSlash(file))
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	out := make([]string, 0, len(parsed.Imports))
	for _, spec := range parsed.Imports {
		unquoted, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("%s: import path %s is not a quoted string: %v",
				file, spec.Path.Value, err)
		}
		out = append(out, unquoted)
	}
	return out
}

// sorted returns the keys of a set in a stable order, so a failure names the
// same file first on every run.
func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
