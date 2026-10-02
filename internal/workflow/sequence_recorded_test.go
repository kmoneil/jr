//go:build write

package workflow_test

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/workflow"
)

// TestASequencePlanReplaysTheRecordings plans the same six steps against a
// real conversation with each deployment: a comment, a label, an assignment,
// a link, a sprint and a move, which between them reach every read the
// pre-flight makes. The fake the other tests use was written from belief;
// these were recorded, so the mypermissions, editmeta and assignable-search
// requests are the shapes the servers answered, and every one of them is
// asked for again here or the replay fails.
func TestASequencePlanReplaysTheRecordings(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		kind    site.Kind
		key     string
		steps   string
	}{
		{
			fixture: "sequence-plan.cloud.json", kind: site.Cloud, key: "AGL-6",
			steps: `[["issue","comment","add","AGL-6","Shipped in 1.4."],` +
				`["issue","edit","AGL-6","--add-label","shipped"],` +
				`["issue","assign","AGL-6","currentUser"],` +
				`["issue","link","add","AGL-6","blocks","AGL-5"],` +
				`["sprint","add","2","AGL-6"],` +
				`["issue","move","AGL-6","In Progress"]]`,
		},
		{
			fixture: "sequence-plan-recorded.datacenter.json", kind: site.DataCenter, key: "ENG-3",
			steps: `[["issue","comment","add","ENG-3","Shipped in 1.4."],` +
				`["issue","edit","ENG-3","--add-label","shipped"],` +
				`["issue","assign","ENG-3","grace"],` +
				`["issue","link","add","ENG-3","blocks","ENG-2"],` +
				`["sprint","add","1","ENG-3"],` +
				`["issue","move","ENG-3","In Progress"]]`,
		},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			conn, replayer := recordedConn(t, tc.fixture)
			cmd, ok := registry.Lookup("issue.sequence")
			if !ok {
				t.Fatal("issue sequence is not registered")
			}
			flags := registry.NewFlags()
			flags.SetString("steps", tc.steps)
			flags.SetString("plan-out", filepath.Join(t.TempDir(), "plan.xml"))
			inv := &registry.Invocation{
				Jira: &seqSession{conn: conn, kind: tc.kind}, Args: []string{tc.key},
				Flags: flags, Stderr: io.Discard, Progress: registry.NoProgress,
			}
			if err := cmd.Validate(t.Context(), inv); err != nil {
				t.Fatalf("validate: %v", err)
			}
			doc, err := cmd.Run(t.Context(), inv)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if doc.Kind != workflow.KindSequencePlan {
				t.Fatalf("kind = %s", doc.Kind)
			}
			if steps, _ := doc.Record.ChildNamed("steps"); len(steps.Children) != 6 {
				t.Errorf("planned %d steps, want 6", len(steps.Children))
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the plan never asked for: %v", unplayed)
			}
			if unmatched := replayer.Unmatched(); len(unmatched) > 0 {
				t.Errorf("the plan asked for what the recording does not hold: %v", unmatched)
			}
		})
	}
}
