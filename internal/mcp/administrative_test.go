//go:build mcp

package mcp_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"

	// Every command the binary ships, because the claim is about the real
	// surface rather than about a registry assembled for the test.
	_ "github.com/kmoneil/jr/internal/commands"
)

// TestTheAdministrativeSurfaceIsNotOnTheWire is the first slice of the policy
// work, and it is worth having on its own.
//
// A peer that can call `context_edit` can point this server at another site;
// one that can call `auth_login` can replace its credential; one that can call
// `auth_token` simply reads the credential out. Any limit placed on such a peer
// is a default rather than a boundary, and moving the server to another
// container does not help, because the surface is on the wire and not in the
// filesystem.
//
// Driven through the real registry and the real session, because the assertion
// is about what this server *offers*. A unit test over `Tools()` would pass
// against a build whose dispatch had drifted.
func TestTheAdministrativeSurfaceIsNotOnTheWire(t *testing.T) {
	reg := cli.Registry()

	replies := session(t, reg, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := replies[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("the server advertised no tools at all; this asserts nothing")
	}

	names := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		names[name] = true
	}

	// The writers, which change what this server is, and the one command whose
	// output is the secret.
	for _, gone := range []string{
		"auth_login", "auth_logout", "auth_token",
		"context_create", "context_delete", "context_edit", "context_use",
	} {
		if names[gone] {
			t.Errorf("%s is advertised as a tool", gone)
		}
	}

	// The readers stay. Without this the test above would pass against a server
	// that had hidden everything, and an agent that cannot tell which site it
	// is talking to is one that guesses.
	for _, kept := range []string{"auth_status", "context_list", "context_show"} {
		if !names[kept] {
			t.Errorf("%s was removed; it reports and reveals nothing", kept)
		}
	}
	// And the ordinary surface is untouched.
	if !names["issue_list"] {
		t.Error("issue_list is missing; the exclusion is too broad")
	}
}

// TestCallingAnAdministrativeToolIsRefused is the control rather than the
// description: leaving a name out of the list says what this server is, and a
// peer can call a name that was never advertised.
//
// Each refusal has to say *why*, because the two classes need different things
// from whoever hits them: `auth_token` is never coming back on this interface,
// and `context_edit` is done with the CLI before the server starts.
func TestCallingAnAdministrativeToolIsRefused(t *testing.T) {
	reg := cli.Registry()

	for _, tc := range []struct{ tool, because string }{
		{"auth_token", "prints the credential"},
		{"auth_login", "changes which site and credential"},
		{"context_edit", "changes which site and credential"},
		{"context_use", "changes which site and credential"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			replies := session(t, reg,
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
					`{"name":"`+tc.tool+`","arguments":{}}}`,
				// A frame after it, so a refusal that killed the session would
				// fail here rather than passing quietly.
				`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

			if len(replies) < 2 {
				t.Fatalf("the session did not survive the refusal: %v", replies)
			}
			rpcErr, _ := replies[0]["error"].(map[string]any)
			if rpcErr == nil {
				t.Fatalf("%s was not refused: %v", tc.tool, replies[0])
			}
			msg, _ := rpcErr["message"].(string)
			if !strings.Contains(msg, tc.because) {
				t.Errorf("the refusal does not say why: %q", msg)
			}
		})
	}
}
