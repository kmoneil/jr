//go:build mcp

package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// frames drives `jr mcp serve` in process with the real registry and returns
// the replies.
//
// It runs the *command*, not mcp.Serve, and that is the whole reason this file
// is in internal/cli rather than beside the server. The registry a tool list is
// built from is assembled here — resources plus the tagged built-ins — so a
// test in internal/mcp can only ever ask the question of a registry it made up.
// This tree has shipped two defects that a test one layer down could not see;
// `mcp serve` was both of them.
func frames(t *testing.T, requests ...string) []map[string]any {
	t.Helper()

	stdin := strings.NewReader(strings.Join(requests, "\n") + "\n")
	got := runWithStdin(t, nil, stdin, "mcp", "serve")
	if got.exit != 0 {
		t.Fatalf("mcp serve exited %v\nstderr: %s", got.exit, got.stderr)
	}

	var replies []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(got.stdout), "\n") {
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

// TestMCPServeIsNotOneOfItsOwnTools drives the shipped command.
//
// Every command in the build becomes a tool, and `mcp serve` is a command in
// the build, so it was advertised — and calling it started a second server on
// the same stdin and stdout, which ended the session for good. `OwnsStdout`
// already said this could not work; nothing read it.
func TestMCPServeIsNotOneOfItsOwnTools(t *testing.T) {
	replies := frames(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1: %v", len(replies), replies)
	}

	result, _ := replies[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		// A list that is empty for some unrelated reason would satisfy the
		// assertion below while proving nothing.
		t.Fatal("the tool list is empty; this test is checking nothing")
	}

	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if name, _ := tool["name"].(string); name == "mcp_serve" {
			t.Errorf("mcp_serve is advertised as a tool, among %d", len(tools))
		}
	}
}

// TestCallingMCPServeAsAToolIsRefused is the control. `tools/list` describes
// this server; a peer is free to call a name it never saw there.
//
// The trailing ping is the point of the test rather than a flourish: the defect
// was the session ending, not the call failing. Note that this harness cannot
// reproduce the hang — with every frame already buffered, a nested server reads
// EOF immediately and returns tidily — which is exactly why the defect survived
// the suite it had. What is asserted is the pair of properties the defect makes
// impossible.
func TestCallingMCPServeAsAToolIsRefused(t *testing.T) {
	replies := frames(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"mcp_serve","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)

	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2 — the session did not survive: %v",
			len(replies), replies)
	}
	if _, isError := replies[0]["error"]; !isError {
		t.Errorf("calling mcp_serve was not refused: %v", replies[0])
	}
	if replies[1]["id"] != float64(2) {
		t.Errorf("the frame after the refusal was not answered: %v", replies[1])
	}
}

// TestAttachmentDownloadIsStillATool pins the distinction `OwnsStdout` and
// `OwnsStdoutWhen` are easy to collapse.
//
// `issue attachment download` sets the conditional one and is a working tool:
// with `output` naming a path it writes a file and returns a document. Only
// `output: "-"` wants the stream, and that is already refused by the command's
// own Validate, which finds `inv.Stdout` nil because a tool call has no stdout
// to hand out. Disqualifying the command would remove a working tool to prevent
// a call that already fails cleanly.
func TestAttachmentDownloadIsStillATool(t *testing.T) {
	replies := frames(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := replies[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)

	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if name, _ := tool["name"].(string); name == "issue_attachment_download" {
			return
		}
	}
	t.Error("issue_attachment_download is not advertised; OwnsStdoutWhen was " +
		"treated as OwnsStdout")
}
