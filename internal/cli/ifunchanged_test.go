//go:build write

package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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
// Found by the probe behind TestEveryModePairIsDecided. Issue 270's writes
// compare from the start, and are held to it here.
func TestADryRunComparesTheBaseline(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repro.py")
	if err := os.WriteFile(file, []byte("print('x')\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"edit", []string{"issue", "edit", "ENG-1", "--summary", "x"}},
		{"move", []string{"issue", "move", "ENG-1", "Done"}},
		{"assign", []string{"issue", "assign", "ENG-1", "Ada Lovelace"}},
		{"upload", []string{"issue", "attachment", "upload", "ENG-1", file}},
		{"comment add", []string{"issue", "comment", "add", "ENG-1", "x"}},
		{"comment edit", []string{"issue", "comment", "edit", "ENG-1", "10042", "x"}},
		{"comment delete", []string{"issue", "comment", "delete", "ENG-1", "10042"}},
		{"worklog add", []string{"issue", "worklog", "add", "ENG-1", "3h"}},
		{"worklog delete", []string{"issue", "worklog", "delete", "ENG-1", "10042"}},
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

// TestAConfirmedDeletionStillComparesTheBaseline is --if-unchanged beside
// --yes on the two deletions that take both. --yes is the caller's word that
// the deletion is wanted, given against what they read; it is not a word about
// the issue as it is now, so a stale token stops the deletion unsent, and a
// fresh one lets it through.
func TestAConfirmedDeletionStillComparesTheBaseline(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"comment", []string{"issue", "comment", "delete", "ENG-1", "10042", "--yes"}},
		{"worklog", []string{"issue", "worklog", "delete", "ENG-1", "10042", "--yes"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			var deletes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/myself"):
					_, _ = w.Write([]byte(`{"name":"ada","displayName":"Ada Lovelace","timeZone":"Etc/UTC"}`))
				case strings.HasSuffix(r.URL.Path, "/serverInfo"):
					_, _ = w.Write([]byte(`{"version":"10.4.0","deploymentType":"Server",` +
						`"serverTime":"2026-09-23T12:00:00.000+0000"}`))
				case r.Method == http.MethodDelete:
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issue/ENG-1"):
					_, _ = w.Write([]byte(`{"id":"1","key":"ENG-1","fields":{"updated":"` +
						freshUpdated + `"}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			env := credentialed(t)
			mustRun(t, env, "context", "create", "work", "--site", srv.URL, "--project", "ENG")

			stale := run(t, env, slices.Concat(tc.args, []string{
				"--if-unchanged", tokenFor(t, "ENG-1", staleUpdated),
			})...)
			if stale.exit != exitcode.Conflict || !strings.Contains(stale.stderr, "STALE_WRITE") {
				t.Errorf("a confirmed deletion over a change exited %v; want STALE_WRITE "+
					"at exit 7:\n%s%s", stale.exit, stale.stdout, stale.stderr)
			}
			if n := deletes.Load(); n != 0 {
				t.Errorf("the stale deletion reached the server %d times", n)
			}

			fresh := run(t, env, slices.Concat(tc.args, []string{
				"--if-unchanged", tokenFor(t, "ENG-1", freshUpdated),
			})...)
			if fresh.exit != exitcode.OK || !strings.Contains(fresh.stdout, `<precondition method="read-compare"/>`) {
				t.Errorf("a confirmed deletion of an unchanged issue exited %v:\n%s%s",
					fresh.exit, fresh.stdout, fresh.stderr)
			}
			if n := deletes.Load(); n != 1 {
				t.Errorf("the deletion reached the server %d times, want once", n)
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
