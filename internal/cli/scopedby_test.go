package cli_test

import (
	"context"
	"io"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"

	// Every resource, so the sweep covers the shipped surface.
	_ "github.com/kmoneil/jr/internal/commands"
)

// TestScopedByMatchesWhatTheCommandReads holds every Command.ScopedBy to what
// the command actually asks its session for, in both directions.
//
// ScopedBy is what `jr schema <command>` reports as affects="result", and it is
// the answer to the question that produced this whole change: which of the
// thirteen inherited globals silently decide what my result set is. A
// declaration nobody checks would be a worse answer than the silence it
// replaces, because it would be believed.
//
// It observes rather than derives. The session is an interface
// (registry.Session), so a command that reads the context scope has to call
// Project, RequireProject, Board or RequireBoard to get it, and a recorder in
// front of the real fake sees every one. That is deliberately not a scan of
// which files mention inv.Jira.Project(): a helper shared by four commands is
// one call site and four different answers, and `_plans/progress.md` has the
// entry about a check that derived its subject from where the code currently
// lived and could not see the code move.
func TestScopedByMatchesWhatTheCommandReads(t *testing.T) {
	requireNothingIsWritten(t)

	var checked int
	for _, c := range cli.Registry().All() {
		if commandNotDriven(c) != "" {
			continue
		}
		checked++
		t.Run(c.Name(), func(t *testing.T) {
			read := driveForScope(t, c)
			declared := slices.Clone(c.ScopedBy)
			sort.Strings(declared)

			// A scope the command declares a flag of its own for is not
			// reached by the global: cobra routes the value to the local flag
			// and omits the global from --help, and InheritedGlobals omits it
			// from the schema for the same reason. `meta createmeta` is the
			// one such collision in the tree, and it genuinely does read the
			// context's project — through its own --project, which `jr schema`
			// already lists among its declared flags.
			read = slices.DeleteFunc(read, func(name string) bool {
				_, shadowed := c.Flag(name)
				return shadowed
			})

			for _, name := range read {
				if slices.Contains(declared, name) {
					continue
				}
				t.Errorf("%s reads the context's %s and does not declare it in "+
					"ScopedBy.\n"+
					"`jr schema %s` therefore reports --%s as affects=%q, which "+
					"tells a caller the flag cannot change this command's rows. "+
					"It can.\n"+
					"Add %q to ScopedBy.",
					c.Name(), name, c.Name(), name,
					registry.EffectInvocation, name)
			}
			for _, name := range declared {
				if slices.Contains(read, name) {
					continue
				}
				if why := scopeReadOnlyOnSomePath[c.Name()+"/"+name]; why != "" {
					continue
				}
				t.Errorf("%s declares --%s in ScopedBy and never asks the "+
					"session for it.\n"+
					"`jr schema %s` reports affects=%q for a flag that changes "+
					"nothing here, which is the same defect as the silence it "+
					"replaced, pointing the other way.\n"+
					"Drop it from ScopedBy, or name %q in "+
					"scopeReadOnlyOnSomePath with the path that reads it.",
					c.Name(), name, c.Name(), registry.EffectResult,
					c.Name()+"/"+name)
			}
		})
	}

	// A sweep that reached nothing reports what a correct tree reports.
	if checked < 20 {
		t.Fatalf("only %d commands were driven; this build cannot show the "+
			"rule holding across the surface", checked)
	}
	t.Logf("checked ScopedBy against observed reads on %d commands", checked)
}

// scopeReadOnlyOnSomePath excuses a declaration this harness cannot observe,
// because the read sits behind a branch the harness does not take.
//
// It only ever excuses the *declared and not read* direction. The other
// direction — a command that reads a scope and does not declare it — is the
// defect this test exists for and has no ledger.
var scopeReadOnlyOnSomePath = map[string]string{}

// TestTheScopeSweepCanFail is the question this sweep has to answer about
// itself: a recorder that saw nothing would report every command's declaration
// correct, including the wrong ones.
//
// Two controls. A command that is known to read the project scope must be
// observed reading it, and a command built here to read nothing must be
// observed reading nothing.
func TestTheScopeSweepCanFail(t *testing.T) {
	requireNothingIsWritten(t)

	reads := &registry.Command{
		Path:      []string{"probe", "scoped"},
		Summary:   "A command that asks its session for the project",
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: "probe.scoped", Version: 1}},
		Run: func(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
			return render.Record("probe.scoped", 1,
				render.El("probe").Attr("project", inv.Jira.Project())), nil
		},
	}
	if got := driveForScope(t, reads); !slices.Contains(got, registry.GlobalProject) {
		t.Errorf("a command that calls Project() was observed reading %v, so "+
			"this sweep cannot tell a scoped command from an unscoped one", got)
	}

	inert := &registry.Command{
		Path:      []string{"probe", "unscoped"},
		Summary:   "A command that asks its session for nothing",
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: "probe.unscoped", Version: 1}},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			return render.Record("probe.unscoped", 1, render.El("probe")), nil
		},
	}
	if got := driveForScope(t, inert); len(got) != 0 {
		t.Errorf("a command that asks the session for nothing was observed "+
			"reading %v, so every command passes as scoped", got)
	}
}

// driveForScope runs one command against a recording session and reports which
// context scopes it asked for, sorted.
//
// It ignores the command's outcome entirely. A refusal is a fine result here:
// what is being observed is whether the scope was consulted on the way to it,
// and a command that reads the project and then fails on a fixture this
// harness cannot supply has still read the project.
func driveForScope(t *testing.T, c *registry.Command) []string {
	t.Helper()

	// Twice: once as the flag sweep drives it, and once with the *optional*
	// positionals left off. A scope read can sit behind an absent argument —
	// `project get` takes the key from argv and falls back to the context's
	// project only when there is none — and a single pass supplying every
	// probe argument never reaches it. The union is what the command can read.
	//
	// Required positionals are supplied in both passes. Dropping them is not a
	// harsher test, it is a different one: `issue edit` indexes Args[0] in its
	// Validate, which is correct because the CLI refuses a missing required
	// argument before Validate runs, and driving it without one asks a
	// question no caller can ask.
	return union(driveOnceForScope(t, c, true), driveOnceForScope(t, c, false))
}

func union(sets ...[]string) []string {
	var out []string
	for _, s := range sets {
		out = append(out, s...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func driveOnceForScope(t *testing.T, c *registry.Command, withOptionalArgs bool) []string {
	t.Helper()

	rt := &recordingTransport{kind: site.DataCenter}
	session := &scopeRecorder{inner: sweepSession{rt: rt, kind: site.DataCenter}}

	flags := registry.NewFlags()
	for _, f := range c.AllFlags() {
		// Preview rather than write, exactly as the flag-effect sweep does.
		if f.Name == "dry-run" || f.Name == "yes" {
			flags.SetBool(f.Name, true)
			continue
		}
		if f.Required {
			if v, _, ok := probePair(f); ok {
				setProbe(flags, f, v)
			}
		}
	}
	for name, value := range companionFlags[c.Name()] {
		flags.SetString(name, value)
	}

	var args []string
	for _, a := range c.Args {
		if !withOptionalArgs && !a.Required {
			continue
		}
		if v := argProbe(c, a); v != "" {
			args = append(args, v)
		}
	}

	inv := &registry.Invocation{
		Jira:     session,
		Args:     args,
		Flags:    flags,
		Limit:    sweepLimit(t, c, flags),
		Format:   render.XML,
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}

	var out strings.Builder
	_ = runFor(t.Context(), c, inv, &out)

	got := slices.Clone(session.read)
	sort.Strings(got)
	return slices.Compact(got)
}

// scopeRecorder wraps the sweep's session and notes which context scopes were
// asked for.
//
// It records on the *call*, not on the value: a command that asks for the
// project and gets an empty string has still been decided by --project, and
// recording only non-empty answers would report `issue list --all-projects` as
// unscoped, which is true of that invocation and false of the command.
type scopeRecorder struct {
	inner sweepSession
	read  []string
}

func (s *scopeRecorder) note(name string) { s.read = append(s.read, name) }

func (s *scopeRecorder) Project() string {
	s.note(registry.GlobalProject)
	return s.inner.Project()
}

func (s *scopeRecorder) RequireProject() (string, error) {
	s.note(registry.GlobalProject)
	return s.inner.RequireProject()
}

func (s *scopeRecorder) Board() string {
	s.note(registry.GlobalBoard)
	return s.inner.Board()
}

func (s *scopeRecorder) RequireBoard() (string, error) {
	s.note(registry.GlobalBoard)
	return s.inner.RequireBoard()
}

func (s *scopeRecorder) Connect(ctx context.Context) (*transport.Client, site.Info, error) {
	return s.inner.Connect(ctx)
}

func (s *scopeRecorder) Metadata(ctx context.Context) (*site.Metadata, error) {
	return s.inner.Metadata(ctx)
}

func (s *scopeRecorder) CheckWritable(v string) error { return s.inner.CheckWritable(v) }
func (s *scopeRecorder) Site() string                 { return s.inner.Site() }
func (s *scopeRecorder) Idempotency() *idem.Ledger    { return s.inner.Idempotency() }
func (s *scopeRecorder) Fields() []string             { return s.inner.Fields() }

// TestAnEmptyScopeIsRefused holds `--project ""` and `--board ""` to
// EMPTY_SCOPE.
//
// An empty scope does not lift the scope. It falls back to the context's, by
// design and with a comment saying so in internal/resource/issue, so a caller
// reaching for it as an escape hatch gets a query against a project they did
// not name and a complete, empty, exit-0 result. That is the tool's own
// silence, manufactured out of a flag value nobody means, and it is what an
// agent reviewing jr 0.9.3 tried before going four more turns without an
// answer.
func TestAnEmptyScopeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"project", []string{"--project", "", "issue", "list"}},
		{"board", []string{"--board", "", "sprint", "list"}},
		{"equals form", []string{"--project=", "issue", "list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, nil, tc.args...)
			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want %v\nstdout: %s\nstderr: %s",
					got.exit, exitcode.Usage, got.stdout, got.stderr)
			}
			if !strings.Contains(got.stderr, "EMPTY_SCOPE") {
				t.Errorf("stderr does not carry EMPTY_SCOPE:\n%s", got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a refused invocation wrote to stdout:\n%s", got.stdout)
			}
		})
	}
}

// TestAScopeThatIsGivenAValueIsNotRefused is the other half, and the half that
// makes the check above worth having.
//
// A guard that refused every --project would pass the test above and break the
// tool. And the environment is deliberately outside it: an exported-but-empty
// JIRA_PROJECT is a shell's configuration, not a request, and refusing it would
// make an environment variable a landmine — the reasoning --format already
// follows in PersistentPreRunE.
func TestAScopeThatIsGivenAValueIsNotRefused(t *testing.T) {
	got := run(t, nil, "--project", "ENG", "issue", "list")
	if strings.Contains(got.stderr, "EMPTY_SCOPE") {
		t.Errorf("--project ENG was refused as an empty scope:\n%s", got.stderr)
	}

	got = run(t, map[string]string{"JIRA_PROJECT": ""}, "issue", "list")
	if strings.Contains(got.stderr, "EMPTY_SCOPE") {
		t.Errorf("an empty JIRA_PROJECT was refused; the guard reads the "+
			"flag, not the environment:\n%s", got.stderr)
	}
}
