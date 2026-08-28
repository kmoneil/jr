package registry

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/render"
)

// Schema kinds and their versions. Bump a version in the same commit that
// changes the corresponding golden file.
const (
	KindCommands = "schema.commands"
	KindCommand  = "schema.command"
	KindContract = "contract"

	VersionCommands = 1
	// VersionCommand is 2 because a command now describes the global flags it
	// inherits, and which of them reach its result set. A consumer that pinned
	// v1 read a command's own flags and had no way to learn that --project
	// filters `issue activity`; v2 adds <global-flags>, and the block is not
	// optional, because a caller reading it to find out what can narrow their
	// results must be able to tell "nothing does" from "this build does not
	// say".
	VersionCommand = 2
	// VersionContract is 2 because each kind now carries its element schema.
	// A consumer that pinned v1 read a name, a version, and a list of
	// emitters; v2 adds the shape, which is the half §3.5 promised and the
	// first version could not deliver.
	VersionContract = 2
)

func init() {
	render.RegisterSchema(KindCommands, CommandSummarySchema())
	render.RegisterSchema(KindCommand, CommandSchema())
	render.RegisterSchema(KindContract, ContractSchema())
}

// CommandSummarySchema is the shape of one row of `jr schema`.
func CommandSummarySchema() *render.Schema {
	return &render.Schema{
		Element: "command",
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString},
			{Name: "use", Type: render.TypeString},
			{Name: "mutating", Type: render.TypeBool},
			{Name: "destructive", Type: render.TypeBool},
			{Name: "kind", Type: render.TypeString},
			{Name: "v", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.Leaf("summary", render.TypeString)},
		},
	}
}

// CommandSchema is the shape of `jr schema <command>`: everything the registry
// knows about one command.
func CommandSchema() *render.Schema {
	return &render.Schema{
		Element: "command",
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString},
			{Name: "use", Type: render.TypeString},
			{Name: "mutating", Type: render.TypeBool},
			{Name: "destructive", Type: render.TypeBool},
			{Name: "paginated", Type: render.TypeBool},
			{Name: "kind", Type: render.TypeString},
			{Name: "v", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: render.Leaf("summary", render.TypeString)},
			{Schema: render.Leaf("description", render.TypeString), Optional: true},
			{Schema: render.ListSchema("outputs", "output", &render.Schema{
				Element: "output",
				Attrs: []render.Field{
					{Name: "kind", Type: render.TypeString},
					{Name: "v", Type: render.TypeInt},
					// What selects this shape, for a command with more than one.
					{Name: "when", Type: render.TypeString, Optional: true},
				},
			})},
			{Schema: render.ListSchema("args", "arg", &render.Schema{
				Element: "arg",
				Attrs: []render.Field{
					{Name: "name", Type: render.TypeString},
					{Name: "required", Type: render.TypeBool},
					{Name: "variadic", Type: render.TypeBool},
				},
				Text: &render.Field{Type: render.TypeString},
			})},
			{Schema: render.ListSchema("flags", "flag", flagSchema())},
			{Schema: render.ListSchema("global-flags", "global-flag", globalFlagSchema())},
			{Schema: render.ListSchema("exit-codes", "exit-code", &render.Schema{
				Element: "exit-code",
				Attrs: []render.Field{
					{Name: "code", Type: render.TypeInt},
					{Name: "name", Type: render.TypeString},
				},
				Text: &render.Field{Type: render.TypeString},
			})},
			{Schema: render.ListSchema("requires-tags", "tag",
				render.Leaf("tag", render.TypeString))},
			{Schema: render.Leaf("example", render.TypeString), Optional: true},
		},
	}
}

func flagSchema() *render.Schema {
	return &render.Schema{
		Element: "flag",
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString},
			{Name: "type", Type: render.TypeString},
			{Name: "required", Type: render.TypeBool},
			{Name: "repeatable", Type: render.TypeBool},
			{Name: "short", Type: render.TypeString, Optional: true},
			{Name: "default", Type: render.TypeString, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("usage", render.TypeString)},
			{Schema: render.ListSchema("values", "value",
				render.Leaf("value", render.TypeString)), Optional: true},
		},
	}
}

// globalFlagSchema is a flag the root declares and this command inherits.
//
// It is the ordinary flag shape plus one attribute, and that attribute is the
// reason the block exists. Thirteen more flag names in front of an agent would
// be a worse answer than none: what it needs is which of them decide what the
// answer is, and `affects` is the only field here that is a property of the
// pair rather than of the flag.
func globalFlagSchema() *render.Schema {
	s := flagSchema()
	s.Element = "global-flag"
	s.Attrs = append(s.Attrs, render.Field{
		Name: "affects", Type: render.TypeString,
		Enum: []string{
			string(EffectResult), string(EffectProvenance),
			string(EffectInvocation),
		},
	})
	return s
}

// ContractSchema is the shape of one kind in `jr contract` — including, since
// v2, the shape of the kind itself.
func ContractSchema() *render.Schema {
	return &render.Schema{
		Element: "kind",
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString},
			{Name: "v", Type: render.TypeInt},
			{Name: "emitters", Type: render.TypeString},
		},
		Children: []render.Child{
			{Schema: elementSchema(), Optional: true},
		},
	}
}

// elementSchema is the shape of render.Schema's own rendering. It is recursive
// in the data and cannot be in the declaration, so nesting stops here: a
// consumer reads the top level from the contract and the rest by walking it.
func elementSchema() *render.Schema {
	return &render.Schema{
		Element: "element",
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString},
			{Name: "list-of", Type: render.TypeString, Optional: true},
			{Name: "optional", Type: render.TypeBool, Optional: true},
			{Name: "repeated", Type: render.TypeBool, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.ListSchema("attributes", "attribute", fieldShape("attribute"))},
			{Schema: fieldShape("text"), Optional: true},
			{Schema: &render.Schema{Element: "elements", ListOf: "element", Attrs: []render.Field{
				{Name: "count", Type: render.TypeInt},
			}, Extra: &render.Extra{
				Named: "element, recursively — this schema does not repeat itself",
				Type:  render.TypeString,
			}}},
			{Schema: &render.Schema{
				Element: "extra",
				Attrs:   []render.Field{{Name: "type", Type: render.TypeString}},
				Text:    &render.Field{Type: render.TypeString},
			}, Optional: true},
		},
	}
}

func fieldShape(element string) *render.Schema {
	return &render.Schema{
		Element: element,
		Attrs: []render.Field{
			{Name: "name", Type: render.TypeString, Optional: true},
			{Name: "type", Type: render.TypeString},
			{Name: "optional", Type: render.TypeBool},
			{Name: "enum", Type: render.TypeString, Optional: true},
		},
	}
}

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

	all := c.AllFlags()
	flags := make([]*render.Node, 0, len(all))
	for _, f := range all {
		flags = append(flags, flagNode(f))
	}
	n.Child(render.ListEl("flags", "flag", flags...))

	// The inherited half. It is a separate element rather than more <flag>
	// rows because the two are declared in different places and a caller may
	// reasonably want only the command's own; merging them would make
	// `flags count` a number that means something new, which is the kind of
	// silent redefinition this document exists to avoid.
	inherited := c.InheritedGlobals()
	globals := make([]*render.Node, 0, len(inherited))
	for _, f := range inherited {
		globals = append(globals,
			flagNodeAs("global-flag", f).
				Attr("affects", string(c.GlobalEffect(f.Name))))
	}
	n.Child(render.ListEl("global-flags", "global-flag", globals...))

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

func flagNode(f Flag) *render.Node { return flagNodeAs("flag", f) }

// flagNodeAs renders a flag under a given element name, so a global and a
// declared flag cannot describe themselves differently: one builder, two
// element names, and the `affects` attribute the caller adds afterwards is the
// only thing that separates them.
func flagNodeAs(element string, f Flag) *render.Node {
	n := render.El(element).
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
		// The shape, which is what makes this verifiable rather than only
		// pinnable. A kind with none is a bug the contract test catches; the
		// element is omitted rather than faked so the gap is visible here too.
		if s, ok := render.SchemaFor(k.Name); ok {
			n.Child(s.Node())
		}
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
