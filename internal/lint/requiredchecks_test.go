package lint_test

import (
	"regexp"
	"slices"
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
	// tagsMatrix captures the list under the `tags:` key of the test job's
	// matrix, which is what decides the concrete names that job reports.
	tagsMatrix = regexp.MustCompile(`(?m)^ {8}tags:\n((?: {10}- ".*"\n)+)`)
	// matrixTags matches the one templated job name this test knows how to
	// expand, capturing the matrix key it reads.
	matrixTags = regexp.MustCompile(`\$\{\{ matrix\.(\w+)[^}]*\}\}`)
	// matrixItem matches one entry of that list. It is line-anchored and its
	// capture may be empty, unlike `quoted`, which reads a bash array on one
	// line: reusing that one here matched from the end of the empty tag set to
	// the start of the next entry and produced job names with newlines in them.
	matrixItem = regexp.MustCompile(`(?m)^ {10}- "(.*)"$`)
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
// became. So is **changing a matrix value**, which is how this test came to be
// rewritten: dropping three build tags changed the test job's tag sets, the
// ruleset went on requiring `test (tags=tui,prompt,render,browser,clipboard,
// mcp,write,admin)`, and the pull request that made the change sat BLOCKED on a
// context nothing would ever report.
//
// This test passed throughout, because it compared the ruleset against the job
// *name*, and that name is `test (tags=${{ matrix.tags ... }})`. Every possible
// tag set matches that string, so the check verified the shape `test (tags=X)`
// and never the values of X, which are declared eleven lines below it in the
// same file. The names are expanded from the matrix now, and the comparison is
// exact in both directions.
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

// slicesContainsMatch reports whether any job name produces this context.
//
// It used to accept a `${{ ... }}` expression as a wildcard over everything
// after it, which is what let a changed matrix value through. Names arrive here
// expanded now, so this is exact and the only reason it still exists is that
// both loops read it.
func slicesContainsMatch(names []string, check string) bool {
	return slices.Contains(names, check)
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

// jobNames reads every job's display name out of the CI workflow, with a
// matrixed name expanded into the one context per leg that it reports.
//
// A templated name is expanded rather than accepted as a wildcard, because the
// wildcard is what made this whole check cosmetic: the values are in the same
// file as the template and there is no reason to look at one and not the other.
func jobNames(t *testing.T) []string {
	t.Helper()

	workflow := readFile(t, ciWorkflow)

	var out []string
	for _, m := range jobName.FindAllStringSubmatch(workflow, -1) {
		name := strings.TrimSpace(m[1])
		switch {
		case !strings.Contains(name, "${{"):
			out = append(out, name)
		case matrixTagsKey(name) != "":
			out = append(out, expand(t, workflow, name)...)
		default:
			// A name whose legs cannot be read from this file, because the
			// matrix is computed at run time. That is allowed only for a job
			// no check requires: `fuzz (${{ matrix.package }})` gets its list
			// from a previous job's output, and it is in notRequired because
			// it never runs on a pull request at all. Anything else is a job
			// whose contexts nothing is holding to the ruleset.
			if _, exempt := notRequired[name]; !exempt {
				t.Fatalf("%s: job name %q carries an expression this test cannot "+
					"expand and is not in notRequired; teach it the shape or "+
					"write down why the job does not gate", ciWorkflow, name)
			}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: no job names found; this test reads them by indentation", ciWorkflow)
	}
	return out
}

// matrixTagsKey returns the matrix key a templated name reads, when that key is
// one this test can resolve from the workflow file itself.
func matrixTagsKey(name string) string {
	m := matrixTags.FindStringSubmatch(name)
	if m == nil || m[1] != "tags" {
		return ""
	}
	return m[1]
}

// expand turns one templated job name into the contexts it reports.
//
// It knows exactly one expression, `${{ matrix.tags ... }}`, because that is
// the only matrix in this workflow whose values are literals in the same file.
// Guessing at any other would reintroduce the wildcard this replaced.
func expand(t *testing.T, workflow, name string) []string {
	t.Helper()

	body := tagsMatrix.FindStringSubmatch(workflow)
	if body == nil {
		t.Fatalf("%s: job name %q reads matrix.tags and no `tags:` list was found; "+
			"this test reads it by indentation, so a reformatted matrix asserts "+
			"nothing", ciWorkflow, name)
	}

	prefix, _, ok := strings.Cut(name, "${{")
	if !ok {
		t.Fatalf("%s: %q has no expression to cut", ciWorkflow, name)
	}
	_, suffix, ok := strings.Cut(name, "}}")
	if !ok {
		t.Fatalf("%s: %q has an unterminated expression", ciWorkflow, name)
	}

	var out []string
	for _, m := range matrixItem.FindAllStringSubmatch(body[1], -1) {
		value := m[1]
		// The workflow's own `|| 'none'` fallback, which is what an empty tag
		// set reports as. Mirrored here rather than parsed, and the test above
		// refuses any expression whose shape it has not been told about.
		if value == "" {
			value = "none"
		}
		out = append(out, prefix+value+suffix)
	}
	if len(out) == 0 {
		t.Fatalf("%s: matrix.tags is empty, so job %q reports nothing", ciWorkflow, name)
	}
	return out
}
