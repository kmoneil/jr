//go:build !windows

package cli_test

import (
	"os"
	"testing"
)

// openToOthers makes a credential store readable by other users, which the
// store refuses to read.
func openToOthers(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
}

// storeRemedy is the command a STORE_PERMISSIONS remedy names here.
const storeRemedy = "chmod"
