package cli

import (
	"github.com/kmoneil/jr/internal/registry"
)

// gate refuses a command whose declaration says it may not run here.
//
// The rule itself is registry.Gate, so this layer and `mcp serve` cash the same
// guarantee from the same code. It stays a method for the one thing that is
// genuinely this layer's: the point in runLeaf where it is called, before
// Validate and before any network call.
func (a *app) gate(rc *registry.Command, inv *registry.Invocation) error {
	return registry.Gate(rc, inv)
}
