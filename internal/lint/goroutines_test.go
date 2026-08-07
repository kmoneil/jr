package lint_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recovered lists every goroutine in non-test source, by the function that
// starts it, together with the reason it is allowed to exist.
//
// A goroutine is on this list because somebody decided it recovers. Adding one
// without adding an entry fails the test, which is the point: the decision has
// to be made rather than inherited.
var recovered = map[string]string{
	"multipartSource": "writeMultipartBody recovers and fails the upload via " +
		"CloseWithError; asserted by TestAPanicInTheBodyProducerFailsTheUploadNotTheProcess",
}

// TestEveryGoroutineReachableFromARequestRecovers holds the one rule that
// cannot be enforced from outside a goroutine.
//
// A panic in a goroutine is not caught by a recover anywhere else. Not by the
// one in mcp.Server.dispatch, not by one in Command.Run, and there is no
// equivalent of net/http's per-connection recovery that reaches it — the
// process dies, and under `mcp serve` that is the session rather than the call.
// So the guarantee has to be given inside each goroutine, and the only way to
// know it was is to enumerate them.
//
// When this was written there was exactly one goroutine in non-test source. The
// test exists so a second cannot arrive unnoticed: at one, remembering works;
// the failure mode is the fifth one, added by someone who reasonably assumed
// the handler above it recovers.
//
// It parses without build constraints on purpose. The only goroutine in the
// tree is behind `//go:build write`, and a check that honoured tags would have
// reported a clean sweep over code it could not see — the same way `make fuzz`
// once swept green over `internal/workflow`.
func TestEveryGoroutineReachableFromARequestRecovers(t *testing.T) {
	const root = ".."
	fset := token.NewFileSet()
	seen := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)

		// The enclosing function is what the allowlist names, so a `go` inside
		// a closure is attributed to the declaration that contains it rather
		// than to an anonymous position nobody can write down.
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				stmt, isGo := n.(*ast.GoStmt)
				if !isGo {
					return true
				}
				seen[fn.Name.Name] = true
				if _, allowed := recovered[fn.Name.Name]; !allowed {
					t.Errorf("%s:%d starts a goroutine in %s, which is not in "+
						"the recovered list.\n"+
						"A panic in a goroutine ends the process — no recover "+
						"outside it can help, and under `mcp serve` that is the "+
						"session. Give it its own recover, then add it to "+
						"`recovered` with the test that proves it.",
						rel, fset.Position(stmt.Pos()).Line, fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A stale entry is the other half. An allowlist that outlives the code it
	// describes reads as coverage and is not.
	for name := range recovered {
		if !seen[name] {
			t.Errorf("`recovered` names %s, which starts no goroutine any more. "+
				"Remove the entry.", name)
		}
	}
	if len(seen) == 0 {
		t.Error("no goroutines found in non-test source; this test scanned nothing")
	}
}

// TestTheGoroutineListIsSorted keeps the failure message useful by keeping the
// list readable. It is cosmetic and cheap.
func TestTheGoroutineListIsSorted(t *testing.T) {
	names := make([]string, 0, len(recovered))
	for name := range recovered {
		names = append(names, name)
	}
	if !slices.IsSorted(names) {
		slices.Sort(names)
		t.Errorf("keep `recovered` in order: %v", names)
	}
}
