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
	// Confirmation first. A destructive command in read-only mode should say
	// the more specific thing — that it needs confirmation it will never get —
	// only after read-only has had its say, so the order below is deliberate:
	// read-only is the broader refusal and comes second precisely because a
	// missing --yes is the easier thing to fix.
	if rc.Destructive && !inv.Flags.Bool("yes") {
		return errs.Blocked("CONFIRMATION_REQUIRED",
			"%s needs confirmation", rc.UseLine()).
			WithRemedy("pass --yes")
	}

	if !rc.Mutating {
		return nil
	}
	if inv.Jira == nil {
		// A mutating command with no session cannot reach Jira anyway, and
		// failing here would hide the real problem behind a policy message.
		return nil
	}
	return inv.Jira.CheckWritable(rc.UseLine())
}
