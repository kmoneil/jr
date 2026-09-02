package registry

import (
	"context"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
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
	// DefaultsToAll makes --limit default to "all" instead of DefaultLimit.
	//
	// It is for a command whose result set is local, finite, and entirely
	// known before it runs — the command surface of this binary, not a query
	// against a server. DefaultLimit exists so an unbounded remote query does
	// not page forever, and that reasoning does not apply to a list the binary
	// already holds. A self-description that truncated at fifty would report
	// most of itself and exit 3, which is honest and useless.
	//
	// --limit still bounds it when asked, so the flag keeps meaning what it
	// says.
	DefaultsToAll bool
	// CollectionName is the container element a streaming command emits, e.g.
	// "issues", and Columns is its default TSV projection. They live on the
	// declaration rather than being returned with the rows, because the stream
	// has to be opened — and its header written — before the first page lands.
	CollectionName string
	Columns        []render.Column
	// ColumnsFor computes the columns for one invocation, for a command whose
	// output shape depends on its flags. It is consulted instead of Columns
	// when set; Columns remains the documented default that `jr schema`
	// reports.
	ColumnsFor func(inv *Invocation) []render.Column
	// EmptyFrame names the bounds this command computed a zero-row answer
	// over, as `key=value` notes, for the EMPTY_RESULT warning.
	//
	// The scope the command read is added for every command and does not
	// belong here. This is for the bounds only this command knows it applied:
	// the instant a relative --since resolved to, the account a --user name
	// resolved to. A bound the caller typed literally is worth reporting only
	// when the command turned it into something else, because the value that
	// misleads is the one nothing echoed back.
	//
	// It is consulted only when the collection came back empty and complete,
	// so it may cost work a populated result would not pay for. It must not
	// reach the network: everything it reports was resolved before the first
	// request went out.
	EmptyFrame func(inv *Invocation) []string
	// OwnsStdout marks a command whose output is a stream it writes itself —
	// a protocol server, not a result document.
	//
	// Such a command declares no output kind, and the CLI renders nothing after
	// it. Emitting a result alongside a protocol stream puts a frame on the
	// wire that the peer cannot parse, and the session dies rather than the
	// message being ignored.
	OwnsStdout bool
	// OwnsStdoutWhen makes that conditional, for a command that either writes
	// a result document or writes raw bytes depending on where it was told to
	// put them.
	//
	// `issue attachment download --output -` is the only case: a file on
	// stdout and a result document on stdout are the same channel, and one of
	// them has to lose. The document does, because the caller asked for the
	// file. Writing to a path emits the document as normal.
	OwnsStdoutWhen func(inv *Invocation) bool
	// ScopedBy names the global flags that reach this command's *result set*,
	// as opposed to the ones that change how it is printed or fetched.
	//
	// A global is declared once and inherited by every command, so nothing
	// about a command's own declaration says which of the thirteen decide what
	// its answer is. Before this existed, `jr schema issue.activity` reported
	// seven flags and `jr issue activity --help` bound eight, and the eighth
	// was --project, which silently narrowed the result set to the context's
	// project. An agent told to prefer the schema over --help — which is what
	// internal/cli/skillassets tells it — had no way to learn the flag was
	// there, and read `flags count="7"` as a count.
	//
	// It is declared rather than derived. A command that *could* read the
	// context scope is not one that does: `issue get ENG-1` resolves a session,
	// takes its key from the argument, and asks the context for nothing.
	// TestScopedByMatchesWhatTheCommandReads holds each declaration to what the
	// command actually asks the session for, in both directions, so a
	// declaration that is wrong fails rather than misinforming.
	ScopedBy []string
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

	// Validate checks an invocation before any output begins.
	//
	// It exists because a streaming command opens its stream — and writes its
	// header — before it runs, so a flag it would have rejected has to be
	// caught earlier or the rejection arrives after bytes are already out. It
	// is also where a command puts a check the generic flag machinery cannot
	// express.
	//
	// It takes a context because checking a flag can require asking the server:
	// resolving --field "Story Points" against the catalogue is the difference
	// between exiting 2 with the near misses and letting Jira 400 opaquely.
	// Whatever it resolves belongs on the invocation, via SetValue — Columns
	// are computed after this and cannot fail.
	Validate func(ctx context.Context, inv *Invocation) error

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
	// PartialElement names a container inside a row that was clipped, for the
	// case where every row arrived and something within one of them did not.
	//
	// A buffered document can be inspected for this; a streamed one cannot,
	// because its rows reached stdout a page at a time and are gone by the
	// time the warning is built. So the command that knew says so, and the
	// warning names the element rather than offering `--limit all`, which
	// would not fetch a single further comment.
	PartialElement string
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
	//
	// The total is the size of the set the server had, not the number of rows
	// this run will write. Passing the count twice makes the ratio one from the
	// first report onward, which says the set is exhausted at the moment
	// `--limit` has just cut it short and exit 3 is about to say otherwise. So
	// a command that clips with Bound reports the length it clipped *from*, and
	// a command whose bound reached the server as maxResults passes zero,
	// because it genuinely does not know.
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
	// Metadata returns the site's descriptive data — the field catalogue, and
	// in time the issue types and transitions — cached on disk with a TTL.
	//
	// It is on the session rather than fetched by each resource so that the
	// cache is shared: two commands resolving the same custom field name in the
	// same day make one request between them, not two.
	Metadata(ctx context.Context) (*site.Metadata, error)
	// Project is the resolved default project. It may be empty: project is
	// never mandatory.
	Project() string
	// RequireProject returns the project or a usage error naming the flag, for
	// the few commands that genuinely cannot proceed without one.
	RequireProject() (string, error)
	// Board is the resolved default board, which may be empty.
	Board() string
	// Fields is the context's default field set: the custom fields this caller
	// wants on every issue without naming them each time. Ids or names, in the
	// order the context lists them, and possibly empty.
	//
	// A command adds these to whatever --field asked for rather than choosing
	// between them. Choosing would mean one ad-hoc --field silently dropped the
	// set, which is the shape of the bug that produced the rule that a flag
	// either affects the output or does not exist.
	//
	// This method is why the card existed: the value was resolved, stored, and
	// printed by `context show`, and no resource could reach it.
	Fields() []string
	// RequireBoard returns the board or a usage error naming the flag, for the
	// agile commands that have nothing to list without one.
	RequireBoard() (string, error)
	// CheckWritable refuses a mutation in read-only mode, before any network
	// call, so a blocked command costs nothing and cannot half-happen.
	CheckWritable(command string) error
	// Idempotency returns the ledger of what a mutating request already did.
	//
	// It may be nil, which means no protection — a build or an environment with
	// no state directory. A caller finds out by the flag having no effect
	// rather than by a silent duplicate.
	Idempotency() *idem.Ledger
	// Site is the Jira this session resolves to, including any context path,
	// and empty when no site can be resolved at all.
	//
	// It is on the interface rather than read by type assertion because a
	// session that cannot say which Jira it points at is a session nobody
	// should be able to write. The document envelope carries this, so an answer
	// stored in a file still names the instance that produced it.
	//
	// Resolving it costs nothing and reaches no network: it is the value
	// Connect would dial, available before Connect is called and unchanged by
	// it.
	Site() string
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
	// Stderr is where a command may emit structured diagnostics.
	Stderr io.Writer
	// Stdout is the raw byte channel, and exists for exactly one shape of
	// command: one whose output is a file rather than a document.
	//
	// It is nil wherever stdout is not free to be written — inside
	// `mcp serve`, bytes here would land on the JSON-RPC stream as a frame the
	// peer cannot parse, which is a bug this project has already shipped once.
	// A command that needs it and finds it nil refuses rather than writing
	// somewhere else.
	//
	// Everything else writes a *render.Doc and lets the CLI encode it. If a
	// second command ever reaches for this, that is the moment to ask why.
	Stdout io.Writer
	// Progress reports the scale of a long operation. It is never nil.
	Progress Progress

	// values carries what Validate worked out into the rest of the invocation.
	//
	// It exists because Columns are computed between Validate and the command
	// body, by a function with no context and no way to fail. Anything that
	// took a request to establish — a field name resolved to its id — has to be
	// resolved once in Validate and left here, or it would be resolved twice
	// with the second attempt unable to report the failure.
	values map[string]any
}

// SetValue records something Validate resolved, for the rest of the invocation
// to read.
func (i *Invocation) SetValue(key string, v any) {
	if i.values == nil {
		i.values = map[string]any{}
	}
	i.values[key] = v
}

// Value returns what SetValue recorded, or nil.
func (i *Invocation) Value(key string) any { return i.values[key] }

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

// Emits reports whether this command produces a result document at all. A
// command that owns stdout does not.
func (c *Command) EmitsDocument() bool { return !c.OwnsStdout }

// EmitsDocumentFor answers the same question for one invocation, which is the
// form the CLI needs: a command may own stdout only for some of its arguments.
func (c *Command) EmitsDocumentFor(inv *Invocation) bool {
	if c.OwnsStdout {
		return false
	}
	if c.OwnsStdoutWhen != nil && c.OwnsStdoutWhen(inv) {
		return false
	}
	return true
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

// Flag returns the named flag this command accepts, declared or implied.
func (c *Command) Flag(name string) (Flag, bool) {
	for _, f := range c.AllFlags() {
		if f.Name == name {
			return f, true
		}
	}
	return Flag{}, false
}

// AllFlags returns the flags this command accepts: the declared ones, plus the
// ones a declaration implies. Paginated implies --limit.
//
// It exists because --limit used to be synthesised from Paginated at each
// consumer that remembered to. The cobra binder and the MCP input schema did;
// `jr schema` and the command reference it generates did not, so nine commands
// documented --page-size and --page-token and denied the existence of the flag
// that decides whether a result set is complete. The MCP copy also read
// DefaultLimit directly and never DefaultsToAll, so `jr schema` over MCP
// answered with fifty of sixty-two commands and complete="false", which is the
// exact failure DefaultsToAll was added to prevent, reintroduced by a second
// copy of the default.
//
// One derivation, four consumers. A flag nobody writes down twice cannot be
// written down differently.
func (c *Command) AllFlags() []Flag {
	if !c.Paginated {
		return c.Flags
	}
	return append(slices.Clip(c.Flags), c.LimitFlag())
}

// LimitFlag is the --limit flag every paginated command carries.
//
// It is derived rather than declared because its default depends on
// DefaultsToAll, which is a property of the command rather than of the flag.
func (c *Command) LimitFlag() Flag {
	def := strconv.Itoa(DefaultLimit)
	if c.DefaultsToAll {
		def = "all"
	}
	return Flag{
		Name:    "limit",
		Type:    TypeString,
		Default: def,
		Usage:   `maximum results, or "all" to exhaust the result set`,
	}
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

// Kinds every mutating command shares.
const (
	// KindDryRun is what --dry-run emits: the requests that would have been
	// sent, not a paraphrase of them.
	KindDryRun = "dry-run"
	// VersionDryRun is its schema version.
	//
	// v2 wraps the request in a `requests` list. v1 was a single `request`
	// record, which was true of every mutating command until `epic add` on
	// Cloud became a PUT of the parent field per issue: the batched agile
	// endpoint it used to call serves company-managed projects only. A preview
	// that showed one of three requests would be a paraphrase of the run, which
	// is the one thing --dry-run promises not to be.
	//
	// The list is always a list, including for the eighteen commands that send
	// exactly one. A shape that varies with the count is a shape every consumer
	// has to branch on, and the branch is the part that goes wrong.
	VersionDryRun = 2
)

// DryRunOutput is the declaration every mutating command adds, so the shape
// --dry-run produces is described in one place rather than copied per verb.
func DryRunOutput() Output {
	return Output{Kind: KindDryRun, Version: VersionDryRun, When: "--dry-run is given"}
}

// DryRunDoc renders the request a command would have sent.
//
// It is the request itself — method, path, query, and body — because §4.1 says
// --dry-run prints the exact request. A paraphrase is a second implementation
// of the thing being previewed, and the two drift; this cannot, because it
// takes the same transport.Request the command was about to hand to the client.
//
// The Authorization header is not here and cannot be: this renders the request
// as the command built it, before the transport attaches a credential.
func DryRunDoc(command string, requests ...transport.Request) *render.Doc {
	items := make([]*render.Node, 0, len(requests))
	for _, r := range requests {
		items = append(items, requestNode(command, r))
	}
	return render.Record(KindDryRun, VersionDryRun,
		render.ListEl("requests", "request", items...))
}

// requestNode renders one request exactly as the command built it.
func requestNode(command string, r transport.Request) *render.Node {
	n := render.El("request").
		Attr("command", command).
		Attr("method", r.Method).
		Attr("path", r.Path)

	if len(r.Query) > 0 {
		params := make([]*render.Node, 0, len(r.Query))
		for _, key := range slices.Sorted(maps.Keys(r.Query)) {
			for _, value := range r.Query[key] {
				params = append(params, render.El("param").
					Attr("name", key).SetText(value))
			}
		}
		n.Child(render.ListEl("query", "param", params...))
	}

	if len(r.Body) > 0 {
		// CDATA, because a JSON body is full of quotes and braces and an
		// escaped one is not something a person can read back or paste into
		// curl — which is most of what a dry run is for.
		n.Child(render.El("body").
			Attr("content-type", "application/json").
			SetCDATA(string(r.Body)))
	}
	return n
}
