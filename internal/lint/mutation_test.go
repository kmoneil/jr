package lint_test

import (
	"errors"
	"os"
	"os/exec"
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

// TestTheMutationSweepExitCodesSayWhichFailureItWas drives the script for the
// distinction the report is built on.
//
// Two things go wrong here and they are not the same thing. A count that
// disagrees with the baseline is a finding about the tests, and somebody writes
// one or lowers a number. A package that produced no summary at all is a
// finding about the run, and the counts this sweep does have are a partial
// sweep's counts. They shared exit 1 until 2026-08-17, so the only consumer of
// the exit code could not tell them apart and reported whichever it had been
// written to assume.
func TestTheMutationSweepExitCodesSayWhichFailureItWas(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gremlins string
		baseline string
		want     int
	}{
		{
			"a package on its baseline",
			"Mutation testing completed in 12s\nKilled: 40, Lived: 5, Not covered: 0",
			"internal/jql\t5\tthe only place a value is quoted\n",
			0,
		},
		{
			"a package that regressed",
			"Mutation testing completed in 12s\nKilled: 38, Lived: 7, Not covered: 0",
			"internal/jql\t5\tthe only place a value is quoted\n",
			1,
		},
		// Fewer is a failure too, and deliberately: a baseline nobody has to
		// update stops describing the tree. It is still a finding about the
		// tests, so it is still a 1.
		{
			"a package that improved",
			"Mutation testing completed in 12s\nKilled: 42, Lived: 3, Not covered: 0",
			"internal/jql\t5\tthe only place a value is quoted\n",
			1,
		},
		// What a tool that could not build its own mutants looks like: no
		// summary line, so there is no count to compare and nothing to say
		// about the package.
		{
			"a package that produced no count",
			"go: build failed\nexit status 1",
			"internal/jql\t5\tthe only place a value is quoted\n",
			2,
		},
		// A partial sweep outranks a moved count in the same run. The table
		// carries both; the exit code has to pick the headline, and it is the
		// one that says the numbers are not trustworthy.
		{
			"one package that moved and one that did not run",
			"",
			"internal/jql\t5\tthe only place a value is quoted\n" +
				"internal/render\t13\tthe output contract itself\n",
			2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			// The stub answers every package the same way, which is why the
			// last case leaves the output empty: with no summary for either
			// package the run is broken whatever the counts would have been.
			stub := filepath.Join(dir, "gremlins")
			body := "#!/bin/sh\ncat <<'END_OF_STUBBED_GREMLINS'\n" + tc.gremlins + "\nEND_OF_STUBBED_GREMLINS\n"
			if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
				t.Fatalf("writing the stubbed gremlins: %v", err)
			}

			baseline := filepath.Join(dir, "baseline.tsv")
			if err := os.WriteFile(baseline, []byte(tc.baseline), 0o644); err != nil {
				t.Fatalf("writing the baseline: %v", err)
			}

			cmd := exec.Command("bash", mutationScript)
			cmd.Env = append(os.Environ(), "GREMLINS="+stub, "MUTATION_BASELINE="+baseline)
			out, err := cmd.CombinedOutput()

			got := 0
			if err != nil {
				exit, ok := errors.AsType[*exec.ExitError](err)
				if !ok {
					t.Fatalf("running %s: %v", mutationScript, err)
				}
				got = exit.ExitCode()
			}
			if got != tc.want {
				t.Errorf("%s exited %d, want %d.\nA sweep whose exit code cannot "+
					"tell a moved baseline from a run that measured nothing "+
					"leaves its reader to guess, and the reader is a workflow "+
					"that opens an issue.\n%s",
					mutationScript, got, tc.want, out)
			}
		})
	}
}

// TestTheMutationWorkflowKeepsTheSweepsOwnStatus is about one character.
//
// A `run:` block on Linux is `bash -e {0}`, with no pipefail. The sweep step
// was `make mutate | tee mutation.log`, so the step exited with tee's zero and
// a regressed baseline could not redden the job: for its whole first scheduled
// run the only failure this workflow could report was the runner going away
// underneath it. Piping is worth keeping, because the log is what the artifact
// is made of, so the rule is that the pipeline's status is not the one read.
func TestTheMutationWorkflowKeepsTheSweepsOwnStatus(t *testing.T) {
	workflow := readFile(t, mutationWorkflow)

	piped := regexp.MustCompile(`make mutate\s*\|`).MatchString(workflow)
	if !piped {
		return
	}
	if !strings.Contains(workflow, "PIPESTATUS") && !strings.Contains(workflow, "pipefail") {
		t.Errorf("%s pipes `make mutate` and reads neither PIPESTATUS nor sets "+
			"pipefail, so the step exits with the status of whatever it piped "+
			"into and the sweep's own verdict is discarded", mutationWorkflow)
	}
}

// TestTheMutationReportNamesWhatActuallyFailed holds the issue to the run.
//
// `if: failure()` is a trigger and not a finding. It fires for a moved
// baseline, for a sweep that could not build a mutant, and for a runner that
// was reclaimed, and on 2026-08-17 it fired for the third and filed an issue
// titled "the weekly sweep moved off its baseline" against a run that had
// printed one header row and compared nothing to anything. So the report reads
// what the sweep recorded about itself, and has something to say when the sweep
// recorded nothing.
func TestTheMutationReportNamesWhatActuallyFailed(t *testing.T) {
	workflow := readFile(t, mutationWorkflow)

	if !strings.Contains(workflow, "needs.mutate.outputs.verdict") {
		t.Errorf("%s does not read the sweep's verdict in its report job, so "+
			"whatever the issue claims is a guess from `failure()`, which "+
			"cannot see the difference between a finding and a dead runner",
			mutationWorkflow)
	}
	if !strings.Contains(workflow, "steps.sweep.outputs.verdict") {
		t.Errorf("%s never publishes the sweep's verdict as a job output, so "+
			"there is nothing for the report to read", mutationWorkflow)
	}

	// The three states the report has to be able to describe. A workflow that
	// can only say one of them is the one this test exists because of.
	for _, title := range []string{
		"moved off its baseline",
		"could not produce a count",
		"did not finish",
	} {
		if !strings.Contains(workflow, title) {
			t.Errorf("%s has no report for a sweep that %q, so that outcome is "+
				"filed under whichever title it does have", mutationWorkflow, title)
		}
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
