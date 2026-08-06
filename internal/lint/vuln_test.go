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

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
