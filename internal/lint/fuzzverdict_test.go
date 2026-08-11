package lint_test

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The fuzz sweep classifies a failing target instead of believing it, because
// Go 1.26 fails targets that found nothing: `internal/fuzz` races its own
// -fuzztime deadline against the context it uses to suppress that deadline, and
// the leftover `context deadline exceeded` is reported as the target's result.
//
// The risk that classification introduces is the only one worth testing. A
// sweep that can call a real crasher a flake is worse than a sweep that flakes,
// so these drive the actual script with the actual shapes `go test -fuzz`
// produces and require it to blame the target every time there is a target to
// blame.
//
// The outputs below are real, not invented: the flake is the run that raised
// the card, and the crasher is what the same target printed the first time it
// found something.

const flakeOutput = `fuzz: elapsed: 0s, gathering baseline coverage: 0/359 completed
fuzz: elapsed: 1s, gathering baseline coverage: 359/359 completed, now fuzzing with 10 workers
fuzz: elapsed: 18s, execs: 1289430 (73443/sec), new interesting: 8 (total: 367)
fuzz: elapsed: 21s, execs: 1430825 (0/sec), new interesting: 8 (total: 367)
--- FAIL: FuzzRelativeStaysOnTheSite (21.03s)
    context deadline exceeded
FAIL
exit status 1
FAIL	github.com/kmoneil/jr/internal/transport	21.047s`

const crasherOutput = `fuzz: elapsed: 0s, gathering baseline coverage: 0/24 completed
fuzz: elapsed: 1s, gathering baseline coverage: 24/24 completed, now fuzzing with 10 workers
fuzz: elapsed: 3s, execs: 148258 (49418/sec), new interesting: 2 (total: 26)
--- FAIL: FuzzRelativeStaysOnTheSite (3.12s)
    --- FAIL: FuzzRelativeStaysOnTheSite (0.00s)
        relative_fuzz_test.go:82: Relative("/jira//x") = "//x", which names ""://"x"

    Failing input written to testdata/fuzz/FuzzRelativeStaysOnTheSite/6f1a2b3c4d5e
    To re-run:
    go test -run=FuzzRelativeStaysOnTheSite/6f1a2b3c4d5e
FAIL
exit status 1
FAIL	github.com/kmoneil/jr/internal/transport	3.146s`

func TestTheFuzzSweepNeverCallsACrasherAFlake(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		output string
		want   string
	}{
		{"a clean run", 0, "fuzz: elapsed: 20s, execs: 1514827 (83295/sec)\nPASS\nok\tpkg\t20.160s", "pass"},
		{"the deadline race", 1, flakeOutput, "flake"},
		{"a real crasher", 1, crasherOutput, "fail"},

		// A seed entry fails before any input is written, so the marker the
		// flake is recognised by — no crasher on disk — is true of a genuine
		// failure too. Named separately in the script for that reason.
		{"a failing seed entry", 1, "failure while testing seed corpus entry: FuzzX/seed#3\n" +
			"--- FAIL: FuzzX (0.02s)\n    context deadline exceeded\nFAIL", "fail"},

		// A different upstream spurious failure (golang/go#56238). This script
		// has no evidence about it and must not absorb it.
		{"a hung worker", 1, "--- FAIL: FuzzX (5.01s)\n" +
			"    fuzzing process hung or terminated unexpectedly: exit status 2\n" +
			"    context deadline exceeded\nFAIL", "fail"},

		// The phrase inside a target's own failure message, which is a real
		// finding that happens to read like the flake.
		{"a target that fails saying it", 1, "--- FAIL: FuzzX (1.00s)\n" +
			"    x_test.go:14: Do(\"a\") = error \"context deadline exceeded\", want nil\n" +
			"    Failing input written to testdata/fuzz/FuzzX/abc\nFAIL", "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "../../scripts/fuzz-verdict.sh", strconv.Itoa(tc.status))
			cmd.Stdin = strings.NewReader(tc.output)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("fuzz-verdict.sh: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("verdict = %q, want %q.\nA wrong verdict here is the sweep "+
					"lying in one direction or the other: %q means a finding is "+
					"discarded, %q means the toolchain's own bug fails the build.",
					got, tc.want, "flake", "fail")
			}
		})
	}
}

// TestTheFuzzFlakeWorkaroundIsStillNeeded deletes the workaround's reason to
// exist as soon as the toolchain carries the fix.
//
// Scaffolding for somebody else's bug is the kind that outlives the bug by
// years, because nothing goes looking for it and it costs nothing visible to
// keep. The fix is golang/go#75804 via https://go.dev/cl/774140, backported to
// release-branch.go1.27 by https://go.dev/cl/804900 — after go1.27rc2 was cut,
// so go1.27.0 is the earliest build that has it. No 1.26.x will.
func TestTheFuzzFlakeWorkaroundIsStillNeeded(t *testing.T) {
	major, minor, release := toolchainVersion(t, runtime.Version())
	if !release || major < 1 || (major == 1 && minor < 27) {
		return
	}
	t.Errorf("this toolchain is %s, which carries the fix for golang/go#75804.\n"+
		"Delete scripts/fuzz-verdict.sh, restore the plain FAIL branch in the "+
		"Makefile's fuzz target, and delete this test and its neighbour: a sweep "+
		"that classifies a failure it can no longer see is a sweep with a rule "+
		"nobody can check.", runtime.Version())
}

var goVersion = regexp.MustCompile(`^go(\d+)\.(\d+)(?:\.(\d+))?(.*)$`)

// toolchainVersion reads runtime.Version(). release is false for anything
// carrying a suffix — an rc or a devel build — because the backport landed
// between go1.27rc2 and go1.27.0 and a prerelease cannot be assumed to have it.
func toolchainVersion(t *testing.T, v string) (major, minor int, release bool) {
	t.Helper()

	m := goVersion.FindStringSubmatch(v)
	if m == nil {
		// A devel or vendor-built toolchain. Nothing to conclude, and failing
		// here would be this test asserting its own regexp.
		t.Logf("cannot read a version out of %q, so the workaround stands", v)
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, m[4] == ""
}
