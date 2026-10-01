//go:build write

package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// The plan-verbs fake serves every issue at freshUpdated; a token minted at
// staleUpdated describes a read made before somebody else's edit.
const (
	freshUpdated = "2026-09-23T10:00:00.000+0000"
	staleUpdated = "2026-09-23T09:00:00.000+0000"
)

// tokenFor mints the precondition jr hands out for an issue read at updated.
func tokenFor(t *testing.T, key, updated string) string {
	t.Helper()
	token, err := issue.EncodePrecondition(site.Info{Kind: site.DataCenter},
		key, updated, issue.PrecisionSecond)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return token
}

// TestADryRunComparesTheBaseline is --if-unchanged beside --dry-run. The
// preview returned before the comparison, so a dry run of a write that would
// be refused STALE_WRITE printed "would send" at exit 0, on all three verbs.
// Found by the probe behind TestEveryModePairIsDecided.
func TestADryRunComparesTheBaseline(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"edit", []string{"issue", "edit", "ENG-1", "--summary", "x"}},
		{"move", []string{"issue", "move", "ENG-1", "Done"}},
		{"assign", []string{"issue", "assign", "ENG-1", "Ada Lovelace"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			rec := newPlanVerbsRecorder()
			url := planVerbsJira(t, rec)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")

			stale := run(t, env, slices.Concat(tc.args, []string{
				"--dry-run", "--if-unchanged", tokenFor(t, "ENG-1", staleUpdated),
			})...)
			if stale.exit != exitcode.Conflict || !strings.Contains(stale.stderr, "STALE_WRITE") {
				t.Errorf("a dry run of a stale %s exited %v; want STALE_WRITE at "+
					"exit 7, as the write would be:\n%s%s", tc.verb, stale.exit,
					stale.stdout, stale.stderr)
			}

			fresh := run(t, env, slices.Concat(tc.args, []string{
				"--dry-run", "--if-unchanged", tokenFor(t, "ENG-1", freshUpdated),
			})...)
			if fresh.exit != exitcode.OK || !strings.Contains(fresh.stdout, `kind="dry-run"`) {
				t.Errorf("a dry run of an unchanged %s exited %v:\n%s", tc.verb,
					fresh.exit, fresh.stderr)
			}
			if n := rec.writes(); n != 0 {
				t.Errorf("a dry run wrote to %d issues", n)
			}
		})
	}
}

// TestAPlanComparesTheCallersBaseline is --if-unchanged beside --plan-out. The
// token was checked for its format and never compared, so a plan was written
// over a change the caller never saw, with fresh baselines from its own
// search, and the apply that followed wrote over the change.
//
// The token may name any issue the plan holds. It had to name the first,
// because the check that tied a token to its issue read only the first key.
func TestAPlanComparesTheCallersBaseline(t *testing.T) {
	for _, tc := range []struct {
		verb string
		plan []string
	}{
		{"edit", []string{"issue", "edit", "ENG-1", "ENG-3", "--add-label", "triaged"}},
		{"move", []string{"issue", "move", "ENG-1", "ENG-3", "Done"}},
		{"assign", []string{"issue", "assign", "ENG-1", "ENG-3", "Ada Lovelace"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			rec := newPlanVerbsRecorder()
			url := planVerbsJira(t, rec)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", url, "--project", "ENG")
			path := filepath.Join(t.TempDir(), "plan.xml")
			plan := func(key, updated string) result {
				return run(t, env, slices.Concat(tc.plan, []string{
					"--plan-out", path, "--if-unchanged", tokenFor(t, key, updated),
				})...)
			}

			for _, key := range []string{"ENG-1", "ENG-3"} {
				stale := plan(key, staleUpdated)
				if stale.exit != exitcode.Conflict || !strings.Contains(stale.stderr, "STALE_WRITE") {
					t.Errorf("a plan over a change to %s exited %v; want STALE_WRITE "+
						"at exit 7:\n%s%s", key, stale.exit, stale.stdout, stale.stderr)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("a plan over a change to %s was written anyway", key)
					_ = os.Remove(path)
				}
			}

			if fresh := plan("ENG-3", freshUpdated); fresh.exit != exitcode.OK {
				t.Errorf("a plan whose token names its second issue exited %v:\n%s",
					fresh.exit, fresh.stderr)
			}

			other := plan("ENG-2", freshUpdated)
			if other.exit != exitcode.Usage || !strings.Contains(other.stderr, "INVALID_PRECONDITION") {
				t.Errorf("a token for an issue the plan does not hold exited %v; "+
					"want INVALID_PRECONDITION:\n%s", other.exit, other.stderr)
			}
			if n := rec.writes(); n != 0 {
				t.Errorf("planning wrote to %d issues", n)
			}
		})
	}
}
