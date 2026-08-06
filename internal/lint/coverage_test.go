package lint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fullyCovered names the packages whose statement coverage must be total, with
// the reason each one earns a gate the rest of the tree does not have.
//
// This is a short list on purpose. A blanket coverage number rewards tests that
// execute lines without asserting anything; a total requirement on the one
// package where an uncovered branch is a security bug is a different claim.
var fullyCovered = map[string]string{
	"internal/jql": "it owns the only place a value is quoted, and an " +
		"unexercised branch there is an injection nobody ran",
}

// TestTheFullyCoveredPackagesAre asserts what the invariant list has claimed
// since the project started and nothing checked.
//
// It was written after moving thirty lines into internal/jql dropped it to
// 96.7% and every gate stayed green. A rule enforced by remembering is a rule
// with a half-life.
func TestTheFullyCoveredPackagesAre(t *testing.T) {
	for pkg, why := range fullyCovered {
		got := statementCoverage(t, pkg)
		if got < 100 {
			t.Errorf("%s is at %.1f%% statement coverage and must be at 100%%: %s\n"+
				"    go test ./%s/ -coverprofile=/tmp/c.out && "+
				"go tool cover -html=/tmp/c.out", pkg, got, why, pkg)
		}
	}
}

// statementCoverage runs one package's tests and returns its total, by asking
// the toolchain rather than parsing source.
func statementCoverage(t *testing.T, pkg string) float64 {
	t.Helper()

	profile := filepath.Join(t.TempDir(), "cover.out")
	test := exec.Command("go", "test", "-coverprofile="+profile, "./"+pkg+"/")
	test.Dir = "../.."
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("go test ./%s/: %v\n%s", pkg, err, out)
	}

	report := exec.Command("go", "tool", "cover", "-func="+profile)
	report.Dir = "../.."
	out, err := report.Output()
	if err != nil {
		t.Fatalf("go tool cover: %v\n%s", err, stderrOf(err))
	}

	// The last line is "total:\t(statements)\t100.0%".
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "total:") {
		t.Fatalf("go tool cover ended with %q, not a total", last)
	}
	percent := strings.TrimSuffix(last[strings.LastIndex(last, "\t")+1:], "%")
	value, err := strconv.ParseFloat(strings.TrimSpace(percent), 64)
	if err != nil {
		t.Fatalf("cannot read the coverage total from %q: %v", last, err)
	}
	return value
}

// TestTheCoverageGateNamesRealPackages keeps the list above from outliving the
// packages in it.
func TestTheCoverageGateNamesRealPackages(t *testing.T) {
	for pkg := range fullyCovered {
		if _, err := os.Stat(filepath.Join("../..", pkg)); err != nil {
			t.Errorf("the coverage gate names %q, which is not a package: %v", pkg, err)
		}
	}
}
