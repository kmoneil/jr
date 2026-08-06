package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// Output kinds owned by the built-in commands.
const (
	kindVersion    = "version"
	versionVersion = 1
)

// builtins are the commands present in every build, whatever tags it was
// compiled with. Everything else registers itself from a tag-gated file.
//
// They are built per-invocation rather than registered globally so that
// `schema` can close over the live registry, and so two runs in one process
// cannot collide.
func (a *app) builtins() []*registry.Command {
	out := []*registry.Command{
		versionCommand(),
		a.schemaCommand(),
		a.contractCommand(),
	}
	out = append(out, a.authCommands()...)
	out = append(out, a.contextCommands()...)
	for _, build := range taggedBuiltins {
		out = append(out, build(a))
	}
	return out
}

// taggedBuiltins holds commands that exist only under a build tag. A file
// carrying that tag appends to this from init, so a build without the tag does
// not merely refuse the command — it does not contain it.
var taggedBuiltins []func(*app) *registry.Command

func versionCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"version"},
		Summary: "Print the build identity and its compiled-in capabilities",
		Description: strings.TrimSpace(`
Reports the release, the build profile, and the build tags this binary was
compiled with.

The tag list is the truth about what this binary can do. A capability that was
compiled out is not merely refused at runtime — it is not present.`),
		Example: buildinfo.App + " version --format json",
		Outputs: []registry.Output{{Kind: kindVersion, Version: versionVersion}},
		Run: func(_ context.Context, _ *registry.Invocation) (*render.Doc, error) {
			n := render.El("version").
				Attr("app", buildinfo.App).
				Attr("release", buildinfo.Release).
				Attr("profile", buildinfo.Profile()).
				Attr("writable", strconv.FormatBool(buildinfo.CanWrite())).
				Attr("interactive", strconv.FormatBool(buildinfo.CanPrompt())).
				Leaf("display", buildinfo.Display()).
				Leaf("commit", buildinfo.Commit).
				Leaf("built", buildinfo.Built).
				Leaf("go", buildinfo.GoVersion).
				Leaf("platform", buildinfo.Platform).
				Child(registry.TagsNode())
			return render.Record(kindVersion, versionVersion, n), nil
		},
	}
}

func (a *app) schemaCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"schema"},
		Summary: "Describe every command this build contains",
		Description: strings.TrimSpace(`
Prints the command surface as data: flags with types and enums, required
inputs, whether a command mutates or destroys, its output kind, and the exit
codes it can produce.

With no argument it lists every command. With a dotted command name it prints
that one command in full. It is generated from the same registry that builds
the command tree, so it cannot drift from what the binary actually does — and
it lists only what this build contains.`),
		Example: strings.Join([]string{
			buildinfo.App + " schema",
			buildinfo.App + " schema version",
			buildinfo.App + " schema --format json",
		}, "\n"),
		Args: []registry.Arg{{
			Name:  "command",
			Usage: "dotted command name, e.g. issue.list",
		}},
		Paginated: true,
		// The command surface is local, finite, and known before this runs, so
		// there is nothing for the default bound to protect against. It stayed
		// at fifty until the binary passed fifty commands, at which point
		// `jr schema` began describing most of itself and exiting 3.
		DefaultsToAll: true,
		Outputs: []registry.Output{
			{Kind: registry.KindCommands, Version: registry.VersionCommands},
			{
				Kind:    registry.KindCommand,
				Version: registry.VersionCommand,
				When:    "a command name is given",
			},
		},
		ExitCodes: []exitcode.Code{exitcode.Partial, exitcode.NotFound},
		Run:       a.runSchema,
	}
}

func (a *app) contractCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"contract"},
		Summary: "Dump the machine-readable output contract for every kind",
		Description: strings.TrimSpace(`
Lists every output kind this build can emit, its schema version, and the
commands that emit it.

A consumer pins the kinds it parses and verifies them against this, so a shape
change shows up as a version mismatch rather than as a parse that quietly
succeeds against the wrong fields.

` + buildinfo.App + ` --contract is a shorthand for this command.`),
		Example: buildinfo.App + " contract --format json",
		Outputs: []registry.Output{{
			Kind:    registry.KindContract,
			Version: registry.VersionContract,
		}},
		Run: func(_ context.Context, _ *registry.Invocation) (*render.Doc, error) {
			return registry.ContractDoc(a.reg), nil
		},
	}
}

// runSchema needs the live registry, which is why the schema command is built
// per-invocation rather than registered globally.
func (a *app) runSchema(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	all := a.reg.All()

	if len(inv.Args) == 1 {
		name := inv.Args[0]
		c, ok := a.reg.Lookup(name)
		if !ok {
			e := errs.NotFound("UNKNOWN_COMMAND", "no command named %q in this build", name).
				WithDetail("build profile %s, tags=%s", buildinfo.Profile(), buildinfo.TagList())
			if near := nearMatches(name, a.reg.Names()); len(near) > 0 {
				return nil, e.WithRemedy("did you mean: %s", strings.Join(near, ", "))
			}
			return nil, e.WithRemedy(
				"run `%s schema` for the commands this build contains", buildinfo.App,
			)
		}
		return registry.CommandDoc(c), nil
	}

	// A limit that cuts the list short produces complete="false" and exit 3.
	// It never produces a truncated list that claims to be exhaustive.
	complete := true
	if !inv.Limit.All && len(all) > inv.Limit.N {
		all = all[:inv.Limit.N]
		complete = false
	}
	return registry.CommandsDoc(all, complete), nil
}

// nearMatches returns candidates sharing a prefix with want, so a mistyped
// command name comes back with alternatives rather than just a refusal.
func nearMatches(want string, candidates []string) []string {
	var out []string
	lower := strings.ToLower(want)
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c), lower) || strings.HasPrefix(lower, c) {
			out = append(out, c)
		}
	}
	return out
}
