package lint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workflowDir holds every scheduled sweep, and the two that file issues are
// what this file is about.
const workflowDir = repoRoot + "/.github/workflows"

// scheduledReportFloor is the number of issue-filing scheduled workflows below
// which this gate assumes its discovery broke rather than that the tree shrank.
//
// Two today: the weekly mutation sweep and the nightly fuzz sweep. A discovery
// that silently matched nothing would satisfy every assertion below, which for
// a gate is worse than failing.
const scheduledReportFloor = 2

// TestAScheduledSweepReportsWhatItMeasured is the general form of a rule that
// had one enforcer and two subjects.
//
// docs/invariants.md states it for every scheduled sweep: `if: failure()` fires
// for a finding, for a tool that could not run, and for a runner that was
// reclaimed, so an issue filed off the trigger alone is a claim nobody
// measured. The mutation workflow learned it on 2026-08-17, when it filed "the
// weekly sweep moved off its baseline" against a run that compared nothing to
// anything. Every test named beside that invariant read `mutation-weekly.yml`,
// and `fuzz-nightly.yml` had the identical shape: `needs: fuzz`, `if:
// failure()`, and an issue titled "the nightly sweep found something" telling
// the reader to commit an input that may not exist.
//
// So the subjects are discovered rather than listed. A third scheduled sweep
// that files an issue is covered the day it is added, which is the only way a
// rule written for a class stays true of the class.
func TestAScheduledSweepReportsWhatItMeasured(t *testing.T) {
	found := 0
	for _, path := range workflowFiles(t) {
		body := readFile(t, path)
		if !strings.Contains(body, "schedule:") || !strings.Contains(body, "gh issue create") {
			continue
		}
		found++

		t.Run(filepath.Base(path), func(t *testing.T) {
			// The claim comes from a record of the work, not from the trigger.
			// A single job can publish what it measured as an output; a matrix
			// cannot, because every leg writes to one namespace, so it reads
			// the run's own jobs back instead. Either is evidence. `failure()`
			// alone is not.
			measured := strings.Contains(body, "outputs.verdict") ||
				strings.Contains(body, "/jobs?per_page=")
			if !measured {
				t.Errorf("%s files an issue without reading what the sweep did, so "+
					"whatever it claims is inferred from `failure()`, which cannot "+
					"tell a finding from a dead runner", path)
			}

			// The outcome that has no finding in it still has to be reportable.
			// A workflow that can only title one thing files that title for
			// everything, which is exactly how a reclaimed runner became a
			// baseline regression.
			if !strings.Contains(body, "did not finish") {
				t.Errorf("%s has no title for a sweep that did not finish, so a job "+
					"that measured nothing is filed under whichever title it does "+
					"have", path)
			}

			// Reading the run's own jobs is an API call against this
			// repository, and the default token cannot make it. Without the
			// grant the step fails at 2am and the report says nothing at all.
			if strings.Contains(body, "/jobs?per_page=") && !strings.Contains(body, "actions: read") {
				t.Errorf("%s reads this run's jobs and never grants `actions: read`, "+
					"so the query 403s and the report is silent", path)
			}
		})
	}

	if found < scheduledReportFloor {
		t.Errorf("found %d scheduled workflows that file issues, want at least %d; "+
			"the discovery in this test has stopped matching them",
			found, scheduledReportFloor)
	}
}

// workflowFiles lists the workflow definitions, both extensions, because a
// gate that reads one of them is a gate somebody can walk around by accident.
func workflowFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", workflowDir, err)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext != ".yml" && ext != ".yaml" {
			continue
		}
		out = append(out, filepath.Join(workflowDir, e.Name()))
	}
	if len(out) == 0 {
		t.Fatalf("no workflow files under %s", workflowDir)
	}
	return out
}
