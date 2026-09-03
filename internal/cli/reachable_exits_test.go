package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
)

// A command's declared exits describe what its own body can produce. Some of
// what it produces is not its own body: internal/cli resolves the config, the
// context, and the credential before the body runs, and each of those fails
// with an exit the command never mentions.
//
// internal/lint's exit sweep cannot see any of it. It walks Validate, Run, and
// Stream as source, and these exits come from three method hops away inside
// internal/jctx and internal/auth — every hop a method call, which an AST walk
// does not follow. For a built-in it is worse: auth status's entry point is a
// method on *app, so the walk never enters the command.
//
// Three declarations were corrected by hand after somebody ran the binary and
// looked. That found `jql explain`, `auth status`, and `auth token` over
// --context, and it could not have found the fourth, because nobody had looked
// at the credential store: `auth logout` reads it to know what to delete, and
// STORE_PERMISSIONS is exit 4 against a declaration that stopped at 5 and 10.
//
// So these two tests drive every command through cli.Main — the layer a user
// actually invokes, which is the mcp serve lesson — and read what comes back.

// exitProbe is one sweep: a description of the broken state and the environment
// that produces it.
type exitProbe struct {
	name string
	// env prepares an XDG root in which every command should fail the same way.
	env func(t *testing.T) map[string]string
	// extra is appended to every command line.
	extra []string
	// want is the error code a command that reaches the broken layer produces.
	want string
	// exempt names each command that does NOT reach it, and why. A command
	// that starts reaching it must be deleted from here, so the list can only
	// shrink and cannot describe a state the tree has left.
	exempt map[string]string
}

// TestNoCommandExitsOutsideItsDeclaration is the claim itself, for both layers.
//
// It asserts the exit and not the code, because the exit is what a script
// branches on and what `AllExitCodes` publishes. The code is used only to tell
// "reached the broken layer" from "refused earlier for its own reasons", which
// is what the exempt lists are about.
func TestNoCommandExitsOutsideItsDeclaration(t *testing.T) {
	for _, probe := range exitProbes() {
		t.Run(probe.name, func(t *testing.T) {
			reached := 0
			for _, c := range cli.Registry().All() {
				if c.OwnsStdout {
					// A protocol server owns stdout and never returns a
					// document; mcp serve resolves per tool call and puts the
					// failure in the reply rather than in an exit.
					continue
				}
				env := probe.env(t)
				got := run(t, env, append(probeArgs(t, c, t.TempDir()), probe.extra...)...)
				code := errorCodeIn(got.stderr)

				if !slices.Contains(c.AllExitCodes(), got.exit) {
					t.Errorf("%s exits %d (%s) with %s and declares %s",
						c.Name(), got.exit, got.exit.Name(), code,
						formatCodes(c.AllExitCodes()))
					continue
				}
				if code == probe.want {
					reached++
					if why, ok := probe.exempt[c.Name()]; ok {
						t.Errorf("%s reaches %s now — delete its exemption "+
							"(%q), or the list describes a tree that has moved on",
							c.Name(), probe.want, why)
					}
					continue
				}
				if _, ok := probe.exempt[c.Name()]; !ok {
					t.Errorf("%s refused with %s before reaching %s, so this "+
						"asserts nothing about it. Give it arguments that get "+
						"further, or add it to the exempt list with the reason",
						c.Name(), code, probe.want)
				}
			}

			// A sweep that reached the broken layer for nothing would pass
			// while testing nothing, which is the failure mode every gate here
			// has had at least once.
			if reached < 25 {
				t.Errorf("only %d commands reached %s; the probe stopped "+
					"working rather than the tree getting better", reached, probe.want)
			}
			t.Logf("%d commands reached %s", reached, probe.want)
		})
	}
}

func exitProbes() []exitProbe {
	return []exitProbe{{
		name:  "an unknown context",
		env:   probeConfig,
		extra: []string{"--context", "nope"},
		want:  "UNKNOWN_CONTEXT",
		exempt: map[string]string{
			"version":  "self-description; it reads no config at all",
			"schema":   "self-description, from the registry in this binary",
			"contract": "self-description, from the registry in this binary",
			// Not a gap. `jr doctor` reports an unknown context as a failed
			// config check and exits 0, because a diagnostic that refused to
			// describe a broken configuration would refuse at the one moment it
			// is the only useful command left. TestDoctorReportsAnUnknownContext
			// asserts the verdict and the code that this probe would have read
			// off stderr.
			"doctor":         "reports the refusal as a check verdict rather than exiting on it",
			"context.list":   "lists what is defined, so an undefined name is the answer rather than an error",
			"context.create": "creates a context; the one being named does not have to exist yet",
			"auth.login":     "refuses with NO_TOKEN_SOURCE first; a token is required input and nothing blocks on a terminal",
			"auth.logout":    "--site is a required flag here, and an explicit site takes precedence over the context, so the name is never looked up",
		},
	}, {
		name:  "a credential store other users can read",
		env:   probeConfigWideStore,
		extra: nil,
		want:  "STORE_PERMISSIONS",
		exempt: map[string]string{
			"version":  "self-description; it reads no credential",
			"schema":   "self-description, from the registry in this binary",
			"contract": "self-description, from the registry in this binary",
			// The same reason as above: the unreadable store is a failed
			// credential check here, not an exit.
			"doctor":         "reports the refusal as a check verdict rather than exiting on it",
			"context.list":   "reads the config, never the store",
			"context.create": "writes the config, never the store",
			"context.edit":   "writes the config, never the store",
			"context.use":    "writes the config, never the store",
			"context.show":   "reads the config, never the store",
			"context.delete": "writes the config; the credential is deliberately left alone",
			"auth.login":     "refuses with NO_TOKEN_SOURCE first, before the store is opened",
			"jql.explain":    "builds the query locally and asks Jira nothing, so no credential is resolved",
			"issue.link.add": "refuses on its own arguments before a session is built",
			"epic.add":       "refuses on its own arguments before a session is built",
			"sprint.add":     "refuses on its own arguments before a session is built",
		},
	}}
}

// probeConfig is an XDG root holding one valid context, so a command that
// refuses has to be refusing the thing under test rather than an empty config.
func probeConfig(t *testing.T) map[string]string {
	t.Helper()
	env := isolate(t, nil)
	dir := filepath.Join(env["XDG_CONFIG_HOME"], "jr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	config := "current = \"real\"\n\n[contexts.real]\n" +
		"site = \"probe.atlassian.invalid\"\nproject = \"ENG\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return env
}

// probeConfigWideStore adds a credential store at 0644, which is refused on
// read because the file mode is the only thing standing between a token and
// every other account on the machine.
func probeConfigWideStore(t *testing.T) map[string]string {
	t.Helper()
	env := probeConfig(t)
	dir := filepath.Join(env["XDG_STATE_HOME"], "jr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := "[credentials.\"https://probe.atlassian.invalid\"]\n" +
		"user = \"ada\"\ntoken = \"not-a-real-token\"\n"
	if err := os.WriteFile(filepath.Join(dir, "credentials.toml"), []byte(store), 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}
	return env
}

// namedInput is what to pass for a positional argument, chosen by its declared
// name because every parser in the tree refuses a value of the wrong shape
// before any session is built.
//
// A single placeholder is not enough and the failure is silent in the direction
// that matters: a rejected argument is exit 2, which every command declares, so
// the sweep passes having asserted nothing. "eng-1" covers the 18 `key`
// arguments and is also the lowercase form a context name is held to; the rest
// are here because their parsers say so.
var namedInput = map[string]string{
	"id":   "1",
	"time": "1h",
}

// namedFlagInput is the same idea for a required *flag* whose value "1" is not.
//
// Every required flag used to take "1", which reaches Validate on a flag that
// wants a number or a name and is refused on one that wants a date — and then
// this probe reports the command as never having got near the layer it was
// breaking, which is true and says nothing about the layer.
var namedFlagInput = map[string]string{
	"since": "-1d",
}

// extraArgs is the handful whose required input cannot be derived from a name.
var extraArgs = map[string][]string{
	// An edit naming no field is refused as a no-op, before anything resolves,
	// and its key is here rather than derived because the argument stopped
	// being Required when --apply arrived: an apply takes its issues from the
	// plan, so probeArgs, which supplies only required positionals, now
	// supplies none and the run is refused with NO_ISSUES.
	"issue.edit": {"--summary", "probe", "eng-1"},
}

// probeArgs builds a command line that gets past cobra and past the command's
// own Validate, so the run reaches the layer being broken.
func probeArgs(t *testing.T, c *registry.Command, dir string) []string {
	t.Helper()
	args := slices.Clone(c.Path)
	for _, a := range c.Args {
		if !a.Required {
			break
		}
		switch {
		case a.Name == "file":
			args = append(args, probeFile(t, dir))
		case namedInput[a.Name] != "":
			args = append(args, namedInput[a.Name])
		default:
			args = append(args, "eng-1")
		}
	}
	for _, f := range c.Flags {
		if !f.Required {
			continue
		}
		value := "1"
		if named := namedFlagInput[f.Name]; named != "" {
			value = named
		}
		args = append(args, "--"+f.Name, value)
	}
	if c.Destructive {
		// Otherwise the confirmation gate answers first and nothing else runs.
		args = append(args, "--yes")
	}
	// A board is a context setting, and the agile commands refuse without one
	// before they resolve anything.
	args = append(args, "--board", "1")
	return append(args, extraArgs[c.Name()]...)
}

// probeFile is an existing file, for the one argument that must name one.
func probeFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "probe.txt")
	if err := os.WriteFile(path, []byte("probe\n"), 0o600); err != nil {
		t.Fatalf("write probe file: %v", err)
	}
	return path
}

// errorCodeIn pulls the code out of a structured error on stderr. Every format
// carries one; the tests run in the default, which is XML for a record.
func errorCodeIn(stderr string) string {
	const open, close = "<code>", "</code>"
	_, after, ok := strings.Cut(stderr, open)
	if !ok {
		return ""
	}
	rest := after
	before, _, ok := strings.Cut(rest, close)
	if !ok {
		return ""
	}
	return before
}

func formatCodes(codes []exitcode.Code) string {
	names := make([]string, 0, len(codes))
	for _, c := range codes {
		names = append(names, c.Name())
	}
	return strings.Join(names, ", ")
}
