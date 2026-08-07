package site

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Cache is a TTL'd on-disk cache for one site's metadata: the deployment
// probe, field ids, issue types, transitions.
//
// It is disposable by design. Everything here can be re-fetched, which is why
// it lives under $XDG_CACHE_HOME and why a write failure is never fatal — a
// user who clears a cache must lose nothing but time.
type Cache struct {
	// Dir is the site's cache directory.
	Dir string
	// Now stamps an entry. It is a field rather than a call to time.Now so
	// that a caller with an injected clock writes entries its own reads will
	// accept — otherwise a resolver with a fake clock stores real timestamps
	// and every entry it writes reads back as expired or as stored in the
	// future.
	Now func() time.Time
}

func (c *Cache) now() time.Time {
	if c == nil || c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// entry wraps a cached value with when it was stored, so the TTL is checked
// against the write rather than against the file's mtime, which a backup tool
// or a copy would reset.
type entry struct {
	StoredAt time.Time       `json:"storedAt"`
	Value    json.RawMessage `json:"value"`
}

// keyFormat is what a cache key may be. It is narrow because the key becomes a
// filename, and a key that could contain a separator could escape the cache
// directory.
var keyFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Get reads a cached value that is still within ttl. A missing, expired, or
// unreadable entry reports ok=false rather than failing: the caller can always
// re-fetch, and a corrupt cache must never break a command.
//
// The read and the write share one clock, taken from the cache itself. Passing
// a time in here separately is what let a caller stamp entries with one clock
// and expire them against another.
func (c *Cache) Get(key string, ttl time.Duration, into any) (bool, error) {
	now := c.now()

	path, err := c.path(key)
	if err != nil || path == "" {
		return false, err
	}

	// Every failure below is a cache miss rather than an error. A missing
	// entry, an unreadable one, and a corrupt one are all answered the same
	// way — by fetching again — and turning any of them into a failed command
	// would mean a stale byte on disk could break a working setup.
	data, readErr := os.ReadFile(path) //nolint:gosec // the key is validated against keyFormat.
	if readErr != nil {
		return false, nil //nolint:nilerr // a cache miss, not a failure.
	}

	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return false, nil //nolint:nilerr // a corrupt entry is a cache miss.
	}
	if now.Sub(e.StoredAt) > ttl || e.StoredAt.After(now) {
		// An entry stored in the future means the clock moved backwards.
		// Treating it as expired is the safe reading.
		return false, nil
	}
	if err := json.Unmarshal(e.Value, into); err != nil {
		return false, nil //nolint:nilerr // a value of the wrong shape is a cache miss.
	}
	return true, nil
}

// Put stores a value. A failure is returned but callers are expected to ignore
// it: a cache that cannot be written costs a round trip, not correctness.
func (c *Cache) Put(key string, value any) error {
	path, err := c.path(key)
	if err != nil || path == "" {
		return err
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return errs.Runtime("CACHE_ENCODE", "cannot encode a cache value").Wrap(err)
	}
	data, err := json.Marshal(entry{StoredAt: c.now(), Value: payload})
	if err != nil {
		return errs.Runtime("CACHE_ENCODE", "cannot encode a cache entry").Wrap(err)
	}

	// 0700, because the entries in this directory are named for the site. Every
	// file under it is already 0600, so the directory mode was the only thing
	// publishing the Jira hostname to other users on the machine — a listing is
	// enough, no file needs to be readable.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errs.Runtime("CACHE_UNWRITABLE", "cannot create %s", filepath.Dir(path)).Wrap(err)
	}
	return writeAtomic(path, data)
}

// Clear removes every entry for this site, which is what --refresh does when a
// caller wants nothing carried over.
func (c *Cache) Clear() error {
	if c == nil || c.Dir == "" {
		return nil
	}
	if err := os.RemoveAll(c.Dir); err != nil {
		return errs.Runtime("CACHE_UNWRITABLE", "cannot clear %s", c.Dir).Wrap(err)
	}
	return nil
}

func (c *Cache) path(key string) (string, error) {
	if c == nil || c.Dir == "" {
		return "", nil
	}
	if !keyFormat.MatchString(key) {
		return "", errs.Runtime("INVALID_CACHE_KEY", "%q is not a valid cache key", key)
	}
	return filepath.Join(c.Dir, key+".json"), nil
}

// writeAtomic writes via a temporary file in the same directory then renames,
// so a crash mid-write leaves the previous entry rather than a truncated one
// that would then be parsed as garbage.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return errs.Runtime("CACHE_UNWRITABLE", "cannot create a temporary file in %s", dir).Wrap(err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errs.Runtime("CACHE_UNWRITABLE", "cannot write %s", tmpName).Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return errs.Runtime("CACHE_UNWRITABLE", "cannot close %s", tmpName).Wrap(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errs.Runtime("CACHE_UNWRITABLE", "cannot replace %s", path).Wrap(err)
	}
	return nil
}
