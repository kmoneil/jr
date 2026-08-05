package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/transport"
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
		Short: "A deterministic Jira client for scripts and agents",
		Long: strings.TrimSpace(`
` + buildinfo.App + ` is a Jira client whose output is a versioned contract.

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
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError(cmd, "%s", err.Error())
	})

	root.PersistentFlags().StringVar(&a.requestedFormat, "format", "",
		fmt.Sprintf("output format: %s (default: tsv for lists, xml for records)",
			strings.Join(render.FormatNames(), "|")))
	root.PersistentFlags().BoolVar(&a.describe, "describe", false,
		"print this command's schema instead of running it")
	root.PersistentFlags().StringVar(&a.contextName, "context", "",
		"use this context for one invocation, without selecting it")
	root.PersistentFlags().StringVar(&a.site, "site", "",
		"Jira site, overriding the context's")
	root.PersistentFlags().StringVar(&a.project, "project", "",
		"project key, overriding the context's")
	root.PersistentFlags().BoolVar(&a.readOnly, "readonly", false,
		"refuse any command that would change Jira")
	root.PersistentFlags().BoolVar(&a.debug, "debug", false,
		"trace HTTP requests to stderr; credentials are redacted in the transport")
	root.PersistentFlags().BoolVar(&a.refresh, "refresh", false,
		"ignore cached site metadata and probe again")
	root.PersistentFlags().IntVar(&a.retries, "retries", transport.DefaultRetries,
		"retry budget per request; exhausting it exits 8 or 9, never 0")
	root.PersistentFlags().IntVar(&a.maxRequests, "max-requests", 0,
		"cap total HTTP calls for this invocation; 0 means no cap")
	root.Flags().BoolVar(&a.contract, "contract", false,
		"dump the machine-readable output contract for every kind")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// An explicit --format wins; otherwise JIRA_FORMAT sets the default
		// globally. Both are validated here so a bad value fails before any
		// work is done, not after.
		if a.requestedFormat == "" {
			a.requestedFormat = a.getenv(EnvFormat)
		}
		if a.requestedFormat != "" {
			if _, err := render.ParseFormat(a.requestedFormat); err != nil {
				return err
			}
		}
		return nil
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
	e := errs.Usage("UNKNOWN_COMMAND", "unknown command %q for %q", args[0], cmd.CommandPath())
	if near := cmd.SuggestionsFor(args[0]); len(near) > 0 {
		return e.WithRemedy("did you mean: %s", strings.Join(near, ", "))
	}
	return e.WithRemedy("run `%s --help` for the available commands", cmd.CommandPath())
}

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
	}

	binder := bindFlags(cc, rc)

	cc.Args = func(cmd *cobra.Command, args []string) error {
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

	cc.RunE = func(cmd *cobra.Command, args []string) error {
		// --describe answers "what would this do" without doing it, so it runs
		// before any validation the real invocation would have to satisfy.
		if a.describe {
			return a.emit(registry.CommandDoc(rc))
		}
		if err := validateFlags(cmd, rc); err != nil {
			return err
		}

		inv := &registry.Invocation{
			Args:     args,
			Flags:    binder(cmd),
			Limit:    registry.Limit{N: registry.DefaultLimit},
			Stderr:   a.stderr,
			Progress: registry.NoProgress,
		}
		// Built lazily inside: a command that never connects never resolves a
		// credential and never probes the deployment.
		if rc.NeedsJira {
			jira, err := a.jiraSession()
			if err != nil {
				return err
			}
			inv.Jira = jira
		}
		if rc.Paginated {
			limit, err := registry.ParseLimit(cmd.Flags().Lookup("limit").Value.String())
			if err != nil {
				return err
			}
			inv.Limit = limit
		}

		format, err := a.resolveFormat(nil)
		if err != nil {
			return err
		}
		inv.Format = format

		// A command's own validation runs before anything is written, which
		// matters for a streaming command: its header goes out before its body
		// runs, so a flag rejected later would arrive after output had started.
		if rc.Validate != nil {
			if err := rc.Validate(cmd.Context(), inv); err != nil {
				return err
			}
		}

		if rc.Streams() {
			return a.stream(cmd.Context(), rc, inv)
		}

		doc, err := rc.Run(cmd.Context(), inv)
		if err != nil {
			return err
		}
		// A command that owns stdout has already written everything that
		// belongs there. Rendering a result on top would put a frame on the
		// wire its peer cannot parse.
		if !rc.EmitsDocument() {
			return nil
		}
		if !rc.Emits(doc.Kind, doc.Version) {
			// A command that emits a kind it did not declare would break every
			// consumer that dispatches on the declared kind, and would be
			// invisible to `jr --contract`.
			return errs.Runtime("UNDECLARED_KIND",
				"command %s emitted kind %q v%d, which it does not declare",
				rc.Name(), doc.Kind, doc.Version)
		}
		return a.emit(doc)
	}

	return cc
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
