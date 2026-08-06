// Package cli turns the command registry into a runnable program.
//
// It owns three things no other package touches: the cobra tree, the
// stdout/stderr split, and the process exit code. stdout carries the result
// document and nothing else — no spinner, no warning, no "Fetching…". Every
// diagnostic is structured and goes to stderr.
package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// EnvFormat overrides the default output format for every command.
const EnvFormat = "JIRA_FORMAT"

// app is the mutable state of one invocation. It exists so Main is a pure
// function of its arguments and environment, and the whole program is testable
// without touching os.Stdout or os.Exit.
type app struct {
	reg    *registry.Registry
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
	getenv func(string) string

	// env holds the config file, credential store, and XDG paths, resolved
	// once per invocation.
	env environment

	// Global flag values, shared by every command.
	contextName string
	site        string
	project     string
	board       string
	readOnly    bool
	debug       bool
	refresh     bool
	retries     int
	maxRequests int

	// requestedFormat is the --format value, or the JIRA_FORMAT value, or
	// empty when the caller expressed no preference and the per-content
	// default applies.
	requestedFormat string
	describe        bool
	contract        bool

	// exit is the status the run resolved to.
	exit exitcode.Code
}

// Options configures a Main run. The zero value uses the default registry,
// os.Stdout, os.Stderr, and os.Getenv.
type Options struct {
	// Registry supplies the resource commands. It defaults to
	// registry.Default, which the tag-gated resource packages populate at init.
	// The built-in commands are added on top of it either way.
	Registry *registry.Registry
	Stdout   io.Writer
	Stderr   io.Writer
	// Stdin supplies a credential to `auth login --token-stdin`. Nothing else
	// reads it, and no command ever blocks on it: a headless build has no
	// prompt to fall back to, so an empty stdin is an error rather than a wait.
	Stdin  io.Reader
	Getenv func(string) string
}

// Main runs one invocation and returns the exit status. It never calls
// os.Exit, so tests can assert on the code.
func Main(ctx context.Context, args []string, opt Options) exitcode.Code {
	a := &app{
		stdout: orElse[io.Writer](opt.Stdout, os.Stdout),
		stderr: orElse[io.Writer](opt.Stderr, os.Stderr),
		stdin:  orElse[io.Reader](opt.Stdin, os.Stdin),
		getenv: opt.Getenv,
	}
	if a.getenv == nil {
		a.getenv = os.Getenv
	}
	a.reg = a.buildRegistry(orElse(opt.Registry, registry.Default))

	root := a.newRoot()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err != nil {
		return a.fail(err)
	}
	return a.exit
}

// orElse returns v when it is non-zero, else fallback.
func orElse[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

// Registry returns the full command surface this build exposes: the resource
// commands plus the built-ins. It is what `jr schema` reports, and what the
// contract tests assert against.
func Registry() *registry.Registry {
	a := &app{}
	a.reg = a.buildRegistry(registry.Default)
	return a.reg
}

// buildRegistry returns a fresh registry holding the resource commands from
// base plus this invocation's built-ins. It never mutates base, so a test can
// run Main twice in one process without the second run tripping over the
// first's registrations.
func (a *app) buildRegistry(base *registry.Registry) *registry.Registry {
	r := registry.New()
	for _, c := range base.All() {
		r.Register(c)
	}
	for _, c := range a.builtins() {
		r.Register(c)
	}
	return r
}

// fail renders a structured error to stderr and returns its exit status.
func (a *app) fail(err error) exitcode.Code {
	if errors.Is(err, context.Canceled) {
		err = errs.Runtime("CANCELED", "canceled").Wrap(err)
	}
	e := errs.Coerce(err)
	format := a.errorFormat()
	if werr := render.WriteError(a.stderr, e, format); werr != nil {
		// The error renderer itself failed. Fall back to a plain line rather
		// than losing the failure entirely.
		_, _ = io.WriteString(a.stderr, e.Error()+"\n")
	}
	return e.Exit
}

// errorFormat resolves the format for a diagnostic. A bad --format value must
// still produce a readable error, so an unparseable request falls back to the
// record default rather than failing twice.
func (a *app) errorFormat() render.Format {
	if a.requestedFormat == "" {
		return render.DefaultFor(nil)
	}
	f, err := render.ParseFormat(a.requestedFormat)
	if err != nil {
		return render.DefaultFor(nil)
	}
	return f
}

// resolveFormat picks the output format for a result: the explicit request if
// there is one, otherwise the per-content default — TSV for collections, XML
// for records and documents.
func (a *app) resolveFormat(d *render.Doc) (render.Format, error) {
	if a.requestedFormat == "" {
		return render.DefaultFor(d), nil
	}
	return render.ParseFormat(a.requestedFormat)
}

// emit writes a result document to stdout and records the exit status. A
// truncated result also produces a structured warning on stderr, so a script
// that checks $? cannot miss it.
func (a *app) emit(d *render.Doc) error {
	format, err := a.resolveFormat(d)
	if err != nil {
		return err
	}
	if err := render.Write(a.stdout, d, format); err != nil {
		return err
	}
	if !d.IsComplete() {
		if err := render.WriteTruncationWarning(a.stderr, d, format); err != nil {
			return err
		}
		a.exit = exitcode.Partial
	}
	return nil
}

// usageError reports a malformed invocation, naming the command so the message
// is actionable without the caller re-reading their shell history.
func usageError(cmd *cobra.Command, format string, args ...any) *errs.Error {
	return errs.Usage("INVALID_USAGE", format, args...).
		WithRemedy("run `%s --help`", strings.TrimSpace(cmd.CommandPath()))
}
