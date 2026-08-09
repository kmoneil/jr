package idem

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// lockPath is where a ledger's lock lives, spelled once so a test that asserts
// about it cannot drift from the code that creates it.
func lockPathFor(dir string) string {
	return filepath.Join(dir, "idempotency.toml.lock")
}

// TestABrokenLockIsNotDeletedByTheRunItWasTakenFrom is the regression test for
// the composition, and it has to drive both halves because neither is wrong on
// its own.
//
// TestTheLockIsReleasedOnEveryPath proves a release happens.
// TestAnAbandonedLockIsBroken proves a stale lock is broken. Both passed while
// releasing by path let this happen:
//
//	A takes the lock and stalls past LockStale — SIGSTOP, a suspended laptop,
//	a paused container, a wedged mount. B breaks the stale lock and takes it.
//	A wakes and releases, deleting *B's* lock. C walks in while B is still
//	inside, and two writers read-modify-write a file that is replaced whole by
//	rename, so one of them loses an entry. A lost claim is a create that runs
//	twice, which is the one thing this package exists to prevent.
func TestABrokenLockIsNotDeletedByTheRunItWasTakenFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotency.toml")
	lock := lockPathFor(dir)

	a := &Ledger{Path: path}
	b := &Ledger{Path: path}
	c := &Ledger{Path: path, LockWait: 50 * time.Millisecond}

	unlockA, err := a.lock()
	if err != nil {
		t.Fatalf("A could not take the lock: %v", err)
	}

	// A stalls. Age is the only signal a lock has, deliberately — a pid means
	// nothing across containers or after a reboot.
	old := time.Now().Add(-2 * LockStale)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	unlockB, err := b.lock()
	if err != nil {
		t.Fatalf("B could not break the stale lock: %v", err)
	}
	heldByB := readLockID(lock)
	if heldByB == "" {
		t.Fatal("B holds the lock and the file carries no id")
	}

	// A wakes up and releases. It must not touch the lock that is now B's, and
	// it must say that it was presumed dead rather than returning quietly.
	relErr := unlockA()
	if relErr == nil {
		t.Error("A released a lock it no longer held without saying so")
	} else if code := errs.Coerce(relErr).Code; code != "LEDGER_LOCK_STOLEN" {
		t.Errorf("A's release reported %q, want LEDGER_LOCK_STOLEN", code)
	}

	if got := readLockID(lock); got != heldByB {
		t.Fatalf("A's release removed or replaced B's lock: id %q, want %q", got, heldByB)
	}

	// And the consequence the whole test is about: C must still be shut out.
	if _, err := c.lock(); err == nil {
		t.Error("C entered the critical section while B was still inside it")
	} else if code := errs.Coerce(err).Code; code != "LEDGER_LOCKED" {
		t.Errorf("C was refused with %q, want LEDGER_LOCKED", code)
	}

	if err := unlockB(); err != nil {
		t.Errorf("B's own release failed: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("B's release left the lock file behind")
	}
}

// TestAReleaseRemovesNothingItDoesNotOwn covers the same rule from the side a
// caller cannot reach: a lock file this process did not write is never removed,
// whatever it contains.
//
// The old format was a bare pid and the tests still write one, so an
// unparseable first line has to be a lock somebody else holds rather than a
// lock nobody holds.
func TestAReleaseRemovesNothingItDoesNotOwn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"a pid, the format this replaced", "99999\n"},
		{"another run's id", "SOMEBODYELSESIDVALUE\n1234\n"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lock := lockPathFor(dir)
			if err := os.WriteFile(lock, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write lock: %v", err)
			}

			if err := release(lock, "this-runs-id"); err == nil {
				t.Error("release reported success for a lock it did not own")
			}
			if _, err := os.Stat(lock); err != nil {
				t.Errorf("release removed a lock it did not own: %v", err)
			}
		})
	}
}

// TestAMissingLockIsReportedRatherThanIgnored covers the third shape: the file
// is gone entirely by the time the release runs, which means somebody decided
// this run was dead. There is nothing to remove and still something to say.
func TestAMissingLockIsReportedRatherThanIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := release(lockPathFor(dir), "this-runs-id"); err == nil {
		t.Error("a release whose lock had vanished reported success")
	}
}

// TestAClaimWhoseLockWasStolenIsNotReportedAsClaimed is the end of the chain,
// and the reason the release reports rather than only declining to delete.
//
// A claim written under a stolen lock raced another writer, and the ledger is
// replaced whole by rename — so this run's entry may be the one that lost. A
// caller told "claimed" sends the request believing a retry is protected, and
// the retry then finds no entry and sends it again. Refusing is the only honest
// answer available: nothing was sent yet, and the ledger cannot say what it
// holds.
//
// The steal happens inside the injected clock, which Claim calls once between
// reading the ledger and writing to it. That is a seam that already exists for
// expiring entries, and it makes this deterministic rather than a race the test
// has to win.
func TestAClaimWhoseLockWasStolenIsNotReportedAsClaimed(t *testing.T) {
	dir := t.TempDir()
	lock := lockPathFor(dir)

	stolen := false
	l := &Ledger{Path: filepath.Join(dir, "idempotency.toml")}
	l.Now = func() time.Time {
		if !stolen {
			stolen = true
			// Another run decided this one was dead and took the lock.
			if err := os.WriteFile(lock, []byte("ANOTHERRUNSID\n1234\n"), 0o600); err != nil {
				t.Fatalf("steal the lock: %v", err)
			}
		}
		return time.Now()
	}

	out, err := l.Claim("https://site.invalid", "k", "issue.create")
	if !stolen {
		t.Fatal("the clock was never called, so nothing was stolen and this test asserted nothing")
	}
	if err == nil {
		t.Fatal("a claim written under a stolen lock was reported as a claim")
	}
	if code := errs.Coerce(err).Code; code != "LEDGER_LOCK_STOLEN" {
		t.Errorf("code = %q, want LEDGER_LOCK_STOLEN", code)
	}
	if out.Claimed {
		t.Error("the outcome still says Claimed, so a caller would send the request")
	}
}

// TestAReadOnlyClaimIsNotFailedByAStolenLock is the other half of that
// decision, and it is why Claim tracks whether it wrote.
//
// A replay reads and returns a recorded result. The ledger is replaced by
// rename, so the read saw one whole file or another and never a torn one — the
// answer is right even if the lock was stolen underneath it. Failing here would
// turn a safe replay into an error and push a caller towards retrying the
// mutation, which is the opposite of what the refusal is for.
func TestAReadOnlyClaimIsNotFailedByAStolenLock(t *testing.T) {
	dir := t.TempDir()
	lock := lockPathFor(dir)
	path := filepath.Join(dir, "idempotency.toml")
	const site = "https://site.invalid"

	// A completed claim, so the next one is a replay.
	seed := &Ledger{Path: path}
	if _, err := seed.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := seed.Complete(site, "k", `{"key":"ENG-1","id":"1"}`); err != nil {
		t.Fatalf("seed complete: %v", err)
	}

	stolen := false
	l := &Ledger{Path: path}
	l.Now = func() time.Time {
		if !stolen {
			stolen = true
			if err := os.WriteFile(lock, []byte("ANOTHERRUNSID\n1234\n"), 0o600); err != nil {
				t.Fatalf("steal the lock: %v", err)
			}
		}
		return time.Now()
	}

	out, err := l.Claim(site, "k", "issue.create")
	if !stolen {
		t.Fatal("the clock was never called, so nothing was stolen")
	}
	if err != nil {
		t.Fatalf("a replay was failed by a stolen lock: %v", err)
	}
	if !out.Replayed {
		t.Error("the replay was lost")
	}
}
