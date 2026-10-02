package registry

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
)

// This file is the one mapping from a declaration to a parser.
//
// The command line binds through it, by handing it the flag set cobra parses,
// and ParseArgv uses it on an argv that never reached a shell: a step of a
// sequence, written as the command it would have been. Both read the same
// declaration through the same pflag calls, so an argv means the same thing
// however it arrives. A second hand-written parser would be the thing this
// exists not to have, because it is the place a quoting rule, a repeated flag
// or a short name would come to mean something different.

// DeclareFlags puts a command's flags on a pflag set: its own and, when it
// pages, --limit. The global flags are not here; they belong to the root.
func DeclareFlags(fs *pflag.FlagSet, c *Command) {
	for _, f := range c.AllFlags() {
		usage := f.Usage
		if len(f.Enum) > 0 {
			usage = fmt.Sprintf("%s (one of: %s)", usage, strings.Join(f.Enum, ", "))
		}
		if f.Required {
			usage += " (required)"
		}
		switch f.Type {
		case TypeBool:
			fs.BoolP(f.Name, f.Short, f.Default == "true", usage)
		case TypeInt:
			fs.IntP(f.Name, f.Short, atoiOrZero(f.Default), usage)
		default:
			if f.Repeatable {
				fs.StringArrayP(f.Name, f.Short, nil, usage)
			} else {
				fs.StringP(f.Name, f.Short, f.Default, usage)
			}
		}
	}
}

// HarvestFlags reads a parsed flag set back into Flags.
//
// --limit is a user intent, decoupled from the API page size, and it comes
// from AllFlags like every other flag. There is deliberately no offset flag:
// the upstream API is cursor-based.
func HarvestFlags(fs *pflag.FlagSet, c *Command) Flags {
	out := NewFlags()
	for _, f := range c.AllFlags() {
		switch f.Type {
		case TypeBool:
			v, _ := fs.GetBool(f.Name)
			out.SetBool(f.Name, v)
		case TypeInt:
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
		if !fs.Changed(f.Name) {
			// The value above is the declaration's, not the caller's. Said
			// after the write rather than instead of it: every reader still
			// gets the effective value, and only a reader that asks whether
			// the caller typed it (Flags.WasSet) sees the difference.
			out.MarkDefault(f.Name)
		}
	}
	return out
}

// CheckFlags rejects what pflag accepted and the declaration does not
// describe: a missing required flag, or an enum value outside its declared
// set. Both are usage errors that name the alternatives, never a silent
// fallback to a default.
//
// Required flags are checked here rather than through cobra's own marking so
// the failure is a structured error with a code, like every other failure.
func CheckFlags(fs *pflag.FlagSet, c *Command) error {
	if err := checkRequired(fs, c); err != nil {
		return err
	}
	return checkEnums(fs, c)
}

func checkRequired(fs *pflag.FlagSet, c *Command) error {
	var missing []string
	for _, f := range c.AllFlags() {
		if f.Required && !fs.Changed(f.Name) {
			missing = append(missing, "--"+f.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return errs.Usage("MISSING_REQUIRED_FLAG",
		"%s requires %s", c.UseLine(), strings.Join(missing, " and ")).
		WithRemedy("pass %s", strings.Join(missing, " "))
}

// checkEnums holds every enum flag to its declared set.
//
// A repeatable enum is checked one value at a time. pflag renders a repeated
// flag as "[a b]", so comparing the whole rendering against the set refused
// every repeated value including the legal ones. A repeatable enum was then a
// shape the registry could declare and the binder could not honor.
func checkEnums(fs *pflag.FlagSet, c *Command) error {
	var firstErr error
	fs.Visit(func(pf *pflag.Flag) {
		if firstErr != nil {
			return
		}
		f, ok := c.Flag(pf.Name)
		if !ok || f.Type != TypeEnum || len(f.Enum) == 0 {
			return
		}
		for _, got := range enumValues(fs, f) {
			if slices.Contains(f.Enum, got) {
				continue
			}
			firstErr = errs.Usage("INVALID_FLAG_VALUE",
				"--%s does not accept %q", f.Name, got).
				WithDetail("valid values: %s", strings.Join(f.Enum, ", ")).
				WithRemedy("pass --%s with one of the listed values", f.Name)
			return
		}
	})
	return firstErr
}

// enumValues returns what a flag was actually given, as one value or several.
func enumValues(fs *pflag.FlagSet, f Flag) []string {
	if !f.Repeatable {
		v, err := fs.GetString(f.Name)
		if err != nil {
			return nil
		}
		return []string{v}
	}
	v, err := fs.GetStringSlice(f.Name)
	if err != nil {
		return nil
	}
	return v
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

// CheckArity holds a call to the argument count the command declared.
func (c *Command) CheckArity(args []string) error {
	minArgs, maxArgs := c.ArgBounds()
	switch {
	case len(args) < minArgs:
		return c.usageError("%s requires %s", c.UseLine(), describeArity(minArgs, maxArgs)).
			WithDetail("got %d positional argument(s)", len(args))
	case maxArgs >= 0 && len(args) > maxArgs:
		return c.usageError("%s accepts %s", c.UseLine(), describeArity(minArgs, maxArgs)).
			WithDetail("got %d positional argument(s): %s", len(args), strings.Join(args, " "))
	}
	return nil
}

// usageError is INVALID_USAGE, pointing at this command's help.
func (c *Command) usageError(format string, args ...any) *errs.Error {
	return errs.Usage("INVALID_USAGE", format, args...).
		WithRemedy("run `%s %s --help`", buildinfo.App, c.UseLine())
}

// describeArity renders an argument count for an error message.
func describeArity(minArgs, maxArgs int) string {
	switch {
	case maxArgs < 0 && minArgs == 0:
		return "any number of arguments"
	case maxArgs < 0:
		return fmt.Sprintf("at least %d argument(s)", minArgs)
	case minArgs == maxArgs:
		return fmt.Sprintf("exactly %d argument(s)", minArgs)
	case minArgs == 0:
		return fmt.Sprintf("at most %d argument(s)", maxArgs)
	default:
		return fmt.Sprintf("between %d and %d arguments", minArgs, maxArgs)
	}
}

// ParseArgv reads one command's own argv, the part after its path, the way
// the command line reads it: the same declaration through the same pflag
// calls, then the same required, enum and arity checks. It returns the flags
// and the positional arguments.
//
// The global flags are not declared, so an argv carrying one is refused as an
// unknown flag. That is deliberate for its one caller, a step of a sequence,
// which runs in the context and against the site of the sequence around it.
func (c *Command) ParseArgv(argv []string) (Flags, []string, error) {
	fs := pflag.NewFlagSet(c.UseLine(), pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	DeclareFlags(fs, c)
	if err := fs.Parse(argv); err != nil {
		return Flags{}, nil, c.usageError("%s", err.Error())
	}
	if err := CheckFlags(fs, c); err != nil {
		return Flags{}, nil, err
	}
	args := fs.Args()
	if err := c.CheckArity(args); err != nil {
		return Flags{}, nil, err
	}
	return HarvestFlags(fs, c), args, nil
}
