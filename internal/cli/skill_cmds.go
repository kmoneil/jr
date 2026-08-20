package cli

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// skillAssets holds the agent skill: the instructions an autonomous caller
// needs that the command declarations cannot carry.
//
// `jr schema` says what exists and what shape it is. It cannot say which of
// five near-synonymous filters answers the question that was asked, what to do
// with exit 3, or that a refusal is information rather than an obstacle to route
// around. That last one is the reason this ships at all: a model's default is to
// find a way past a blocker, and UNCONSTRAINED_QUERY "solved" with
// --jql 'project is not empty' is the whole-instance sweep the refusal exists to
// prevent.
//
//go:embed skillassets
var skillAssets embed.FS

const (
	skillRoot     = "skillassets"
	skillMain     = "SKILL.md"
	skillRefDir   = "references"
	commandsToken = "{{commands}}"
)

func (a *app) skillCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"skill"},
		Summary: "Print the agent skill for this build",
		Description: strings.TrimSpace(`
Writes the instructions an agent needs to drive this tool correctly, as
Markdown, to stdout.

With no argument it prints the skill itself. With a name it prints one
reference: workflows, failures, or gotchas.

It is generated against the registry of the binary that runs it, so the command
inventory it carries is what this build contains rather than what the project
has. A reader build's skill lists no mutating commands, because a reader build
holds none.

Install it wherever the agent reads skills from:

    ` + buildinfo.App + ` skill > .claude/skills/` + buildinfo.App + `/SKILL.md

The Markdown is the output, so nothing else is written to stdout: there is no
result envelope, and --format does not apply. It is deliberately in every
profile, including the smallest, because a build made for an agent is the one
that most needs to explain itself.`),
		Example: strings.Join([]string{
			buildinfo.App + " skill",
			buildinfo.App + " skill workflows",
		}, "\n"),
		Args: []registry.Arg{{
			Name:  "reference",
			Usage: "one of: " + strings.Join(skillReferences(), ", "),
		}},
		// The Markdown is the output, not a result document. An envelope around
		// it would produce something no skill loader can read.
		OwnsStdout: true,
		Run:        a.runSkill,
	}
}

func (a *app) runSkill(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	name := skillMain
	if len(inv.Args) > 0 {
		ref := inv.Args[0]
		if !contains(skillReferences(), ref) {
			return nil, errs.Usage("UNKNOWN_REFERENCE",
				"%q is not a reference this skill carries", ref).
				WithDetail("valid values: %s", strings.Join(skillReferences(), ", ")).
				WithRemedy("pass one of the above, or no argument for the skill itself")
		}
		name = path.Join(skillRefDir, ref+".md")
	}

	body, err := fs.ReadFile(skillAssets, path.Join(skillRoot, name))
	if err != nil {
		return nil, errs.Runtime("SKILL_UNREADABLE",
			"cannot read the embedded skill %s", name).Wrap(err)
	}

	out := strings.ReplaceAll(string(body), commandsToken, a.skillInventory())
	if _, err := fmt.Fprint(a.stdout, out); err != nil {
		return nil, errs.Runtime("SKILL_UNWRITABLE",
			"cannot write the skill").Wrap(err)
	}
	return nil, nil
}

// skillInventory renders this build's command list as a Markdown table.
//
// It is the one part of the skill that is generated rather than written, and it
// is what makes the document describe a binary instead of a project. The
// alternative, a hand-maintained list, is the second place to update that
// CLAUDE.md calls a bug: it would go stale on the next command and read as
// current forever.
func (a *app) skillInventory() string {
	commands := a.reg.All()
	rows := make([]string, 0, len(commands))
	for _, c := range commands {
		// Two independent facts, not a scale. `auth logout` is destructive and
		// does not touch Jira, so a switch that tested Destructive first would
		// report it as mutating and put the read-only gate on the wrong side of
		// the truth.
		var marks []string
		if c.Mutating {
			marks = append(marks, "M")
		}
		if c.Destructive {
			marks = append(marks, "D")
		}
		mark := ""
		if len(marks) > 0 {
			mark = "`" + strings.Join(marks, " ") + "`"
		}
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |",
			c.UseLine(), mark, c.Summary))
	}
	sort.Strings(rows)

	tags := buildinfo.Tags()
	list := "none"
	if len(tags) > 0 {
		list = strings.Join(tags, ", ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d commands, profile `%s`, tags `%s`.\n\n",
		len(commands), buildinfo.Profile(), list)
	b.WriteString("| Command | | Does |\n| --- | --- | --- |\n")
	b.WriteString(strings.Join(rows, "\n"))
	return b.String()
}

// skillReferences lists the bundled references, from the embedded tree rather
// than from a constant, so a reference added to skillassets is offered by
// --help and accepted by the command without a second edit.
func skillReferences() []string {
	entries, err := fs.ReadDir(skillAssets, path.Join(skillRoot, skillRefDir))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
