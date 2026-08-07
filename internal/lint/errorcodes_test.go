package lint_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
)

// contractDoc publishes the error codes and the exit each one produces.
const contractDoc = "../../docs/output-contract.md"

// ctorExit maps each errs constructor to the exit it produces, by asking it
// rather than by repeating the table in errs.go. A constructor whose exit
// changed would otherwise leave this test asserting the old contract while the
// binary shipped the new one — which is the exact failure it exists to catch.
var ctorExit = map[string]exitcode.Code{
	"Usage":      errs.Usage("", "").Exit,
	"Runtime":    errs.Runtime("", "").Exit,
	"Auth":       errs.Auth("", "").Exit,
	"NotFound":   errs.NotFound("", "").Exit,
	"Permission": errs.Permission("", "").Exit,
	"Conflict":   errs.Conflict("", "").Exit,
	"RateLimit":  errs.RateLimit("", "").Exit,
	"Remote":     errs.Remote("", "").Exit,
	"Blocked":    errs.Blocked("", "").Exit,
}

// TestTheDocumentedErrorCodesAreTheOnesTheBinaryProduces holds the contract's
// error tables to the source.
//
// A consumer pins a code and the exit beside it; publishing them is the whole
// point of those tables. Nothing read them, and they had gone wrong in two
// places — OFF_SITE_URL documented at exit 1 and built with errs.Remote, which
// is 9 and also publishes the refusal as retryable, and INVALID_FIELD produced
// at two different exits depending on which layer noticed the bad field.
//
// Both were found by scripting this comparison once, by hand, while editing a
// neighbouring row. This is that script, kept.
//
// It asserts the documented rows and not the reverse. The tables are a curated
// set — 34 of the 200-odd codes this tree can emit — so a code that is not in
// them is not thereby undocumented; it is one the contract does not promise.
// What must hold is that everything the contract does promise is true.
func TestTheDocumentedErrorCodesAreTheOnesTheBinaryProduces(t *testing.T) {
	documented := errorCodesFromDoc(t)
	emitted := codeSites(t)

	for _, code := range slices.Sorted(maps(documented)) {
		want := documented[code]
		sites, ok := emitted[code]
		if !ok {
			t.Errorf("%s documents %s at exit %d, and nothing emits it",
				contractDoc, code, want)
			continue
		}
		for _, got := range slices.Sorted(maps(sites)) {
			if got == want {
				continue
			}
			t.Errorf("%s documents %s at exit %d (%s) and %s produces exit %d (%s)",
				contractDoc, code, want, want.Name(),
				strings.Join(sites[got], ", "), got, got.Name())
		}
		if len(sites) > 1 {
			t.Errorf("%s is one code with %d exits; a caller branching on the "+
				"exit gets a different answer depending on how deep the input "+
				"got before something noticed", code, len(sites))
		}
	}
}

// maps yields the keys of a map, which is all slices.Sorted needs.
func maps[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// docErrorRow matches a row of an error table: `| `OFF_SITE_URL` | 1 | … |`.
//
// The exit-code table earlier in the same document is `| 5 | `NOT_FOUND` | … |`
// — number first, name second — so it does not match, which is deliberate. That
// table is frozen in internal/exitcode/exitcode_test.go and is a different
// claim.
var docErrorRow = regexp.MustCompile("^\\|\\s*`([A-Z_]+)`\\s*\\|\\s*(\\d+)\\s*\\|")

func errorCodesFromDoc(t *testing.T) map[string]exitcode.Code {
	t.Helper()

	out := map[string]exitcode.Code{}
	for _, line := range readLines(t, contractDoc) {
		m := docErrorRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s: %q has an unreadable exit", contractDoc, line)
		}
		if prev, dup := out[m[1]]; dup && prev != exitcode.Code(n) {
			t.Errorf("%s documents %s at both exit %d and exit %d",
				contractDoc, m[1], prev, n)
		}
		out[m[1]] = exitcode.Code(n)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no error table found; this test reads them by shape, so a "+
			"reformatted table silently asserts nothing", contractDoc)
	}
	return out
}

// codeSites reports, for every code an errs constructor is called with, each
// exit it can produce and the files that produce it.
//
// It reads the source rather than the running binary because no registry of
// codes exists to ask: they are string literals at their call sites, spread
// across every package. Build tags are ignored on purpose — the contract
// documents what this tool can emit, not what one profile can.
func codeSites(t *testing.T) map[string]map[exitcode.Code][]string {
	t.Helper()

	out := map[string]map[exitcode.Code][]string{}
	for _, dir := range goPackageDirs(t) {
		fset := token.NewFileSet()
		files := parsePackage(t, fset, dir)

		// Package scope, not file scope: idem's `conflictExit` is declared in
		// lock.go and used in idem.go, and a file-local lookup would miss it
		// and quietly cover one code less than it reports.
		scope := packageScope(files)

		for name, file := range files {
			ast.Inspect(file, func(n ast.Node) bool {
				code, exit, ok := errsCall(n, scope)
				if !ok {
					return true
				}
				if out[code] == nil {
					out[code] = map[exitcode.Code][]string{}
				}
				rel := filepath.Join(dir, filepath.Base(name))
				if !slices.Contains(out[code][exit], rel) {
					out[code][exit] = append(out[code][exit], rel)
				}
				return true
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("no errs constructor calls found; this test read nothing")
	}
	return out
}

// errsCall reports the code and exit of one `errs.X(...)` call, if the node is
// one.
func errsCall(n ast.Node, scope map[string]ast.Expr) (string, exitcode.Code, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return "", 0, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", 0, false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "errs" {
		return "", 0, false
	}

	// New carries the exit as its first argument; every other constructor
	// fixes one, and takes the code first.
	codeArg := 0
	exit, fixed := ctorExit[sel.Sel.Name]
	if sel.Sel.Name == "New" {
		codeArg, fixed = 1, true
		if len(call.Args) == 0 {
			return "", 0, false
		}
		var known bool
		if exit, known = exitOf(call.Args[0], scope); !known {
			return "", 0, false
		}
	}
	if !fixed || len(call.Args) <= codeArg {
		return "", 0, false
	}
	code, ok := stringOf(call.Args[codeArg], scope)
	if !ok {
		return "", 0, false
	}
	return code, exit, true
}

// exitIdents maps the Go spelling of an exit code to its value. It is derived
// from the package rather than listed, so a new code needs no change here:
// exitcode.NotFound is the identifier and NOT_FOUND is the name.
var exitIdents = func() map[string]exitcode.Code {
	out := map[string]exitcode.Code{}
	for _, c := range exitcode.All() {
		out[strings.ReplaceAll(c.Name(), "_", "")] = c
	}
	return out
}()

// exitOf resolves an expression naming an exit code.
func exitOf(e ast.Expr, scope map[string]ast.Expr) (exitcode.Code, bool) {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		c, ok := exitIdents[strings.ToUpper(v.Sel.Name)]
		return c, ok
	case *ast.Ident:
		// A local alias, e.g. `const conflictExit = exitcode.Conflict`.
		if decl, ok := scope[v.Name]; ok {
			return exitOf(decl, scope)
		}
	}
	return 0, false
}

// stringOf resolves an expression to a string constant.
func stringOf(e ast.Expr, scope map[string]ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		// A named code, e.g. `const BudgetExceededCode = "…"`.
		if decl, ok := scope[v.Name]; ok {
			return stringOf(decl, scope)
		}
	}
	return "", false
}

// packageScope collects the package-level const and var values of one package.
func packageScope(files map[string]*ast.File) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i < len(value.Values) {
						out[name.Name] = value.Values[i]
					}
				}
			}
		}
	}
	return out
}

func parsePackage(t *testing.T, fset *token.FileSet, dir string) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join("../..", dir))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join("../..", dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		out[name] = file
	}
	return out
}

// skipDirsForCodes are not this module's shipped source.
var skipDirsForCodes = []string{".git", ".github", "_plans", "_reviews", "bin", "docs", "scripts", "testdata"}

// goPackageDirs lists the module-relative directories holding shipped Go files.
func goPackageDirs(t *testing.T) []string {
	t.Helper()

	var out []string
	for _, root := range []string{"internal", "cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join("../..", root),
			func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					return nil
				}
				if slices.Contains(skipDirsForCodes, d.Name()) {
					return filepath.SkipDir
				}
				rel, relErr := filepath.Rel("../..", path)
				if relErr != nil {
					return relErr
				}
				out = append(out, rel)
				return nil
			})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}
