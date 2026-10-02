package cli

import (
	"github.com/spf13/cobra"

	"github.com/kmoneil/jr/internal/registry"
)

// bindFlags declares a command's flags on its cobra command and returns a
// function that harvests the parsed values back into a registry.Flags.
//
// The registry is the only place a flag is described, and since 2026-10-02 it
// is also the only place a declaration becomes a pflag set: this hands cobra's
// flag set to registry.DeclareFlags, which is what registry.ParseArgv uses for
// an argv that never reached a shell. Nothing here invents a flag, and no
// command reads pflag directly, so `jr schema` and `--help` cannot disagree
// about what exists, and a step of a sequence cannot parse differently from
// the command line it was written as.
func bindFlags(cc *cobra.Command, rc *registry.Command) func(*cobra.Command) registry.Flags {
	registry.DeclareFlags(cc.Flags(), rc)
	return func(cmd *cobra.Command) registry.Flags {
		return registry.HarvestFlags(cmd.Flags(), rc)
	}
}

// validateFlags rejects an invocation that pflag accepted but the registry does
// not describe: a missing required flag, or an enum value outside its declared
// set. registry.CheckFlags is the rule, shared with registry.ParseArgv.
func validateFlags(cmd *cobra.Command, rc *registry.Command) error {
	return registry.CheckFlags(cmd.Flags(), rc)
}
