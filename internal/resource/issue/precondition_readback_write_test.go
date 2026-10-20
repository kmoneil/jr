//go:build write

package issue_test

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The versions the read-back fixtures move the issue through. preconditionAt
// is the one a caller read before the first write; each write moves it on.
const (
	readBackAfterFirst  = "2026-08-04T11:32:08.937+0000"
	readBackAfterSecond = "2026-08-04T11:32:09.204+0000"
)

// guardedWrite is one write verb and the arguments that make it run against
// the read-back fixtures.
type guardedWrite struct {
	command, fixture string
	args             []string
	flags            map[string]string
}

var guardedWrites = []guardedWrite{
	{
		command: "issue.edit", fixture: "readback-edit", args: []string{"ENG-101"},
		flags: map[string]string{"summary": "a better summary"},
	},
	{command: "issue.move", fixture: "readback-move", args: []string{"ENG-101", "Close Issue"}},
	{command: "issue.assign", fixture: "readback-assign", args: []string{"ENG-101", "Ada Lovelace"}},
}

// runWrite runs one registered write verb against conn, through Validate and
// Run as a caller reaches it.
func runWrite(
	t *testing.T, kind site.Kind, conn *transport.Client, w guardedWrite,
	set func(registry.Flags),
) (*render.Doc, error) {
	t.Helper()

	cmd, ok := registry.Lookup(w.command)
	if !ok {
		t.Fatalf("%s is not registered", w.command)
	}
	flags := registry.NewFlags()
	for k, v := range w.flags {
		flags.SetString(k, v)
	}
	if set != nil {
		set(flags)
	}
	session := &stubSession{
		doer: &stubDoer{
			body:   catalogueJSON,
			byPath: map[string]string{"/user/search": directoryJSON[kind]},
		},
		conn: conn, kind: kind,
	}
	if w.command == "issue.move" && conn != nil {
		// Transitions come through Metadata, on the move's own connection.
		// Only for the move: assign resolves its person through the doer's
		// directory. And only a real one: a nil *transport.Client is a non-nil
		// site.Doer, and a lookup reaching it dereferences nil.
		session.metaClient = conn
	}
	inv := &registry.Invocation{
		Jira: session,
		Args: w.args, Flags: flags,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}
	if err := cmd.Validate(t.Context(), inv); err != nil {
		return nil, err
	}
	doc, err := cmd.Run(t.Context(), inv)
	if err == nil {
		if !cmd.Emits(doc.Kind, doc.Version) {
			t.Errorf("emitted %s v%d, which %s does not declare", doc.Kind, doc.Version, w.command)
		}
		if verr := doc.Validate(); verr != nil {
			t.Errorf("validate doc: %v", verr)
		}
	}
	return doc, err
}

func withReadBack(flags registry.Flags) { flags.SetBool("precondition", true) }

// codeOf is the error's code, and "accepted" for no error, so a refusal that
// stopped happening fails on that rather than on a nil dereference.
func codeOf(err error) string {
	if err == nil {
		return "accepted"
	}
	return errs.Coerce(err).Code
}

// TestAWriteReportsThePreconditionItLeft is issue 266: a write moves the
// issue's `updated`, so the token it was given is stale, and the next guarded
// write had nothing to pass until the caller read the issue again.
//
// The token asserted is the one minted from the read after the write. One
// minted from anything earlier (the caller's own token, or the check's read)
// describes a version the write itself replaced, and the next guarded write
// would refuse it as stale.
func TestAWriteReportsThePreconditionItLeft(t *testing.T) {
	for _, w := range guardedWrites {
		for _, kind := range deployments {
			t.Run(w.command+"/"+string(kind), func(t *testing.T) {
				conn, replayer := replayConn(t, w.fixture+"."+string(kind)+".json")
				set := withReadBack
				if w.command == "issue.edit" {
					// Guarded as well, so the fixture's first read is the check
					// and the token cannot be mistaken for its answer.
					set = func(flags registry.Flags) {
						withReadBack(flags)
						flags.SetString("if-unchanged", mint(t, kind, "ENG-101", preconditionAt))
					}
				}

				doc, err := runWrite(t, kind, conn, w, set)
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				got, _ := doc.Record.AttrValue("next-precondition")
				if want := mint(t, kind, "ENG-101", readBackAfterFirst); got != want {
					t.Errorf("precondition = %q, want the version read after the "+
						"write, %q", got, want)
				}
				if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
					t.Errorf("the fixture has interactions this run never used: %v", unplayed)
				}
			})
		}
	}
}

// TestAChainOfGuardedWritesNeedsNoRead is the card's done-when, on one
// conversation: an edit guarded by the caller's read hands back a token, a
// move guarded by that token runs and hands back another, and the caller's
// original token is stale by then.
func TestAChainOfGuardedWritesNeedsNoRead(t *testing.T) {
	for _, kind := range deployments {
		t.Run(string(kind), func(t *testing.T) {
			conn, replayer := replayConn(t, "readback-chain."+string(kind)+".json")
			first := mint(t, kind, "ENG-101", preconditionAt)
			edit, move := guardedWrites[0], guardedWrites[1]
			guarded := func(token string) func(registry.Flags) {
				return func(flags registry.Flags) {
					withReadBack(flags)
					flags.SetString("if-unchanged", token)
				}
			}

			doc, err := runWrite(t, kind, conn, edit, guarded(first))
			if err != nil {
				t.Fatalf("edit: %v", err)
			}
			second, _ := doc.Record.AttrValue("next-precondition")

			doc, err = runWrite(t, kind, conn, move, guarded(second))
			if err != nil {
				t.Fatalf("the move guarded by the edit's token was refused: %v", err)
			}
			third, _ := doc.Record.AttrValue("next-precondition")
			if want := mint(t, kind, "ENG-101", readBackAfterSecond); third != want {
				t.Errorf("the move reported %q, want %q", third, want)
			}

			_, err = runWrite(t, kind, conn, edit, guarded(first))
			if code := codeOf(err); code != "STALE_WRITE" {
				t.Errorf("a write from the caller's first token = %v, want STALE_WRITE", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the chain did not run as recorded: %v", unplayed)
			}
		})
	}
}

// TestWithoutReadBackNothingIsReadAfter keeps the flag free for a write that
// does not ask: edit.cloud.json holds the PUT and nothing else, so a read
// after it would have had nothing to answer it.
func TestWithoutReadBackNothingIsReadAfter(t *testing.T) {
	conn, replayer := replayConn(t, "edit.cloud.json")
	doc, err := runWrite(t, site.Cloud, conn, guardedWrites[0], nil)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
		t.Errorf("an unrequested read went out: %v", unmatched)
	}
	if token, ok := doc.Record.AttrValue("next-precondition"); ok {
		t.Errorf("a write that did not ask reports a precondition, %q", token)
	}
}

// TestAFailedReadBackReportsTheWriteItMade: the edit landed and the read
// after it did not. Reporting that as a failed edit invites a retry that sends
// it again, which for a move is a second transition, so the document says
// what was done and the cause says not to send it again.
func TestAFailedReadBackReportsTheWriteItMade(t *testing.T) {
	conn, replayer := replayConn(t, "readback-fails.datacenter.json")
	_, err := runWrite(t, site.DataCenter, conn, guardedWrites[0], withReadBack)
	partial, ok := errors.AsType[*registry.PartiallyApplied](err)
	if !ok {
		t.Fatalf("err = %v, want the edit reported as applied with the read's failure", err)
	}
	if action, _ := partial.Doc.Record.AttrValue("action"); action != "edited" {
		t.Errorf("action = %q, want edited", action)
	}
	if _, has := partial.Doc.Record.AttrValue("next-precondition"); has {
		t.Error("a token was reported from a read that failed")
	}

	e := errs.Coerce(partial.Cause)
	if !strings.Contains(e.Message, "ENG-101 was edited") {
		t.Errorf("message = %q, want it to say the edit was made", e.Message)
	}
	if !strings.Contains(e.Remedy, "issue get ENG-101") {
		t.Errorf("remedy = %q, want the read that recovers the token", e.Remedy)
	}
	if e.Retryable {
		t.Error("the cause is retryable, and retrying the command sends the write again")
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("unplayed = %v", unplayed)
	}
}

// TestReadBackIsRefusedWhereThereIsNoOneWrite covers the modes that write
// nothing, or write a plan's rows into a document that has no token to carry.
// Refused rather than ignored: a caller who asked for a token and silently got
// none would pass the empty attribute to --if-unchanged next.
func TestReadBackIsRefusedWhereThereIsNoOneWrite(t *testing.T) {
	modes := map[string]func(registry.Flags){
		"dry-run":  func(f registry.Flags) { f.SetBool("dry-run", true) },
		"plan-out": func(f registry.Flags) { f.SetString("plan-out", "p.xml") },
		"apply":    func(f registry.Flags) { f.SetString("apply", "p.xml") },
	}
	for _, w := range guardedWrites {
		for mode, set := range modes {
			t.Run(w.command+"/"+mode, func(t *testing.T) {
				_, err := runWrite(t, site.Cloud, nil, w, func(f registry.Flags) {
					withReadBack(f)
					set(f)
				})
				if code := codeOf(err); code != "PRECONDITION_NEEDS_A_WRITE" {
					t.Fatalf("code = %q (%v), want PRECONDITION_NEEDS_A_WRITE", code, err)
				}
				e := errs.Coerce(err)
				if !strings.Contains(e.Message, "--"+mode) {
					t.Errorf("message = %q, want it to name --%s", e.Message, mode)
				}
			})
		}
	}
}

// TestATokenGivenToReadBackIsNamed is the misreading the name invites: a
// caller who meant `--if-unchanged <token>` and typed `--precondition
// <token>` leaves the token as an argument. Without this the verb's own
// argument check answers first, BULK_NEEDS_A_PLAN, about a bulk edit nobody
// asked for. Without the flag the same arguments keep that answer, because
// changing the code an existing invocation gets is the breaking row.
func TestATokenGivenToReadBackIsNamed(t *testing.T) {
	token := mint(t, site.Cloud, "ENG-101", preconditionAt)
	for _, w := range guardedWrites {
		t.Run(w.command, func(t *testing.T) {
			w.args = append(append([]string(nil), w.args...), token)

			_, err := runWrite(t, site.Cloud, nil, w, withReadBack)
			if code := codeOf(err); code != "PRECONDITION_TAKES_NO_VALUE" {
				t.Errorf("with --precondition: code = %q (%v), want PRECONDITION_TAKES_NO_VALUE", code, err)
			}
			_, err = runWrite(t, site.Cloud, nil, w, nil)
			if code := codeOf(err); code != "BULK_NEEDS_A_PLAN" {
				t.Errorf("without it: code = %q (%v), want BULK_NEEDS_A_PLAN as before", code, err)
			}
		})
	}
}

// TestEveryGuardedVerbCanReadBack holds the two flags together. A verb that
// takes --if-unchanged and cannot hand back the next token is the gap issue
// 266 was filed about, reopened one verb at a time.
func TestEveryGuardedVerbCanReadBack(t *testing.T) {
	for _, cmd := range registry.All() {
		if declaresFlag(cmd, "if-unchanged") && !declaresFlag(cmd, "precondition") {
			t.Errorf("%s takes --if-unchanged and has no --precondition to hand "+
				"back the next one", strings.Join(cmd.Path, " "))
		}
	}
}

// TestARecordedChainHandsTheTokenOn replays what Cloud answered: an edit
// guarded by a read, then a move guarded by the token the edit handed back.
// Recorded on the oneil-team sandbox on 2026-10-20 against a probe issue made
// and deleted for it, scrubbed to AGL-11.
//
// The move's own check is the evidence the constructed chain cannot give: its
// first read of `updated` is the server's, so a token minted from anything but
// the edit's read-back would have been refused as stale by the recording.
func TestARecordedChainHandsTheTokenOn(t *testing.T) {
	const key = "AGL-11"
	editConn, editReplay := recordedConn(t, "readback-edit-recorded.cloud.json")
	first := mint(t, site.Cloud, key, firstUpdated(t, "readback-edit-recorded.cloud.json"))

	edit := guardedWrite{
		command: "issue.edit", args: []string{key},
		flags: map[string]string{"summary": "Read back after a write"},
	}
	doc, err := runWrite(t, site.Cloud, editConn, edit, func(f registry.Flags) {
		withReadBack(f)
		f.SetString("if-unchanged", first)
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	next, _ := doc.Record.AttrValue("next-precondition")

	moveConn, moveReplay := recordedConn(t, "readback-move-recorded.cloud.json")
	move := guardedWrite{command: "issue.move", args: []string{key, "In Progress"}}
	doc, err = runWrite(t, site.Cloud, moveConn, move, func(f registry.Flags) {
		withReadBack(f)
		f.SetString("if-unchanged", next)
	})
	if err != nil {
		t.Fatalf("the move guarded by the edit's token was refused: %v", err)
	}
	if token, _ := doc.Record.AttrValue("next-precondition"); token == "" || token == next {
		t.Errorf("the move reported %q, want a token past the edit's %q", token, next)
	}
	for _, r := range []*transport.Replayer{editReplay, moveReplay} {
		if unplayed := r.Unplayed(); len(unplayed) > 0 {
			t.Errorf("the recording was not replayed in full: %v", unplayed)
		}
	}
}

// firstUpdated is the `updated` the first interaction of a recording answered,
// the version the caller would have read before the first write.
func firstUpdated(t *testing.T, fixture string) string {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	var got struct {
		Fields struct {
			Updated string `json:"updated"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(cassette.Interactions[0].Response.Body), &got); err != nil ||
		got.Fields.Updated == "" {
		t.Fatalf("%s opens with no updated to read: %v", fixture, err)
	}
	return got.Fields.Updated
}
