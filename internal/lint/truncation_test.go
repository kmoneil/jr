package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// inlineTruncation matches a command deciding for itself whether a result set
// survived whole, by comparing what it holds against --limit.
//
// The spelling is deliberately loose — any comparison of a length against
// inv.Limit.N — because the defect is not a particular four lines. It is a
// command answering "is this complete" on its own, and the shapes that answer
// wrongly do not all look alike.
var inlineTruncation = regexp.MustCompile(`len\([^)]*\)\s*[<>=!]+\s*inv\.Limit\.N`)

// TestOnlyOnePlaceDecidesWhetherAResultIsComplete keeps the fetch-all listings
// answering that question with one voice.
//
// Eleven commands had written the same four lines out longhand: declare
// complete, compare the length against --limit, reslice, unset. They agreed,
// which is why nobody minded. Then `user list` wrote a twelfth copy that looked
// the same and was not — it pushed the caller's limit into the request as
// maxResults first, so the comparison ran against a response the server had
// already bounded, could never fire, and reported every truncated search as
// exhaustive. Its declared exit 3 was unreachable for the life of the command.
//
// One implementation is not tidiness here. `complete="false"` or exit 3 is the
// invariant this project exists to hold, and a decision made in twelve places
// is twelve chances to hold it differently. registry.Bound is that place, its
// boundary is pinned by TestBoundIsExactAtTheBoundary, and its doc comment
// carries the one rule a call site can still get wrong: a result the server
// bounded must not come through it.
//
// A resource that genuinely cannot use Bound — because the server, not the
// client, decided how much came back — belongs in serverBoundedResults with the
// reason written down, the way the goroutine allowlist works. That is a
// deliberate exemption rather than a silent one.
func TestOnlyOnePlaceDecidesWhetherAResultIsComplete(t *testing.T) {
	// serverBoundedResults are the commands whose result set is bounded by the
	// server rather than trimmed here, so registry.Bound does not apply. Each
	// must report completeness from evidence in the response instead.
	//
	// Empty today: `user list` was the only one, and it now asks for one row
	// more than the bound and reports whether that row came back, which lives
	// in site.SearchUsers rather than in a comparison at the call site.
	serverBoundedResults := map[string]string{}

	// The whole tree, not just internal/resource. The first version of this
	// test scanned the resources alone and reported clean while `context list`
	// and `jr schema` held two more copies in internal/cli — which is the same
	// mistake the fuzz sweep made when it ran untagged and saw no targets. A
	// check that cannot see part of the tree is a check that passes for it.
	root := filepath.Join(repoRoot, "internal")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// registry is where Bound lives and where the comparison belongs.
		if strings.Contains(filepath.ToSlash(path), "/internal/registry/") {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // a path from the test tree.
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			rel = path
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !inlineTruncation.MatchString(line) {
				continue
			}
			if reason, ok := serverBoundedResults[rel]; ok {
				t.Logf("%s:%d is exempt: %s", rel, i+1, reason)
				continue
			}
			t.Errorf("%s:%d decides completeness by comparing a length against "+
				"--limit:\n\t%s\nUse registry.Bound(inv.Limit, items), which "+
				"returns the trimmed set and the verdict together. If the server "+
				"bounded this result rather than the client trimming it, Bound is "+
				"the wrong answer and the comparison is unreachable — report "+
				"completeness from the response and add an entry to "+
				"serverBoundedResults saying so.", rel, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
