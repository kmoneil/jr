//go:build write

package issue_test

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// preconditionAt is the version the fresh fixture reports and the stale one has
// moved past. Millisecond, because that is what the token carries.
const preconditionAt = "2026-08-04T11:32:07.412+0000"

// editWith runs `issue edit ENG-101 --summary ...` against a fixture, with
// whatever --if-unchanged value the caller supplies.
//
// It goes through the registered command rather than through Client, because
// the check has two halves in two places — Command.Validate refuses a token
// locally, the body compares it against the server — and a test that called
// only the second would pass over a validation that never ran. That is the
// shape `mcp serve` shipped with: the wrapper above the tested layer.
func editWith(
	t *testing.T, kind site.Kind, fixture, token string,
	opts ...func(registry.Flags),
) (*render.Doc, *transport.Replayer, error) {
	t.Helper()

	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}
	conn, replayer := replayConn(t, fixture)

	flags := registry.NewFlags()
	flags.SetString("summary", "a better summary")
	if token != "" {
		flags.SetString("if-unchanged", token)
	}
	for _, opt := range opts {
		opt(flags)
	}
	inv := &registry.Invocation{
		Jira:   &stubSession{conn: conn, kind: kind},
		Args:   []string{"ENG-101"},
		Flags:  flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	if err := cmd.Validate(t.Context(), inv); err != nil {
		return nil, replayer, err
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err != nil {
		return nil, replayer, err
	}
	if doc == nil || doc.Record == nil {
		t.Fatal("the edit returned no record")
	}
	return doc, replayer, nil
}

// withEmptyPrecondition types `--if-unchanged ""`, which is what a shell loop
// produces from a row that carried no token. Distinct from omitting the flag,
// which is what editWith does with an empty token, and that distinction is the
// whole point of the test that uses this.
func withEmptyPrecondition(flags registry.Flags) {
	flags.SetString("if-unchanged", "")
}

// theWriteWasNeverSent reads the fixture rather than the error.
//
// Both cassettes carry the PUT, so the replayer can answer one; what is
// asserted is that it was never asked to. A refusal that still sent the write
// would lose the same edit more slowly, and no exit code can tell you that.
func theWriteWasNeverSent(t *testing.T, replayer *transport.Replayer) {
	t.Helper()

	var sawPUT bool
	for _, unplayed := range replayer.Unplayed() {
		if strings.HasPrefix(unplayed, "PUT ") {
			sawPUT = true
		}
	}
	if !sawPUT {
		t.Error("the write went out anyway, so the edit is lost and the exit " +
			"code is the only thing that noticed")
	}
}

// mint builds the token `issue get` would have reported for a given version.
func mint(t *testing.T, kind site.Kind, key, updated string) string {
	t.Helper()
	token, err := issue.EncodePrecondition(site.Info{Kind: kind}, key, updated, issue.PrecisionMillisecond)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return token
}

// TestAStaleWriteIsRefusedAndNeverSent is §6.3's third bullet, and the reason
// this card existed.
//
// Two callers edit one issue. The second holds a copy read before the first's
// change landed. Without a precondition Jira applies both, the first edit is
// gone, and both commands exit 0 — nothing truncated, nothing in error, and
// nothing anywhere saying a write was lost.
//
// The assertion that matters is not the exit code, it is that the PUT stays
// unplayed. A refusal that still sent the write would be a slower way to lose
// the same edit, and only the fixture can tell the difference.
func TestAStaleWriteIsRefusedAndNeverSent(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			token := mint(t, kind, "ENG-101", preconditionAt)

			_, replayer, err := editWith(
				t, kind, "precondition-stale."+string(kind)+".json", token,
			)
			if err == nil {
				t.Fatal("a write built on a stale read was applied")
			}

			e := errs.Coerce(err)
			if e.Code != "STALE_WRITE" {
				t.Errorf("code = %q, want STALE_WRITE", e.Code)
			}
			if e.Exit != exitcode.Conflict {
				t.Errorf("exit = %d, want %d", e.Exit, exitcode.Conflict)
			}
			// Both versions, so a caller can see what moved rather than being
			// told only that something did.
			if !strings.Contains(e.Detail, "2026-08-04T11:32:07.412Z") ||
				!strings.Contains(e.Detail, "2026-08-04T11:41:55.008Z") {
				t.Errorf("detail = %q, want both versions", e.Detail)
			}

			theWriteWasNeverSent(t, replayer)
		})
	}
}

// TestAFreshPreconditionWritesAndRecordsTheMethod is the other side: an issue
// nobody touched is written, and the acknowledgement says which guarantee ran.
//
// method is published because a conditional request the server evaluates and a
// read-then-write are not the same promise. Jira offers no validator on an
// issue, so this is read-compare and a caller should be told so rather than
// left to assume the strong one from the word precondition.
func TestAFreshPreconditionWritesAndRecordsTheMethod(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			token := mint(t, kind, "ENG-101", preconditionAt)

			doc, replayer, err := editWith(
				t, kind, "precondition-fresh."+string(kind)+".json", token,
			)
			if err != nil {
				t.Fatalf("an unchanged issue was refused: %v", err)
			}

			node, ok := doc.Record.ChildNamed("precondition")
			if !ok {
				t.Fatal("the acknowledgement does not say a check ran")
			}
			if method, _ := node.AttrValue("method"); method != "read-compare" {
				t.Errorf("method = %q, want read-compare", method)
			}

			// Both interactions used: the read that took the current version
			// and the write that followed it.
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the fixture has interactions this test never used: %v", unplayed)
			}
		})
	}
}

// TestWithoutThePreconditionNothingExtraIsRead is what keeps the flag free for
// everybody who does not pass it.
//
// The check costs one request and must cost it only when asked for. It also
// pins the other half: no element appears saying a check did not happen, which
// would put a sentence about the absence of a flag on every write in the tree.
func TestWithoutThePreconditionNothingExtraIsRead(t *testing.T) {
	doc, replayer, err := editWith(t, site.Cloud, "edit.cloud.json", "")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	// edit.cloud.json holds the PUT and nothing else, so a read would have had
	// nothing to answer it.
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("an unrequested read went out: %v", unmatched)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the write did not go out: %v", unplayed)
	}
	if _, ok := doc.Record.ChildNamed("precondition"); ok {
		t.Error("a write with no --if-unchanged claims a check")
	}
}

// TestAPreconditionThisToolDidNotIssueIsRefusedLocally covers every way the
// value can be wrong, and asserts the refusal costs no round trip.
//
// Local, because the deployment probe behind Connect is a request: a typo
// answered with NETWORK at exit 9 tells the caller their mistake is worth
// retrying. Everything except which server minted it is decidable here, which
// is the split ParsePageToken already makes for the same reason.
func TestAPreconditionThisToolDidNotIssueIsRefusedLocally(t *testing.T) {
	cmd, ok := registry.Lookup("issue.edit")
	if !ok {
		t.Fatal("issue edit is not registered")
	}

	cases := map[string]struct{ token, wantIn string }{
		"not base64": {"not a token", "issued"},
		"legal base64 JSON naming nothing": {
			// {"not":"valid"} — legal at every layer and a precondition about
			// nothing. Saying it was never issued beats reporting a mismatch
			// against values it does not have.
			"eyJub3QiOiJ2YWxpZCJ9", "names no issue",
		},
		"another issue": {
			mint(t, site.Cloud, "ENG-999", preconditionAt), "ENG-999",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			flags := registry.NewFlags()
			flags.SetString("summary", "a better summary")
			flags.SetString("if-unchanged", tc.token)

			// No connection at all. A check that needed one could not be
			// running where this one claims to run.
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Args: []string{"ENG-101"}, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			})
			if err == nil {
				t.Fatal("accepted")
			}
			e := errs.Coerce(err)
			if e.Code != "INVALID_PRECONDITION" {
				t.Errorf("code = %q, want INVALID_PRECONDITION", e.Code)
			}
			if e.Exit != exitcode.Usage {
				t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
			}
			// Everything the caller is shown, because which part carries the
			// naming differs by cause: a value that is not a token at all has
			// nothing to name beyond itself, and one for another issue names
			// the issue in the detail.
			shown := e.Message + e.Detail + e.Remedy
			if !strings.Contains(shown, tc.wantIn) {
				t.Errorf("the refusal reads %q, which names neither the problem "+
					"nor the fix; wanted %q", shown, tc.wantIn)
			}
		})
	}
}

// TestAPreconditionFromTheOtherDeploymentIsRefused is the one check that cannot
// move earlier, because which server minted a token is the only question about
// it that needs a server.
//
// It is refused rather than compared. Two sites' timestamps have nothing to say
// to each other, and comparing them would refuse the write as "the issue
// changed" — a claim about this issue that nobody checked.
func TestAPreconditionFromTheOtherDeploymentIsRefused(t *testing.T) {
	token := mint(t, site.DataCenter, "ENG-101", preconditionAt)

	_, replayer, err := editWith(t, site.Cloud, "precondition-fresh.cloud.json", token)
	if err == nil {
		t.Fatal("a Data Center precondition was honoured against Cloud")
	}
	e := errs.Coerce(err)
	if e.Code != "INVALID_PRECONDITION" {
		t.Errorf("code = %q, want INVALID_PRECONDITION", e.Code)
	}
	if !strings.Contains(e.Detail, "Data Center") || !strings.Contains(e.Detail, "Cloud") {
		t.Errorf("detail = %q, want both deployments named", e.Detail)
	}
	theWriteWasNeverSent(t, replayer)
}

// TestEveryVerbTakingAPreconditionChecksIt holds the three to one verdict.
//
// The flag is declared per command and the check is called per command, which
// is two lists with no reason to agree — the shape that let `--jql` be
// validated on one surface and not on the other. A verb that grows the flag and
// forgets the call would ship a precondition that is accepted and ignored,
// which is worse than not offering one.
func TestEveryVerbTakingAPreconditionChecksIt(t *testing.T) {
	args := map[string][]string{
		"issue.edit":   {"ENG-101"},
		"issue.move":   {"ENG-101", "Done"},
		"issue.assign": {"ENG-101", "Ada Lovelace"},
	}

	for _, cmd := range registry.All() {
		name := strings.Join(cmd.Path, ".")
		if !declaresFlag(cmd, "if-unchanged") {
			continue
		}
		positional, known := args[name]
		if !known {
			t.Fatalf("%s declares --if-unchanged and this test has no arguments "+
				"for it; add them rather than letting the verb go unchecked", name)
		}

		t.Run(name, func(t *testing.T) {
			flags := registry.NewFlags()
			flags.SetString("if-unchanged", "not a token")
			err := cmd.Validate(t.Context(), &registry.Invocation{
				Args: positional, Flags: flags,
				Stderr: io.Discard, Progress: registry.NoProgress,
			})
			if err == nil {
				t.Fatal("a garbage precondition was accepted, so the flag is " +
					"declared and never read")
			}
			if code := errs.Coerce(err).Code; code != "INVALID_PRECONDITION" {
				t.Errorf("code = %q, want INVALID_PRECONDITION", code)
			}
		})
	}
}

func declaresFlag(cmd *registry.Command, name string) bool {
	for _, f := range cmd.Flags {
		if f.Name == name {
			return true
		}
	}
	return false
}

// FuzzParsePreconditionAcceptsOnlyWhatItCanCompare is the postcondition, not a
// liveness check.
//
// Anything this parser accepts goes on to be compared against a canonical stamp
// minted from the server's answer. So a value it lets through in some other
// spelling of the same instant — `+0100`, no milliseconds, a nano suffix —
// would never compare equal, and every write would come back STALE_WRITE at
// exit 7: the caller told their issue changed when what happened is that they
// passed something this tool did not issue. The refusal is a usage error at
// exit 2 and has to stay one, which makes "accepted implies canonical" the
// property worth fuzzing rather than "does not panic".
//
// The seeds are the shapes that motivated it: a token this tool minted, and the
// legal-at-every-layer values that reach the checks one at a time.
func FuzzParsePreconditionAcceptsOnlyWhatItCanCompare(f *testing.F) {
	f.Add(mintFuzzSeed(f, site.Cloud, "ENG-101", preconditionAt))
	f.Add("")
	f.Add("not a token")
	f.Add("eyJub3QiOiJ2YWxpZCJ9") // {"not":"valid"}
	f.Add(encodeFuzzToken(`{"d":"cloud","k":"ENG-1","u":"2026-08-04T11:32:07.412Z"}`))
	f.Add(encodeFuzzToken(`{"d":"cloud","k":"ENG-1","u":"2026-08-04T12:32:07.412+01:00"}`))
	f.Add(encodeFuzzToken(`{"d":"cloud","k":"ENG-1","u":"2026-08-04T11:32:07Z"}`))
	f.Add(encodeFuzzToken(`{"d":"cloud","k":"../../admin-1","u":"2026-08-04T11:32:07.412Z"}`))

	f.Fuzz(func(t *testing.T, encoded string) {
		p, err := issue.ParsePrecondition(encoded)
		if err != nil {
			// Every refusal is a usage error. One that reached exit 7 would be
			// advertising a typo as a conflict somebody else caused.
			if e := errs.Coerce(err); e.Exit != exitcode.Usage {
				t.Fatalf("refused %q at exit %d, want %d", encoded, e.Exit, exitcode.Usage)
			}
			return
		}

		if p.Deployment == "" {
			t.Fatalf("accepted %q naming no deployment", encoded)
		}
		if _, ok := issue.ParseKey(p.Key); !ok {
			t.Fatalf("accepted %q naming %q, which is not an issue key", encoded, p.Key)
		}
		// The load-bearing one: what was accepted must already be spelled the
		// way the comparison spells it, or the check can only ever refuse.
		//
		// Canonicalized here rather than through the package's own helper, on
		// the rule the residue guard produced: a check that shares its
		// definition with the thing it checks is blind wherever that definition
		// is wrong. This is what a reader of the contract would write.
		canonical, ok := canonicalVersion(p.Updated)
		if !ok {
			t.Fatalf("accepted %q carrying an unreadable version %q", encoded, p.Updated)
		}
		if canonical != p.Updated {
			t.Fatalf("accepted %q carrying %q, which canonicalizes to %q — "+
				"every write with it would report STALE_WRITE",
				encoded, p.Updated, canonical)
		}
	})
}

// canonicalVersion is RFC 3339, UTC, milliseconds — written out independently
// of internal/resource/issue so the two have to agree rather than being the
// same line read twice.
func canonicalVersion(version string) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, version)
	if err != nil {
		return "", false
	}
	utc := t.UTC()
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d.%03dZ",
		utc.Year(), utc.Month(), utc.Day(),
		utc.Hour(), utc.Minute(), utc.Second(),
		utc.Nanosecond()/int(time.Millisecond)), true
}

func mintFuzzSeed(f *testing.F, kind site.Kind, key, updated string) string {
	f.Helper()
	token, err := issue.EncodePrecondition(site.Info{Kind: kind}, key, updated, issue.PrecisionMillisecond)
	if err != nil {
		f.Fatalf("mint: %v", err)
	}
	return token
}

func encodeFuzzToken(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
