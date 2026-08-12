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

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
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
	apiVersion  string
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

	// cleanup runs after the command, in order. It exists for the recorder,
	// whose output is not complete until the last request has been made.
	cleanup []func()
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
	for _, done := range a.cleanup {
		done()
	}
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
	e := a.explainCredential(a.explainSite(errs.Coerce(err)))
	format := a.errorFormat()
	if werr := render.WriteError(a.stderr, e, format); werr != nil {
		// The error renderer itself failed. Fall back to a plain line rather
		// than losing the failure entirely.
		_, _ = io.WriteString(a.stderr, e.Error()+"\n")
	}
	return e.Exit
}

// siteErrors are the failures where "which site was that" is the next question
// and the answer is not in the message.
//
// A NO_SUCH_ENDPOINT for a site you believe you configured is unresolvable
// without running `jr context show` to find out what the tool actually used —
// which cost several round trips during setup, and is exactly the kind of
// second command an error should make unnecessary.
var siteErrors = map[string]bool{
	"NO_SUCH_ENDPOINT":      true,
	"NETWORK":               true,
	"TIMEOUT":               true,
	"MALFORMED_SERVER_INFO": true,
	"UNKNOWN_DEPLOYMENT":    true,
	"OFF_SITE_URL":          true,
}

// credentialErrors are the failures where which credential was used is the
// thing a caller needs and cannot see.
var credentialErrors = map[string]bool{
	"UNAUTHORIZED":          true,
	"AUTH_SCHEME_REFUSED":   true,
	"AUTHENTICATION_DENIED": true,
}

// explainCredential adds where the credential came from to a failure about it.
//
// Three things can supply one, in precedence order — the environment, this
// tool's store, and .netrc — and the environment winning is the whole reason
// this is needed. A caller who has just run `auth login` successfully has a
// working credential in the store and can still be sending a different one from
// JIRA_API_TOKEN, which is not site-scoped and goes to whatever site is
// pointed at. The stock remedy for a 401 is "run `jr auth login`", which they
// did, seconds ago, and it changed nothing.
//
// A decoration, never a replacement: the refusal is still Jira's.
func (a *app) explainCredential(e *errs.Error) *errs.Error {
	if e == nil || !credentialErrors[e.Code] {
		return e
	}
	site, err := a.siteFor("")
	if err != nil {
		return e
	}
	cred, found, err := a.chain().Lookup(site)
	if err != nil || !found {
		return e
	}

	origin := "the credential came from " + cred.Source
	if strings.Contains(e.Detail, origin) {
		return e
	}
	if e.Detail == "" {
		e = e.WithDetail("%s", origin)
	} else {
		e = e.WithDetail("%s; %s", e.Detail, origin)
	}

	// The environment is the one that surprises people, because it wins over a
	// credential they deliberately stored and it is not scoped to a site.
	if cred.Source == auth.EnvToken {
		return e.WithRemedy("%s overrides the credential store and is not tied "+
			"to a site, so it is sent to whatever site you point at. Unset it "+
			"to use the one `%s auth login` stored, or run `%s auth status` to "+
			"see which credential a site would use",
			auth.EnvToken, buildinfo.App, buildinfo.App)
	}
	return e
}

// explainSite adds where the site came from to a failure about reaching it.
//
// Three things can supply a site — --site, JIRA_SITE, a context — and which one
// won is visible nowhere else. Resolving here rather than at the point of
// failure means every command gets it, including the requests that never pass
// through the connection code.
func (a *app) explainSite(e *errs.Error) *errs.Error {
	if e == nil || !siteErrors[e.Code] {
		return e
	}
	cfg, err := a.config()
	if err != nil {
		// The config is why we are here, or it is broken too. Either way this
		// is a decoration, not the failure, and it does not get to replace one.
		return e
	}
	resolved, err := a.resolve(cfg)
	if err != nil {
		return e
	}
	origin := resolved.SiteOrigin()
	if origin == "" || strings.Contains(e.Detail, origin) {
		return e
	}

	if e.Detail == "" {
		return e.WithDetail("%s", origin)
	}
	return e.WithDetail("%s; %s", e.Detail, origin)
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
