// Package registry holds the one description of every command, from which the
// cobra tree, the MCP tool list, and `jr schema` are all generated.
//
// Registration is tag-gated: a command that needs a build tag lives in a file
// carrying that tag, so a build without the tag does not merely refuse the
// command — it does not contain it. See docs/build-profiles.md.
package registry

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Registry is a set of commands addressed by dotted name.
type Registry struct {
	byName map[string]*Command
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{byName: map[string]*Command{}}
}

// Default is the registry the binary builds its command tree from. Command
// packages register into it from an init function in a tag-gated file.
var Default = New()

// Register adds a command. A duplicate name is a programming error and panics
// at init, which is the only time it can happen.
func (r *Registry) Register(c *Command) {
	if c == nil {
		panic("registry: nil command")
	}
	if len(c.Path) == 0 {
		panic("registry: command has no path")
	}
	name := c.Name()
	if _, dup := r.byName[name]; dup {
		panic(fmt.Sprintf("registry: duplicate command %q", name))
	}
	r.byName[name] = c
}

// Register adds a command to the default registry.
func Register(c *Command) { Default.Register(c) }

// Lookup returns the command with the given dotted name.
func (r *Registry) Lookup(name string) (*Command, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// Lookup returns the command with the given dotted name from the default
// registry.
func Lookup(name string) (*Command, bool) { return Default.Lookup(name) }

// All returns every registered command, ordered by path so output is stable.
func (r *Registry) All() []*Command {
	out := make([]*Command, 0, len(r.byName))
	for _, c := range r.byName {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *Command) int {
		return slices.CompareFunc(a.Path, b.Path, cmp.Compare)
	})
	return out
}

// All returns every command in the default registry.
func All() []*Command { return Default.All() }

// Len returns the number of registered commands.
func (r *Registry) Len() int { return len(r.byName) }

// Names returns every registered dotted name, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Kind describes one output payload shape and the commands that emit it.
type Kind struct {
	Name     string
	Version  int
	Emitters []string
}

// Kinds returns every output kind this build can emit, sorted by name. It backs
// `jr --contract`, so a consumer can pin the shapes it parses and verify them.
//
// A kind declared at two different versions by two commands is a contract
// violation, so the higher version wins here and the registry test fails.
func (r *Registry) Kinds() []Kind {
	byKind := map[string]*Kind{}
	for _, c := range r.All() {
		for _, o := range c.Outputs {
			k, ok := byKind[o.Kind]
			if !ok {
				k = &Kind{Name: o.Kind, Version: o.Version}
				byKind[o.Kind] = k
			}
			k.Version = max(k.Version, o.Version)
			if !slices.Contains(k.Emitters, c.Name()) {
				k.Emitters = append(k.Emitters, c.Name())
			}
		}
	}
	out := make([]Kind, 0, len(byKind))
	for _, k := range byKind {
		slices.Sort(k.Emitters)
		out = append(out, *k)
	}
	slices.SortFunc(out, func(a, b Kind) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Kinds returns every output kind in the default registry.
func Kinds() []Kind { return Default.Kinds() }
