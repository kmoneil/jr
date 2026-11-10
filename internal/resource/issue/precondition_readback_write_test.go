//go:build write

package issue_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	// guarded is whether the fixture opens with the check's read, so the run
	// passes --if-unchanged too and the token cannot be mistaken for the
	// check's answer. The move and assign fixtures from issue 266 hold no
	// check.
	guarded bool
	// plans is whether the verb has --plan-out and --apply, which read-back is
	// refused beside as it is beside --dry-run.
	plans bool
	// file is what an upload attaches. It is written as repro.py in a temporary
	// directory, and that path takes the place of the repro.py argument.
	file string
	// alone names the cassettes, one per deployment, that hold the write and
	// nothing else, so a run without either flag that reads anything fails.
	alone string
}

// guardedWrites is every verb that takes both flags. The first three are issue
// 266's; the rest are issue 270's, the writes that change one named issue and
// move its updated without being an edit.
var guardedWrites = []guardedWrite{
	{
		command: "issue.edit", fixture: "readback-edit", args: []string{"ENG-101"},
		flags:   map[string]string{"summary": "a better summary"},
		guarded: true, plans: true, alone: "edit",
	},
	{command: "issue.move", fixture: "readback-move", args: []string{"ENG-101", "Close Issue"}, plans: true},
	{command: "issue.assign", fixture: "readback-assign", args: []string{"ENG-101", "Ada Lovelace"}, plans: true},
	{
		command: "issue.attachment.upload", fixture: "readback-upload",
		args: []string{"ENG-101", "repro.py"}, file: "print('reproduces issue 270')\n",
		guarded: true,
	},
	{
		command: "issue.comment.add", fixture: "readback-comment-add",
		args: []string{"ENG-101", "a new comment"}, guarded: true, alone: "comment-add",
	},
	{
		command: "issue.comment.edit", fixture: "readback-comment-edit",
		args: []string{"ENG-101", "10001", "an edited comment"}, guarded: true,
	},
	{
		command: "issue.comment.delete", fixture: "readback-comment-delete",
		args: []string{"ENG-101", "10001"}, guarded: true, alone: "comment-delete",
	},
	{
		command: "issue.worklog.add", fixture: "readback-worklog-add",
		args: []string{"ENG-101", "3h"}, flags: map[string]string{"started": "2026-08-05T09:00:00Z"},
		guarded: true, alone: "worklog-add",
	},
	{
		command: "issue.worklog.delete", fixture: "readback-worklog-delete",
		args: []string{"ENG-101", "10001"}, guarded: true, alone: "worklog-delete",
	},
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
	if w.file != "" {
		path := filepath.Join(t.TempDir(), "repro.py")
		if err := os.WriteFile(path, []byte(w.file), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		w.args = slices.Clone(w.args)
		if i := slices.Index(w.args, "repro.py"); i >= 0 {
			w.args[i] = path
		}
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

// TestAWriteReportsThePreconditionItLeft is issues 266 and 270: a write moves
// the issue's `updated`, so the token it was given is stale, and the next
// guarded write had nothing to pass until the caller read the issue again.
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
				if w.guarded {
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

// TestWithoutReadBackNothingIsReadAfter keeps the flags free for a write that
// asks for neither: each `alone` cassette holds the write and nothing else, so
// a read before or after it would have had nothing to answer it. An upload has
// no such cassette and needs none: every upload test runs against a server
// that answers anything but a multipart form with a 400.
func TestWithoutReadBackNothingIsReadAfter(t *testing.T) {
	for _, w := range guardedWrites {
		if w.alone == "" {
			continue
		}
		for _, kind := range deployments {
			t.Run(w.command+"/"+string(kind), func(t *testing.T) {
				conn, replayer := replayConn(t, w.alone+"."+string(kind)+".json")
				doc, err := runWrite(t, kind, conn, w, nil)
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
					t.Errorf("an unrequested read went out: %v", unmatched)
				}
				if token, ok := doc.Record.AttrValue("next-precondition"); ok {
					t.Errorf("a write that did not ask reports a precondition, %q", token)
				}
				if _, checked := doc.Record.ChildNamed("precondition"); checked {
					t.Error("a write that was not guarded records a check")
				}
			})
		}
	}
}

// TestAStaleTokenStopsEveryGuardedWrite is the other half of issue 270's
// flags: --if-unchanged on a comment, a worklog or an upload is compared before
// the write, and a stale one is refused with nothing sent.
func TestAStaleTokenStopsEveryGuardedWrite(t *testing.T) {
	const before = "2026-08-04T11:30:00.000+0000"
	for _, w := range guardedWrites {
		if !w.guarded {
			continue
		}
		for _, kind := range deployments {
			t.Run(w.command+"/"+string(kind), func(t *testing.T) {
				conn, replayer := replayConn(t, w.fixture+"."+string(kind)+".json")
				_, err := runWrite(t, kind, conn, w, func(flags registry.Flags) {
					withReadBack(flags)
					flags.SetString("if-unchanged", mint(t, kind, "ENG-101", before))
				})
				if code := codeOf(err); code != "STALE_WRITE" {
					t.Fatalf("code = %q (%v), want STALE_WRITE", code, err)
				}
				// The fixture's write and its read after: neither may have gone.
				if unplayed := replayer.Unplayed(); len(unplayed) != 2 {
					t.Errorf("unplayed = %v, want the write and the read after it", unplayed)
				}
			})
		}
	}
}

// TestAFailedReadBackReportsTheWriteItMade: the write landed and the read
// after it did not. Reporting that as a failed write invites a retry that sends
// it again, which for a move is a second transition and for a comment a second
// comment, so the document says what was done and the cause says not to send
// it again.
func TestAFailedReadBackReportsTheWriteItMade(t *testing.T) {
	for _, tc := range []struct {
		write   guardedWrite
		fixture string
		said    string
	}{
		{guardedWrites[0], "readback-fails.datacenter.json", "ENG-101 was edited"},
		{guardedWrites[4], "readback-comment-add-fails.datacenter.json", "comment 10099 was added to ENG-101"},
	} {
		t.Run(tc.write.command, func(t *testing.T) {
			conn, replayer := replayConn(t, tc.fixture)
			_, err := runWrite(t, site.DataCenter, conn, tc.write, withReadBack)
			partial, ok := errors.AsType[*registry.PartiallyApplied](err)
			if !ok {
				t.Fatalf("err = %v, want the write reported as applied with the read's failure", err)
			}
			if partial.Doc == nil || partial.Doc.Kind != tc.write.command {
				t.Fatalf("doc = %+v, want the %s document", partial.Doc, tc.write.command)
			}
			if _, has := partial.Doc.Record.AttrValue("next-precondition"); has {
				t.Error("a token was reported from a read that failed")
			}

			e := errs.Coerce(partial.Cause)
			if !strings.Contains(e.Message, tc.said) {
				t.Errorf("message = %q, want it to say %q", e.Message, tc.said)
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
		})
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
			if mode != "dry-run" && !w.plans {
				continue
			}
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
// argument check answers first: BULK_NEEDS_A_PLAN on the verbs that take
// several keys, about a bulk edit nobody asked for, and on the others the
// token is taken for the argument it displaced, a comment's text or a file or
// an id, which for a comment is posting the token. Without the flag the same
// arguments keep their answer, because changing the code an existing
// invocation gets is the breaking row.
func TestATokenGivenToReadBackIsNamed(t *testing.T) {
	token := mint(t, site.Cloud, "ENG-101", preconditionAt)
	for _, w := range guardedWrites {
		t.Run(w.command, func(t *testing.T) {
			w.args = slices.Clone(w.args)
			if w.plans {
				w.args = append(w.args, token)
			} else {
				// `issue comment add ENG-101 --precondition <token>`: the
				// token is where the body was.
				w.args[len(w.args)-1] = token
			}

			_, err := runWrite(t, site.Cloud, nil, w, withReadBack)
			if code := codeOf(err); code != "PRECONDITION_TAKES_NO_VALUE" {
				t.Errorf("with --precondition: code = %q (%v), want PRECONDITION_TAKES_NO_VALUE", code, err)
			}
			if !w.plans {
				// Without the flag nothing reaches a server here, so a verb
				// that would send leaves Run on the missing connection; only
				// the local refusals are compared.
				err = cmdValidate(t, w)
				if code := codeOf(err); code == "PRECONDITION_TAKES_NO_VALUE" {
					t.Errorf("without it: code = %q, the flag's refusal without the flag", code)
				}
				return
			}
			_, err = runWrite(t, site.Cloud, nil, w, nil)
			if code := codeOf(err); code != "BULK_NEEDS_A_PLAN" {
				t.Errorf("without it: code = %q (%v), want BULK_NEEDS_A_PLAN as before", code, err)
			}
		})
	}
}

// cmdValidate runs only a verb's Validate, for the arguments w carries.
func cmdValidate(t *testing.T, w guardedWrite) error {
	t.Helper()
	cmd, ok := registry.Lookup(w.command)
	if !ok {
		t.Fatalf("%s is not registered", w.command)
	}
	flags := registry.NewFlags()
	for k, v := range w.flags {
		flags.SetString(k, v)
	}
	return cmd.Validate(t.Context(), &registry.Invocation{
		Args: w.args, Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
	})
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

// TestEveryReadBackIsGuarded is the converse, from issue 270. A write that
// hands back a token and takes no guard makes a chain that is only guarded from
// its second step: whatever changed between the caller's read and the first
// write is inside the token that write reports, and the next guarded write is
// checked against a version nobody looked at. `issue list --precondition` is a
// read and is not a write's read-back.
func TestEveryReadBackIsGuarded(t *testing.T) {
	writes := 0
	for _, cmd := range registry.All() {
		if !cmd.Mutating || !declaresFlag(cmd, "precondition") {
			continue
		}
		writes++
		if !declaresFlag(cmd, "if-unchanged") {
			t.Errorf("%s hands back a precondition and takes no --if-unchanged, "+
				"so the first write of a chain is unguarded", strings.Join(cmd.Path, " "))
		}
	}
	if writes < len(guardedWrites) {
		t.Errorf("found %d writes with --precondition and the table holds %d; "+
			"the sweep read the wrong registry", writes, len(guardedWrites))
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

// TestARecordedUploadHandsTheTokenToAnEdit is issue 270 as it was observed,
// replayed from what each deployment answered: an upload guarded by a read and
// asked for the token after it, then an edit of the description guarded by that
// token, with no read between. Recorded on 2026-11-04 against a probe issue
// made and deleted for it, on the oneil-team sandbox (scrubbed to AGL-18) and
// on the Data Center rig (scripts/dc/record-writes.sh). The Data Center pair
// was recorded again on 2026-11-10 with the same commands, when a Data Center
// edit began reading its edit screen first (issue 291).
//
// The edit's own check is the evidence: its first read is the server's, after
// the upload, so a token minted from anything but the upload's read-back would
// be refused by the recording. And the caller's first token, the one the issue
// passed, is refused by that same read.
func TestARecordedUploadHandsTheTokenToAnEdit(t *testing.T) {
	for _, tc := range []struct {
		kind site.Kind
		key  string
	}{
		{site.Cloud, "AGL-18"},
		{site.DataCenter, "ENG-6"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			uploadFixture := "readback-upload-recorded." + string(tc.kind) + ".json"
			editFixture := "readback-upload-edit-recorded." + string(tc.kind) + ".json"
			first := mint(t, tc.kind, tc.key, firstUpdated(t, uploadFixture))

			uploadConn, uploadReplay := recordedConn(t, uploadFixture)
			upload := guardedWrite{
				command: "issue.attachment.upload", args: []string{tc.key, "repro.py"},
				file: "print('reproduces issue 270')\n",
			}
			doc, err := runWrite(t, tc.kind, uploadConn, upload, func(f registry.Flags) {
				withReadBack(f)
				f.SetString("if-unchanged", first)
			})
			if err != nil {
				t.Fatalf("upload: %v", err)
			}
			// Not compared with the caller's token here: the edit's recorded
			// check is what refuses a token from the wrong read.
			next, _ := doc.Record.AttrValue("next-precondition")
			if next == "" {
				t.Fatal("the upload reported no next precondition")
			}

			desc := filepath.Join(t.TempDir(), "desc.txt")
			if err := os.WriteFile(desc, []byte("Steps to reproduce are in the attached repro.py.\n"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			edit := guardedWrite{
				command: "issue.edit", args: []string{tc.key},
				flags: map[string]string{"description-file": desc},
			}
			editConn, editReplay := recordedConn(t, editFixture)
			if _, err := runWrite(t, tc.kind, editConn, edit, func(f registry.Flags) {
				f.SetString("if-unchanged", next)
			}); err != nil {
				t.Fatalf("the edit guarded by the upload's token was refused: %v", err)
			}
			for _, r := range []*transport.Replayer{uploadReplay, editReplay} {
				if unplayed := r.Unplayed(); len(unplayed) > 0 {
					t.Errorf("the recording was not replayed in full: %v", unplayed)
				}
			}

			staleConn, _ := recordedConn(t, editFixture)
			_, err = runWrite(t, tc.kind, staleConn, edit, func(f registry.Flags) {
				f.SetString("if-unchanged", first)
			})
			if code := codeOf(err); code != "STALE_WRITE" {
				t.Errorf("an edit from the token read before the upload = %v, want STALE_WRITE", err)
			}
		})
	}
}
