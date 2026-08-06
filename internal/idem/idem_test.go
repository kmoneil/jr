package idem_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/idem"
)

const site = "https://jira.example.invalid"

// testNow is the fixed clock, so an entry can be aged without waiting a week.
var testNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func ledgerAt(t *testing.T, dir string, now time.Time) *idem.Ledger {
	t.Helper()
	return &idem.Ledger{
		Path: filepath.Join(dir, "idempotency.toml"),
		Now:  func() time.Time { return now },
	}
}

// TestOneKeyProducesOneRequest is the card's headline, and the whole reason the
// package exists: the same create run twice with one key sends one request.
func TestOneKeyProducesOneRequest(t *testing.T) {
	dir := t.TempDir()
	sent := 0

	// First run: claims, sends, records what it made.
	first := ledgerAt(t, dir, testNow)
	out, err := first.Claim(site, "deploy-42", "issue.create")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !out.Claimed {
		t.Fatal("the first claim was refused")
	}
	sent++
	if err := first.Complete(site, "deploy-42", "ENG-101"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Second run, a minute later: the key is spent, so nothing is sent and the
	// original result comes back.
	second := ledgerAt(t, dir, testNow.Add(time.Minute))
	out, err = second.Claim(site, "deploy-42", "issue.create")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if out.Claimed {
		t.Fatal("the second run was allowed to send a duplicate")
	}
	if !out.Replayed {
		t.Fatal("the second run was not told this was a replay")
	}
	if out.Entry.Result != "ENG-101" {
		t.Errorf("replayed result = %q, want the original", out.Entry.Result)
	}
	if sent != 1 {
		t.Errorf("sent %d requests, want 1", sent)
	}
}

// TestConcurrentClaimsElectOneWinner is the property a sequential test cannot
// show. Agents retry in parallel; a read-modify-write of a shared file without
// a lock would tell several of them they claimed the key, and each would create
// an issue.
func TestConcurrentClaimsElectOneWinner(t *testing.T) {
	dir := t.TempDir()

	const racers = 12
	var (
		mu       sync.Mutex
		claimed  int
		inFlight int
		replayed int
		failures []error
	)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	for range racers {
		done.Go(func() {
			start.Wait()

			// Each racer is its own process as far as the ledger is concerned:
			// a separate Ledger value over the same file.
			l := &idem.Ledger{
				Path: filepath.Join(dir, "idempotency.toml"),
				Now:  func() time.Time { return testNow },
			}
			out, err := l.Claim(site, "race-1", "issue.create")

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failures = append(failures, err)
			case out.Claimed:
				claimed++
			case out.InFlight:
				inFlight++
			case out.Replayed:
				replayed++
			}
		})
	}

	start.Done()
	done.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d racers failed, first: %v", len(failures), failures[0])
	}
	if claimed != 1 {
		t.Errorf("%d racers were told they claimed the key, want exactly 1", claimed)
	}
	if replayed != 0 {
		t.Errorf("%d racers saw a replay of a request that never completed", replayed)
	}
	if claimed+inFlight != racers {
		t.Errorf("%d claimed + %d in flight != %d racers", claimed, inFlight, racers)
	}
}

// TestAnUnfinishedClaimBlocksARetry covers the case that matters most. A run
// that claimed and then died may have had its request processed, so a retry
// must not send another one — "I do not know" has to behave like "it happened".
func TestAnUnfinishedClaimBlocksARetry(t *testing.T) {
	dir := t.TempDir()

	if _, err := ledgerAt(t, dir, testNow).Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The process dies here: no Complete, no Release.

	retry := ledgerAt(t, dir, testNow.Add(time.Minute))
	out, err := retry.Claim(site, "k", "issue.create")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if out.Claimed {
		t.Error("a retry was allowed while the first attempt's outcome was unknown")
	}
	if !out.InFlight {
		t.Error("the retry was not told the claim is still held")
	}
}

// TestAStaleClaimIsHandedOverAndSaysSo covers the other end of the same case: a
// claim cannot block forever, but the handover has to admit the earlier attempt
// may have succeeded rather than pretending it did not happen.
func TestAStaleClaimIsHandedOverAndSaysSo(t *testing.T) {
	dir := t.TempDir()

	if _, err := ledgerAt(t, dir, testNow).Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	later := ledgerAt(t, dir, testNow.Add(idem.StaleClaim+time.Minute))
	out, err := later.Claim(site, "k", "issue.create")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !out.Claimed {
		t.Fatal("a stale claim blocked a retry forever")
	}
	if !out.Reclaimed {
		t.Error("the handover did not report that an earlier attempt may have succeeded")
	}
}

// TestReleaseFreesAKeyForARequestThatNeverHappened covers the only case where
// giving up a claim is safe.
func TestReleaseFreesAKeyForARequestThatNeverHappened(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)

	if _, err := l.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := l.Release(site, "k"); err != nil {
		t.Fatalf("release: %v", err)
	}

	out, err := l.Claim(site, "k", "issue.create")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !out.Claimed || out.Reclaimed {
		t.Errorf("after release the key was not cleanly free: %+v", out)
	}
}

// TestKeysAreScopedToTheSite stops one site's ledger answering another's. The
// same key against two Jiras is two different requests.
func TestKeysAreScopedToTheSite(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)

	if _, err := l.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := l.Complete(site, "k", "ENG-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	out, err := l.Claim("https://other.example.invalid", "k", "issue.create")
	if err != nil {
		t.Fatalf("other site: %v", err)
	}
	if !out.Claimed {
		t.Error("a key used on one site blocked the same key on another")
	}
}

// TestSiteAndKeyCannotCollide covers the entry id. Two different pairs that
// joined to one string would make one request replay as another.
func TestSiteAndKeyCannotCollide(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)

	// Naively joined with "|", ("a|b", "c") and ("a", "b|c") are both "a|b|c".
	if _, err := l.Claim("a|b", "c", "issue.create"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := l.Complete("a|b", "c", "FIRST-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	out, err := l.Claim("a", "b|c", "issue.create")
	if err != nil {
		// A "|" in a key is rejected by ValidateKey, which is the other way to
		// be safe here. Either answer is correct; silently replaying is not.
		if code := errs.Coerce(err).Code; code != "INVALID_IDEMPOTENCY_KEY" {
			t.Fatalf("second: %v", err)
		}
		return
	}
	if out.Replayed {
		t.Error("two different (site, key) pairs collided into one entry")
	}
}

// TestOneKeyForTwoOperationsIsRefused covers a caller bug that would otherwise
// answer one question with another's result.
func TestOneKeyForTwoOperationsIsRefused(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)

	if _, err := l.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := l.Complete(site, "k", "ENG-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_, err := l.Claim(site, "k", "issue.comment.add")
	if err == nil {
		t.Fatal("one key was accepted for two different operations")
	}
	e := errs.Coerce(err)
	if e.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("code = %q, want IDEMPOTENCY_KEY_REUSED", e.Code)
	}
	// A precondition failed, which is exit 7 — not a usage error, because the
	// flags were well formed.
	if e.Exit != exitcode.Conflict {
		t.Errorf("exit = %v, want %v", e.Exit, exitcode.Conflict)
	}
}

// TestExpiredKeysAreReusable covers the TTL. Nothing is retrying a week later,
// so a key past its lifetime is a new request rather than a permanent tombstone.
func TestExpiredKeysAreReusable(t *testing.T) {
	dir := t.TempDir()

	first := ledgerAt(t, dir, testNow)
	if _, err := first.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := first.Complete(site, "k", "ENG-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	later := ledgerAt(t, dir, testNow.Add(idem.DefaultTTL+time.Hour))
	out, err := later.Claim(site, "k", "issue.create")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !out.Claimed || out.Replayed {
		t.Errorf("an expired key was not reusable: %+v", out)
	}
}

// TestTheLedgerIsPruned is the third "done when": it must not grow without
// bound.
func TestTheLedgerIsPruned(t *testing.T) {
	dir := t.TempDir()

	old := ledgerAt(t, dir, testNow)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := old.Claim(site, key, "issue.create"); err != nil {
			t.Fatalf("claim %s: %v", key, err)
		}
		if err := old.Complete(site, key, "ENG-"+key); err != nil {
			t.Fatalf("complete %s: %v", key, err)
		}
	}

	entries, err := old.Entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// A write well past the TTL drops what expired and keeps what did not.
	fresh := ledgerAt(t, dir, testNow.Add(idem.DefaultTTL+time.Hour))
	if _, err := fresh.Claim(site, "d", "issue.create"); err != nil {
		t.Fatalf("claim d: %v", err)
	}

	entries, err = fresh.Entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Key != "d" {
		t.Errorf("after pruning: %+v, want only the fresh entry", entries)
	}
}

// TestALiveClaimSurvivesPruning stops the pruner from freeing a key another
// process is actively using, which would let a duplicate straight through.
func TestALiveClaimSurvivesPruning(t *testing.T) {
	dir := t.TempDir()
	l := &idem.Ledger{
		Path: filepath.Join(dir, "idempotency.toml"),
		Now:  func() time.Time { return testNow },
		// A TTL shorter than the stale window is the trap: the entry is expired
		// by age while its request may still be in flight.
		TTL: time.Second,
	}

	if _, err := l.Claim(site, "live", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Another claim in the same instant triggers a prune.
	if _, err := l.Claim(site, "other", "issue.create"); err != nil {
		t.Fatalf("second claim: %v", err)
	}

	out, err := l.Claim(site, "live", "issue.create")
	if err != nil {
		t.Fatalf("recheck: %v", err)
	}
	if out.Claimed {
		t.Error("pruning freed a claim whose request may still be in flight")
	}
}

func TestValidateKey(t *testing.T) {
	for _, ok := range []string{
		"a", "deploy-42", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		"run:2026-08-05", "A.B_C-1", strings.Repeat("k", 128),
	} {
		if err := idem.ValidateKey(ok); err != nil {
			t.Errorf("ValidateKey(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", " ", "-leading", ".leading", "has space", "has/slash",
		"has|pipe", "has\nnewline", strings.Repeat("k", 129), "émoji",
	} {
		err := idem.ValidateKey(bad)
		if err == nil {
			t.Errorf("ValidateKey(%q) was accepted", bad)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("%q exits %v, want %v", bad, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

// TestDerivedKeysDistinguishRequests covers the hash behind the unkeyed
// warning. Two different requests hashing alike would report one as a repeat of
// the other.
func TestDerivedKeysDistinguishRequests(t *testing.T) {
	same := idem.DeriveKey("issue.create", "ENG", "a summary")
	if same != idem.DeriveKey("issue.create", "ENG", "a summary") {
		t.Error("the same request derived two different keys")
	}

	for _, other := range [][]string{
		{"ENG", "another summary"},
		{"OPS", "a summary"},
		{"ENG", "a summary", "extra"},
		// The classic split ambiguity: without length prefixing, ("ab","c")
		// and ("a","bc") hash the same and two different creates look like a
		// retry of one another.
		{"ENGa", " summary"},
	} {
		if idem.DeriveKey("issue.create", other...) == same {
			t.Errorf("%v derived the same key as the original", other)
		}
	}
	if idem.DeriveKey("issue.comment.add", "ENG", "a summary") == same {
		t.Error("two operations with the same arguments derived one key")
	}
}

// TestRecentReportsAnIdenticalSuccess backs the unkeyed warning: §6.3 says warn
// if an identical request succeeded within 60s, and warn means warn — the
// second request is not blocked, because two deliberate identical creates are
// a legitimate thing to want.
func TestRecentReportsAnIdenticalSuccess(t *testing.T) {
	dir := t.TempDir()
	key := idem.DeriveKey("issue.create", "ENG", "a summary")

	l := ledgerAt(t, dir, testNow)
	if _, err := l.Claim(site, key, "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := l.Complete(site, key, "ENG-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	within := ledgerAt(t, dir, testNow.Add(30*time.Second))
	entry, found, err := within.Recent(site, key, idem.RecentWindow)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if !found {
		t.Fatal("an identical request 30s ago was not reported")
	}
	if entry.Result != "ENG-1" {
		t.Errorf("result = %q, want the original", entry.Result)
	}

	after := ledgerAt(t, dir, testNow.Add(2*idem.RecentWindow))
	if _, found, err = after.Recent(site, key, idem.RecentWindow); err != nil {
		t.Fatalf("recent: %v", err)
	}
	if found {
		t.Error("a request outside the window was reported as recent")
	}

	// A claim that never completed is not a success, so it is not warned about.
	pendingKey := idem.DeriveKey("issue.create", "ENG", "pending")
	if _, err := l.Claim(site, pendingKey, "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, found, err = l.Recent(site, pendingKey, idem.RecentWindow); err != nil {
		t.Fatalf("recent: %v", err)
	}
	if found {
		t.Error("an unfinished request was reported as a recent success")
	}
}

// TestACorruptLedgerFailsRatherThanIgnoring is the deliberate difference from
// every cache in this tool. A corrupt cache is a miss because the cost is a
// round trip; here the cost is a duplicate issue.
func TestACorruptLedgerFailsRatherThanIgnoring(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotency.toml")
	if err := os.WriteFile(path, []byte("this is not toml ["), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	l := ledgerAt(t, dir, testNow)
	_, err := l.Claim(site, "k", "issue.create")
	if err == nil {
		t.Fatal("a corrupt ledger was treated as an empty one")
	}
	e := errs.Coerce(err)
	if e.Code != "LEDGER_INVALID" {
		t.Errorf("code = %q, want LEDGER_INVALID", e.Code)
	}
	// The remedy has to admit what moving the file aside costs.
	if !strings.Contains(e.Remedy, "duplicate") {
		t.Errorf("the remedy does not say what is lost: %q", e.Remedy)
	}
}

// TestNoLedgerIsHonestRatherThanSilent covers the disabled path. A build or a
// test with no state directory gets no protection, and finds out by the claim
// always succeeding rather than by a silent duplicate.
func TestNoLedgerIsHonestRatherThanSilent(t *testing.T) {
	var l *idem.Ledger
	out, err := l.Claim(site, "k", "issue.create")
	if err != nil || !out.Claimed {
		t.Errorf("a nil ledger did not allow the request: %+v, %v", out, err)
	}
	if err := l.Complete(site, "k", "ENG-1"); err != nil {
		t.Errorf("complete on a nil ledger: %v", err)
	}
	if _, found, err := l.Recent(site, "k", idem.RecentWindow); err != nil || found {
		t.Errorf("a nil ledger reported a recent request: %v, %v", found, err)
	}

	empty := &idem.Ledger{}
	if out, err := empty.Claim(site, "k", "issue.create"); err != nil || !out.Claimed {
		t.Errorf("an unconfigured ledger did not allow the request: %+v, %v", out, err)
	}
}

// TestAClockThatMovedBackwardsDoesNotFreeAKey covers the safe reading of a
// future timestamp: refuse a retry rather than allow a duplicate.
func TestAClockThatMovedBackwardsDoesNotFreeAKey(t *testing.T) {
	dir := t.TempDir()

	future := ledgerAt(t, dir, testNow.Add(time.Hour))
	if _, err := future.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := future.Complete(site, "k", "ENG-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	back := ledgerAt(t, dir, testNow)
	out, err := back.Claim(site, "k", "issue.create")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if out.Claimed {
		t.Error("a clock moving backwards freed a spent key")
	}
	if !out.Replayed {
		t.Error("the entry was not replayed")
	}
}

// TestALockedLedgerTimesOutRatherThanHanging covers the wedged-writer case. A
// command that waited forever on a stale lock is indistinguishable from a hang,
// which this tool refuses everywhere else.
func TestALockedLedgerTimesOutRatherThanHanging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotency.toml")
	if err := os.WriteFile(path+".lock", []byte("99999\n"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	l := ledgerAt(t, dir, testNow)
	l.LockWait = 50 * time.Millisecond
	start := time.Now()
	_, err := l.Claim(site, "k", "issue.create")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a held lock did not stop the claim")
	}
	if code := errs.Coerce(err).Code; code != "LEDGER_LOCKED" {
		t.Errorf("code = %q, want LEDGER_LOCKED", code)
	}
	if elapsed > time.Second {
		t.Errorf("waited %s, want about the configured 50ms", elapsed)
	}
}

// TestAnAbandonedLockIsBroken covers the other side: a process killed while
// holding the lock must not block every later run forever.
func TestAnAbandonedLockIsBroken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotency.toml")
	lock := path + ".lock"
	if err := os.WriteFile(lock, []byte("99999\n"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	// Backdate it past the staleness window, which is how a dead holder is
	// recognized — a pid would be wrong across containers and after a reboot.
	old := time.Now().Add(-2 * idem.LockStale)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	l := ledgerAt(t, dir, testNow)
	out, err := l.Claim(site, "k", "issue.create")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !out.Claimed {
		t.Error("an abandoned lock blocked a claim forever")
	}
}

// TestTheLockIsReleasedOnEveryPath stops a refused claim from leaving the
// ledger locked for the next run.
func TestTheLockIsReleasedOnEveryPath(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)

	if _, err := l.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// A refused claim: same key, different operation.
	if _, err := l.Claim(site, "k", "issue.delete"); err == nil {
		t.Fatal("the conflicting claim was accepted")
	}

	if _, err := os.Stat(filepath.Join(dir, "idempotency.toml.lock")); err == nil {
		t.Error("a refused claim left the ledger locked")
	}
	// And the ledger is still usable.
	if _, err := l.Claim(site, "other", "issue.create"); err != nil {
		t.Errorf("the ledger was unusable after a refusal: %v", err)
	}
}

// TestTheLedgerIsNotWorldReadable keeps it in line with the rest of state. It
// holds issue keys and a caller's own identifiers, which is not a credential
// but is not public either.
func TestTheLedgerIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)
	if _, err := l.Claim(site, "k", "issue.create"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "idempotency.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Errorf("mode is %04o, want nothing for other users", perm)
	}
}

// TestAnUnwritableLedgerFailsTheClaim is the one failure mode that must never
// be quiet. A claim that could not be recorded gives no protection at all, and
// proceeding anyway would let the caller believe a retry is safe when the next
// run will find an empty ledger and send the request again.
func TestAnUnwritableLedgerFailsTheClaim(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o700) })

	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the mode")
	}

	l := &idem.Ledger{
		Path: filepath.Join(state, "idempotency.toml"),
		Now:  func() time.Time { return testNow },
	}
	if _, err := l.Claim(site, "k", "issue.create"); err == nil {
		t.Fatal("a claim that could not be recorded reported success")
	} else if code := errs.Coerce(err).Code; !strings.HasPrefix(code, "LEDGER_") {
		t.Errorf("code = %q, want a LEDGER_ failure", code)
	}
}

// TestEntriesAreOrdered keeps the listing stable, so two runs of whatever shows
// the ledger produce the same output.
func TestEntriesAreOrdered(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)

	for _, e := range []struct{ site, key string }{
		{"https://b.example.invalid", "z"},
		{"https://a.example.invalid", "m"},
		{"https://b.example.invalid", "a"},
		{"https://a.example.invalid", "b"},
	} {
		if _, err := l.Claim(e.site, e.key, "issue.create"); err != nil {
			t.Fatalf("claim %s/%s: %v", e.site, e.key, err)
		}
	}

	entries, err := l.Entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Site+"/"+e.Key)
	}
	want := "https://a.example.invalid/b,https://a.example.invalid/m," +
		"https://b.example.invalid/a,https://b.example.invalid/z"
	if strings.Join(got, ",") != want {
		t.Errorf("entries = %v\nwant %s", got, want)
	}

	// A nil ledger lists nothing rather than failing, so a caller need not
	// branch on whether state is configured.
	var none *idem.Ledger
	if list, err := none.Entries(); err != nil || len(list) != 0 {
		t.Errorf("a nil ledger listed %v, %v", list, err)
	}
}

// TestNoteRecordsWithoutClaiming covers what backs the unkeyed warning. It has
// to record — otherwise the warning looks for an entry nothing ever writes —
// without reserving anything, because an unkeyed request gets no protection.
func TestNoteRecordsWithoutClaiming(t *testing.T) {
	dir := t.TempDir()
	l := ledgerAt(t, dir, testNow)
	derived := idem.DeriveKey("issue.create", site, "a body")

	if err := l.Note(site, derived, "ENG-1"); err != nil {
		t.Fatalf("note: %v", err)
	}

	entry, found, err := l.Recent(site, derived, idem.RecentWindow)
	if err != nil || !found {
		t.Fatalf("a noted request was not recent: %v, %v", found, err)
	}
	if entry.Result != "ENG-1" {
		t.Errorf("result = %q", entry.Result)
	}

	// Nothing was reserved, so a real key on the same string is refused as
	// reused rather than replaying an advisory record as if it were a claim.
	if _, err := l.Claim(site, derived, "issue.create"); err == nil {
		t.Error("an advisory record was claimable as a real one")
	} else if code := errs.Coerce(err).Code; code != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("code = %q, want IDEMPOTENCY_KEY_REUSED", code)
	}

	// A nil ledger accepts a note and does nothing, so a caller need not branch.
	var none *idem.Ledger
	if err := none.Note(site, derived, "ENG-1"); err != nil {
		t.Errorf("note on a nil ledger: %v", err)
	}
}
