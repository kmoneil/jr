//go:build mcp

package mcp_test

import (
	"context"
	"testing"

	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// stdoutOwners registers the two shapes that write to stdout themselves, so the
// distinction between them is pinned rather than inferred from one example.
//
// `serve` owns the stream for the life of the process. `download` owns it only
// when told to, and is a working tool the rest of the time.
func stdoutOwners(ran *bool) *registry.Registry {
	r := registry.New()

	r.Register(&registry.Command{
		Path:       []string{"proto", "serve"},
		Summary:    "Speak a protocol on stdio",
		Example:    "jr proto serve",
		OwnsStdout: true,
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			*ran = true
			return nil, nil //nolint:nilnil // the stream is the result.
		},
	})

	r.Register(&registry.Command{
		Path:    []string{"proto", "download"},
		Summary: "Write a file, or put it on stdout",
		Example: "jr proto download",
		Flags: []registry.Flag{
			{Name: "output", Type: registry.TypeString, Usage: "a path, or - for stdout"},
		},
		Outputs: []registry.Output{{Kind: "proto.download", Version: 1}},
		OwnsStdoutWhen: func(inv *registry.Invocation) bool {
			return inv.Flags.String("output") == "-"
		},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			return render.Record("proto.download", 1, render.El("written")), nil
		},
	})
	return r
}

// TestACommandThatOwnsStdoutIsNotATool covers both halves of the refusal, which
// are separate holes: the list is a description of this server, and the call is
// the control. A peer can call a name that was never advertised.
//
// Calling `mcp_serve` started a second server reading the same stdin and
// writing the same stdout. The outer request never answered — the outer server
// was blocked inside Command.Run — and the nested one consumed and replied to
// the frames that followed, from a server the client does not know exists.
//
// This test does not reproduce that hang, and the reason is worth writing down:
// with every frame already in the reader, the nested server reads EOF at once
// and returns tidily, so the harness that makes the suite deterministic is
// exactly the one that hides the failure. It was found with a driver that sends
// frames over time. What is asserted instead is the pair of properties that the
// defect makes impossible — not in the list, refused at the call.
func TestACommandThatOwnsStdoutIsNotATool(t *testing.T) {
	var ran bool
	reg := stdoutOwners(&ran)

	replies := session(t, reg, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := replies[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)

	names := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		names[name] = true
	}
	if names["proto_serve"] {
		t.Error("a command that owns stdout is advertised as a tool")
	}
	// The counterpart, so the check above cannot pass by hiding everything.
	if !names["proto_download"] {
		t.Errorf("OwnsStdoutWhen disqualified a command it should not: %v", names)
	}
}

// TestCallingACommandThatOwnsStdoutIsRefused is the control rather than the
// description. It is a protocol error, not a tool error: a tool result would
// mean the server accepted the call and the command failed, and this call is
// one the server will not make.
func TestCallingACommandThatOwnsStdoutIsRefused(t *testing.T) {
	var ran bool
	reg := stdoutOwners(&ran)

	replies := session(t, reg,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"proto_serve","arguments":{}}}`,
		// A frame after it, because the failure this prevents is the session
		// dying rather than the call failing. If the refusal did not return,
		// this would go unanswered.
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)

	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2 — the session did not survive: %v",
			len(replies), replies)
	}
	if _, isError := replies[0]["error"]; !isError {
		t.Errorf("calling a stdout-owning command was not a protocol error: %v", replies[0])
	}
	if ran {
		t.Error("the command ran; a second server was started on this stream")
	}
	if replies[1]["id"] != float64(2) {
		t.Errorf("the frame after the refusal was not answered: %v", replies[1])
	}
}
