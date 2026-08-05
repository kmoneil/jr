// Package lint holds architecture tests: assertions about the shape of the
// module rather than the behavior of any one package.
package lint_test

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/kmoneil/jira-cli"

type pkg struct {
	ImportPath  string
	Imports     []string
	TestImports []string
	Deps        []string
}

// loadPackages asks the go tool for the real import graph rather than parsing
// source, so a build-tagged file cannot hide an import from this test.
func loadPackages(t *testing.T) []pkg {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list: %v\n%s", err, stderr)
	}

	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func short(importPath string) string {
	return strings.TrimPrefix(strings.TrimPrefix(importPath, module), "/")
}

// TestOnlyTheEdgesImportResources enforces the dependency rule: resources are
// leaves that the entry points consume, and nothing else reaches into them.
func TestOnlyTheEdgesImportResources(t *testing.T) {
	allowed := []string{
		"cmd/",
		"internal/tui",
		"internal/mcp",
		"internal/workflow",
		"internal/resource/",
		// internal/commands exists only to link resources in, so it is the one
		// place their init functions run. See its package comment.
		"internal/commands",
	}

	for _, p := range loadPackages(t) {
		self := short(p.ImportPath)
		if isAllowed(self, allowed) {
			continue
		}
		for _, imp := range p.Imports {
			if strings.HasPrefix(short(imp), "internal/resource/") {
				t.Errorf("%s imports %s; only cmd, tui, mcp, and workflow may import a resource",
					self, short(imp))
			}
		}
	}
}

// TestResourcesDoNotImportEachOther keeps every resource independently
// compilable, which is what makes compile-out work and what lets a new resource
// be added without touching an existing one.
func TestResourcesDoNotImportEachOther(t *testing.T) {
	for _, p := range loadPackages(t) {
		self := short(p.ImportPath)
		if !strings.HasPrefix(self, "internal/resource/") {
			continue
		}
		for _, imp := range p.Imports {
			dep := short(imp)
			if !strings.HasPrefix(dep, "internal/resource/") || dep == self {
				continue
			}
			t.Errorf("%s imports %s; a cross-resource operation belongs in internal/workflow",
				self, dep)
		}
	}
}

// TestPublicLibraryHasNoCLIConcepts asserts pkg/jira stays a typed API. If it
// depended on flags, exit codes, or output formats it would not be usable as a
// library, and it could not be versioned independently of the CLI.
func TestPublicLibraryHasNoCLIConcepts(t *testing.T) {
	forbidden := []string{
		"internal/cli",
		"internal/registry",
		"internal/render",
		"internal/exitcode",
		"github.com/spf13/cobra",
		"github.com/spf13/pflag",
	}
	for _, p := range loadPackages(t) {
		if !strings.HasPrefix(short(p.ImportPath), "pkg/") {
			continue
		}
		for _, dep := range p.Deps {
			for _, bad := range forbidden {
				if short(dep) == bad || dep == bad {
					t.Errorf("%s depends on %s; the public library must have no CLI concepts",
						short(p.ImportPath), bad)
				}
			}
		}
	}
}

// TestFoundationPackagesStayLeaves asserts the packages everything else builds
// on do not reach back up the stack. A cycle here would make them untestable in
// isolation and would defeat compile-out.
func TestFoundationPackagesStayLeaves(t *testing.T) {
	foundations := []string{
		"internal/exitcode",
		"internal/errs",
		"internal/render",
		"internal/buildinfo",
		"internal/jql",
		"internal/adf",
	}
	forbidden := []string{
		"internal/cli",
		"internal/resource",
		"internal/tui",
		"internal/mcp",
		"internal/workflow",
	}

	for _, p := range loadPackages(t) {
		self := short(p.ImportPath)
		if !slices.Contains(foundations, self) {
			continue
		}
		for _, dep := range p.Deps {
			d := short(dep)
			for _, bad := range forbidden {
				if d == bad || strings.HasPrefix(d, bad+"/") {
					t.Errorf("%s depends on %s; foundation packages must not reach up the stack",
						self, d)
				}
			}
		}
	}
}

// TestRenderIsTheOnlyWriterOfOutput asserts no package outside render decides
// what the output looks like. The output contract is a public API, and it stays
// reviewable only while it lives in one place.
func TestRenderIsTheOnlyWriterOfOutput(t *testing.T) {
	for _, p := range loadPackages(t) {
		self := short(p.ImportPath)
		if self == "internal/render" || strings.HasPrefix(self, "cmd/") {
			continue
		}
		for _, imp := range p.Imports {
			switch imp {
			case "encoding/xml", "gopkg.in/yaml.v3", "encoding/csv":
				t.Errorf("%s imports %s; only internal/render encodes output", self, imp)
			}
		}
	}
}

// TestTransportOwnsHTTP asserts nothing but the transport speaks HTTP, so
// header redaction cannot be bypassed by a package that builds its own client.
func TestTransportOwnsHTTP(t *testing.T) {
	for _, p := range loadPackages(t) {
		self := short(p.ImportPath)
		if self == "internal/transport" || strings.HasPrefix(self, "internal/lint") {
			continue
		}
		for _, imp := range p.Imports {
			if imp == "net/http" {
				t.Errorf("%s imports net/http; every request goes through internal/transport, "+
					"which is where Authorization headers are redacted", self)
			}
		}
	}
}

func isAllowed(self string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasSuffix(a, "/") {
			if strings.HasPrefix(self, a) {
				return true
			}
			continue
		}
		if self == a {
			return true
		}
	}
	return false
}
