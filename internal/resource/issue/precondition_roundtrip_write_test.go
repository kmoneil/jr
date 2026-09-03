//go:build write

package issue_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/issue"
	"github.com/kmoneil/jr/internal/site"
)

// listedBaseline runs `issue list --precondition` over one row at the version
// the write fixtures were recorded against, and returns the token it minted.
func listedBaseline(t *testing.T, kind site.Kind) string {
	t.Helper()

	out, err := runListingAs(t, kind, listPage(listedAt), render.XML, "precondition")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	tokens := tokensIn(t, out)
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens for one row:\n%s", len(tokens), out)
	}
	return tokens[0]
}

// TestABaselineFromAListingWrites is the claim the flag is sold on, end to end
// and across the seam where it could fail.
//
// `--precondition` exists so that "list the issues, edit the ones that matter"
// does not spend an `issue get` per row. That saving is only real if the write
// verbs accept what the listing minted. Two commands, two packages' worth of
// encoding and decoding, and one shared assumption about how a timestamp is
// spelled: if the listing ever produced a token `issue edit` refused, the flag
// would be worse than not having it, because the caller would have paid the
// bytes and still have to fetch.
//
// listedAt and preconditionAt are the same instant for exactly this reason: the
// row the listing describes is the version the fixture recorded.
func TestABaselineFromAListingWrites(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			token := listedBaseline(t, kind)

			doc, _, err := editWith(
				t, kind, "precondition-fresh."+string(kind)+".json", token,
			)
			if err != nil {
				t.Fatalf("a baseline from a listing was refused by the write "+
					"it exists to authorise: %v", err)
			}
			if doc == nil || doc.Record == nil {
				t.Fatal("the edit returned no record")
			}
		})
	}
}

// TestABaselineFromAListingRefusesAStaleWrite is the other half, and the half
// that would go unnoticed.
//
// A token that is accepted always is not a precondition, it is a formality: the
// write goes through, the caller believes a check ran, and the lost edit looks
// exactly like a successful one. Asserting that the listing's token still
// *refuses* is what separates a working baseline from a decorative one, and the
// PUT staying unplayed is what separates a refusal from a slower way to lose
// the same edit.
func TestABaselineFromAListingRefusesAStaleWrite(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			token := listedBaseline(t, kind)

			_, replayer, err := editWith(
				t, kind, "precondition-stale."+string(kind)+".json", token,
			)
			if err == nil {
				t.Fatal("a write built on a stale listing was applied")
			}

			e := errs.Coerce(err)
			if e.Code != "STALE_WRITE" {
				t.Errorf("code = %q, want STALE_WRITE", e.Code)
			}
			if e.Exit != exitcode.Conflict {
				t.Errorf("exit = %d, want %d", e.Exit, exitcode.Conflict)
			}
			if !strings.Contains(e.Detail, "2026-08-04T11:32:07.412Z") {
				t.Errorf("detail = %q, want the version the listing reported",
					e.Detail)
			}

			theWriteWasNeverSent(t, replayer)
		})
	}
}

// TestAnEmptyPreconditionIsRefusedRatherThanIgnored is the hole
// `issue list --precondition` opened, closed.
//
// An empty flag value and an absent flag are the same string once the value
// reaches the command, and they are opposite requests: one caller asked for a
// guarantee, the other asked for none. Before the listing could mint tokens,
// producing the empty one meant typing `--if-unchanged ""` on purpose. Now a
// row whose issue has no version carries an empty cell, and the loop in
// docs/recipes.md pipes that cell into this flag.
//
// Writing unconditionally would be the project's own worst failure: the caller
// typed the flag, believes a check ran, and the lost edit looks exactly like a
// successful one. The PUT staying unplayed is the assertion that matters,
// because an exit code alone cannot tell you the write went out anyway.
func TestAnEmptyPreconditionIsRefusedRatherThanIgnored(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			_, replayer, err := editWith(
				t, kind, "precondition-fresh."+string(kind)+".json", "",
				withEmptyPrecondition,
			)
			if err == nil {
				t.Fatal("an empty precondition was written straight past, " +
					"so the flag is decorative exactly when it is needed")
			}

			e := errs.Coerce(err)
			if e.Code != "INVALID_PRECONDITION" {
				t.Errorf("code = %q, want INVALID_PRECONDITION", e.Code)
			}
			if e.Exit != exitcode.Usage {
				t.Errorf("exit = %d, want %d", e.Exit, exitcode.Usage)
			}
			theWriteWasNeverSent(t, replayer)
		})
	}
}

// TestOmittingThePreconditionStillWrites is the other side of the same
// condition, and the reason it is WasSet rather than a bare emptiness check.
//
// Writing unconditionally is a legitimate request. If the refusal above fired
// on an absent flag as well, every write in the tool that does not opt into the
// check would stop working, which is a much louder failure but the same
// confusion between two states underneath.
func TestOmittingThePreconditionStillWrites(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			_, _, err := editWith(
				t, kind, "precondition-fresh."+string(kind)+".json", "",
			)
			if err != nil {
				t.Fatalf("a write that asked for no guarantee was refused: %v", err)
			}
		})
	}
}

// FuzzAMintedBaselineIsAlwaysAcceptedBack is the complement of
// FuzzParsePreconditionAcceptsOnlyWhatItCanCompare, which fuzzes the parser's
// postcondition and says nothing about what the minter produces.
//
// The two halves meet in one place and the flag put a lot more traffic through
// it: `issue list --precondition` mints a token per row, and every one of them
// is a value some later `issue edit --if-unchanged` has to parse. A minter that
// could produce a spelling the parser refuses would turn a listing into a set
// of tokens that all fail as INVALID_PRECONDITION, and the caller would read
// that as having typed something wrong.
//
// The property is conditional on the key, deliberately. EncodePrecondition
// takes the key the server sent and does not validate it, while
// ParsePrecondition requires one ParseKey accepts, so the round trip is only
// claimed where a real issue key is involved. That asymmetry is real and this
// says so rather than papering over it.
//
// The timestamp half is unconditional and is the interesting one: anything
// site.ParseTime accepts must come back out canonical, whatever offset it
// arrived with. Data Center serves `updated` in the instance's own timezone,
// so a non-UTC offset is the ordinary case there and not an exotic one.
func FuzzAMintedBaselineIsAlwaysAcceptedBack(f *testing.F) {
	f.Add("ENG-101", "2026-08-04T11:32:07.412+0000")
	f.Add("ENG-101", "2026-08-04T11:32:07.412-0400")
	f.Add("OPS-1", "2026-08-04T11:32:07.412+0530")
	f.Add("ENG-101", "2026-08-04T11:32:07.412Z")
	f.Add("ENG-101", "2026-08-04T11:32:07.000+0000")
	f.Add("ENG-101", "")
	f.Add("", "2026-08-04T11:32:07.412Z")
	f.Add("../../admin-1", "2026-08-04T11:32:07.412Z")
	// The one this target found. Kept as a seed as well as in the corpus under
	// testdata/fuzz, because a seed is read by `go test` and the corpus entry
	// is what a future change is measured against.
	f.Add("Aa0-0", "0000-01-01T0:00:00+0010")

	f.Fuzz(func(t *testing.T, key, rawUpdated string) {
		for _, kind := range []site.Kind{site.Cloud, site.DataCenter} {
			token, err := issue.EncodePrecondition(
				site.Info{Kind: kind}, key, rawUpdated)
			if err != nil || token == "" {
				// A version this tool cannot read, or nothing to mint from.
				// Both are refusals the caller is told about by absence.
				continue
			}
			if _, ok := issue.ParseKey(key); !ok {
				continue
			}

			p, err := issue.ParsePrecondition(token)
			if err != nil {
				t.Fatalf("minted a baseline for %q at %q that the write verbs "+
					"refuse: %v", key, rawUpdated, err)
			}
			if p.Deployment != kind {
				t.Fatalf("minted against %q and parsed back as %q", kind, p.Deployment)
			}
			// Same issue, however either side spells the key.
			want, _ := issue.ParseKey(key)
			got, ok := issue.ParseKey(p.Key)
			if !ok || got.String() != want.String() {
				t.Fatalf("minted for %q and parsed back as %q", key, p.Key)
			}
		}
	})
}
