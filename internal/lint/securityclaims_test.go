package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// securityDoc is the document this file exists for.
const securityDoc = "../../SECURITY.md"

// citedTest matches a test or fuzz target named in prose: "held there by
// `TestNothingShippedExecutesAProcess`".
var citedTest = regexp.MustCompile(`\b((?:Test|Fuzz)[A-Z][A-Za-z0-9]*)`)

// declaredTest matches the declaration of one, in any file, under any build
// tag. A tagged file is read rather than compiled here on purpose: SECURITY.md
// cites tests behind `write` and `mcp`, and a walk that could only see the
// default build would report half of them missing.
var declaredTest = regexp.MustCompile(`(?m)^func ((?:Test|Fuzz)[A-Za-z0-9_]*)\(`)

// TestEverySecurityClaimNamesATestThatExists holds SECURITY.md to its own last
// paragraph: every claim in it names the test that holds it.
//
// A security document is the one place where a stale sentence is worse than no
// sentence, because a reader has no way to check it and every reason to believe
// it. "A server-supplied filename is not written blindly" is either true or a
// vulnerability, and the difference is a test that still exists and still runs
// — not a paragraph that was accurate when it was written.
//
// This checks the cheaper half of that: the named test exists. It cannot check
// that the test still asserts what the sentence claims, which is why the
// document's own "Keeping this current" section asks for the harder half from
// whoever moves one.
func TestEverySecurityClaimNamesATestThatExists(t *testing.T) {
	declared := declaredTests(t)

	cited := map[string]bool{}
	for _, m := range citedTest.FindAllStringSubmatch(readDoc(t, securityDoc), -1) {
		cited[m[1]] = true
	}

	// A document naming nothing would pass, and passing is exactly what it must
	// not do: the claims are the whole point of the file.
	if len(cited) < 20 {
		t.Fatalf("SECURITY.md names %d tests; it used to name far more, so "+
			"either the claims lost their evidence or this pattern stopped "+
			"matching", len(cited))
	}

	var missing []string
	for name := range cited {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("SECURITY.md cites %s as the test holding a security claim, "+
			"and no such test exists. Either it was renamed — in which case "+
			"the document is part of that change — or the claim now rests on "+
			"nothing", name)
	}
}

// declaredTests returns every test and fuzz target in the tree by name.
func declaredTests(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// bin/ is build output and _plans is gitignored working notes.
			if name := d.Name(); name == "bin" || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // walking this module's own tree.
		if readErr != nil {
			return readErr
		}
		for _, m := range declaredTest.FindAllStringSubmatch(string(body), -1) {
			out[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(out) < 500 {
		t.Fatalf("found only %d tests in the whole tree, so this walked the "+
			"wrong one and every citation would look missing", len(out))
	}
	return out
}
