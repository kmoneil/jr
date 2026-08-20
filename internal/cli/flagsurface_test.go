package cli_test

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/registry"

	// Every resource, so the sweep covers the shipped surface and not only the
	// built-ins.
	_ "github.com/kmoneil/jr/internal/commands"
)

// helpFlag is cobra's own, added to every command and declared by none.
const helpFlag = "help"

// flagLine matches a long flag at the head of a --help flag entry, with or
// without a short form in front of it.
var flagLine = regexp.MustCompile(`^\s+(?:-[a-zA-Z], )?--([a-z][a-z0-9-]*)`)

// TestEveryBoundFlagIsDeclared holds the binary's flags to the registry's, in
// the direction nothing else checks.
//
// Every other gate in this suite reads the declaration. contract_test asserts
// properties of declared flags, flageffect_test drives each declared flag and
// requires it to move something, and the command reference is rendered from the
// declaration and compared. All of them are blind to the same thing: a flag the
// binary accepts that the declaration never mentions. There is nothing to read,
// so there is nothing to check, and the flag is invisible to every consumer of
// the registry while working perfectly on the command line.
//
// That is not hypothetical. --limit was created directly on the cobra FlagSet
// whenever Command.Paginated was set, and synthesised a second time in the MCP
// input schema, and not at all in `jr schema` or the reference it generates. So
// 18 paginated commands accepted --limit, advertised it in --help, and denied
// it in the surface §7 tells an agent to trust instead of the prose. The MCP
// copy read DefaultLimit directly rather than the command's DefaultsToAll, so
// `jr schema` over MCP answered with 50 of 62 commands and complete="false":
// every sprint and user command invisible to an agent introspecting the binary
// through the interface built for agents.
//
// It reads --help rather than the cobra tree because --help is what a caller
// actually sees, and because a correct declaration can still render wrongly one
// layer down: `--if-unchanged` printed as `--if-unchanged jr issue get` from a
// usage string cobra reads for the argument name. Asserting the rendering is
// the only way that class is visible from here.
func TestEveryBoundFlagIsDeclared(t *testing.T) {
	commands := cli.Registry().All()
	if len(commands) == 0 {
		t.Fatal("the registry reports no commands at all")
	}

	var checked int
	for _, rc := range commands {
		t.Run(rc.Name(), func(t *testing.T) {
			args := append(strings.Fields(rc.UseLine()), "--help")
			got := run(t, nil, args...)
			if got.exit != 0 {
				t.Fatalf("%s --help exited %v: %s", rc.UseLine(), got.exit, got.stderr)
			}

			bound := boundFlags(got.stdout)
			if len(bound) == 0 && len(rc.AllFlags()) == 0 {
				return
			}
			declared := declaredFlags(rc)

			for _, name := range bound {
				if !contains(declared, name) {
					t.Errorf("--%s is accepted by `%s` and declared nowhere.\n"+
						"Every consumer of the registry is blind to it: `jr schema`, "+
						"docs/commands.md, and the MCP tool schema all describe a "+
						"command that does not have it.\n"+
						"Declare it in the registry, or derive it there the way "+
						"Command.AllFlags derives --limit from Paginated.",
						name, rc.UseLine())
				}
			}
			for _, name := range declared {
				if !contains(bound, name) {
					t.Errorf("--%s is declared by `%s` and not bound.\n"+
						"`jr schema` and docs/commands.md advertise a flag the "+
						"binary will refuse as unknown.",
						name, rc.UseLine())
				}
			}
		})
		checked++
	}

	// A sweep that reached nothing reports what a clean surface reports. The
	// count is against the registry rather than a constant, because a reduced
	// profile genuinely holds fewer commands; the floor only rules out a
	// registry that answered with almost nothing.
	if checked != len(commands) {
		t.Errorf("swept %d of %d commands", checked, len(commands))
	}
	if len(commands) < 30 {
		t.Fatalf("the registry reports %d commands, too few to be any profile",
			len(commands))
	}
}

// TestTheFlagSurfaceSweepCanFail drives the comparison against a command whose
// bound and declared sets are known to differ, so a green sweep means the
// comparison ran rather than that it could never disagree.
//
// Without it, a bug in boundFlags that returned nothing would pass every
// command in the tree.
func TestTheFlagSurfaceSweepCanFail(t *testing.T) {
	const help = `Flags:
  -a, --assignee string   assignee
      --limit string      maximum results
  -h, --help              help for list
`
	bound := boundFlags(help)
	want := []string{"assignee", "limit"}
	if len(bound) != len(want) {
		t.Fatalf("boundFlags parsed %v, want %v", bound, want)
	}
	for i := range want {
		if bound[i] != want[i] {
			t.Fatalf("boundFlags parsed %v, want %v", bound, want)
		}
	}
	if contains(bound, helpFlag) {
		t.Error("boundFlags kept cobra's own --help, which no command declares")
	}
}

// boundFlags returns the long flag names in the command's own Flags section,
// excluding cobra's --help and the persistent flags inherited from the root.
//
// Global Flags are declared on the root rather than per command, so a command's
// declaration is not expected to carry them.
func boundFlags(help string) []string {
	var out []string
	var inFlags bool
	for line := range strings.SplitSeq(help, "\n") {
		switch {
		case strings.HasPrefix(line, "Flags:"):
			inFlags = true
			continue
		case strings.HasPrefix(line, "Global Flags:"):
			inFlags = false
			continue
		case line != "" && !strings.HasPrefix(line, " "):
			inFlags = false
			continue
		}
		if !inFlags {
			continue
		}
		if m := flagLine.FindStringSubmatch(line); m != nil && m[1] != helpFlag {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func declaredFlags(rc *registry.Command) []string {
	out := make([]string, 0, len(rc.AllFlags()))
	for _, f := range rc.AllFlags() {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
