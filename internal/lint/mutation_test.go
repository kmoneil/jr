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
		verdict  string
	}{
		{
			"a package on its baseline",
			"Mutation testing completed in 12s\nKilled: 40, Lived: 5, Not covered: 0",
			"internal/jql\t5\tthe only place a value is quoted\n",
			0,
			"matched",
		},
		{
			"a package that regressed",
			"Mutation testing completed in 12s\nKilled: 38, Lived: 7, Not covered: 0",
			"internal/jql\t5\tthe only place a value is quoted\n",
			1,
			"moved",
		},
		// Fewer is a failure too, and deliberately: a baseline nobody has to
		// update stops describing the tree. It is still a finding about the
		// tests, so it is still a 1.
		{
			"a package that improved",
			"Mutation testing completed in 12s\nKilled: 42, Lived: 3, Not covered: 0",
			"internal/jql\t5\tthe only place a value is quoted\n",
			1,
			"moved",
		},
		// What a tool that could not build its own mutants looks like: no
		// summary line, so there is no count to compare and nothing to say
		// about the package.
		{
			"a package that produced no count",
			"go: build failed\nexit status 1",
			"internal/jql\t5\tthe only place a value is quoted\n",
			2,
			"broken",
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
			"broken",
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

			// The same distinction in words, which is the copy that reaches
			// the workflow. See TestTheMutationVerdictSurvivesTheMakefile for
			// what happens to the number on the way there.
			if got := sweepVerdict(string(out)); got != tc.verdict {
				t.Errorf("%s recorded the verdict %q, want %q.\nEvery path out "+
					"of the script writes one, so a missing line means an exit "+
					"nobody described.\n%s",
					mutationScript, got, tc.verdict, out)
			}
		})
	}
}

// TestTheMutationVerdictSurvivesTheMakefile watches the sweep's finding cross
// the layer its exit code cannot.
//
// Nothing schedules the script. CI schedules `make mutate`, and make exits 2
// for any failed recipe whatever the recipe returned, so the 1 that means "a
// count moved" and the 2 that means "no count could be taken" arrive at the
// workflow as one number. The workflow mapped that number to a title, and every
// moved baseline it ever found was therefore filed as a sweep that could not
// produce a count. Issue 110 is one: it says the run measured nothing, about a
// run that measured all three packages and found internal/render had improved
// from 13 survivors to 12.
//
// Neither test around this one could see it. The exit-code table above runs the
// script directly, where the codes are still distinct, and
// TestTheMutationWorkflowKeepsTheSweepsOwnStatus asserts the step reads
// PIPESTATUS rather than tee's status, which it does and always did:
// PIPESTATUS[0] is make's status, and make is what lost the distinction. So the
// sweep writes its verdict into the log it is already piping, and this drives
// the real recipe to watch that line arrive where the number does not.
func TestTheMutationVerdictSurvivesTheMakefile(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("no make on PATH, and `make mutate` is how the sweep is run "+
			"everywhere except a developer's shell: %v", err)
	}

	dir := t.TempDir()

	// A count that moved: the one case the exit code cannot carry.
	stub := filepath.Join(dir, "gremlins")
	body := "#!/bin/sh\necho 'Mutation testing completed in 12s'\n" +
		"echo 'Killed: 42, Lived: 3, Not covered: 0'\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the stubbed gremlins: %v", err)
	}
	baseline := filepath.Join(dir, "baseline.tsv")
	if err := os.WriteFile(baseline, []byte("internal/jql\t5\tthe reason\n"), 0o644); err != nil {
		t.Fatalf("writing the baseline: %v", err)
	}

	// MAKEFLAGS is exported by the `make test` that usually runs this, and a
	// sub-make that inherits a jobserver it was not handed warns into the
	// output this test reads its answer out of.
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MAKEFLAGS=") ||
			strings.HasPrefix(kv, "MFLAGS=") ||
			strings.HasPrefix(kv, "MAKELEVEL=") {
			continue
		}
		env = append(env, kv)
	}

	cmd := exec.Command("make", "mutate")
	cmd.Dir = repoRoot
	cmd.Env = append(env, "GREMLINS="+stub, "MUTATION_BASELINE="+baseline)
	out, err := cmd.CombinedOutput()

	code := 0
	if err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running `make mutate`: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}

	// The flattening, asserted rather than assumed, because everything else
	// here is built on it. The script exited 1 and this is what came out. If a
	// future make preserves the status, delete this assertion and not the
	// verdict line: the line is what the workflow reads either way.
	if code != 2 {
		t.Errorf("`make mutate` exited %d where the sweep exited 1. Every comment "+
			"written around this says make flattens a failed recipe to 2, and "+
			"those comments are what somebody reads before touching it", code)
	}

	if got := sweepVerdict(string(out)); got != "moved" {
		t.Errorf("`make mutate` reported the verdict %q, want \"moved\". The sweep "+
			"found a count that disagreed with its baseline, and if the only "+
			"thing that reaches the workflow is a 2 then the issue it files says "+
			"the run measured nothing.\n%s", got, out)
	}

	// The workflow has to read that line. Mapping the status back to a verdict
	// is the defect itself: at that point make decides the title.
	workflow := readFile(t, mutationWorkflow)
	if !regexp.MustCompile(`verdict=\$\([^)]*mutation\.log`).MatchString(workflow) {
		t.Errorf("%s does not take its verdict from mutation.log, so what it reads "+
			"has been through make, which cannot tell a moved count from an "+
			"unmeasured one", mutationWorkflow)
	}
	if regexp.MustCompile(`(?s)case\s+"\$status".*?verdict=`).MatchString(workflow) {
		t.Errorf("%s maps the sweep's exit status to a verdict. make exits 2 for "+
			"every failed recipe, so that mapping files a moved baseline as a "+
			"sweep that measured nothing", mutationWorkflow)
	}
}

// sweepVerdict returns the last verdict the sweep printed, which is the line
// the workflow reads and the only half of its finding that survives `make`.
func sweepVerdict(out string) string {
	verdict := ""
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(line, "sweep: "); ok {
			verdict = strings.TrimSpace(v)
		}
	}
	return verdict
}

// TestTheMutationSweepBoundsARunawayMutant holds the cap where it works.
//
// Four mutants in internal/jql/token.go do not terminate: the scan loop there
// has no post statement, so each case advances `i` itself, and flipping one
// `i++` to `i--` makes it oscillate against a neighbour while appending on every
// cycle. Gremlins classifies them correctly, as timed out, but the timeout is
// the coefficient times a measured suite time that includes a cold build, so a
// runner grants about 104 seconds where this machine grants 4.8. Both
// `mutation-weekly` runs that predate the cap died taking sixteen gigabytes and
// the runner with them, as did every arm of the probe that chased it.
//
// The cap has to reach `go` and not this script's own shell. Capping the shell
// caps gremlins, which sizes itself from NumCPU and then dies copying the
// source tree per worker on a machine with enough cores. So this asserts the
// shim is what children actually resolve, rather than asserting that the file
// contains the word ulimit, which would pass just as happily with the limit in
// the place that broke.
func TestTheMutationSweepBoundsARunawayMutant(t *testing.T) {
	dir := t.TempDir()

	// The stub records what `go` resolves to for a child of the script, which
	// is the only question worth asking here. It writes to a file rather than
	// to stdout because the script captures a run's output to parse the count
	// out of it, so anything printed here never reaches the caller.
	// It records the resolved file's contents and not its path, because the
	// script removes the shim on exit and the path would be gone by the time
	// this test read it.
	resolvedPath := filepath.Join(dir, "resolved")
	stub := filepath.Join(dir, "gremlins")
	body := "#!/bin/sh\ng=$(command -v go)\n" +
		"{ echo \"resolved: $g\"; cat \"$g\"; } >" + resolvedPath + " 2>/dev/null\n" +
		"echo 'Lived: 5'\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the stubbed gremlins: %v", err)
	}
	baseline := filepath.Join(dir, "baseline.tsv")
	if err := os.WriteFile(baseline, []byte("internal/jql\t5\tthe reason\n"), 0o644); err != nil {
		t.Fatalf("writing the baseline: %v", err)
	}

	cmd := exec.Command("bash", mutationScript)
	cmd.Env = append(os.Environ(), "GREMLINS="+stub, "MUTATION_BASELINE="+baseline)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			t.Fatalf("running %s: %v", mutationScript, err)
		}
	}

	recorded, err := os.ReadFile(resolvedPath)
	if err != nil {
		t.Fatalf("%s ran a child that recorded no go at all: %v\n%s",
			mutationScript, err, out)
	}
	if len(recorded) == 0 {
		t.Fatalf("%s ran a child that could not resolve go:\n%s", mutationScript, out)
	}
	if !strings.Contains(string(recorded), "ulimit -v") {
		t.Errorf("a child of %s resolves go to something that sets no "+
			"address-space limit. A mutant whose loop does not terminate then "+
			"allocates until the machine dies, which is what killed every "+
			"mutation-weekly run that predates the cap.\nIt resolved to:\n%s",
			mutationScript, firstLine(string(recorded)))
	}
}

// firstLine keeps a failure message readable when what was resolved is a
// megabyte of compiled toolchain rather than a shell script.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
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
