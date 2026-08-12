package lint_test

import (
	"regexp"
	"strings"
	"testing"
)

// setupScript installs the ruleset, and its CHECKS array is the list of
// contexts a pull request has to report before it can merge.
const setupScript = "../../scripts/github-setup.sh"

var (
	// checksArray captures the body of the CHECKS=( ... ) declaration.
	checksArray = regexp.MustCompile(`(?s)CHECKS=\((.*?)\n\)`)
	// quoted matches one entry of it.
	quoted = regexp.MustCompile(`"([^"]+)"`)
	// jobName matches a job's display name, which is indented four spaces
	// under the job id.
	jobName = regexp.MustCompile(`(?m)^ {4}name: (.+)$`)
)

// notRequired is every job in ci.yml that no required check names, with the
// reason it does not need one.
//
// All three entries are the fuzz sweep, which runs on a push to main and in
// fuzz-nightly.yml but not on a pull request. Requiring any of them would
// require a context that only ever reports "skipped", and a check that cannot
// fail reads as coverage while providing none.
var notRequired = map[string]string{
	"fuzz":                                "runs after a merge and nightly, never on a pull request",
	"fuzz (${{ matrix.package }})":        "a leg of that sweep",
	"list the packages with fuzz targets": "an input to that sweep's matrix, not a check",
}

// TestEveryRequiredCheckIsAJobThatRuns holds the ruleset's context list to the
// workflow that produces those contexts.
//
// This is the one drift in the repository that cannot be recovered from inside
// the repository. A required check names a context as a string; if no job ever
// reports that string, the check sits pending forever and **every** pull
// request is unmergeable, including the one that would fix the workflow. The
// way out is editing the ruleset by hand in a browser, which is exactly the
// state scripts/github-setup.sh exists to make unnecessary.
//
// Renaming a job is enough to cause it. So is deleting one, or matrixing one
// that used to report a single fixed name, which is what the fuzz job just
// became.
func TestEveryRequiredCheckIsAJobThatRuns(t *testing.T) {
	required := requiredChecks(t)
	names := jobNames(t)

	for _, check := range required {
		if !slicesContainsMatch(names, check) {
			t.Errorf("%s requires the check %q and no job in %s reports that name: %v",
				setupScript, check, ciWorkflow, names)
		}
	}

	for _, name := range names {
		if reason, ok := notRequired[name]; ok {
			if reason == "" {
				t.Errorf("%s: job %q is exempt with no reason written down", ciWorkflow, name)
			}
			continue
		}
		if !slicesContainsMatch([]string{name}, pick(required, name)) {
			t.Errorf("%s runs the job %q and %s does not require it; add it to "+
				"CHECKS, or to notRequired with the reason it does not gate",
				ciWorkflow, name, setupScript)
		}
	}
}

// slicesContainsMatch reports whether any job name can produce this context.
//
// A name carrying a `${{ ... }}` expression produces one context per matrix
// leg, and all of them begin with whatever is written before the expression.
func slicesContainsMatch(names []string, check string) bool {
	for _, name := range names {
		if name == check {
			return true
		}
		if i := strings.Index(name, "${{"); i > 0 && strings.HasPrefix(check, name[:i]) {
			return true
		}
	}
	return false
}

// pick returns the check that covers this job name, or "" when none does.
func pick(required []string, name string) string {
	for _, check := range required {
		if slicesContainsMatch([]string{name}, check) {
			return check
		}
	}
	return ""
}

// requiredChecks reads the CHECKS array out of the setup script.
func requiredChecks(t *testing.T) []string {
	t.Helper()

	body := checksArray.FindStringSubmatch(readFile(t, setupScript))
	if body == nil {
		t.Fatalf("%s: no CHECKS=( ... ) array found; this test reads it by shape, "+
			"so a reformatted declaration silently asserts nothing", setupScript)
	}

	var out []string
	for _, m := range quoted.FindAllStringSubmatch(body[1], -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s: CHECKS is empty, which would leave main gated by nothing", setupScript)
	}
	return out
}

// jobNames reads every job's display name out of the CI workflow.
func jobNames(t *testing.T) []string {
	t.Helper()

	var out []string
	for _, m := range jobName.FindAllStringSubmatch(readFile(t, ciWorkflow), -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	if len(out) == 0 {
		t.Fatalf("%s: no job names found; this test reads them by indentation", ciWorkflow)
	}
	return out
}
