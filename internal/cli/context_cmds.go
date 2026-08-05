package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/jctx"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// Output kinds owned by the context commands.
const (
	kindContextList    = "context.list"
	kindContextGet     = "context.get"
	versionContextList = 1
	versionContextGet  = 1
)

func (a *app) contextCommands() []*registry.Command {
	return []*registry.Command{
		a.contextCreateCommand(),
		a.contextListCommand(),
		a.contextUseCommand(),
		a.contextShowCommand(),
		a.contextDeleteCommand(),
	}
}

func (a *app) contextCreateCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"context", "create"},
		Summary: "Create or replace a named site and project pairing",
		Description: strings.TrimSpace(`
A context is {site, credential ref, default project, default board, default
fields}. Naming an existing context replaces it.

The project is a default, not a requirement: any command can override it with
--project or omit it entirely. The few commands that genuinely cannot proceed
without one exit 2 and name the flag.

--readonly bakes read-only mode into the context. It is a one-way latch: an
invocation that simply omits --readonly does not become read-write, because a
context created read-only is a statement about what it is for.

Credentials are not stored here. Store one with "` + buildinfo.App + ` auth login".`),
		Example: strings.Join([]string{
			buildinfo.App + " context create work --site your-site.atlassian.net --project ENG",
			buildinfo.App + " context create audit --site your-site.atlassian.net --readonly",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "name", Usage: "context name", Required: true,
		}},
		Flags: []registry.Flag{
			{
				Name: "site", Type: registry.TypeString, Required: true,
				Usage: "Jira site, e.g. your-site.atlassian.net",
			},
			{Name: "project", Type: registry.TypeString, Usage: "default project key"},
			{Name: "board", Type: registry.TypeString, Usage: "default board id"},
			{
				Name: "field", Type: registry.TypeString, Repeatable: true,
				Usage: "default field to request; repeat for several",
			},
			{
				Name: "credential", Type: registry.TypeString,
				Usage: "credential key to use, if not the site's host",
			},
			{
				Name: "readonly", Type: registry.TypeBool,
				Usage: "refuse every command that would change Jira",
			},
		},
		LocalState: true,
		Outputs:    []registry.Output{{Kind: kindContextGet, Version: versionContextGet}},
		ExitCodes:  []exitcode.Code{exitcode.NotFound},
		Run:        a.runContextCreate,
	}
}

func (a *app) runContextCreate(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}

	name := inv.Args[0]
	ctx := jctx.Context{
		Site:       inv.Flags.String("site"),
		Project:    inv.Flags.String("project"),
		Board:      inv.Flags.String("board"),
		Fields:     inv.Flags.StringSlice("field"),
		Credential: inv.Flags.String("credential"),
		ReadOnly:   inv.Flags.Bool("readonly"),
	}
	if err := cfg.Set(name, ctx); err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}

	saved, _ := cfg.Get(name)
	return contextDoc(name, saved, cfg.Current == name, cfg.Path()), nil
}

func (a *app) contextListCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"context", "list"},
		Summary: "List every configured context",
		Description: strings.TrimSpace(`
The current context is marked. A context is a local setting; nothing here
contacts Jira.`),
		Example:   buildinfo.App + " context list",
		Paginated: true,
		Outputs:   []registry.Output{{Kind: kindContextList, Version: versionContextList}},
		ExitCodes: []exitcode.Code{exitcode.Partial},
		Run:       a.runContextList,
	}
}

func (a *app) runContextList(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}

	names := cfg.Names()
	complete := true
	if !inv.Limit.All && len(names) > inv.Limit.N {
		names, complete = names[:inv.Limit.N], false
	}

	items := make([]*render.Node, 0, len(names))
	for _, name := range names {
		ctx, _ := cfg.Get(name)
		items = append(items, contextNode(name, ctx, cfg.Current == name))
	}

	return render.List(kindContextList, versionContextList, &render.Collection{
		Name:     "contexts",
		Items:    items,
		Complete: complete,
		Columns: []render.Column{
			{Header: "name", Path: "@name"},
			{Header: "current", Path: "@current"},
			{Header: "site", Path: "@site"},
			{Header: "project", Path: "@project"},
			{Header: "board", Path: "@board"},
			{Header: "readonly", Path: "@readonly"},
		},
	}), nil
}

func (a *app) contextUseCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"context", "use"},
		Summary: "Select the context every command uses by default",
		Description: strings.TrimSpace(`
Sets the current context. A single command can still override it with
--context without changing this setting.`),
		Example: buildinfo.App + " context use work",
		Args: []registry.Arg{{
			Name: "name", Usage: "context name", Required: true,
		}},
		LocalState: true,
		Outputs:    []registry.Output{{Kind: kindContextGet, Version: versionContextGet}},
		ExitCodes:  []exitcode.Code{exitcode.NotFound},
		Run:        a.runContextUse,
	}
}

func (a *app) runContextUse(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	name := inv.Args[0]
	if err := cfg.Use(name); err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	ctx, _ := cfg.Get(name)
	return contextDoc(name, ctx, true, cfg.Path()), nil
}

func (a *app) contextShowCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"context", "show"},
		Summary: "Show one context, or the effective settings for this invocation",
		Description: strings.TrimSpace(`
With a name, prints that context as stored.

With no name, prints what this invocation would actually use once flags, the
environment, and the current context have all been applied — which is the
question worth asking when a command is not doing what you expected.`),
		Example: strings.Join([]string{
			buildinfo.App + " context show",
			buildinfo.App + " context show work",
			buildinfo.App + " context show --project OPS",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "name", Usage: "context name; omit for the effective settings",
		}},
		Outputs:   []registry.Output{{Kind: kindContextGet, Version: versionContextGet}},
		ExitCodes: []exitcode.Code{exitcode.NotFound},
		Run:       a.runContextShow,
	}
}

func (a *app) runContextShow(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}

	if len(inv.Args) == 1 {
		name := inv.Args[0]
		ctx, ok := cfg.Get(name)
		if !ok {
			return nil, unknownContext(cfg, name)
		}
		return contextDoc(name, ctx, cfg.Current == name, cfg.Path()), nil
	}

	resolved, err := a.resolve(cfg)
	if err != nil {
		return nil, err
	}
	return resolvedDoc(resolved, cfg.Path()), nil
}

func (a *app) contextDeleteCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"context", "delete"},
		Summary: "Delete a context",
		Description: strings.TrimSpace(`
Removes the context from the config file. Any credential stored for its site is
left alone; remove that with "` + buildinfo.App + ` auth logout".

Deleting the current context leaves no context selected, unless exactly one
remains, in which case that one is selected because the choice is unambiguous.`),
		Example: buildinfo.App + " context delete work --yes",
		Args: []registry.Arg{{
			Name: "name", Usage: "context name", Required: true,
		}},
		Flags: []registry.Flag{{
			Name: "yes", Type: registry.TypeBool, Usage: "confirm the deletion",
		}},
		LocalState:  true,
		Destructive: true,
		Outputs:     []registry.Output{{Kind: kindContextGet, Version: versionContextGet}},
		ExitCodes:   []exitcode.Code{exitcode.NotFound, exitcode.Blocked},
		Run:         a.runContextDelete,
	}
}

func (a *app) runContextDelete(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}

	name := inv.Args[0]
	ctx, ok := cfg.Get(name)
	if !ok {
		return nil, unknownContext(cfg, name)
	}
	if err := requireYes(inv, "deleting context "+name); err != nil {
		return nil, err
	}
	if err := cfg.Delete(name); err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}

	doc := contextDoc(name, ctx, false, cfg.Path())
	doc.Record.Attr("deleted", "true")
	return doc, nil
}

// requireYes enforces the confirmation gate.
//
// A headless build never blocks on input: there is no prompt to fall back to,
// so the absence of --yes is exit 10 rather than a question nobody can answer.
func requireYes(inv *registry.Invocation, what string) error {
	if inv.Flags.Bool("yes") {
		return nil
	}
	return errs.Blocked("CONFIRMATION_REQUIRED", "%s needs confirmation", what).
		WithRemedy("pass --yes")
}

func unknownContext(cfg *jctx.Config, name string) error {
	e := errs.NotFound("UNKNOWN_CONTEXT", "no context named %q", name)
	if names := cfg.Names(); len(names) > 0 {
		return e.WithDetail("defined: %s", strings.Join(names, ", "))
	}
	return e.WithRemedy("create one with `%s context create <name> --site <host>`", buildinfo.App)
}

func contextNode(name string, ctx jctx.Context, current bool) *render.Node {
	n := render.El("context").
		Attr("name", name).
		Attr("current", strconv.FormatBool(current)).
		Attr("site", ctx.Site).
		Attr("project", ctx.Project).
		Attr("board", ctx.Board).
		Attr("readonly", strconv.FormatBool(ctx.ReadOnly)).
		Attr("credential", ctx.CredentialRef())

	fields := make([]*render.Node, 0, len(ctx.Fields))
	for _, f := range ctx.Fields {
		fields = append(fields, render.El("field").SetText(f))
	}
	n.Child(render.ListEl("fields", "field", fields...))
	return n
}

func contextDoc(name string, ctx jctx.Context, current bool, configPath string) *render.Doc {
	n := contextNode(name, ctx, current)
	n.Leaf("config", configPath)
	return render.Record(kindContextGet, versionContextGet, n)
}

// resolvedDoc renders the effective settings, naming the context they came
// from so "why is it using that project" has an answer in the output itself.
func resolvedDoc(r *jctx.Resolved, configPath string) *render.Doc {
	n := render.El("context").
		Attr("name", r.Name).
		Attr("current", "true").
		Attr("site", r.Site).
		Attr("project", r.Project).
		Attr("board", r.Board).
		Attr("readonly", strconv.FormatBool(r.ReadOnly)).
		Attr("credential", r.CredentialRef).
		Attr("effective", "true")

	fields := make([]*render.Node, 0, len(r.Fields))
	for _, f := range r.Fields {
		fields = append(fields, render.El("field").SetText(f))
	}
	n.Child(render.ListEl("fields", "field", fields...))
	n.Leaf("config", configPath)
	return render.Record(kindContextGet, versionContextGet, n)
}
