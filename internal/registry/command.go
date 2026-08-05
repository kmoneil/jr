package registry

import (
	"context"
	"io"
	"slices"
	"strings"

	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/render"
)

// FlagType is the declared type of a flag, used for the cobra binding, the MCP
// tool schema, and `jr schema` alike.
type FlagType string

// The supported flag types.
const (
	TypeString   FlagType = "string"
	TypeInt      FlagType = "int"
	TypeBool     FlagType = "bool"
	TypeEnum     FlagType = "enum"
	TypeDuration FlagType = "duration"
)

// Flag is one command-line option.
//
// Long flags are canonical. A short flag exists only for the highest-frequency
// options, is never reused with a different meaning on another command, and its
// letter must appear in its own name — a rule the registry test enforces.
type Flag struct {
	Name       string
	Short      string
	Type       FlagType
	Enum       []string
	Default    string
	Repeatable bool
	Required   bool
	Usage      string
}

// Output is one payload shape a command can emit: a stable kind, the schema
// version of that kind, and the condition under which it is chosen.
type Output struct {
	Kind    string
	Version int
	// When describes what selects this shape, e.g. "a command name is given".
	// It is empty for a command with only one output.
	When string
}

// Arg is one positional argument. Required args come first, and only the last
// arg may be variadic — the registry test enforces both.
type Arg struct {
	Name     string
	Usage    string
	Required bool
	Variadic bool
}

// Command is the single description of one command, from which the cobra tree,
// the MCP tool list, and `jr schema` are all generated. They cannot drift
// because there is only one of them.
type Command struct {
	// Path is the noun-verb address, e.g. ["issue", "list"].
	Path []string

	Summary     string
	Description string
	Example     string
	Flags       []Flag
	// Args describes the positional arguments, in order.
	Args []Arg

	// Mutating marks a command that changes state in Jira. Every mutating
	// command accepts --dry-run, requires the write tag, and is refused by
	// read-only mode.
	Mutating bool
	// LocalState marks a command that writes local config or credentials.
	//
	// It is deliberately not Mutating. Read-only mode and the write tag are
	// about Jira; a build that could not create a context or store a
	// credential would have no way to configure itself, which would make the
	// reader profile useless rather than safe.
	LocalState bool
	// Destructive marks a command that removes or closes something, in Jira or
	// locally. It requires --yes in every build.
	Destructive bool
	// Paginated marks a command that returns a collection the caller can
	// bound with --limit.
	Paginated bool

	// Outputs lists every payload shape this command can emit. The first is
	// the one it emits by default. A command that emits a kind it did not
	// declare is a bug the CLI layer rejects, because a consumer dispatching
	// on the declared kind would silently mis-parse it.
	Outputs []Output

	// ExitCodes lists every status this command can exit with, beyond the
	// universal 0/1/2.
	ExitCodes []exitcode.Code

	// RequiresTags names the build tags this command needs. A command whose
	// implementation depends on a tagged package without declaring the tag
	// fails the registry test.
	RequiresTags []string

	Run RunFunc
}

// RunFunc executes a command. It returns a result document; it never writes to
// stdout itself, so the render layer stays the only thing that decides what the
// output looks like.
type RunFunc func(ctx context.Context, inv *Invocation) (*render.Doc, error)

// Invocation is everything a command needs from the caller.
type Invocation struct {
	// Args are the positional arguments, after flag parsing.
	Args []string
	// Flags holds the parsed flag values.
	Flags Flags
	// Limit is the caller's requested result bound.
	Limit Limit
	// Format is the resolved output format, for commands that need to know
	// (almost none should).
	Format render.Format
	// Stderr is where a command may emit structured diagnostics. Nothing a
	// command writes ever reaches stdout.
	Stderr io.Writer
}

// Name returns the dotted command name, e.g. "issue.list". It is also the
// default output kind and the MCP tool name.
func (c *Command) Name() string { return strings.Join(c.Path, ".") }

// UseLine returns the space-separated invocation, e.g. "issue list".
func (c *Command) UseLine() string { return strings.Join(c.Path, " ") }

// Kind returns the command's default output kind, defaulting to the dotted
// command name when the command declares none.
func (c *Command) Kind() string {
	if len(c.Outputs) > 0 {
		return c.Outputs[0].Kind
	}
	return c.Name()
}

// KindVersion returns the schema version of the command's default output kind.
func (c *Command) KindVersion() int {
	if len(c.Outputs) > 0 {
		return c.Outputs[0].Version
	}
	return 0
}

// Emits reports whether the command declared the given output kind at the
// given schema version.
func (c *Command) Emits(kind string, version int) bool {
	for _, o := range c.Outputs {
		if o.Kind == kind && o.Version == version {
			return true
		}
	}
	return false
}

// Flag returns the named flag declared by this command.
func (c *Command) Flag(name string) (Flag, bool) {
	for _, f := range c.Flags {
		if f.Name == name {
			return f, true
		}
	}
	return Flag{}, false
}

// AllExitCodes returns the declared exit codes plus the universal ones, in
// numeric order and without duplicates.
func (c *Command) AllExitCodes() []exitcode.Code {
	out := []exitcode.Code{exitcode.OK, exitcode.Error, exitcode.Usage}
	out = append(out, c.ExitCodes...)
	slices.Sort(out)
	return slices.Compact(out)
}

// ArgBounds returns the minimum and maximum number of positional arguments.
// A maximum of -1 means unbounded.
func (c *Command) ArgBounds() (minArgs, maxArgs int) {
	for _, a := range c.Args {
		if a.Variadic {
			if a.Required {
				minArgs++
			}
			return minArgs, -1
		}
		if a.Required {
			minArgs++
		}
		maxArgs++
	}
	return minArgs, maxArgs
}

// ArgSpec renders the positional arguments for a usage line, e.g.
// "<key> [comment]".
func (c *Command) ArgSpec() string {
	parts := make([]string, 0, len(c.Args))
	for _, a := range c.Args {
		name := a.Name
		if a.Variadic {
			name += "..."
		}
		if a.Required {
			parts = append(parts, "<"+name+">")
		} else {
			parts = append(parts, "["+name+"]")
		}
	}
	return strings.Join(parts, " ")
}

// Available reports whether every tag this command requires was compiled in.
// A command that is not available is not registered at all, so this is a
// belt-and-braces check for the registry test rather than a runtime gate.
func (c *Command) Available(has func(string) bool) bool {
	for _, t := range c.RequiresTags {
		if !has(t) {
			return false
		}
	}
	return true
}
