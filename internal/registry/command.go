package registry

import (
	"context"
	"io"
	"slices"
	"strings"

	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
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
	// CollectionName is the container element a streaming command emits, e.g.
	// "issues", and Columns is its default TSV projection. They live on the
	// declaration rather than being returned with the rows, because the stream
	// has to be opened — and its header written — before the first page lands.
	CollectionName string
	Columns        []render.Column
	// NeedsJira marks a command that talks to the configured site. The CLI
	// layer builds a Session for it; a command without this never resolves a
	// credential and never probes the deployment.
	NeedsJira bool

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

	// Exactly one of Run and Stream is set. Stream is for a command that emits
	// a collection; Run is for one that emits a record.
	Run    RunFunc
	Stream StreamFunc
}

// Emitter reports whether this command streams its output.
func (c *Command) Streams() bool { return c.Stream != nil }

// RunFunc executes a command. It returns a result document; it never writes to
// stdout itself, so the render layer stays the only thing that decides what the
// output looks like.
type RunFunc func(ctx context.Context, inv *Invocation) (*render.Doc, error)

// StreamFunc executes a command that emits a collection incrementally.
//
// It writes rows to the stream as they arrive rather than returning them, so a
// long paged run produces output immediately instead of after its last request.
// The stream decides for itself whether that means writing through or
// buffering — TSV can stream, an envelope that carries a count cannot — so the
// command never branches on format.
//
// Close reports whether the result set was exhausted. Only the thing doing the
// paging knows, which is why it is the command that says.
type StreamFunc func(ctx context.Context, inv *Invocation, out *render.Stream) (StreamResult, error)

// StreamResult is what a streaming command reports once its rows are out.
type StreamResult struct {
	// Complete is true only if the result set was exhausted.
	Complete bool
	// NextPageToken resumes a truncated result set.
	NextPageToken string
}

// Progress reports how far a long operation has got.
//
// Implementations write to stderr and only when it is a terminal, so a piped or
// redirected run emits nothing at all — which is what keeps "stderr is always
// structured" true. It is an interface here so a resource can report progress
// without knowing where stderr is or whether anyone is watching.
type Progress interface {
	// Update reports rows so far, and the total if the server disclosed one.
	// A total of zero means unknown.
	Update(done, total int)
	// Done clears the report.
	Done()
}

// noProgress discards reports, so a command need never nil-check.
type noProgress struct{}

func (noProgress) Update(int, int) {}
func (noProgress) Done()           {}

// NoProgress is the reporter used when nobody is watching.
var NoProgress Progress = noProgress{}

// Session is how a command reaches Jira.
//
// It is an interface so a resource never learns where a site, a credential, or
// a context comes from — it asks for a connection and gets one. That is what
// lets a resource be tested against a recorded fixture with no auth, no config,
// and no network.
type Session interface {
	// Connect returns a client bound to the resolved site, probing the
	// deployment on first use and caching the answer.
	Connect(ctx context.Context) (*transport.Client, site.Info, error)
	// Project is the resolved default project. It may be empty: project is
	// never mandatory.
	Project() string
	// RequireProject returns the project or a usage error naming the flag, for
	// the few commands that genuinely cannot proceed without one.
	RequireProject() (string, error)
	// Board is the resolved default board, which may be empty.
	Board() string
	// CheckWritable refuses a mutation in read-only mode, before any network
	// call, so a blocked command costs nothing and cannot half-happen.
	CheckWritable(command string) error
}

// Invocation is everything a command needs from the caller.
type Invocation struct {
	// Jira reaches the configured site. It is nil for a command that does not
	// talk to Jira, so a resource that dereferences it without needing it
	// fails loudly in its own tests rather than quietly in production.
	Jira Session

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
	// Progress reports the scale of a long operation. It is never nil.
	Progress Progress
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
