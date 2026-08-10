package registry

import (
	"github.com/kmoneil/jira-cli/internal/errs"
)

// Gate refuses a command that declares itself mutating or destructive when the
// conditions for it are not met.
//
// It is driven by the declaration rather than written into each command. A
// resource author who forgot the check would ship a verb that ignores read-only
// mode, and the guarantee would be worth exactly as much as the least careful
// command. Here it comes from `Mutating: true`, which the contract test already
// requires be accurate.
//
// # Why it lives in registry and not in the caller
//
// It used to be a method on the CLI's app, called from `runLeaf` and from
// nowhere else. Everything it reads is a registry type — Destructive, Mutating,
// Flags, Session — so there was nothing CLI-specific about it; it was there
// because that was where the only caller was when it was written.
//
// Then a second caller appeared. `internal/mcp` binds arguments and calls
// `Command.Run` directly, so every declaration-driven refusal in the CLI wrapper
// was absent on the interface §6 of the spec exists for: a context created
// `--readonly` sent a real DELETE, and a destructive command ran with no
// confirmation. Both were verified against a server before this moved.
//
// So the rule is the one the paragraph above already states about resources,
// with *caller* substituted for *resource author*. A guarantee enforced by a
// caller is a guarantee each caller has to remember, and the answer is to put
// the check beside the declaration that drives it.
//
// Both refusals happen before Validate and before any network call, so a
// blocked command costs nothing and cannot half-happen. A caller that runs this
// after Validate has weakened it: Validate is where a streaming command resolves
// fields, which takes requests.
func Gate(cmd *Command, inv *Invocation) error {
	if cmd == nil || inv == nil {
		return errs.Runtime("NO_COMMAND", "nothing to gate")
	}

	// A preview is not the thing being confirmed. --yes exists to confirm an
	// irreversible action, and refusing to *show* the request until it has been
	// confirmed inverts the order a caller works in: you look at what would
	// happen in order to decide whether to allow it.
	if cmd.Destructive && !inv.Flags.Bool("dry-run") && !inv.Flags.Bool("yes") {
		return errs.Blocked("CONFIRMATION_REQUIRED",
			"%s needs confirmation", cmd.UseLine()).
			WithRemedy("pass --yes, or --dry-run to see the request first")
	}

	if !cmd.Mutating {
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
	return inv.Jira.CheckWritable(cmd.UseLine())
}
