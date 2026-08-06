package cli

import (
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
)

// gate refuses a command that declares itself mutating or destructive when the
// conditions for it are not met.
//
// It runs in the CLI layer, driven by the declaration, rather than in each
// command. A resource author who forgot the check would ship a verb that
// ignores read-only mode, and the guarantee would be worth exactly as much as
// the least careful command. Here it comes from `Mutating: true`, which the
// contract test already requires be accurate.
//
// Both refusals happen before Validate and before any network call, so a
// blocked command costs nothing and cannot half-happen.
func (a *app) gate(rc *registry.Command, inv *registry.Invocation) error {
	// A preview is not the thing being confirmed. --yes exists to confirm an
	// irreversible action, and refusing to *show* the request until it has been
	// confirmed inverts the order a caller works in: you look at what would
	// happen in order to decide whether to allow it.
	if rc.Destructive && !inv.Flags.Bool("dry-run") && !inv.Flags.Bool("yes") {
		return errs.Blocked("CONFIRMATION_REQUIRED",
			"%s needs confirmation", rc.UseLine()).
			WithRemedy("pass --yes, or --dry-run to see the request first")
	}

	if !rc.Mutating {
		return nil
	}

	// Read-only is *not* relaxed for a dry run, and the asymmetry is deliberate.
	// A missing --yes is a step the caller has not taken yet; a read-only
	// context is a statement about what that context is for. The latch stays
	// one-way, and a caller who wants to plan a change uses a context that
	// permits one.
	if inv.Jira == nil {
		// A mutating command with no session cannot reach Jira anyway, and
		// failing here would hide the real problem behind a policy message.
		return nil
	}
	return inv.Jira.CheckWritable(rc.UseLine())
}
