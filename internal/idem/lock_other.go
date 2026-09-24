//go:build !windows

package idem

import "os"

// removeLock removes the lock file. On Unix a file is removed whoever has it
// open, which is the property the Windows version has to work for.
func removeLock(path string) error {
	return os.Remove(path)
}

// deletePending is false on Unix, where a removed name is gone at once and a
// create over it either succeeds or fails for a reason that lasts.
func deletePending(error) bool { return false }
