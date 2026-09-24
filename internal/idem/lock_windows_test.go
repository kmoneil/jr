//go:build windows

package idem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAReleaseOutlastsAWaiterReadingTheLock reproduces what the racing claims
// did on Windows, one reader and one release at a time: a waiter holds the lock
// file open to read its id while the holder releases it.
func TestAReleaseOutlastsAWaiterReadingTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotency.toml.lock")
	id := lockID()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		t.Fatalf("writing the lock: %v", err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the lock as a waiter would: %v", err)
	}

	released := make(chan error, 1)
	go func() { released <- release(path, id) }()
	time.Sleep(50 * time.Millisecond)
	if err := reader.Close(); err != nil {
		t.Fatalf("closing the waiter's handle: %v", err)
	}

	if err := <-released; err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the lock outlived its release (stat: %v), and every waiter "+
			"would wait for it to go stale", err)
	}
}
