package cli

import (
	"context"
	"slices"
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
		a.contextEditCommand(),
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

// unsettable names the fields `context edit --unset` can clear.
//
// Site is not among them. A context without a site is not a context with one
// fewer setting; it is one that cannot be used, and deleting it is the honest
// way to say so.
var unsettable = []string{"project", "board", "field", "credential", "readonly"}

func (a *app) contextEditCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"context", "edit"},
		Summary: "Change one setting of a context, leaving the rest alone",
		Description: strings.TrimSpace(`
Changes only what you name. Everything else is left as it was.

` + "`" + buildinfo.App + ` context create` + "`" + ` replaces a context wholesale, which is right for
creating one and wrong for adjusting one: re-stating a context to change its
project is how a board and a default field set get dropped without anyone
noticing. That has already happened once.

--unset clears a setting, because an empty flag value cannot be told apart from
an absent one — ` + "`" + `--project ""` + "`" + ` and no --project at all arrive here identically,
so clearing needs its own spelling. --unset site is refused: a context without a
site is not a context with one fewer setting, it is one that cannot be used, and
` + "`" + buildinfo.App + ` context delete` + "`" + ` is how you say that.

--field replaces the whole default set, exactly as --label does on issue edit.
--unset field empties it.

--unset readonly makes a read-only context writable again. The one-way latch
governs an invocation — nothing a command does can promote itself — and not the
configuration: changing what a context is for is a deliberate edit, and this is
where it happens rather than by deleting and re-creating it.`),
		Example: strings.Join([]string{
			buildinfo.App + " context edit work --project OPS",
			buildinfo.App + " context edit work --unset board --unset field",
		}, "\n"),
		Args: []registry.Arg{{
			Name: "name", Usage: "context name", Required: true,
		}},
		Flags: []registry.Flag{
			{Name: "site", Type: registry.TypeString, Usage: "Jira site"},
			{Name: "project", Type: registry.TypeString, Usage: "default project key"},
			{Name: "board", Type: registry.TypeString, Usage: "default board id"},
			{
				Name: "field", Type: registry.TypeString, Repeatable: true,
				Usage: "default field to request; replaces the whole set",
			},
			{
				Name: "credential", Type: registry.TypeString,
				Usage: "credential key to use, if not the site's host",
			},
			{
				Name: "readonly", Type: registry.TypeBool,
				Usage: "refuse every command that would change Jira",
			},
			{
				Name: "unset", Type: registry.TypeEnum, Enum: unsettable, Repeatable: true,
				Usage: "clear a setting; repeat for several",
			},
		},
		LocalState: true,
		Outputs:    []registry.Output{{Kind: kindContextGet, Version: versionContextGet}},
		ExitCodes:  []exitcode.Code{exitcode.NotFound},
		Validate:   validateContextEdit,
		Run:        a.runContextEdit,
	}
}

func validateContextEdit(_ context.Context, inv *registry.Invocation) error {
	unset := inv.Flags.StringSlice("unset")
	for _, name := range unset {
		if !slices.Contains(unsettable, name) {
			e := errs.Usage("INVALID_UNSET", "%q is not a setting that can be cleared", name)
			if name == "site" {
				return e.
					WithDetail("a context without a site cannot be used").
					WithRemedy("change it with --site, or remove the context with " +
						"`" + buildinfo.App + " context delete`")
			}
			return e.WithRemedy("clearable: %s", strings.Join(unsettable, ", "))
		}
	}

	// Setting and clearing the same thing has no single right answer, and
	// picking one would make the result depend on an implementation detail
	// nobody can see. This is the label-flag lesson from issue edit.
	for _, name := range unset {
		conflict := inv.Flags.String(name) != ""
		if name == "field" {
			conflict = len(inv.Flags.StringSlice("field")) > 0
		}
		if name == "readonly" {
			conflict = inv.Flags.Bool("readonly")
		}
		if conflict {
			return errs.Usage("CONFLICTING_EDIT",
				"--unset %s cannot be combined with setting it", name).
				WithRemedy("do one or the other")
		}
	}

	if !editTouchesTheContext(inv) {
		return errs.Usage("NOTHING_TO_EDIT", "context edit was given nothing to change").
			WithRemedy("name a setting, e.g. --project, or clear one with --unset")
	}
	return nil
}

func editTouchesTheContext(inv *registry.Invocation) bool {
	for _, name := range []string{"site", "project", "board", "credential"} {
		if inv.Flags.String(name) != "" {
			return true
		}
	}
	return len(inv.Flags.StringSlice("field")) > 0 ||
		len(inv.Flags.StringSlice("unset")) > 0 ||
		inv.Flags.Bool("readonly")
}

func (a *app) runContextEdit(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}

	name := inv.Args[0]
	ctx, ok := cfg.Get(name)
	if !ok {
		return nil, unknownContext(cfg, name)
	}

	// Only what was named. Everything absent from the invocation keeps the
	// value it already had, which is the entire point of this command.
	if v := inv.Flags.String("site"); v != "" {
		ctx.Site = v
	}
	if v := inv.Flags.String("project"); v != "" {
		ctx.Project = v
	}
	if v := inv.Flags.String("board"); v != "" {
		ctx.Board = v
	}
	if v := inv.Flags.String("credential"); v != "" {
		ctx.Credential = v
	}
	if v := inv.Flags.StringSlice("field"); len(v) > 0 {
		ctx.Fields = v
	}
	if inv.Flags.Bool("readonly") {
		ctx.ReadOnly = true
	}

	for _, name := range inv.Flags.StringSlice("unset") {
		switch name {
		case "project":
			ctx.Project = ""
		case "board":
			ctx.Board = ""
		case "field":
			ctx.Fields = nil
		case "credential":
			ctx.Credential = ""
		case "readonly":
			ctx.ReadOnly = false
		}
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
