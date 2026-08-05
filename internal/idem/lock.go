package idem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
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
const LockStale = 30 * time.Second

// lockPoll is how often a waiter re-checks. Short, because the wait is
// normally microseconds and a long poll would dominate it.
const lockPoll = 2 * time.Millisecond

// lock takes the ledger's write lock and returns the release function.
//
// It exists because the ledger's whole promise is that two processes racing
// with one key cannot both be told they claimed it — and a read-modify-write of
// a shared file without a lock gives exactly that. An O_EXCL create is the
// primitive: it is atomic on every filesystem this runs on, and needs no
// platform-specific syscall.
func (l *Ledger) lock() (func(), error) {
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
			// The pid is written for a human debugging a stuck lock. Nothing
			// reads it: a pid from another container or after a reboot means
			// nothing, so staleness is decided by age instead.
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
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

// breakStaleLock removes a lock file left behind by a dead process, reporting
// whether it did.
//
// Age is the only usable signal. A pid check would be wrong across containers
// and after a reboot, and would break the lock while the holder was alive.
func (l *Ledger) breakStaleLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		// It vanished between the failed create and this stat, which means the
		// holder released it. Retrying is the right move either way.
		return errors.Is(err, fs.ErrNotExist)
	}
	if time.Since(info.ModTime()) < LockStale {
		return false
	}
	// Removing the wrong file here would let two writers in at once, so a
	// failed remove is not treated as success.
	return os.Remove(path) == nil
}
