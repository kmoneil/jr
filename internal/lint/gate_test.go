package lint_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ungated lists every package that invokes a command without calling
// registry.Gate first, together with the reason it is allowed to.
//
// It is empty, and an entry is a decision rather than an oversight: a caller
// that runs commands and does not gate them is one where `--readonly` and
// `--yes` mean nothing, so the reason has to say why that is acceptable there.
var ungated = map[string]string{}

// TestEveryCallerOfACommandGatesIt holds the refusal that no caller may skip.
//
// registry.Gate turns two declarations into refusals — Destructive needs --yes,
// Mutating is refused in read-only mode — and it is the whole of §6's promise to
// an autonomous caller. It used to be a method on the CLI's app, called from
// runLeaf and nowhere else, which was correct for exactly as long as the CLI was
// the only thing that ran a command.
//
// It was not. `internal/mcp` binds arguments and calls Command.Run directly, so
// all 20 mutating and 4 destructive commands in the agent profile ran ungated
// there: a context created --readonly sent a real DELETE and the tool reply said
// isError false, verified against a server before any of this moved.
//
// So this test is not about the two callers that exist. It is about the third.
// At two, remembering works; the failure mode is the caller added by someone who
// reasonably assumes the layer above already refused — which is precisely the
// assumption that was true of the CLI and false of MCP.
//
// It parses without build constraints on purpose, the way
// TestEveryGoroutineReachableFromARequestRecovers does. `internal/mcp` is behind
// the `mcp` tag, and a check that honoured tags would sweep green over the one
// caller that had the defect.
func TestEveryCallerOfACommandGatesIt(t *testing.T) {
	const root = ".."
	fset := token.NewFileSet()

	// invokers are packages that call Command.Run or Command.Stream; gaters are
	// packages that call registry.Gate. Both keyed by directory, because the
	// gate and the invocation are routinely in different files of one package —
	// in internal/cli they are gate.go and root.go.
	invokers := map[string][]string{}
	gaters := map[string]bool{}

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
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		dir := filepath.ToSlash(filepath.Dir(path))

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case isGateCall(sel):
				gaters[dir] = true
			case isCommandInvocation(sel, call):
				// Position already carries the filename; prefixing path too is
				// how the message ended up naming the file twice.
				invokers[dir] = append(invokers[dir], fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(invokers) == 0 {
		// A sweep that matched nothing is a sweep that asserted nothing, and
		// this one matches on a call shape that a refactor could rename.
		t.Fatal("no command invocations found; the matcher has stopped seeing " +
			"Command.Run and Command.Stream, so this test is checking nothing")
	}

	for _, dir := range sortedDirs(invokers) {
		if gaters[dir] {
			continue
		}
		if reason, exempt := ungated[dir]; exempt {
			t.Logf("%s runs commands without gating them: %s", dir, reason)
			continue
		}
		t.Errorf("%s invokes a command and never calls registry.Gate\n"+
			"  call sites: %s\n"+
			"  a caller that skips the gate is one where --readonly and --yes "+
			"mean nothing; call registry.Gate before Validate and before any "+
			"network call, or add %q to ungated with the reason",
			dir, strings.Join(invokers[dir], ", "), dir)
	}
}

// isGateCall reports whether a selector is registry.Gate, or Gate called from
// inside registry itself.
func isGateCall(sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != "Gate" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "registry"
}

// isCommandInvocation reports whether a call looks like Command.Run or
// Command.Stream.
//
// Matched on shape rather than on type, because resolving types would mean a
// go/packages dependency this module does not have and does not otherwise need.
// The shape is narrow enough: Run and Stream are the only two fields a command
// is invoked through, both take a context plus an invocation, and the two other
// Run methods a Go tree usually has — exec.Cmd.Run and cobra's — take none and
// one respectively. If that stops being true, the empty-result check above fires
// rather than this passing quietly.
func isCommandInvocation(sel *ast.SelectorExpr, call *ast.CallExpr) bool {
	if sel.Sel.Name != "Run" && sel.Sel.Name != "Stream" {
		return false
	}
	return len(call.Args) >= 2
}

func sortedDirs(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
