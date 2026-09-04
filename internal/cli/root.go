package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/nearest"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// groupSummaries describes the intermediate nouns in the command tree. A noun
// with no entry gets a generated summary rather than an empty one.
var groupSummaries = map[string]string{
	"auth":    "Authenticate against a Jira site",
	"context": "Manage named site/project contexts",
	"issue":   "Work with issues",
	"epic":    "Work with epics",
	"sprint":  "Work with sprints",
	"board":   "Work with boards",
	"project": "Inspect projects",
	"user":    "Look up users",
	"field":   "Discover fields, including custom fields",
	"jql":     "Validate and explain JQL without executing it",
	"meta":    "Ask Jira what can be done to an issue",
	"mcp":     "Serve the command registry over MCP",
}

func (a *app) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   buildinfo.App,
		Short: "A deterministic client for Jira, built for scripts and agents",
		Long: strings.TrimSpace(`
` + buildinfo.App + ` is a client for Jira whose output is a versioned contract.

Every result carries a kind and a schema version. A result set that was
truncated is never reported as complete: it exits 3 and says so. Any request
that cannot be honored exactly fails instead of approximating.

stdout carries the result and nothing else. Warnings and errors are structured
and go to stderr.`),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          rejectUnknownSubcommand,
		// The root itself does nothing; running it bare prints help.
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.contract {
				// --contract is shorthand for the contract command, which is
				// registered like any other so it describes its own kind.
				return a.emit(registry.ContractDoc(a.reg))
			}
			return cmd.Help()
		},
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)

	// cobra's generated `completion` command is not in the registry, so it
	// would appear in --help and not in `jr schema`. Self-description that
	// disagrees with the binary is the drift this design exists to prevent.
	// Shell completion belongs behind an interactive build tag.
	root.CompletionOptions.DisableDefaultCmd = true

	// pflag's own parse errors carry no code and no exit status. Wrapping them
	// here is what makes an unknown flag a structured exit 2 rather than a bare
	// line on stderr.
	root.SetFlagErrorFunc(a.flagError)

	// Every persistent flag is bound from registry.GlobalFlags, which is the
	// only place its name, usage and default are written down. They used to be
	// declared here, straight onto cobra, and that was the second declaration
	// CLAUDE.md says does not exist: `jr schema`, docs/commands.md and the MCP
	// tool list all read the registry, so thirteen flags were bound and
	// accepted and described nowhere.
	//
	// The variables stay local. What moved is the description, not the storage.
	bind := globalBinder{flags: root.PersistentFlags()}
	bind.str(&a.requestedFormat, registry.GlobalFormat)
	bind.boolean(&a.describe, registry.GlobalDescribe)
	bind.str(&a.contextName, registry.GlobalContext)
	bind.str(&a.site, registry.GlobalSite)
	bind.str(&a.project, registry.GlobalProject)
	bind.str(&a.board, registry.GlobalBoard)
	bind.str(&a.apiVersion, registry.GlobalAPIVersion)
	bind.str(&a.caBundle, registry.GlobalCABundle)
	bind.boolean(&a.readOnly, registry.GlobalReadOnly)
	bind.boolean(&a.debug, registry.GlobalDebug)
	bind.boolean(&a.refresh, registry.GlobalRefresh)
	bind.number(&a.retries, registry.GlobalRetries)
	bind.number(&a.maxRequests, registry.GlobalMaxRequests)
	bind.mustHaveBoundEveryGlobal()
	root.Flags().BoolVar(&a.contract, "contract", false,
		"dump the machine-readable output contract for every kind")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// An explicit --format wins; otherwise JIRA_FORMAT sets the default
		// globally. Both are validated here so a bad value fails before any
		// work is done, not after.
		//
		// Which of the two it came from is recorded before they are merged,
		// because a command that refuses --format has to refuse the flag and
		// not the environment: JIRA_FORMAT is set once for a whole shell, and
		// refusing an invocation because of it would make that setting a
		// landmine rather than a default.
		a.formatFromFlag = a.requestedFormat != ""
		if a.requestedFormat == "" {
			a.requestedFormat = a.getenv(EnvFormat)
		}
		if a.requestedFormat != "" {
			if _, err := render.ParseFormat(a.requestedFormat); err != nil {
				return err
			}
		}
		return a.refuseEmptyScopes(cmd.Root().PersistentFlags())
	}

	a.attach(root)
	return root
}

// attach builds the cobra tree from the registry. Intermediate nouns become
// group commands; leaves become runnable commands.
func (a *app) attach(root *cobra.Command) {
	groups := map[string]*cobra.Command{}

	for _, rc := range a.reg.All() {
		parent := root
		for i, seg := range rc.Path[:len(rc.Path)-1] {
			key := strings.Join(rc.Path[:i+1], ".")
			g, ok := groups[key]
			if !ok {
				g = newGroup(seg, key)
				groups[key] = g
				parent.AddCommand(g)
			}
			parent = g
		}
		parent.AddCommand(a.newLeaf(rc))
	}
}

func newGroup(name, key string) *cobra.Command {
	short := groupSummaries[key]
	if short == "" {
		short = "Commands for " + name
	}
	return &cobra.Command{
		Use:           name,
		Short:         short,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          rejectUnknownSubcommand,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// rejectUnknownSubcommand turns a mistyped verb into exit 2 with the near
// matches listed, instead of silently printing the parent's help and exiting 0.
func rejectUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	e := errs.Usage("UNKNOWN_COMMAND", "unknown command %q for %q", args[0], cmd.CommandPath()).
		WithRemedy("run `%s --help` for the available commands", cmd.CommandPath())

	// The candidates go in `detail` and the remedy stays put, which is the
	// shape UNKNOWN_FIELD already uses and what docs/troubleshooting.md tells a
	// reader `detail` is for. This used to replace the remedy with the
	// suggestions, so a caller who was offered a wrong guess lost the pointer to
	// --help along with it.
	//
	// Ranked here rather than by cobra's SuggestionsFor: that has its own
	// distance and its own prefix rule, and a tool with four refusals of the
	// same shape should not have four opinions about what "close" means.
	if near := nearest.Strings(args[0], subcommandNames(cmd), nearLimit); len(near) > 0 {
		return e.WithDetail("did you mean: %s", strings.Join(near, ", "))
	}
	return e
}

// annotationCommand is the key under which a cobra command carries its
// registry name.
const annotationCommand = "jr.command"

// newLeaf builds the cobra command for one registered command.
//
// The two callbacks are methods returning closures rather than closures
// written inline. Cobra wants functions, so something has to close over `rc`
// and the flag binder; putting the bodies here made one function out of three
// and hid the order the pieces actually run in.
func (a *app) newLeaf(rc *registry.Command) *cobra.Command {
	use := rc.Path[len(rc.Path)-1]
	if spec := rc.ArgSpec(); spec != "" {
		use += " " + spec
	}

	cc := &cobra.Command{
		Use:           use,
		Short:         rc.Summary,
		Long:          rc.Description,
		Example:       rc.Example,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Cobra hands a flag-error callback the cobra command and nothing
		// else, so the registry name travels on the command itself. The
		// alternative is rebuilding the dotted name out of CommandPath by
		// stripping the binary's own name, which breaks the day somebody
		// renames the binary.
		Annotations: map[string]string{annotationCommand: rc.Name()},
	}
	cc.SetHelpTemplate(leafHelpTemplate)

	binder := bindFlags(cc, rc)
	cc.Args = a.checkArity(rc)
	cc.RunE = a.runLeaf(rc, binder)
	return cc
}

// leafHelpTemplate prints a leaf's detail after its flags rather than before
// them, which is the whole of what cobra's default does differently.
//
// A reader opens `--help` holding a command they already know the name of, to
// find a flag. The default template puts Long first, and Long is up to 527
// words here: `jr issue list --help` was 119 lines with `Usage:` on line 55, so
// the first of its 38 flags arrived on line 60. On an 80x24 terminal that is
// two screens of prose before the reference begins, which is what a user
// reported on 2026-09-04 as documentation that is hard to follow.
//
// Nothing is shortened, because the prose is not padding: eight of the ten
// paragraphs on `issue list` are gotchas a caller needs, and moving them costs
// nothing precisely because the operative half is also in the flag usages
// printed just above. `--sort` says the ordering is by issue key, `--label`
// says a comma is part of the label. A reader who stops at the flags has
// already been told; a reader who wants the argument reads on.
//
// Short leads instead of Long, so the command still says what it is in one
// line before the usage block. Parents and the root keep cobra's default:
// their Long is orientation, they have no long flag list to bury, and the
// root's help is a golden file.
const leafHelpTemplate = `{{with .Short}}{{. | trimTrailingWhitespaces}}

{{end}}{{.UsageString}}{{with .Long}}
{{. | trimTrailingWhitespaces}}
{{end}}`

// checkArity holds a call to the argument count the command declared.
func (a *app) checkArity(rc *registry.Command) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		// --describe asks what this command needs. Requiring the caller to
		// already supply it would make the question unanswerable for exactly
		// the commands someone would ask it about.
		if a.describe {
			return nil
		}
		minArgs, maxArgs := rc.ArgBounds()
		switch {
		case len(args) < minArgs:
			return usageError(cmd, "%s requires %s", rc.UseLine(), describeArity(minArgs, maxArgs)).
				WithDetail("got %d positional argument(s)", len(args))
		case maxArgs >= 0 && len(args) > maxArgs:
			return usageError(cmd, "%s accepts %s", rc.UseLine(), describeArity(minArgs, maxArgs)).
				WithDetail("got %d positional argument(s): %s", len(args), strings.Join(args, " "))
		}
		return nil
	}
}

// runLeaf is what a command does when it is invoked: describe, or gate,
// validate, run, and emit.
func (a *app) runLeaf(
	rc *registry.Command,
	binder func(*cobra.Command) registry.Flags,
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		// --describe answers "what would this do" without doing it, so it runs
		// before any validation the real invocation would have to satisfy.
		if a.describe {
			return a.emit(registry.CommandDoc(rc))
		}
		if err := validateFlags(cmd, rc); err != nil {
			return err
		}

		inv, err := a.newInvocation(cmd, rc, binder, args)
		if err != nil {
			return err
		}

		// A command's own validation runs before anything is written, which
		// matters for a streaming command: its header goes out before its body
		// runs, so a flag rejected later would arrive after output had started.
		// Read-only and confirmation are enforced here, from the declaration,
		// so a resource author cannot ship a verb that forgets them. Both come
		// before Validate and before any network call.
		if err := a.gate(rc, inv); err != nil {
			return err
		}

		if rc.Validate != nil {
			if err := rc.Validate(cmd.Context(), inv); err != nil {
				return err
			}
		}

		if rc.Streams() {
			return a.stream(cmd.Context(), rc, inv)
		}
		return a.runDocument(cmd.Context(), rc, inv)
	}
}

// runDocument runs a command that returns a result document, and writes it.
func (a *app) runDocument(
	ctx context.Context, rc *registry.Command, inv *registry.Invocation,
) error {
	doc, err := rc.Run(ctx, inv)
	if err != nil {
		// A write that applied some of what it was asked has to say which part,
		// and the failure alone cannot. Everything else fails with stdout
		// untouched, which is the rule this is the one exception to.
		if applied, writeErr := a.writeAppliedSoFar(rc, inv, err); applied {
			if writeErr != nil {
				return writeErr
			}
		}
		return err
	}
	// A command that owns stdout has already written everything that belongs
	// there. Rendering a result on top would put a frame on the wire its peer
	// cannot parse.
	if !rc.EmitsDocumentFor(inv) {
		return nil
	}
	if !rc.Emits(doc.Kind, doc.Version) {
		// A command that emits a kind it did not declare would break every
		// consumer that dispatches on the declared kind, and would be invisible
		// to `jr --contract`.
		return errs.Runtime("UNDECLARED_KIND",
			"command %s emitted kind %q v%d, which it does not declare",
			rc.Name(), doc.Kind, doc.Version)
	}
	doc.Site = siteOf(inv)
	doc.Project, doc.Board = scopeOf(inv)
	return a.emit(doc)
}

// siteOf names the Jira an invocation reached, for the envelope.
//
// Empty for a command with no session, which is every local one. It is read
// here, at the boundary that renders, rather than by each resource: a resource
// that had to remember to stamp its own provenance would be a resource that
// eventually forgot, and the envelope is the one place every document passes
// through.
func siteOf(inv *registry.Invocation) string {
	if inv == nil || inv.Jira == nil {
		return ""
	}
	return inv.Jira.Site()
}

// scopeOf names the context scope the command asked for, for the envelope.
//
// It reports what was *read*, not what is configured, which is why it goes
// through the watcher rather than calling Project() here. Calling it here would
// answer for every command that has a session, including `issue list
// --all-projects`, whose rows came from every project on the site and whose
// envelope would then name one.
//
// A session that is not a watcher reports nothing rather than guessing. That
// is not a hole: newInvocation wraps every session it builds, and a caller that
// assembles its own — a test, a future embedder — gets an envelope with no
// scope rather than a wrong one.
func scopeOf(inv *registry.Invocation) (project, board string) {
	if inv == nil {
		return "", ""
	}
	w, ok := inv.Jira.(*registry.ScopeWatcher)
	if !ok {
		return "", ""
	}
	return w.Scope()
}

// newInvocation assembles everything a command is handed before it runs.
//
// Nothing here reaches Jira. The session is built lazily inside jiraSession, so
// a command that never connects never resolves a credential and never probes
// the deployment.
func (a *app) newInvocation(
	cmd *cobra.Command,
	rc *registry.Command,
	binder func(*cobra.Command) registry.Flags,
	args []string,
) (*registry.Invocation, error) {
	inv := &registry.Invocation{
		Args:     args,
		Flags:    binder(cmd),
		Limit:    registry.Limit{N: registry.DefaultLimit},
		Stderr:   a.stderr,
		Stdout:   a.stdout,
		Progress: registry.NoProgress,
	}

	if rc.NeedsJira {
		jira, err := a.jiraSession()
		if err != nil {
			return nil, err
		}
		// Wrapped so the envelope can report the scope this command asked for
		// rather than the scope the context happens to hold. The two differ on
		// exactly the invocation that matters: --all-projects reads no project
		// and must report none.
		inv.Jira = registry.WatchScope(jira)
	}
	if rc.Paginated {
		limit, err := registry.ParseLimit(cmd.Flags().Lookup("limit").Value.String())
		if err != nil {
			return nil, err
		}
		inv.Limit = limit
	}

	format, err := a.resolveFormat(nil)
	if err != nil {
		return nil, err
	}
	inv.Format = format
	return inv, nil
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

// formatUsage describes --format, and names markdown as the one for reading
// where the build has it.
//
// Discoverability is the whole point of the extra clause. `markdown` exists so
// a person does not have to read XML, and a person who does not know it is
// there reads XML — the format list alone says every name is equivalent, and
// they are not: four are a versioned contract and one is presentation that may
// change in any release. Saying which is which is also the warning, so the
// sentence does two jobs and neither of them is a footnote nobody reaches.
//
// Built from the names this binary actually has, so a build without the
// `render` tag neither lists markdown nor mentions it — advertising a format
// that would be refused is the drift the whole self-describing surface exists
// to prevent.
// refuseEmptyScopes refuses `--project ""` and `--board ""`.
//
// An empty scope does not unscope. It falls back to the context, deliberately —
// see the comment on listQuery in internal/resource/issue — so somebody
// reaching for it as an escape hatch gets the context's project, a query that
// cannot match what they named, and a complete empty exit-0 result. That is
// the tool's own silence, manufactured out of a flag value nobody means.
//
// It is checked on the flag rather than on the resolved settings, because an
// empty JIRA_PROJECT is a shell that has the variable exported and unset, which
// is a configuration and not a request. Refusing that would make an environment
// variable a landmine, which is the reasoning --format already follows above.
//
// And it reads the root's persistent set rather than the command's merged one,
// because `context create --project ENG` sets that command's *own* --project
// and leaves the global untouched. Reading cmd.Flags() saw Changed on the local
// flag and an empty value on the global, and refused every context create in
// the suite.
func (a *app) refuseEmptyScopes(persistent *pflag.FlagSet) error {
	for _, scope := range []struct {
		name  string
		value string
	}{
		{registry.GlobalProject, a.project},
		{registry.GlobalBoard, a.board},
	} {
		if !persistent.Changed(scope.name) || scope.value != "" {
			continue
		}
		return errs.Usage("EMPTY_SCOPE",
			"--%s was given an empty value", scope.name).
			WithDetail("an empty --%s does not lift the scope; it falls back "+
				"to the context's, and the query runs against that", scope.name).
			WithRemedy("drop the flag to use the context's %s, or pass "+
				"--all-projects where the command has it", scope.name)
	}
	return nil
}

// globalBinder binds a persistent flag from its registry declaration, so the
// name, the usage string and the default have exactly one source.
//
// It panics on a name the registry does not declare, and
// mustHaveBoundEveryGlobal panics on a declared global nothing bound. Both are
// programmer errors in this file and neither is reachable from a caller, which
// is why they are panics rather than errors: a binary that got either wrong
// must not start and describe itself wrongly.
type globalBinder struct {
	flags *pflag.FlagSet
	bound []string
}

func (b *globalBinder) declaration(name string) registry.Flag {
	f, ok := registry.GlobalFlag(name)
	if !ok {
		panic("cli: no global flag named " + name)
	}
	b.bound = append(b.bound, name)
	return f
}

func (b *globalBinder) str(p *string, name string) {
	f := b.declaration(name)
	b.flags.StringVar(p, f.Name, f.Default, f.Usage)
}

func (b *globalBinder) boolean(p *bool, name string) {
	f := b.declaration(name)
	b.flags.BoolVar(p, f.Name, f.Default == "true", f.Usage)
}

func (b *globalBinder) number(p *int, name string) {
	f := b.declaration(name)
	def, err := strconv.Atoi(f.Default)
	if err != nil {
		panic("cli: global flag --" + name + " has a non-numeric default " + f.Default)
	}
	b.flags.IntVar(p, f.Name, def, f.Usage)
}

// mustHaveBoundEveryGlobal is the direction a compiler cannot check. Adding a
// flag to registry.GlobalFlags and forgetting to bind it here would describe a
// flag the binary refuses as unknown, which is the same defect as the one this
// whole change is about, pointing the other way.
func (b *globalBinder) mustHaveBoundEveryGlobal() {
	for _, f := range registry.GlobalFlags() {
		if !slices.Contains(b.bound, f.Name) {
			panic("cli: global flag --" + f.Name + " is declared and never bound")
		}
	}
}

// subcommandNames is what this command's children are called, for ranking a
// verb the caller typed. Hidden and generated commands are left out: offering a
// name that does not appear in --help sends somebody looking for it there.
func subcommandNames(cmd *cobra.Command) []string {
	var out []string
	for _, c := range cmd.Commands() {
		if c.Hidden || !c.IsAvailableCommand() {
			continue
		}
		out = append(out, c.Name())
	}
	return out
}
