package lint_test

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"

	// Every resource, so the coverage check below can hold the source sweep to
	// the command set the binary actually registers.
	_ "github.com/kmoneil/jira-cli/internal/commands"
)

// modulePath prefixes every import that resolves to a package in this tree.
const modulePath = "github.com/kmoneil/jira-cli/"

// entryPoints are the fields of a registry.Command that hold code the CLI runs
// on the caller's behalf, and whose failures are therefore that command's
// failures.
var entryPoints = []string{"Validate", "Run", "Stream"}

// TestEveryExitACommandCanReachIsDeclared holds each command's declared exit
// codes to the errs constructors its own code can reach.
//
// `jr schema` publishes, per command, every exit it can produce, and §7 exists
// so an agent can branch on that set without reading documentation. What
// checked it read one declared capability at a time: contract_test.go asserts
// that a mutating command declares Blocked and a paginated one declares
// Partial, and errorcodes_test.go asserts that the documented error table
// agrees with the source. Nothing read the code to see what it could actually
// exit with.
//
// It deliberately does not single out exit 2. The finding that prompted it —
// twenty-one commands carry a Validate, whose whole job is to refuse, and do
// not list exitcode.Usage — was reading the declaration and calling it the
// contract. Command.ExitCodes holds what a command adds *beyond* the universal
// 0, 1, and 2, and AllExitCodes, which is what `jr schema` and
// docs/commands.md both render, puts them back for every command. Nothing was
// under-published and adding Usage to those twenty-one would have changed no
// byte of output. What the finding had right was the direction, so this test
// makes that claim for all ten codes; Usage comes out of it for free by being
// universal.
//
// It starts at each entry point, follows every call it can resolve statically,
// and collects the exit of each errs constructor it finds.
//
// What it cannot follow is a method call: the receiver's type is not knowable
// without a full type check, so a call through registry.Session, or on a
// resource Client, ends the walk. That is a floor and not a ceiling — every
// exit reported here is one the command really can produce, and an exit that
// only appears past a method boundary is missed rather than invented. A gate
// that under-reports still catches what it sees; one that over-reports would
// have to be silenced, and a silenced gate is the one that was never true.
func TestEveryExitACommandCanReachIsDeclared(t *testing.T) {
	idx := indexSource(t)
	cmds := idx.commands(t)

	for _, c := range cmds {
		t.Run(c.name, func(t *testing.T) {
			declared := c.declaredExits(t, idx)
			for _, field := range entryPoints {
				entry, ok := c.fields[field]
				if !ok {
					continue
				}
				reached := idx.exitsFrom(c.dir, c.file, entry)
				for _, code := range slices.Sorted(maps(reached)) {
					if slices.Contains(declared, code) {
						continue
					}
					t.Errorf("%s can exit %d (%s) and does not declare it\n"+
						"\treached from %s via %s\n"+
						"\tdeclared: %s",
						c.name, code, code.Name(), field, reached[code],
						formatExits(declared))
				}
			}
		})
	}
}

// requestExits are the exits any request to a Jira site can produce, whatever
// it asked for.
//
// Each is a failure of the connection rather than of the endpoint: a rejected
// or expired credential answers 401 to anything, an account without the
// permission answers 403 to anything, a throttled account answers 429 to
// anything, and a broken instance answers 5xx to anything. A caller can branch
// on all four without knowing which command produced them, which is why every
// command that sends a request declares them.
//
// Conflict is deliberately not here. transport.Err does map 409 and 412 to it
// for any request, but a conflict is a statement about a resource's state, so
// it is declared by the commands whose semantics can produce one rather than by
// all forty that read.
var requestExits = []exitcode.Code{
	exitcode.Auth, exitcode.Permission, exitcode.RateLimit, exitcode.Remote,
}

// sessionWithoutRequests names the commands that are given a session and never
// send a request, with the reason each does not.
//
// The list is the point, like jqlReportsRatherThanRefuses in internal/cli: a
// command that quietly stopped talking to Jira cannot fall out of the check
// without somebody writing down why.
var sessionWithoutRequests = map[string]string{
	"jql.explain": "it takes a session for the resolved context — the default " +
		"project its fragment would be scoped by — and asks Jira nothing. " +
		"Declaring the request exits would publish four codes it cannot " +
		"produce, which is the defect user list's unreachable Partial was",
}

// TestEveryCommandThatReachesJiraDeclaresTheSiteExits holds every command that
// takes a session to the exits that come with having one.
//
// This is the uniform half of the claim above, and the same shape as
// contract_test.go's "a mutating command declares Blocked" and "a paginated one
// declares Partial": a capability the command declares implies the exits that
// capability can produce. Static reachability stops at the first method call,
// so which commands are seen to reach transport.Err is an accident of how their
// call chain happens to be written; whether a command takes a session at all is
// not, and NeedsJira already says.
//
// NotFound is required of every one of them, including the exempt. Taking a
// session means resolving a context, and `--context nope` is UNKNOWN_CONTEXT at
// exit 5 before a byte goes anywhere — which is how `jql explain`, whose whole
// argument for exemption is that it asks Jira nothing, was found publishing an
// empty set while exiting 5 on a name it could not find. Sending a request is
// what adds the other four.
func TestEveryCommandThatReachesJiraDeclaresTheSiteExits(t *testing.T) {
	for _, c := range cli.Registry().All() {
		reason, exempt := sessionWithoutRequests[c.Name()]
		if !c.NeedsJira {
			if exempt {
				t.Errorf("%s is exempt from the request exits as a command that "+
					"takes a session and sends nothing, and it does not take "+
					"a session at all: %s", c.Name(), reason)
			}
			continue
		}
		declared := c.AllExitCodes()

		if !slices.Contains(declared, exitcode.NotFound) {
			t.Errorf("%s takes a session and does not declare exit %d (%s)\n"+
				"\tresolving the context can fail with UNKNOWN_CONTEXT before "+
				"anything is sent", c.Name(), exitcode.NotFound, exitcode.NotFound.Name())
		}
		for _, code := range requestExits {
			switch {
			case exempt && slices.Contains(declared, code):
				t.Errorf("%s declares exit %d (%s) and is exempt from the "+
					"request exits because %s; one of the two is wrong",
					c.Name(), code, code.Name(), reason)
			case !exempt && !slices.Contains(declared, code):
				t.Errorf("%s sends requests and does not declare exit %d (%s)\n"+
					"\tany request can fail this way; see requestExits",
					c.Name(), code, code.Name())
			}
		}
	}
}

// TestTheExitSweepSeesEveryCommand holds the source sweep to the registry.
//
// The sweep reads registry.Command literals out of the AST, so a command
// assembled some other way — built by a helper, or with a computed Path —
// would be absent from it and would pass by not being looked at. That is the
// failure mode of every source-reading gate in this package, and the one worth
// a test of its own: a sweep that ran nothing reports the same green as a sweep
// that found nothing wrong.
func TestTheExitSweepSeesEveryCommand(t *testing.T) {
	idx := indexSource(t)

	swept := map[string]bool{}
	for _, c := range idx.commands(t) {
		swept[c.name] = true
	}
	for _, c := range cli.Registry().All() {
		if !swept[c.Name()] {
			t.Errorf("%s is registered and the exit sweep did not find its "+
				"registry.Command literal; the sweep is blind to it", c.Name())
		}
	}
}

// commandLiteral is one registry.Command declaration, as it appears in source.
type commandLiteral struct {
	// name is the dotted command name, from Path.
	name string
	// dir and file locate the declaration, which is where a call to an
	// unqualified function resolves from.
	dir  string
	file string
	// exitCodes is the expression assigned to ExitCodes, unresolved.
	exitCodes ast.Expr
	// fields holds the entry-point expressions this declaration sets.
	fields map[string]ast.Expr
}

// declaredExits resolves the ExitCodes expression, plus the universal codes
// every command carries.
func (c *commandLiteral) declaredExits(t *testing.T, idx *sourceIndex) []exitcode.Code {
	t.Helper()

	// From a real Command rather than a list repeated here: AllExitCodes is
	// what `jr schema` renders, so what counts as declared is its answer and
	// not a second opinion about it.
	out := (&registry.Command{}).AllExitCodes()

	if c.exitCodes == nil {
		return out
	}
	codes, ok := idx.exitList(c.dir, c.file, c.exitCodes)
	if !ok {
		t.Fatalf("%s declares its exit codes in a shape this test cannot read; "+
			"a gate that cannot read a declaration is not gating it", c.name)
	}
	out = append(out, codes...)
	slices.Sort(out)
	return slices.Compact(out)
}

func formatExits(codes []exitcode.Code) string {
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		parts = append(parts, strconv.Itoa(c.Int())+" "+c.Name())
	}
	return strings.Join(parts, ", ")
}

// sourceIndex is every shipped package of this module, parsed, with what it
// takes to follow a call from one into another.
type sourceIndex struct {
	pkgs map[string]*sourcePkg
}

type sourcePkg struct {
	dir   string
	name  string
	files map[string]*ast.File
	// scope holds the package's const and var values, for resolving a named
	// exit code or a code string.
	scope map[string]ast.Expr
	// funcs are the package-level functions, by name. Methods are absent
	// because a method call cannot be resolved without type information.
	funcs map[string]sourceFunc
	// imports maps, per file, the local name of an import to the directory it
	// resolves to. Per file rather than per package, because two files may
	// import different packages under the same name.
	imports map[string]map[string]string
}

type sourceFunc struct {
	decl *ast.FuncDecl
	file string
}

// indexSource parses every shipped package.
//
// Build tags are ignored, exactly as internal/lint/errorcodes_test.go ignores
// them: a command's declaration is a fact about the tree and not about one
// profile, and reading only the compiled subset would leave every write verb
// unchecked under the default `make test`.
func indexSource(t *testing.T) *sourceIndex {
	t.Helper()

	fset := token.NewFileSet()
	idx := &sourceIndex{pkgs: map[string]*sourcePkg{}}

	for _, dir := range goPackageDirs(t) {
		files := parsePackage(t, fset, dir)
		if len(files) == 0 {
			continue
		}
		p := &sourcePkg{
			dir:     dir,
			files:   files,
			scope:   packageScope(files),
			funcs:   map[string]sourceFunc{},
			imports: map[string]map[string]string{},
		}
		for name, file := range files {
			p.name = file.Name.Name
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				p.funcs[fn.Name.Name] = sourceFunc{decl: fn, file: name}
			}
		}
		idx.pkgs[dir] = p
	}

	byPath := map[string]*sourcePkg{}
	for dir, p := range idx.pkgs {
		byPath[modulePath+filepath.ToSlash(dir)] = p
	}
	for _, p := range idx.pkgs {
		for name, file := range p.files {
			local := map[string]string{}
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				target, ok := byPath[path]
				if !ok {
					continue
				}
				alias := target.name
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				if alias == "_" || alias == "." {
					continue
				}
				local[alias] = target.dir
			}
			p.imports[name] = local
		}
	}
	return idx
}

// commands finds every registry.Command literal in the tree.
func (idx *sourceIndex) commands(t *testing.T) []*commandLiteral {
	t.Helper()

	var out []*commandLiteral
	for _, dir := range slices.Sorted(maps(idx.pkgs)) {
		p := idx.pkgs[dir]
		for _, name := range slices.Sorted(maps(p.files)) {
			ast.Inspect(p.files[name], func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isCommandType(lit.Type) {
					return true
				}
				c := &commandLiteral{dir: dir, file: name, fields: map[string]ast.Expr{}}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					switch {
					case key.Name == "Path":
						c.name = dottedPath(kv.Value)
					case key.Name == "ExitCodes":
						c.exitCodes = kv.Value
					case slices.Contains(entryPoints, key.Name):
						c.fields[key.Name] = kv.Value
					}
				}
				if c.name == "" {
					t.Errorf("%s: a registry.Command literal has no readable Path; "+
						"the sweep cannot name it and so cannot check it",
						filepath.Join(dir, name))
					return true
				}
				out = append(out, c)
				return true
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("no registry.Command literals found; this test read nothing")
	}
	return out
}

// isCommandType reports whether a composite literal's type is registry.Command.
func isCommandType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Command" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "registry"
}

// dottedPath renders a Path literal as the dotted command name.
func dottedPath(e ast.Expr) string {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		s, ok := elt.(*ast.BasicLit)
		if !ok || s.Kind != token.STRING {
			return ""
		}
		v, err := strconv.Unquote(s.Value)
		if err != nil {
			return ""
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, ".")
}

// exitList resolves an expression to a list of exit codes. It reads a slice
// literal, and a call to a package function that returns one — writeExits() is
// how every mutating command declares its set.
func (idx *sourceIndex) exitList(dir, file string, e ast.Expr) ([]exitcode.Code, bool) {
	p := idx.pkgs[dir]
	if p == nil {
		return nil, false
	}
	switch v := e.(type) {
	case *ast.CompositeLit:
		out := []exitcode.Code{}
		for _, elt := range v.Elts {
			code, ok := exitOf(elt, p.scope)
			if !ok {
				return nil, false
			}
			out = append(out, code)
		}
		return out, true
	case *ast.CallExpr:
		tdir, name, ok := idx.target(dir, file, v.Fun)
		if !ok {
			return nil, false
		}
		fn := idx.pkgs[tdir].funcs[name]
		if fn.decl == nil || fn.decl.Body == nil {
			return nil, false
		}
		var out []exitcode.Code
		var found bool
		ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 || found {
				return true
			}
			out, found = idx.exitList(tdir, fn.file, ret.Results[0])
			return true
		})
		return out, found
	}
	return nil, false
}

// exitsFrom reports every exit reachable from one entry-point expression, and
// for each the call path that reaches it.
func (idx *sourceIndex) exitsFrom(dir, file string, entry ast.Expr) map[exitcode.Code]string {
	out := map[exitcode.Code]string{}
	seen := map[string]bool{}

	switch v := entry.(type) {
	case *ast.FuncLit:
		idx.walk(dir, file, v.Body, nil, seen, out)
	case *ast.Ident, *ast.SelectorExpr:
		if tdir, name, ok := idx.target(dir, file, entry); ok {
			idx.follow(tdir, name, nil, seen, out)
		}
	}
	return out
}

// walk collects the exits produced in one function body, following every call
// it can resolve.
func (idx *sourceIndex) walk(
	dir, file string, body ast.Node, path []string,
	seen map[string]bool, out map[exitcode.Code]string,
) {
	p := idx.pkgs[dir]
	if p == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if code, ok := errsExit(call, p.scope); ok {
			if _, dup := out[code]; !dup {
				out[code] = describePath(path)
			}
			return true
		}
		tdir, name, ok := idx.target(dir, file, call.Fun)
		if !ok {
			return true
		}
		idx.follow(tdir, name, path, seen, out)
		return true
	})
}

// serverChosenExits names the functions whose exit is decided by an HTTP
// response rather than by the command that made the request.
//
// transport.Err maps a status to an exit for every request in the tree, so
// following it would report that any command reaching it can produce every
// status code Jira has — which is true of the transport and useless as a
// per-command claim. It is also unevenly visible: nearly every command reaches
// it through a resource client's method, which this walk cannot follow, so
// including it would demand a code from the handful whose call chain happens to
// be a package function and stay silent about the rest.
//
// What a command can produce by talking to a site at all is a uniform claim,
// and TestEveryCommandThatReachesJiraDeclaresTheSiteExits makes it.
var serverChosenExits = map[string]bool{
	"internal/transport.Err": true,
}

// follow walks one named function, once.
func (idx *sourceIndex) follow(
	dir, name string, path []string,
	seen map[string]bool, out map[exitcode.Code]string,
) {
	key := dir + "." + name
	if seen[key] || serverChosenExits[key] {
		return
	}
	seen[key] = true

	fn := idx.pkgs[dir].funcs[name]
	if fn.decl == nil || fn.decl.Body == nil {
		return
	}
	idx.walk(dir, fn.file, fn.decl.Body, append(slices.Clone(path), key), seen, out)
}

// describePath renders a call path for the failure message.
func describePath(path []string) string {
	if len(path) == 0 {
		return "the declaration itself"
	}
	return strings.Join(path, " → ")
}

// target resolves a call's callee to a package directory and function name.
//
// It resolves an unqualified call within the same package, and a qualified call
// into another package of this module. Everything else — a method, a call
// through an interface, a function value in a variable — returns false, and
// ends the walk there.
func (idx *sourceIndex) target(dir, file string, fun ast.Expr) (string, string, bool) {
	p := idx.pkgs[dir]
	if p == nil {
		return "", "", false
	}
	switch v := fun.(type) {
	case *ast.Ident:
		if _, ok := p.funcs[v.Name]; ok {
			return dir, v.Name, true
		}
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok {
			return "", "", false
		}
		tdir, ok := p.imports[file][pkg.Name]
		if !ok {
			return "", "", false
		}
		if _, ok := idx.pkgs[tdir].funcs[v.Sel.Name]; !ok {
			return "", "", false
		}
		return tdir, v.Sel.Name, true
	}
	return "", "", false
}

// errsExit reports the exit an errs constructor call produces.
//
// It differs from errsCall in errorcodes_test.go by not needing the code to be
// resolvable: validNumericID builds its code as "INVALID_" + the thing being
// checked, and the exit is knowable there even though the string is not.
func errsExit(call *ast.CallExpr, scope map[string]ast.Expr) (exitcode.Code, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "errs" {
		return 0, false
	}
	if sel.Sel.Name == "New" {
		if len(call.Args) == 0 {
			return 0, false
		}
		return exitOf(call.Args[0], scope)
	}
	exit, ok := ctorExit[sel.Sel.Name]
	return exit, ok
}
