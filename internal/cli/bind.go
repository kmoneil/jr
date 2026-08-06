package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
)

// bindFlags declares a command's flags on its cobra command and returns a
// function that harvests the parsed values back into a registry.Flags.
//
// The registry is the only place a flag is described. Nothing here invents a
// flag, and no command reads pflag directly, so `jr schema` and `--help` cannot
// disagree about what exists.
func bindFlags(cc *cobra.Command, rc *registry.Command) func(*cobra.Command) registry.Flags {
	fs := cc.Flags()

	for _, f := range rc.Flags {
		usage := f.Usage
		if len(f.Enum) > 0 {
			usage = fmt.Sprintf("%s (one of: %s)", usage, strings.Join(f.Enum, ", "))
		}
		if f.Required {
			usage += " (required)"
		}
		switch f.Type {
		case registry.TypeBool:
			fs.BoolP(f.Name, f.Short, f.Default == "true", usage)
		case registry.TypeInt:
			fs.IntP(f.Name, f.Short, atoiOrZero(f.Default), usage)
		default:
			if f.Repeatable {
				fs.StringArrayP(f.Name, f.Short, nil, usage)
			} else {
				fs.StringP(f.Name, f.Short, f.Default, usage)
			}
		}
	}

	if rc.Paginated {
		// --limit is a user intent, decoupled from the API page size. There is
		// deliberately no offset flag: the upstream API is cursor-based.
		def := fmt.Sprint(registry.DefaultLimit)
		if rc.DefaultsToAll {
			def = "all"
		}
		fs.String("limit", def,
			`maximum results, or "all" to exhaust the result set`)
	}

	return func(cmd *cobra.Command) registry.Flags {
		return harvest(cmd, rc)
	}
}

func harvest(cmd *cobra.Command, rc *registry.Command) registry.Flags {
	out := registry.NewFlags()
	fs := cmd.Flags()
	for _, f := range rc.Flags {
		switch f.Type {
		case registry.TypeBool:
			v, _ := fs.GetBool(f.Name)
			out.SetBool(f.Name, v)
		case registry.TypeInt:
			v, _ := fs.GetInt(f.Name)
			out.SetInt(f.Name, v)
		default:
			if f.Repeatable {
				vs, _ := fs.GetStringArray(f.Name)
				for _, v := range vs {
					out.SetString(f.Name, v)
				}
				continue
			}
			v, _ := fs.GetString(f.Name)
			out.SetString(f.Name, v)
		}
	}
	return out
}

// validateFlags rejects an invocation that pflag accepted but the registry does
// not describe: a missing required flag, or an enum value outside its declared
// set. Both are usage errors that name the alternatives, never a silent
// fallback to a default.
//
// Required flags are checked here rather than through cobra's own marking so
// the failure is a structured error with a code, like every other failure.
func validateFlags(cmd *cobra.Command, rc *registry.Command) error {
	if err := validateRequired(cmd, rc); err != nil {
		return err
	}
	return validateEnums(cmd, rc)
}

func validateRequired(cmd *cobra.Command, rc *registry.Command) error {
	var missing []string
	for _, f := range rc.Flags {
		if f.Required && !cmd.Flags().Changed(f.Name) {
			missing = append(missing, "--"+f.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return errs.Usage("MISSING_REQUIRED_FLAG",
		"%s requires %s", rc.UseLine(), strings.Join(missing, " and ")).
		WithRemedy("pass %s", strings.Join(missing, " "))
}

func validateEnums(cmd *cobra.Command, rc *registry.Command) error {
	var firstErr error
	cmd.Flags().Visit(func(pf *pflag.Flag) {
		if firstErr != nil {
			return
		}
		f, ok := rc.Flag(pf.Name)
		if !ok || f.Type != registry.TypeEnum || len(f.Enum) == 0 {
			return
		}
		got := pf.Value.String()
		if slices.Contains(f.Enum, got) {
			return
		}
		firstErr = errs.Usage("INVALID_FLAG_VALUE",
			"--%s does not accept %q", f.Name, got).
			WithDetail("valid values: %s", strings.Join(f.Enum, ", ")).
			WithRemedy("pass --%s with one of the listed values", f.Name)
	})
	return firstErr
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
