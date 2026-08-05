package registry

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/render"
)

// Schema kinds and their versions. Bump a version in the same commit that
// changes the corresponding golden file.
const (
	KindCommands = "schema.commands"
	KindCommand  = "schema.command"
	KindContract = "contract"

	VersionCommands = 1
	VersionCommand  = 1
	VersionContract = 1
)

// CommandsDoc renders the command list. It is the self-description an agent
// reads instead of documentation, and it lists only what this build contains.
func CommandsDoc(cmds []*Command, complete bool) *render.Doc {
	items := make([]*render.Node, 0, len(cmds))
	for _, c := range cmds {
		items = append(items, commandSummaryNode(c))
	}
	return render.List(KindCommands, VersionCommands, &render.Collection{
		Name:     "commands",
		Items:    items,
		Complete: complete,
		Columns: []render.Column{
			{Header: "name", Path: "@name"},
			{Header: "summary", Path: "summary"},
			{Header: "mutating", Path: "@mutating"},
			{Header: "destructive", Path: "@destructive"},
			{Header: "kind", Path: "@kind"},
		},
	})
}

func commandSummaryNode(c *Command) *render.Node {
	return render.El("command").
		Attr("name", c.Name()).
		Attr("use", c.UseLine()).
		Attr("mutating", strconv.FormatBool(c.Mutating)).
		Attr("destructive", strconv.FormatBool(c.Destructive)).
		Attr("kind", c.Kind()).
		Attr("v", strconv.Itoa(c.KindVersion())).
		Leaf("summary", c.Summary)
}

// CommandDoc renders the full description of one command: flags with types and
// enums, required inputs, whether it mutates or destroys, its output kind, its
// possible exit codes, and an example invocation.
func CommandDoc(c *Command) *render.Doc {
	n := render.El("command").
		Attr("name", c.Name()).
		Attr("use", c.UseLine()).
		Attr("mutating", strconv.FormatBool(c.Mutating)).
		Attr("destructive", strconv.FormatBool(c.Destructive)).
		Attr("paginated", strconv.FormatBool(c.Paginated)).
		Attr("kind", c.Kind()).
		Attr("v", strconv.Itoa(c.KindVersion())).
		Leaf("summary", c.Summary)

	if c.Description != "" {
		n.Child(render.El("description").SetCDATA(c.Description))
	}

	outputs := make([]*render.Node, 0, len(c.Outputs))
	for _, o := range c.Outputs {
		out := render.El("output").
			Attr("kind", o.Kind).
			Attr("v", strconv.Itoa(o.Version))
		out.AttrIf("when", o.When)
		outputs = append(outputs, out)
	}
	n.Child(render.ListEl("outputs", "output", outputs...))

	args := make([]*render.Node, 0, len(c.Args))
	for _, a := range c.Args {
		args = append(args, render.El("arg").
			Attr("name", a.Name).
			Attr("required", strconv.FormatBool(a.Required)).
			Attr("variadic", strconv.FormatBool(a.Variadic)).
			SetText(a.Usage))
	}
	n.Child(render.ListEl("args", "arg", args...))

	flags := make([]*render.Node, 0, len(c.Flags))
	for _, f := range c.Flags {
		flags = append(flags, flagNode(f))
	}
	n.Child(render.ListEl("flags", "flag", flags...))

	exits := make([]*render.Node, 0, len(c.ExitCodes))
	for _, code := range c.AllExitCodes() {
		exits = append(exits, render.El("exit-code").
			Attr("code", strconv.Itoa(code.Int())).
			Attr("name", code.Name()).
			SetText(code.Description()))
	}
	n.Child(render.ListEl("exit-codes", "exit-code", exits...))

	tags := make([]*render.Node, 0, len(c.RequiresTags))
	for _, t := range c.RequiresTags {
		tags = append(tags, render.El("tag").SetText(t))
	}
	n.Child(render.ListEl("requires-tags", "tag", tags...))

	if c.Example != "" {
		n.Child(render.El("example").SetCDATA(c.Example))
	}
	return render.Record(KindCommand, VersionCommand, n)
}

func flagNode(f Flag) *render.Node {
	n := render.El("flag").
		Attr("name", f.Name).
		Attr("type", string(f.Type)).
		Attr("required", strconv.FormatBool(f.Required)).
		Attr("repeatable", strconv.FormatBool(f.Repeatable))
	n.AttrIf("short", f.Short)
	n.AttrIf("default", f.Default)
	n.Leaf("usage", f.Usage)
	if len(f.Enum) > 0 {
		vals := make([]*render.Node, 0, len(f.Enum))
		for _, v := range f.Enum {
			vals = append(vals, render.El("value").SetText(v))
		}
		n.Child(render.ListEl("values", "value", vals...))
	}
	return n
}

// ContractDoc renders the machine-readable contract: every output kind this
// build can emit, its schema version, the commands that emit it, and the exit
// codes and formats that are stable across all of them.
func ContractDoc(r *Registry) *render.Doc {
	kinds := r.Kinds()
	items := make([]*render.Node, 0, len(kinds))
	for _, k := range kinds {
		n := render.El("kind").
			Attr("name", k.Name).
			Attr("v", strconv.Itoa(k.Version)).
			Attr("emitters", strings.Join(k.Emitters, ","))
		items = append(items, n)
	}
	return render.List(KindContract, VersionContract, &render.Collection{
		Name:     "kinds",
		Items:    items,
		Complete: true,
		Columns: []render.Column{
			{Header: "kind", Path: "@name"},
			{Header: "v", Path: "@v"},
			{Header: "emitters", Path: "@emitters"},
		},
	})
}

// Filter returns the commands available in this build, optionally narrowed to
// those whose dotted name starts with prefix.
func Filter(cmds []*Command, prefix string) []*Command {
	if prefix == "" {
		return cmds
	}
	out := make([]*Command, 0, len(cmds))
	for _, c := range cmds {
		if c.Name() == prefix || strings.HasPrefix(c.Name(), prefix+".") {
			out = append(out, c)
		}
	}
	return out
}

// TagsNode renders the enabled build tags, shared by `jr version` and
// `jr schema`.
func TagsNode() *render.Node {
	enabled := buildinfo.Tags()
	tags := make([]*render.Node, 0, len(enabled))
	for _, t := range enabled {
		tags = append(tags, render.El("tag").
			Attr("name", t).
			SetText(buildinfo.TagDescriptions[t]))
	}
	return render.ListEl("tags", "tag", tags...)
}

// KnownTagsSorted returns the documented tags in alphabetical order, for usage
// strings.
func KnownTagsSorted() []string {
	out := slices.Clone(buildinfo.KnownTags)
	slices.Sort(out)
	return out
}
