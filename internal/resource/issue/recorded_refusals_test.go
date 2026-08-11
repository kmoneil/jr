//go:build write

package issue_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedDuplicateCreateIsAConversationAServerHad is the recorded half
// of the unkeyed-duplicate case.
//
// Two identical creates, and the point is that the second really is sent: an
// unkeyed create is not deduplicated, it is warned about. The constructed
// cassette beside this one asserts the same thing with two responses its author
// chose, which cannot show that a server accepts the same body twice and issues
// two keys — this one does, ENG-10 and ENG-11.
func TestTheRecordedDuplicateCreateIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("issue.create")
	if !ok {
		t.Fatal("issue create is not registered")
	}
	conn, replayer := recordedConn(t, "create-twice-recorded.datacenter.json")
	ledger := &idem.Ledger{Path: filepath.Join(t.TempDir(), "idempotency.toml")}

	var stderr strings.Builder
	keys := make([]string, 0, 2)
	for range 2 {
		flags := registry.NewFlags()
		flags.SetString("project", "ENG")
		flags.SetString("type", "Bug")
		flags.SetString("summary", "a summary")

		doc, err := cmd.Run(t.Context(), &registry.Invocation{
			Jira: &stubSession{
				conn: conn, metaClient: conn, kind: site.DataCenter, ledger: ledger,
			},
			Flags: flags, Stderr: &stderr, Stdout: io.Discard,
			// The warning is rendered in the invocation's format, so a
			// zero Format writes nothing and the assertion below reads as
			// "it did not warn".
			Format: render.XML, Progress: registry.NoProgress,
		})
		if err != nil {
			t.Fatalf("the request this code builds is not the one the server "+
				"answered: %v", err)
		}
		key, _ := doc.Record.AttrValue("key")
		keys = append(keys, key)
	}

	// Two issues, not one. Warning about a duplicate and refusing to make one
	// are different promises, and only the first is made without a key.
	if keys[0] == keys[1] {
		t.Errorf("both creates returned %s; the second was not really sent", keys[0])
	}
	if !strings.Contains(stderr.String(), "POSSIBLE_DUPLICATE") {
		t.Errorf("the second create went out unremarked:\n%s", stderr.String())
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("only one create was sent: %v", unplayed)
	}
}

// TestTheRecordedSubtaskRefusalIsAConversationAServerHad covers the hint, with
// Jira's real wording behind it.
//
// The constructed cassette says the server answers "The issue 'ENG-101' has
// subtasks and subtasks are not deleted." A real Data Center 10.4.0 says "The
// issue 'ENG-12' has subtasks.  You must specify the 'deleteSubtasks'
// parameter to delete this issue and all its subtasks." — different prose, two
// spaces and all, invented in good faith by whoever wrote the fixture.
//
// Nothing broke, and the reason is worth keeping: `explainSubtaskRefusal`
// matches the substring "subtask" rather than the sentence. A fixture that
// invents prose is only safe while nothing reads the prose closely, and this is
// the recording that says which of those two things is true here.
func TestTheRecordedSubtaskRefusalIsAConversationAServerHad(t *testing.T) {
	cmd, ok := registry.Lookup("issue.delete")
	if !ok {
		t.Fatal("issue delete is not registered")
	}
	conn, replayer := recordedConn(t, "delete-parent-recorded.datacenter.json")

	flags := registry.NewFlags()
	flags.SetBool("yes", true)

	_, err := cmd.Run(t.Context(), &registry.Invocation{
		Jira:  &stubSession{conn: conn, metaClient: conn, kind: site.DataCenter},
		Args:  []string{"ENG-12"},
		Flags: flags, Stderr: io.Discard, Stdout: io.Discard,
		Progress: registry.NoProgress,
	})
	if err == nil {
		t.Fatal("an issue with a subtask was deleted without asking")
	}

	structured := errs.Coerce(err)
	if !strings.Contains(structured.Remedy, "--subtasks") {
		t.Errorf("the refusal does not name the flag that would work: %q",
			structured.Remedy)
	}
	// Jira's own sentence survives into the detail, which is where the caller
	// finds out it was subtasks rather than permissions.
	if !strings.Contains(structured.Detail, "deleteSubtasks") {
		t.Errorf("detail = %q, and it drops what the server said", structured.Detail)
	}
	if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
		t.Errorf("the delete was never attempted: %v", unplayed)
	}
}
