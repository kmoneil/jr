//go:build windows

package idem

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// removeLock removes the lock file, waiting out a process that has it open.
//
// Windows refuses to delete a file while any handle to it lacks
// FILE_SHARE_DELETE, and os.Open never passes that. Every waiter polls every
// lockPoll and reads the lock's id each time, so with a few of them the file
// is open more often than not, and the holder's remove failed with a sharing
// violation that release discarded. The lock stayed, every waiter stopped at
// LockTimeout with LEDGER_LOCKED, and nobody could claim until LockStale:
// TestConcurrentClaimsElectOneWinner, on the first Windows run the suite had.
func removeLock(path string) error {
	deadline := time.Now().Add(transientWait)
	for {
		err := os.Remove(path)
		if err == nil || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
			time.Now().After(deadline) {
			return err
		}
		time.Sleep(lockPoll)
	}
}

// deletePending reports whether a failed create is Windows refusing to reuse a
// name whose file is still being deleted. It says so as access denied, which
// on the second run of the suite on Windows reached a racer as
// LEDGER_UNWRITABLE mid-release.
func deletePending(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
