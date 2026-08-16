package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	mutationBaseline = repoRoot + "/scripts/mutation-baseline.tsv"
	mutationScript   = repoRoot + "/scripts/mutate.sh"
	mutationWorkflow = repoRoot + "/.github/workflows/mutation-weekly.yml"
)

// TestTheMutationBaselineNamesPackagesThatExist keeps the ratchet pointed at
// the tree.
//
// The baseline is the only list of what gets swept: the script reads it and the
// Makefile reads the script, so there is no second place to drift. What can
// drift is a package moving or being renamed, and the failure then is silent in
// the worst way. `gremlins unleash ./internal/gone/` reports nothing to mutate
// and exits zero, so a sweep of three packages quietly becomes a sweep of two
// and the score goes up.
func TestTheMutationBaselineNamesPackagesThatExist(t *testing.T) {
	entries := mutationEntries(t)

	if len(entries) < 3 {
		t.Fatalf("the baseline names %d packages and named three when it was "+
			"written; a sweep that lost a package reports a better score for it",
			len(entries))
	}

	for pkg, lived := range entries {
		dir := filepath.Join(repoRoot, pkg)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("the baseline names %s and there is no such package; "+
				"gremlins reports nothing to mutate for a path that is not "+
				"there and exits zero", pkg)
			continue
		}

		matches, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if len(matches) == 0 {
			t.Errorf("%s has no tests, so every mutant in it survives and the "+
				"baseline is measuring nothing", pkg)
		}
		if lived < 0 {
			t.Errorf("%s has a baseline of %d; a negative count cannot be "+
				"measured against", pkg, lived)
		}
	}
}

// TestTheMutationSweepSetsItsOwnTimeout is the one number in this whole
// arrangement that is not allowed to be the tool's default.
//
// Gremlins derives a per-mutant timeout by multiplying the measured suite time,
// and the default coefficient of 2 is not tight here, it is fatal:
// `internal/jql` runs in 15 milliseconds, so the timeout lands under the
// compile and 194 of 216 mutants are abandoned unrun. The tool then reports
// "Test efficacy: 100.00%" over the 25 that finished, which is a perfect score
// for a tenth of the work.
//
// So the coefficient is set in the script, and this holds it there. A sweep
// that measures nothing while reporting everything is the exact failure this
// repository keeps writing rules about, and here it is the out-of-the-box
// configuration.
func TestTheMutationSweepSetsItsOwnTimeout(t *testing.T) {
	script := readFile(t, mutationScript)

	found := regexp.MustCompile(`TIMEOUT_COEFFICIENT=\$\{TIMEOUT_COEFFICIENT:-(\d+)\}`).
		FindStringSubmatch(script)
	if found == nil {
		t.Fatalf("%s sets no TIMEOUT_COEFFICIENT default. Gremlins' own default "+
			"abandons most mutants unrun and calls the result 100%%",
			mutationScript)
	}

	coefficient, err := strconv.Atoi(found[1])
	if err != nil || coefficient < 10 {
		t.Errorf("%s runs at coefficient %s, and anything near the tool's "+
			"default of 2 times a 15ms suite is a timeout below the compile",
			mutationScript, found[1])
	}
}

// TestTheMutationWorkflowGoesThroughTheMakefile keeps the coefficient on the
// scheduled path too.
//
// A workflow that called gremlins directly would be a second place the timeout
// is decided, and the one that drifts is the one nobody runs by hand. It also
// has to install the pinned version the same way every other tool step does,
// which TestEveryMakefileVariableAWorkflowPrintsExists then holds to naming a
// variable that exists.
func TestTheMutationWorkflowGoesThroughTheMakefile(t *testing.T) {
	workflow := readFile(t, mutationWorkflow)

	if !strings.Contains(workflow, "make mutate") {
		t.Errorf("%s does not run `make mutate`, so the timeout coefficient and "+
			"the baseline comparison are decided somewhere else", mutationWorkflow)
	}
	if !strings.Contains(workflow, "print-GREMLINS_VERSION") {
		t.Errorf("%s does not read the pinned version from the Makefile, so a "+
			"scheduled sweep and a local one can mutate with different binaries",
			mutationWorkflow)
	}
	if strings.Contains(workflow, "gremlins@latest") {
		t.Errorf("%s installs gremlins at @latest; the baseline would move "+
			"without anybody changing a test", mutationWorkflow)
	}
}

// mutationEntries reads the baseline into package to surviving-mutant count.
func mutationEntries(t *testing.T) map[string]int {
	t.Helper()

	out := map[string]int{}
	for i, line := range strings.Split(readFile(t, mutationBaseline), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			t.Errorf("%s:%d is %q, and a row is package, count, and the reason "+
				"the package is swept, tab-separated",
				mutationBaseline, i+1, line)
			continue
		}

		lived, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Errorf("%s:%d has %q where the surviving-mutant count belongs",
				mutationBaseline, i+1, fields[1])
			continue
		}
		if strings.TrimSpace(fields[2]) == "" {
			t.Errorf("%s:%d names %s with no reason; a package swept for no "+
				"written reason is one nobody can argue about removing",
				mutationBaseline, i+1, fields[0])
		}
		out[fields[0]] = lived
	}
	return out
}
