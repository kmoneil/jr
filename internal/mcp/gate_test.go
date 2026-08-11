//go:build mcp

package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/mcp"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// gateSession is a Session that can be read-only, and that counts every attempt
// to reach Jira.
//
// The count is the point. A tool call that is refused with the right code after
// the DELETE has already gone out is the defect this file exists to catch, and
// an assertion on the code alone passes against it. Connect is the only way a
// command gets a transport, so counting it is counting requests.
type gateSession struct {
	readOnly bool
	connects int
}

func (s *gateSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	s.connects++
	return nil, site.Info{}, errs.Remote("NETWORK",
		"the gate should have refused before anything reached here")
}

func (s *gateSession) Metadata(context.Context) (*site.Metadata, error) {
	s.connects++
	return nil, errs.Remote("NETWORK",
		"the gate should have refused before anything reached here")
}

func (s *gateSession) CheckWritable(command string) error {
	if !s.readOnly {
		return nil
	}
	return errs.Blocked("READ_ONLY",
		"%s would change Jira, and this is read-only", command)
}

func (*gateSession) Project() string                 { return "ENG" }
func (*gateSession) RequireProject() (string, error) { return "ENG", nil }
func (*gateSession) Board() string                   { return "1" }
func (*gateSession) RequireBoard() (string, error)   { return "1", nil }
func (*gateSession) Fields() []string                { return nil }
func (*gateSession) Idempotency() *idem.Ledger       { return nil }

// gatedRegistry registers one mutating command that records whether it ran, so
// a test can tell "refused" from "ran and failed".
//
// Validate records too, and that is deliberate: the gate has to run before
// Validate, not merely before Run. Validate is where a streaming command
// resolves field names against the site's catalogue, which costs requests — so
// a gate that fired after it would already have talked to Jira.
func gatedRegistry(ran, validated *bool, destructive bool) *registry.Registry {
	r := registry.New()
	flags := []registry.Flag{
		{Name: "dry-run", Type: registry.TypeBool, Usage: "print the request"},
	}
	if destructive {
		flags = append(flags, registry.Flag{
			Name: "yes", Type: registry.TypeBool, Usage: "confirm",
		})
	}
	r.Register(&registry.Command{
		Path:        []string{"thing", "write"},
		Summary:     "Change something",
		Example:     "jr thing write",
		Flags:       flags,
		Mutating:    true,
		Destructive: destructive,
		NeedsJira:   true,
		Outputs:     []registry.Output{{Kind: "thing.write", Version: 1}},
		ExitCodes:   []exitcode.Code{exitcode.Blocked},
		Validate: func(context.Context, *registry.Invocation) error {
			*validated = true
			return nil
		},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			*ran = true
			return render.Record("thing.write", 1, render.El("done")), nil
		},
	})
	return r
}

// gatedSession is session() with a Session attached, which the shared helper
// has no need for: none of its commands set NeedsJira, and every command here
// does.
func gatedSession(
	t *testing.T, reg *registry.Registry, sess registry.Session, requests ...string,
) []map[string]any {
	t.Helper()

	var out strings.Builder
	err := mcp.Serve(context.Background(), mcp.Options{
		Registry: reg,
		In:       strings.NewReader(strings.Join(requests, "\n") + "\n"),
		Out:      &out,
		Log:      io.Discard,
		Name:     "jr",
		Version:  "0.0.0-test",
		Session:  func() (registry.Session, error) { return sess, nil },
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	var replies []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("reply is not JSON: %v\n%s", err, line)
		}
		replies = append(replies, reply)
	}
	return replies
}

// callGatedTool drives one tools/call against a session and returns what the
// reply carried, plus whether it was a tool error. It is callTool with a
// Session, which the shared helper does not take.
func callGatedTool(
	t *testing.T, reg *registry.Registry, sess registry.Session, args any,
) (text string, isError bool) {
	t.Helper()

	params, err := json.Marshal(map[string]any{"name": "thing_write", "arguments": args})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	replies := gatedSession(t, reg, sess,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":`+string(params)+`}`)
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1: %v", len(replies), replies)
	}
	if rpcErr, has := replies[0]["error"]; has {
		t.Fatalf("protocol error where a tool result was expected: %v", rpcErr)
	}
	result, ok := replies[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", replies[0])
	}
	isError, _ = result["isError"].(bool)
	return toolText(t, result), isError
}

// TestAToolCallHonoursTheReadOnlyLatch is the one-way latch reaching the thing
// it exists to stop, over MCP rather than over the CLI.
//
// §6.1 says every mutating command exits 10 before any network call, and §6.2
// says a destructive one requires --yes in every build. Both were enforced in
// the CLI's runLeaf and nowhere else, so neither held here: a context created
// --readonly sent a real DELETE, verified against a server. The rule now lives
// in registry.Gate, and this is that guarantee cashed on the interface §6 is
// about — the one where nobody is watching.
func TestAToolCallHonoursTheReadOnlyLatch(t *testing.T) {
	var ran, validated bool
	sess := &gateSession{readOnly: true}

	// --yes given, so the confirmation gate is satisfied and read-only is what
	// is left to refuse. Without it the reply would be CONFIRMATION_REQUIRED
	// and this test would pass while saying nothing about read-only.
	text, isError := callGatedTool(t, gatedRegistry(&ran, &validated, true), sess,
		map[string]any{"yes": true})

	if !isError {
		t.Error("a mutation under read-only was not reported as an error")
	}
	if !strings.Contains(text, "READ_ONLY") {
		t.Errorf("reply does not carry the code:\n%s", text)
	}
	if ran {
		t.Error("the command ran despite read-only mode")
	}
	if validated {
		t.Error("Validate ran; the gate must refuse before it, because Validate makes requests")
	}
	if sess.connects != 0 {
		t.Errorf("%d attempts to reach Jira, want 0 — refusing after the "+
			"request has gone out is the defect this test exists for", sess.connects)
	}
}

// TestAToolCallHonoursTheConfirmationGate is the other half. Nothing ever
// blocks on input, so the absence of a confirmation is a refusal rather than a
// question nobody can answer — and a tool call has nobody to ask.
func TestAToolCallHonoursTheConfirmationGate(t *testing.T) {
	var ran, validated bool
	sess := &gateSession{}

	text, isError := callGatedTool(t, gatedRegistry(&ran, &validated, true), sess, map[string]any{})

	if !isError {
		t.Error("a destructive command with no confirmation was not reported as an error")
	}
	if !strings.Contains(text, "CONFIRMATION_REQUIRED") {
		t.Errorf("reply does not carry the code:\n%s", text)
	}
	if ran {
		t.Error("a destructive command ran without confirmation")
	}
	if validated {
		t.Error("Validate ran; the gate must refuse before it")
	}
	if sess.connects != 0 {
		t.Errorf("%d attempts to reach Jira, want 0", sess.connects)
	}
}

// TestAToolCallRunsWhenNothingBlocksIt is the counterpart, so the two tests
// above cannot pass because the command was broken for an unrelated reason.
func TestAToolCallRunsWhenNothingBlocksIt(t *testing.T) {
	var ran, validated bool

	_, isError := callGatedTool(t, gatedRegistry(&ran, &validated, true), &gateSession{},
		map[string]any{"yes": true})

	if isError {
		t.Error("a permitted mutation was refused")
	}
	if !ran {
		t.Error("the command did not run")
	}
	if !validated {
		t.Error("Validate did not run")
	}
}

// TestReadOnlyIsNotRelaxedForADryRunOverMCP pins the deliberate asymmetry on
// this path too. A missing confirmation is a step the caller has not taken; a
// read-only context is a statement about what that context is for.
func TestReadOnlyIsNotRelaxedForADryRunOverMCP(t *testing.T) {
	var ran, validated bool
	sess := &gateSession{readOnly: true}

	text, isError := callGatedTool(t, gatedRegistry(&ran, &validated, true), sess,
		map[string]any{"dry-run": true, "yes": true})

	if !isError || !strings.Contains(text, "READ_ONLY") {
		t.Errorf("a dry run under read-only was allowed:\n%s", text)
	}
	if ran {
		t.Error("the command ran")
	}
	if sess.connects != 0 {
		t.Errorf("%d attempts to reach Jira, want 0", sess.connects)
	}
}

// TestANonMutatingToolCallIsNotGated keeps the gate from becoming a tax on
// every read. A reader build's whole tool list is non-mutating, and a gate that
// refused there would make `mcp serve` useless in the profile it matters most
// in.
func TestANonMutatingToolCallIsNotGated(t *testing.T) {
	replies := gatedSession(t, testRegistry(t), &gateSession{readOnly: true},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"thing_get","arguments":{"key":"K-1"}}}`)

	result, _ := replies[0]["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		t.Errorf("a read was refused under read-only: %v", result)
	}
}
