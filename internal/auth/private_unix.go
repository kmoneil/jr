//go:build !windows

package auth

import (
	"io/fs"
	"os"

	"github.com/kmoneil/jr/internal/errs"
)

// checkPrivate refuses a credential file anyone but its owner can open.
//
// Any bit for group or other, not only read: a file others can write is one
// they can replace with a credential of their choosing.
func checkPrivate(path string, info fs.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return errs.Auth("STORE_PERMISSIONS",
			"%s is readable by other users", path).
			WithDetail("mode is %04o, want %04o", perm, storePerm).
			WithRemedy("run: chmod 600 %s", path)
	}
	return nil
}

// restrictPrivate makes a file just created for the store private to its
// owner, before anything is written to it.
func restrictPrivate(f *os.File) error {
	return f.Chmod(storePerm)
}
