package idem

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

// conflictExit is what a reused key exits with: a precondition failed.
const conflictExit = exitcode.Conflict

// LockTimeout is how long a claim waits for another process to finish its
// read-modify-write of the ledger.
//
// The critical section is a file read, a map update, and a rename, so anything
// beyond a moment means the holder is wedged rather than busy.
const LockTimeout = 5 * time.Second

// LockStale is how long a lock file is honored before it is presumed
// abandoned.
//
// A process killed between taking the lock and releasing it would otherwise
// block every later claim forever. This is not the same as StaleClaim: this is
// about a wedged *writer*, that one is about an unfinished *request*.
//
// 30 seconds is enormous for what it covers. The critical section is a file
// read, a map update, and a rename, and the HTTP request happens after the
// lock is released — so a holder still inside it after 30 seconds is not slow,
// it is away: SIGSTOP, a suspended laptop, a paused container, a wedged NFS or
// overlay mount. The number is generous on purpose, because breaking a live
// holder's lock is the worse error of the two and the cost of waiting is one
// command feeling slow. Anything that makes it *shorter* needs to argue about
// those cases specifically, not about how long the work takes.
const LockStale = 30 * time.Second

// lockPoll is how often a waiter re-checks. Short, because the wait is
// normally microseconds and a long poll would dominate it.
const lockPoll = 2 * time.Millisecond

// lockID identifies one holder's lock file, so a release can tell its own lock
// from somebody else's.
//
// It replaces the pid that used to be written here. A pid was for a human
// debugging a stuck lock and nothing read it, because a pid means nothing
// across containers or after a reboot — which left the file with no identity
// at all, and a release with nothing to check. This is a value that means the
// same thing anywhere, so it can do both jobs.
func lockID() string { return rand.Text() }

// errLockStolen is what a release reports when the lock it held is no longer
// the lock on disk.
//
// It is not a failure of the release. It means this process was presumed dead
// while it was still working: something broke its lock as stale, and whatever
// this process wrote to the ledger raced another writer. The write may have
// been the one that lost, so a caller has to treat what it just did as
// unrecorded rather than as done.
//
// A function and not a package-level value, because errs.Error's builders
// mutate the receiver — a shared one would let any caller's WithDetail or Wrap
// rewrite every future copy of this error.
func errLockStolen() *errs.Error {
	return errs.Runtime("LEDGER_LOCK_STOLEN",
		"this run was presumed dead while it held the idempotency ledger").
		WithDetail("another run broke the lock, so this run's write may have been lost").
		WithRemedy("read the issue before retrying; the ledger cannot say whether it applied")
}

// lock takes the ledger's write lock and returns the release function.
//
// It exists because the ledger's whole promise is that two processes racing
// with one key cannot both be told they claimed it — and a read-modify-write of
// a shared file without a lock gives exactly that. An O_EXCL create is the
// primitive: it is atomic on every filesystem this runs on, and needs no
// platform-specific syscall.
//
// The release removes the lock only if it is still the one this call created.
// Removing by path alone composed with breakStaleLock into a third writer: a
// holder that stalled past LockStale had its lock broken by B, and then on
// waking deleted *B's* lock, letting C in while B was still inside. Each half
// was right and the composition was not, which is why the regression test
// drives both together rather than either alone.
func (l *Ledger) lock() (func() error, error) {
	path := l.Path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errs.Runtime("LEDGER_UNWRITABLE",
			"cannot create %s", filepath.Dir(path)).Wrap(err)
	}

	wait := l.LockWait
	if wait <= 0 {
		wait = LockTimeout
	}
	// Wall clock, deliberately, not the ledger's injected one. The injected
	// clock exists so a test can age an *entry* without waiting; a test that
	// froze it here would spin forever, because the deadline could never pass.
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			id := lockID()
			// The pid follows the id on its own line, still for a human
			// debugging a stuck lock. Only the first line is read.
			_, _ = f.WriteString(id + "\n" + strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Close()
			return func() error { return release(path, id) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, errs.Runtime("LEDGER_UNWRITABLE",
				"cannot lock %s", l.Path).Wrap(err)
		}

		if l.breakStaleLock(path) {
			continue
		}
		if time.Now().After(deadline) {
			return nil, errs.Runtime("LEDGER_LOCKED",
				"another %s is holding the idempotency ledger", "jr").
				WithDetail("waited %s for %s", wait, path).
				WithRemedy("if no other run is active, delete the lock file")
		}
		time.Sleep(lockPoll)
	}
}

// release removes the lock file if it still carries this holder's id.
//
// A missing file and a different id are the same answer: somebody else decided
// this holder was dead. Saying so is the whole point — the alternative is
// removing whatever is there, which is how one broken lock becomes two live
// writers.
func release(path, id string) error {
	if readLockID(path) != id {
		return errLockStolen()
	}
	// There is still a window here: between the read above and this remove,
	// a holder that has gone stale could have its lock broken and replaced.
	// It cannot be closed without an atomic compare-and-delete, which no
	// portable filesystem primitive offers. It is microseconds wide and needs
	// this process to be stale at exactly that moment, where the version this
	// replaces was wide open for as long as the stall lasted.
	_ = os.Remove(path)
	return nil
}

// lockLost releases the lock and reports a theft worth telling the caller
// about, or nil. It is the one place unlock is called, so the two wrappers
// below cannot both release or both forget to.
//
// wrote is what decides whether a stolen lock matters. Every locked call here
// reads before it writes, and a read under a stolen lock is still sound: the
// ledger is replaced whole by rename, so the read saw one entire file or
// another and never a torn one, and every read-only branch refuses rather than
// proceeds. A *write* under a stolen lock is the one with something to lose —
// another run replaced the file, and this run's entry may be what it replaced.
//
// An error already on its way out wins. It describes what the caller asked
// about; the lock is how this run found out, and reporting the lock instead
// would answer a question nobody asked.
func lockLost(err error, wrote bool, unlock func() error) error {
	if uerr := unlock(); uerr != nil && wrote && err == nil {
		return uerr
	}
	return nil
}

// released folds a release into the error a locked call is about to return.
func released(err error, wrote bool, unlock func() error) error {
	if uerr := lockLost(err, wrote, unlock); uerr != nil {
		return uerr
	}
	return err
}

// releasedOutcome is released for the one caller that returns a value beside
// its error. A stolen lock takes the Outcome with it, so no caller can read
// Claimed off a claim that may not be in the ledger.
func releasedOutcome(
	out Outcome, err error, wrote bool, unlock func() error,
) (Outcome, error) {
	if uerr := lockLost(err, wrote, unlock); uerr != nil {
		return Outcome{}, uerr
	}
	return out, err
}

// readLockID returns the id on a lock file's first line, or "" if there is
// none to read. An unreadable or foreign lock file yields "", which no id
// matches — so it is never removed by a release that does not own it.
func readLockID(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // the path comes from XDG resolution.
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
}

// breakStaleLock removes a lock file left behind by a dead process, reporting
// whether it did.
//
// Age is the only usable signal. A pid check would be wrong across containers
// and after a reboot, and would break the lock while the holder was alive.
func (l *Ledger) breakStaleLock(path string) bool {
	// Read the id before judging the file, so what gets removed is the same
	// lock that was found stale. Without it the holder could release and a new
	// holder take the lock between the stat and the remove, and this would
	// break the new one — the same defect the release side had, one layer up.
	before := readLockID(path)

	info, err := os.Stat(path)
	if err != nil {
		// It vanished between the failed create and this stat, which means the
		// holder released it. Retrying is the right move either way.
		return errors.Is(err, fs.ErrNotExist)
	}
	if time.Since(info.ModTime()) < LockStale {
		return false
	}
	if readLockID(path) != before {
		// Somebody else's lock now. Whether it is stale is a question for the
		// next pass, with its own timestamp.
		return false
	}
	// Removing the wrong file here would let two writers in at once, so a
	// failed remove is not treated as success.
	return os.Remove(path) == nil
}
