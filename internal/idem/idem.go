// Package idem records what a mutating request already did, so a retry does
// not do it twice.
//
// Agents retry. The transport already refuses to replay a POST after an
// upstream error, because a 503 may have arrived after Jira processed the
// request — that is the half that fails safely. This is the half that lets a
// retry *succeed* safely: a create carrying an idempotency key that has been
// used before returns the original result instead of making a second issue.
//
// The ledger is under state rather than cache. A cache is disposable by
// definition, and losing this one means a retry creates a duplicate — which is
// the exact outcome the package exists to prevent.
package idem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// ledgerPerm is the mode the ledger is written at.
//
// Not a credential, and not public either: it records which mutating requests
// were made against which site, with the idempotency key of each. Another user
// on the machine has no business reading what this account changed in Jira.
const ledgerPerm fs.FileMode = 0o600

// Status is where an entry is in its life.
type Status string

const (
	// Pending means the request was claimed but its outcome is unknown: it may
	// have been sent, and it may have been processed. This is the state that
	// matters. Reporting a pending entry as "nothing happened" is how a crashed
	// run becomes two issues on the next attempt.
	Pending Status = "pending"
	// Done means the request completed and Result holds what it produced.
	Done Status = "done"
)

// Entry is one recorded request.
type Entry struct {
	Site   string    `toml:"site"`
	Key    string    `toml:"key"`
	Status Status    `toml:"status"`
	Result string    `toml:"result,omitempty"`
	At     time.Time `toml:"at"`
	// Operation names what was claimed, e.g. "issue.create". It is stored so a
	// replay can refuse to answer a different question with a cached result.
	Operation string `toml:"operation"`
}

// DefaultTTL is how long an entry is kept.
//
// A week is long enough to cover any retry a caller would consider the same
// attempt, and short enough that the ledger does not grow without bound. A
// key reused after it expires is a new request, which is correct: nothing is
// retrying a week later.
const DefaultTTL = 7 * 24 * time.Hour

// StaleClaim is how long a pending entry is honored before it is treated as
// abandoned.
//
// It is deliberately much longer than any request: a claim released too early
// lets a second attempt run while the first is still in flight, which is the
// duplicate this package exists to prevent. Waiting is the safe direction.
const StaleClaim = 10 * time.Minute

// keyFormat is what an idempotency key may be. It is narrow because a key is a
// caller's own identifier and ends up in a file a person may read; anything
// exotic buys nothing and invites an encoding question.
var keyFormat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidateKey rejects a key this ledger cannot record.
func ValidateKey(key string) error {
	if !keyFormat.MatchString(key) {
		return errs.Usage("INVALID_IDEMPOTENCY_KEY",
			"%q is not a usable idempotency key", key).
			WithDetail("1 to 128 characters of letters, digits, and . _ : -").
			WithRemedy("a UUID works, and so does a short slug you can regenerate")
	}
	return nil
}

// DeriveKey computes a key from the request itself, for a caller who passed
// none.
//
// It exists so that "without a key, warn if an identical request succeeded
// within the last 60s" has something to compare. It is not used to suppress the
// second request — a caller who did not ask for idempotency does not silently
// get it, because two deliberate identical creates are a legitimate thing to
// want.
func DeriveKey(operation string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(operation))
	for _, p := range parts {
		// Length-prefixed so ("ab","c") and ("a","bc") do not collide, which
		// would make two different requests look like a retry of each other.
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return "auto-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// RecentWindow is how long an unkeyed identical request is treated as a
// suspected accidental repeat, per §6.3.
const RecentWindow = 60 * time.Second

// Ledger records claims and outcomes on disk.
type Ledger struct {
	// Path is the ledger file. An empty path disables the ledger entirely,
	// which is what a test that does not care gets.
	Path string
	// Now is the time source, so a test can expire an entry without waiting.
	Now func() time.Time
	// TTL overrides DefaultTTL.
	TTL time.Duration
	// LockWait overrides LockTimeout. It is here so a test can assert the
	// timeout without spending it, and so a caller that would rather fail fast
	// than queue behind another run can say so.
	LockWait time.Duration
}

func (l *Ledger) now() time.Time {
	if l == nil || l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

func (l *Ledger) ttl() time.Duration {
	if l == nil || l.TTL == 0 {
		return DefaultTTL
	}
	return l.TTL
}

// ledgerFile is the on-disk shape.
type ledgerFile struct {
	Entries map[string]Entry `toml:"entries,omitempty"`
}

const ledgerHeader = "# jr idempotency ledger. Safe to delete; deleting it means" +
	" a retry can duplicate.\n\n"

// Claim reserves (site, key) for an operation.
//
// The three outcomes are the whole design:
//
//   - claimed: nothing had this key. The caller may send the request, and must
//     call Complete or Release afterwards.
//   - replay: the key was used and the request finished. The caller must not
//     send anything and should return the recorded result.
//   - in flight: the key was claimed and has not finished. The caller must not
//     send anything, because the first attempt may still be running — and
//     doing it anyway is precisely the duplicate this prevents.
//
// The read, the decision, and the write happen under a lock, so two processes
// racing with one key cannot both be told they claimed it.
func (l *Ledger) Claim(site, key, operation string) (Outcome, error) {
	if l == nil || l.Path == "" {
		// No ledger means no protection, which is honest: the caller finds out
		// by the flag having no effect rather than by a silent duplicate.
		return Outcome{Claimed: true}, nil
	}
	if err := ValidateKey(key); err != nil {
		return Outcome{}, err
	}

	unlock, err := l.lock()
	if err != nil {
		return Outcome{}, err
	}
	defer unlock()

	file, err := l.read()
	if err != nil {
		return Outcome{}, err
	}

	now := l.now()
	id := entryID(site, key)

	if existing, ok := file.Entries[id]; ok && !l.expired(existing, now) {
		if existing.Operation != operation {
			// The same key meaning two different operations is a caller bug,
			// and answering one with the other's result would be worse than
			// saying so.
			return Outcome{}, errs.New(conflictExit, "IDEMPOTENCY_KEY_REUSED",
				"idempotency key %q was already used for %s", key, existing.Operation).
				WithDetail("this invocation is %s", operation).
				WithRemedy("use a different key, one per logical request")
		}
		switch existing.Status {
		case Done:
			return Outcome{Replayed: true, Entry: existing}, nil
		case Pending:
			if now.Sub(existing.At) < StaleClaim {
				return Outcome{InFlight: true, Entry: existing}, nil
			}
			// Past the stale window the first attempt is presumed dead. Its
			// outcome is still unknown, so the claim is handed over with that
			// said out loud rather than silently.
			return l.claim(file, id, site, key, operation, now, true)
		}
	}

	return l.claim(file, id, site, key, operation, now, false)
}

func (l *Ledger) claim(
	file *ledgerFile, id, site, key, operation string, now time.Time, reclaimed bool,
) (Outcome, error) {
	file.Entries[id] = Entry{
		Site: site, Key: key, Status: Pending, At: now, Operation: operation,
	}
	l.prune(file, now)
	if err := l.write(file); err != nil {
		return Outcome{}, err
	}
	return Outcome{Claimed: true, Reclaimed: reclaimed}, nil
}

// Outcome is what Claim decided.
type Outcome struct {
	// Claimed means the caller may proceed.
	Claimed bool
	// Replayed means the request already completed; Entry holds its result.
	Replayed bool
	// InFlight means another attempt holds the claim and has not finished.
	InFlight bool
	// Reclaimed means a previous attempt claimed this key, never finished, and
	// has passed the stale window. The caller may proceed, but the earlier
	// attempt's outcome is unknown — it may have succeeded.
	Reclaimed bool
	Entry     Entry
}

// Complete records what a claimed request produced.
func (l *Ledger) Complete(site, key, result string) error {
	return l.finish(site, key, result, true)
}

// Release gives up a claim whose request definitely did not happen.
//
// It is only correct when the request was never sent, or was refused before it
// could take effect. After an ambiguous failure the claim must be left pending:
// the stale window will hand it back eventually, and until then a retry is
// refused — which is the safe answer when nobody knows whether the first
// attempt landed.
func (l *Ledger) Release(site, key string) error {
	return l.finish(site, key, "", false)
}

func (l *Ledger) finish(site, key, result string, done bool) error {
	if l == nil || l.Path == "" {
		return nil
	}

	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()

	file, err := l.read()
	if err != nil {
		return err
	}

	id := entryID(site, key)
	entry, ok := file.Entries[id]
	if !ok {
		return nil
	}
	if !done {
		delete(file.Entries, id)
	} else {
		entry.Status = Done
		entry.Result = result
		entry.At = l.now()
		file.Entries[id] = entry
	}
	l.prune(file, l.now())
	return l.write(file)
}

// Note records a completed request that was never claimed.
//
// It backs the unkeyed warning: a create with no key gets no protection, but it
// is still worth remembering so the *next* identical one can say "this happened
// a moment ago". It is deliberately not a claim — nothing is reserved, nothing
// is blocked, and a later Claim on the same derived key would still be refused
// as a reused key rather than silently replayed.
//
// A failure is returned but callers are expected to ignore it: the request
// already succeeded, and the only cost is a warning that will not appear.
func (l *Ledger) Note(site, key, result string) error {
	if l == nil || l.Path == "" {
		return nil
	}
	if err := ValidateKey(key); err != nil {
		return err
	}

	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()

	file, err := l.read()
	if err != nil {
		return err
	}

	now := l.now()
	file.Entries[entryID(site, key)] = Entry{
		Site: site, Key: key, Status: Done, Result: result,
		At: now, Operation: noteOperation,
	}
	l.prune(file, now)
	return l.write(file)
}

// noteOperation marks an entry that was recorded rather than claimed. It is a
// distinct operation name so a later --idempotency-key using the same string
// is refused as reused instead of replaying an advisory record.
const noteOperation = "note"

// Recent reports whether an identical request finished within the window.
//
// This is what backs the unkeyed warning: a caller who did not pass a key gets
// told that the same request succeeded a moment ago, and decides for itself.
// Nothing is blocked, because two deliberate identical creates are legitimate.
func (l *Ledger) Recent(site, key string, window time.Duration) (Entry, bool, error) {
	if l == nil || l.Path == "" {
		return Entry{}, false, nil
	}

	file, err := l.read()
	if err != nil {
		return Entry{}, false, err
	}
	entry, ok := file.Entries[entryID(site, key)]
	if !ok || entry.Status != Done {
		return Entry{}, false, nil
	}
	if l.now().Sub(entry.At) > window {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

// Entries returns every live entry, ordered, for `jr` to show and for tests.
func (l *Ledger) Entries() ([]Entry, error) {
	if l == nil || l.Path == "" {
		return nil, nil
	}
	file, err := l.read()
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(file.Entries))
	for _, e := range file.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Site != out[j].Site {
			return out[i].Site < out[j].Site
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// prune drops entries past the TTL, so the ledger does not grow without bound.
//
// A pending entry is kept past the TTL until it is also past the stale window,
// because dropping a live claim would let a concurrent retry through.
func (l *Ledger) prune(file *ledgerFile, now time.Time) {
	for id, e := range file.Entries {
		if e.Status == Pending && now.Sub(e.At) < StaleClaim {
			continue
		}
		if l.expired(e, now) {
			delete(file.Entries, id)
		}
	}
}

func (l *Ledger) expired(e Entry, now time.Time) bool {
	// An entry stamped in the future means the clock moved backwards. Treating
	// it as live is the safe reading: it refuses a retry rather than allowing a
	// duplicate.
	if e.At.After(now) {
		return false
	}
	return now.Sub(e.At) > l.ttl()
}

// entryID is the map key. Site and key are joined with a separator neither can
// contain, so two pairs cannot collide into one entry.
func entryID(site, key string) string {
	return strings.ReplaceAll(site, "|", "%7C") + "|" + key
}

func (l *Ledger) read() (*ledgerFile, error) {
	file := &ledgerFile{Entries: map[string]Entry{}}
	if l.Path == "" {
		return file, nil
	}

	data, err := os.ReadFile(l.Path) //nolint:gosec // the path comes from XDG resolution.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return file, nil
		}
		return nil, errs.Runtime("LEDGER_UNREADABLE", "cannot read %s", l.Path).Wrap(err)
	}

	// A corrupt ledger is an error rather than an empty one. Everywhere else in
	// this tool an unreadable cache is a miss, because the cost is a round trip;
	// here the cost is a duplicate issue, so it fails instead.
	if err := toml.Unmarshal(data, file); err != nil {
		return nil, errs.Runtime("LEDGER_INVALID", "cannot parse %s", l.Path).
			WithDetail("%s", err.Error()).
			WithRemedy("move the file aside; a retry may then duplicate a request " +
				"the old ledger would have caught")
	}
	if file.Entries == nil {
		file.Entries = map[string]Entry{}
	}
	return file, nil
}

func (l *Ledger) write(file *ledgerFile) error {
	dir := filepath.Dir(l.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errs.Runtime("LEDGER_UNWRITABLE", "cannot create %s", dir).Wrap(err)
	}

	var buf bytes.Buffer
	buf.WriteString(ledgerHeader)
	if err := toml.NewEncoder(&buf).Encode(file); err != nil {
		return errs.Runtime("LEDGER_UNWRITABLE", "cannot encode the ledger").Wrap(err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(l.Path)+".*")
	if err != nil {
		return errs.Runtime("LEDGER_UNWRITABLE",
			"cannot create a temporary file in %s", dir).Wrap(err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	// Set before the write, not after, so the ledger never exists on disk at a
	// wider mode even briefly — the same ordering writeSecretFile uses.
	//
	// os.CreateTemp already returns 0600, so this changes nothing today. It is
	// here because `docs/architecture.md` publishes 0600 for this file, and
	// until now that was a statement about the standard library's default
	// restated as a guarantee of ours: swapping this for os.WriteFile, or
	// reopening the file anywhere, would have moved the mode with the document
	// still saying 0600. TestTheDocumentedModesAreTheOnesOnDisk asserts it.
	if err := tmp.Chmod(ledgerPerm); err != nil {
		_ = tmp.Close()
		return errs.Runtime("LEDGER_UNWRITABLE", "cannot restrict %s", tmpName).Wrap(err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return errs.Runtime("LEDGER_UNWRITABLE", "cannot write %s", tmpName).Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return errs.Runtime("LEDGER_UNWRITABLE", "cannot close %s", tmpName).Wrap(err)
	}
	// Renamed into place so a crash mid-write leaves the previous ledger rather
	// than a truncated one, which would parse as garbage and fail every later
	// claim.
	if err := os.Rename(tmpName, l.Path); err != nil {
		return errs.Runtime("LEDGER_UNWRITABLE", "cannot replace %s", l.Path).Wrap(err)
	}
	return nil
}
