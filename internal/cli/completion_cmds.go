//go:build prompt

package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

func init() {
	taggedBuiltins = append(taggedBuiltins, (*app).completionCommand)
}

// shells are the ones cobra can generate for. The list is an enum rather than
// free text so an unrecognized shell is exit 2 naming the valid ones, instead
// of an empty script that would be sourced and silently do nothing.
var shells = []string{"bash", "zsh", "fish", "powershell"}

func (a *app) completionCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"completion"},
		Summary: "Print a shell completion script",
		Description: strings.TrimSpace(`
Writes a completion script for the named shell to stdout.

It is declared here rather than left to cobra's generated command, which is not
in the registry and would therefore appear in --help while ` + "`" + buildinfo.App +
			` schema` + "`" + ` denied
it existed. Self-description that disagrees with the binary is the drift this
design exists to prevent.

It is behind the prompt tag because completion is an interactive convenience,
and a build made for an agent has nobody to complete for.

The script is the output, so nothing else is written to stdout — there is no
result envelope, and --format does not apply.`),
		Example: strings.Join([]string{
			buildinfo.App + " completion bash > /etc/bash_completion.d/" + buildinfo.App,
			"source <(" + buildinfo.App + " completion zsh)",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "shell", Usage: "one of: " + strings.Join(shells, ", "), Required: true,
		}},
		RequiresTags: []string{"prompt"},
		// The script is the output, not a result document. Rendering an
		// envelope around it would produce something no shell can source.
		OwnsStdout: true,
		Run:        a.runCompletion,
	}
}

func (a *app) runCompletion(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	root := a.completionTree()

	var err error
	switch shell := inv.Args[0]; shell {
	case "bash":
		err = root.GenBashCompletionV2(a.stdout, true)
	case "zsh":
		err = root.GenZshCompletion(a.stdout)
	case "fish":
		err = root.GenFishCompletion(a.stdout, true)
	case "powershell":
		err = root.GenPowerShellCompletionWithDesc(a.stdout)
	default:
		return nil, errs.Usage("UNKNOWN_SHELL",
			"%q is not a shell this build can complete for", shell).
			WithDetail("valid values: %s", strings.Join(shells, ", ")).
			WithRemedy("pass one of the above")
	}
	if err != nil {
		return nil, errs.Runtime("COMPLETION_FAILED",
			"cannot generate the completion script").Wrap(err)
	}
	return nil, nil
}

// completionTree builds a command tree for the generators to describe.
//
// It is a second tree rather than the one being executed, because cobra's
// generators walk from a root and the running command is mid-invocation. Both
// come from the same registry, so they cannot describe different things.
func (a *app) completionTree() *cobra.Command {
	root := a.newRoot()
	a.attach(root)
	return root
}
