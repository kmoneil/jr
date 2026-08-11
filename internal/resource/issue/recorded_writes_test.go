//go:build write

package issue_test

import (
	"io"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
)

// TestTheRecordedWritesAreConversationsAServerHad drives every write verb
// against the exchange a real Data Center had with it.
//
// The reads were recorded first and the writes stayed hand-written, which left
// the half of this tool that changes somebody's Jira resting entirely on what
// its author believed the server would accept. That is the weaker half to
// guess at: a read that asks the wrong question comes back empty and a write
// that asks the wrong question is a 400 in front of a user, or worse, a
// mutation that half happened.
//
// Recorded by scripts/dc/record-writes.sh against Jira Software Data Center
// 10.4.0, in one ordered pass — a comment cannot be deleted before it is
// added, and its id is not knowable until the add answers.
//
// What each case establishes is narrow and worth stating: the method, path,
// query and body this code builds are the ones a server answered, and the
// status it answered with is one this code treats as success. The replayer
// matches on all four, so a change to any of them fails here rather than in
// front of somebody's issue tracker.
func TestTheRecordedWritesAreConversationsAServerHad(t *testing.T) {
	// The subject of every recording. The script creates it, so the key is the
	// one that instance issued rather than one chosen here.
	const (
		subject = "ENG-8"
		clone   = "ENG-9"
	)

	for _, tc := range []struct {
		name     string
		cassette string
		command  string
		args     []string
		flags    map[string]string
		bools    map[string]bool
	}{
		{
			name: "create", cassette: "create-recorded", command: "issue.create",
			flags: map[string]string{
				"project": "ENG", "type": "Task",
				"summary":     "Recorded write path",
				"description": "Created by scripts/dc/record-writes.sh",
			},
		},
		{
			name: "edit", cassette: "edit-recorded", command: "issue.edit",
			args:  []string{subject},
			flags: map[string]string{"summary": "Recorded write path, edited"},
		},
		{
			name: "assign", cassette: "assign-recorded", command: "issue.assign",
			// Data Center resolves a user by username through /user/search,
			// where Cloud sends an accountId. Both requests are in the
			// cassette, so the resolution is covered as well as the write.
			args: []string{subject, "grace"},
		},
		{
			name: "watch", cassette: "watch-recorded", command: "issue.watch",
			args: []string{subject},
		},
		{
			name: "unwatch", cassette: "unwatch-recorded", command: "issue.watch",
			args: []string{subject}, bools: map[string]bool{"remove": true},
		},
		{
			name: "comment add", cassette: "comment-add-recorded",
			command: "issue.comment.add",
			args:    []string{subject, "Recorded by the fixture rig"},
		},
		{
			name: "comment delete", cassette: "comment-delete-recorded",
			command: "issue.comment.delete",
			args:    []string{subject, "10001"},
		},
		{
			// --started is not decoration. Without it the body carries the
			// clock, the replayer matches on the body, and the recording is of
			// a request nobody can build twice — the first attempt at this
			// cassette failed with FIXTURE_MISS against its own recording.
			name: "worklog add", cassette: "worklog-add-recorded",
			command: "issue.worklog.add", args: []string{subject, "1h"},
			flags: map[string]string{"started": "2026-01-02T09:00:00Z"},
		},
		{
			name: "worklog delete", cassette: "worklog-delete-recorded",
			command: "issue.worklog.delete", args: []string{subject, "10002"},
		},
		{
			name: "link add", cassette: "link-add-recorded",
			command: "issue.link.add",
			// The link type catalogue is read first, because the phrase a site
			// uses is a site setting.
			args: []string{subject, "relates to", "ENG-2"},
		},
		{
			name: "move", cassette: "move-recorded", command: "issue.move",
			args: []string{subject, "In Progress"},
		},
		{
			name: "clone", cassette: "clone-recorded", command: "issue.clone",
			args:  []string{subject},
			flags: map[string]string{"summary": "Recorded write path, cloned"},
		},
		{
			name: "delete", cassette: "delete-recorded", command: "issue.delete",
			args: []string{clone},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := registry.Lookup(tc.command)
			if !ok {
				t.Fatalf("%s is not registered", tc.command)
			}
			conn, replayer := recordedConn(t, tc.cassette+".datacenter.json")

			flags := registry.NewFlags()
			for name, value := range tc.flags {
				flags.SetString(name, value)
			}
			for name, value := range tc.bools {
				flags.SetBool(name, value)
			}

			inv := &registry.Invocation{
				// metaClient as well as conn: resolving an assignee, a link
				// phrase, or a transition goes through Session.Metadata, and a
				// stub that left it unset reached a nil doer. Both point at
				// the same replayer, so the resolution and the write are
				// answered from the one recording that really carried them.
				Jira: &stubSession{conn: conn, metaClient: conn, kind: site.DataCenter},
				Args: tc.args, Flags: flags,
				Stderr: io.Discard, Stdout: io.Discard,
				Progress: registry.NoProgress,
			}
			if cmd.Validate != nil {
				if err := cmd.Validate(t.Context(), inv); err != nil {
					t.Fatalf("validate: %v", err)
				}
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("the request this code builds is not the one the "+
					"server answered: %v", err)
			}
			if doc != nil {
				if err := doc.Validate(); err != nil {
					t.Errorf("the result does not match its declared kind: %v", err)
				}
			}
			// The mutation itself. A cassette left unplayed means the write
			// never went out, which is the failure a test asserting only on
			// the returned document cannot see.
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the write was never sent: %v", unplayed)
			}
			if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
				t.Errorf("a request went somewhere unrecorded: %v", unmatched)
			}
		})
	}
}
