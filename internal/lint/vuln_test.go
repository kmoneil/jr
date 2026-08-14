package lint_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	goModPath     = "../../go.mod"
	ciWorkflow    = "../../.github/workflows/ci.yml"
	vulnTarget    = "vuln"
	vulnInstall   = "golang.org/x/vuln/cmd/govulncheck"
	goDirectiveRe = `(?m)^go\s+(\S+)\s*$`
	goVersionRe   = `(?m)^\s*GO_VERSION:\s*"([^"]+)"`

	// The lint pass that runs with no build tags: its target, the tool it
	// has to use, the check it has to enable, and what CI must install.
	untaggedTarget  = "lint-untagged"
	untaggedTool    = "staticcheck"
	untaggedCheck   = "U1000"
	untaggedInstall = "honnef.co/go/tools/cmd/staticcheck"
)

// TestTheVulnerabilityScanIsWiredIntoBothCIs is the gate on the gate.
//
// govulncheck reported GO-2026-5856 reachable in crypto/tls the whole time it
// was reachable, and nothing ran it. A scan wired in once and quietly dropped
// later is the same state with more paperwork, so both places that have to
// invoke it are asserted rather than remembered.
//
// Two places, because `make ci` is a convenience for a human at a terminal and
// is not what GitHub runs — the workflow calls the individual targets. Adding
// `vuln` to `make ci` alone would leave CI not enforcing it while CLAUDE.md and
// the README both say `make ci` is everything CI enforces.
func TestTheVulnerabilityScanIsWiredIntoBothCIs(t *testing.T) {
	makefile := readFile(t, makefilePath)

	target := regexp.MustCompile(`(?m)^ci:\s*(.*)$`).FindStringSubmatch(makefile)
	if target == nil {
		t.Fatal("the Makefile has no ci target")
	}
	// Fields rather than a substring test, which would accept `vulnerable`.
	if !slices.Contains(strings.Fields(target[1]), vulnTarget) {
		t.Errorf("`make ci` does not run %s; its prerequisites are %q",
			vulnTarget, target[1])
	}

	if !regexp.MustCompile(`(?m)^` + vulnTarget + `:`).MatchString(makefile) {
		t.Errorf("`make ci` names a %s target the Makefile does not define",
			vulnTarget)
	}

	workflow := readFile(t, ciWorkflow)
	if !strings.Contains(workflow, "make "+vulnTarget) {
		t.Errorf("%s never runs `make %s`, so the scan is enforced only for "+
			"whoever runs the Makefile by hand", ciWorkflow, vulnTarget)
	}
	// The tool is not part of the Go distribution. A workflow that calls the
	// target without installing it fails on a missing binary, which reads like
	// a broken runner rather than like a missing check.
	if !strings.Contains(workflow, vulnInstall) {
		t.Errorf("%s runs `make %s` without installing %s",
			ciWorkflow, vulnTarget, vulnInstall)
	}
}

// TestTheGoDirectivePinsAPatchVersion keeps the toolchain fix from floating.
//
// GO-2026-5856 was fixed in the standard library, not in this module, so the
// only thing that makes a build of this tool safe is the toolchain it is built
// with. `go 1.26.5` requires it; `go 1.26` accepts whatever the machine has,
// and a developer on 1.26.4 would build a vulnerable binary while CI on a newer
// runner reported the scan clean.
//
// The assertion is the shape rather than the number, so an ordinary bump does
// not have to come here first.
func TestTheGoDirectivePinsAPatchVersion(t *testing.T) {
	m := regexp.MustCompile(goDirectiveRe).FindStringSubmatch(readFile(t, goModPath))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	if parts := strings.Split(m[1], "."); len(parts) != 3 {
		t.Errorf("go.mod says `go %s`; it has to name a patch version, because "+
			"a toolchain vulnerability is fixed by the patch and nothing else "+
			"here requires it", m[1])
	}
}

// goWorkflows are every workflow that installs a toolchain. Each one has to
// name the same patch version go.mod does.
var goWorkflows = []string{
	ciWorkflow,
	"../../.github/workflows/release.yml",
	"../../.github/workflows/fuzz-nightly.yml",
}

// TestTheWorkflowsPinTheSameToolchainAsGoMod couples two numbers that have no
// other reason to agree.
//
// actions/setup-go sets GOTOOLCHAIN=local, which is what makes a build
// reproducible: the toolchain is the one the workflow installed and never one
// fetched mid-build. The cost is that go.mod's directive stops being a request
// the runner can satisfy and becomes a floor it either clears or fails at, with
// `go.mod requires go >= 1.26.6 (running go 1.26.5; GOTOOLCHAIN=local)`.
//
// Bumping go.mod for a standard-library CVE and leaving GO_VERSION at "1.26"
// therefore does not scan the fixed toolchain; it fails every job in the run,
// including the scan the bump was for. The comment in ci.yml claimed the
// opposite for as long as the two happened to agree.
func TestTheWorkflowsPinTheSameToolchainAsGoMod(t *testing.T) {
	m := regexp.MustCompile(goDirectiveRe).FindStringSubmatch(readFile(t, goModPath))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	want := m[1]

	for _, path := range goWorkflows {
		got := regexp.MustCompile(goVersionRe).FindStringSubmatch(readFile(t, path))
		if got == nil {
			t.Errorf("%s sets no GO_VERSION, so what it installs is whatever "+
				"the runner had cached", path)
			continue
		}
		if got[1] != want {
			t.Errorf("%s pins GO_VERSION %q and go.mod requires %q; setup-go "+
				"sets GOTOOLCHAIN=local, so every job in that workflow fails "+
				"until they match", path, got[1], want)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestTheUntaggedUnusedPassIsWiredIntoBothCIs is the same gate as the one
// above, on a check with the same failure mode.
//
// `.golangci.yml` sets build-tags to all eight on purpose — a capability that
// only compiles under a tag is still shipped code. The side effect is that
// `unused` analyses one build in which every file is present, so a symbol
// reachable only from a `//go:build write` file looks used. `echoMode` and a
// test fixture were both dead in the `ci` and `reader` profiles for as long as
// they existed, and every gate in the tree reported green.
//
// Two places again, because the workflow calls targets one at a time: a pass
// wired only into `make lint` is a pass CI does not run.
func TestTheUntaggedUnusedPassIsWiredIntoBothCIs(t *testing.T) {
	makefile := readFile(t, makefilePath)

	target := regexp.MustCompile(`(?m)^` + untaggedTarget + `:\s*\n\t(.*)$`).
		FindStringSubmatch(makefile)
	if target == nil {
		t.Fatalf("the Makefile has no %s target", untaggedTarget)
	}
	// staticcheck, not golangci-lint. `golangci-lint run --build-tags=` does
	// not override `run.build-tags` from the config: that pass loads the same
	// eight tags and reports the same clean answer, which is a gate that runs
	// and cannot fail. This assertion is what keeps somebody from folding the
	// two passes back together on the reasonable-looking grounds that the
	// repository already has a linter.
	if !strings.Contains(target[1], untaggedTool) {
		t.Errorf("`make %s` runs %q rather than %s; a second golangci-lint run "+
			"inherits the config's build tags and cannot see what this pass "+
			"exists for", untaggedTarget, target[1], untaggedTool)
	}
	if !strings.Contains(target[1], untaggedCheck) {
		t.Errorf("`make %s` runs %q, which does not enable %s",
			untaggedTarget, target[1], untaggedCheck)
	}

	lint := regexp.MustCompile(`(?m)^lint:\s*(.*)$`).FindStringSubmatch(makefile)
	if lint == nil {
		t.Fatal("the Makefile has no lint target")
	}
	if !slices.Contains(strings.Fields(lint[1]), untaggedTarget) {
		t.Errorf("`make lint` does not run %s; its prerequisites are %q",
			untaggedTarget, lint[1])
	}

	workflow := readFile(t, ciWorkflow)
	if !strings.Contains(workflow, "make "+untaggedTarget) {
		t.Errorf("%s never runs `make %s`, so the pass is enforced only for "+
			"whoever runs the Makefile by hand", ciWorkflow, untaggedTarget)
	}
	// Same reason govulncheck's install is asserted: the tool is not part of
	// the Go distribution, and a workflow that calls the target without it
	// fails on a missing binary, which reads like a broken runner rather than
	// like a missing check.
	if !strings.Contains(workflow, untaggedInstall) {
		t.Errorf("%s runs `make %s` without installing %s",
			ciWorkflow, untaggedTarget, untaggedInstall)
	}
}
