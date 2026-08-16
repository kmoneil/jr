package lint_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

const (
	releaseWorkflow   = "../../.github/workflows/release.yml"
	golangciVarName   = "GOLANGCI_LINT_VERSION"
	golangciActionRe  = `(?m)^\s*version:\s*(v[0-9][^\s#]*)`
	makefileVarRePat  = `(?m)^%s\s*:=\s*(\S+)\s*$`
	makefilePrintRe   = `\$\(make -s print-([A-Z_]+)\)`
	makefileToolsList = "TOOLS :="
)

// TestNoWorkflowInstallsAToolAtLatest is the reason the versions moved into the
// Makefile at all.
//
// A tag is the release here, and the release gate re-runs `make ci` on the
// tagged commit, so `@latest` means the verdict on an immutable commit depends
// on what somebody published that morning: a new gofumpt can fail a release
// that changed no code, and the remedy would be a tag that cannot be moved.
//
// Two of the five tools were pinned, each with a paragraph in ci.yml explaining
// that a floating tool fails a pull request that changed nothing. The other
// three were not, and the argument applies identically to them. This is the
// same shape as every other finding of the 2026-08-13 sweep: a guard that
// covers one path and not its twin.
func TestNoWorkflowInstallsAToolAtLatest(t *testing.T) {
	for _, path := range []string{ciWorkflow, releaseWorkflow} {
		body := readFile(t, path)
		for i, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.Contains(trimmed, "go install") && strings.Contains(trimmed, "@latest") {
				t.Errorf("%s:%d installs a tool at @latest:\n\t%s",
					path, i+1, trimmed)
			}
		}
	}
}

// TestEveryPinnedToolVersionComesFromTheMakefile holds the two declarations of
// golangci-lint's version to each other.
//
// It is the one tool an action installs rather than `go install`, so its
// version is a YAML input and cannot read a Makefile variable the way the other
// four do. Two places declaring one version is exactly the drift ci.yml's own
// comments warn about, and the failure it produces is the confusing kind: a
// pull request goes green under one linter and the release gate goes red under
// another, on the same commit.
func TestEveryPinnedToolVersionComesFromTheMakefile(t *testing.T) {
	makefile := readFile(t, makefilePath)

	want := makefileVar(t, makefile, golangciVarName)
	got := firstSubmatch(t, ciWorkflow, readFile(t, ciWorkflow), golangciActionRe)

	if got != want {
		t.Errorf("golangci-lint-action pins %s and the Makefile's %s is %s; "+
			"a pull request and the release gate would lint with different "+
			"binaries on the same commit", got, golangciVarName, want)
	}
}

// TestEveryMakefileVariableAWorkflowPrintsExists catches the other direction.
//
// A step reads its version with `make -s print-GOFUMPT_VERSION`, and `print-%`
// echoes an empty string for a name that does not exist rather than failing.
// That would install `mvdan.cc/gofumpt@`, which `go install` rejects, so the
// job does fail — but it fails inside a shell expansion with no mention of the
// variable, which is a long way from the typo that caused it.
func TestEveryMakefileVariableAWorkflowPrintsExists(t *testing.T) {
	makefile := readFile(t, makefilePath)
	named := regexp.MustCompile(makefilePrintRe)

	for _, path := range []string{ciWorkflow, releaseWorkflow} {
		for _, m := range named.FindAllStringSubmatch(readFile(t, path), -1) {
			if makefileVar(t, makefile, m[1]) == "" {
				t.Errorf("%s reads %s from the Makefile and no such variable is set",
					path, m[1])
			}
		}
	}
}

// TestTheToolListCoversEveryToolTheGateInstalls keeps `make tools` and the
// cache key honest.
//
// The release gate installs nothing when the cache hits, so a tool missing from
// TOOLS is a tool that is in neither the key nor the install: the gate would
// restore a bin directory that never held it and `make ci` would fail on a
// warm cache and pass on a cold one.
func TestTheToolListCoversEveryToolTheGateInstalls(t *testing.T) {
	makefile := readFile(t, makefilePath)

	start := strings.Index(makefile, makefileToolsList)
	if start < 0 {
		t.Fatalf("%s has no %s", makefilePath, makefileToolsList)
	}
	list := makefile[start:]
	if end := strings.Index(list, "\n\n"); end > 0 {
		list = list[:end]
	}

	for _, want := range []string{
		"gofumpt", "staticcheck", "govulncheck", "golangci-lint", "gocognit",
		"gremlins",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("TOOLS does not install %s, which `make ci` shells out to", want)
		}
	}
	if strings.Contains(list, "@latest") {
		t.Error("TOOLS pins a tool at @latest, which has no cache key")
	}
}

// makefileVar returns a `:=` assignment's value, or "" when there is none.
func makefileVar(t *testing.T, makefile, name string) string {
	t.Helper()

	found := regexp.MustCompile(fmt.Sprintf(makefileVarRePat, name)).
		FindStringSubmatch(makefile)
	if found == nil {
		return ""
	}
	return found[1]
}

func firstSubmatch(t *testing.T, path, body, pattern string) string {
	t.Helper()

	found := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if found == nil {
		t.Fatalf("%s has nothing matching %s", path, pattern)
	}
	return found[1]
}
