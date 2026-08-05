package jctx

import (
	"slices"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Environment variables that participate in resolution.
const (
	EnvContext  = "JIRA_CONTEXT"
	EnvSite     = "JIRA_SITE"
	EnvProject  = "JIRA_PROJECT"
	EnvBoard    = "JIRA_BOARD"
	EnvReadOnly = "JIRA_READONLY"
)

// Overrides are the per-invocation flag values that take precedence over the
// context. A zero value overrides nothing.
type Overrides struct {
	// Context names a one-off context, as --context does.
	Context string
	Site    string
	Project string
	Board   string
	Fields  []string
	// ReadOnly forces read-only mode on. It cannot force it off: see Resolved.
	ReadOnly bool
}

// Resolved is the effective configuration for one invocation.
type Resolved struct {
	// Name is the context these values came from, or empty when they came
	// only from flags and the environment.
	Name    string
	Site    string
	Project string
	Board   string
	Fields  []string

	// ReadOnly is true if any source asked for it.
	//
	// It is a one-way latch on purpose. A context created with --readonly is a
	// statement about what that context is for, and an invocation that simply
	// omits --readonly must not quietly promote itself to read-write. To make
	// a change, use a context that permits it.
	ReadOnly bool

	// CredentialRef is the key the credential store is asked for.
	CredentialRef string
}

// Resolve computes the effective settings.
//
// Precedence is flag, then environment, then context, then nothing. Each field
// resolves independently, so --project against a context that names a different
// one changes the project and nothing else.
func Resolve(cfg *Config, over Overrides, getenv Getenv) (*Resolved, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if cfg == nil {
		cfg = &Config{Contexts: map[string]Context{}}
	}

	name, ctx, err := selectContext(cfg, over, getenv)
	if err != nil {
		return nil, err
	}

	r := &Resolved{
		Name:    name,
		Site:    firstNonEmpty(over.Site, getenv(EnvSite), ctx.Site),
		Project: firstNonEmpty(over.Project, getenv(EnvProject), ctx.Project),
		Board:   firstNonEmpty(over.Board, getenv(EnvBoard), ctx.Board),
		// Read-only latches on from any source and never off.
		ReadOnly:      over.ReadOnly || truthy(getenv(EnvReadOnly)) || ctx.ReadOnly,
		CredentialRef: ctx.Credential,
	}

	switch {
	case len(over.Fields) > 0:
		r.Fields = slices.Clone(over.Fields)
	default:
		r.Fields = slices.Clone(ctx.Fields)
	}

	if r.Site != "" {
		normalized, err := NormalizeSite(r.Site)
		if err != nil {
			return nil, err
		}
		r.Site = normalized
	}
	if r.CredentialRef == "" {
		r.CredentialRef = Context{Site: r.Site}.Host()
	}
	return r, nil
}

// selectContext decides which context applies: --context, then JIRA_CONTEXT,
// then the current one. Naming a context that does not exist is always an
// error; having no context at all is not, because --site alone is a legitimate
// way to run a one-off command.
func selectContext(cfg *Config, over Overrides, getenv Getenv) (string, Context, error) {
	requested := firstNonEmpty(over.Context, getenv(EnvContext))
	if requested != "" {
		ctx, ok := cfg.Get(requested)
		if !ok {
			return "", Context{}, cfg.unknownContext(requested)
		}
		return requested, ctx, nil
	}
	if cfg.Current == "" {
		return "", Context{}, nil
	}
	ctx, ok := cfg.Get(cfg.Current)
	if !ok {
		// Load validates this, so reaching here means the config was mutated
		// in memory rather than read from disk.
		return "", Context{}, cfg.unknownContext(cfg.Current)
	}
	return cfg.Current, ctx, nil
}

// RequireSite returns the site, or a usage error naming what to do about it.
func (r *Resolved) RequireSite() (string, error) {
	if r != nil && r.Site != "" {
		return r.Site, nil
	}
	return "", errs.Usage("NO_SITE", "no Jira site configured").
		WithRemedy("pass --site, set JIRA_SITE, or create a context with " +
			"`jr context create <name> --site <host>`")
}

// RequireProject returns the project, or a usage error naming the flag.
//
// Project is never mandatory in general: it defaults from the context and can
// always be overridden or omitted. This is for the handful of commands that
// genuinely cannot proceed without one, and it exits 2 saying so rather than
// guessing at a default.
func (r *Resolved) RequireProject() (string, error) {
	if r != nil && r.Project != "" {
		return r.Project, nil
	}
	e := errs.Usage("NO_PROJECT", "this command needs a project and none is set")
	if r != nil && r.Name != "" {
		return "", e.
			WithDetail("context %q sets no default project", r.Name).
			WithRemedy("pass --project, or run `jr context create %s --project <KEY>` "+
				"to set a default", r.Name)
	}
	return "", e.WithRemedy("pass --project, set JIRA_PROJECT, or select a context that sets one")
}

// RequireBoard returns the board, or a usage error naming the flag.
func (r *Resolved) RequireBoard() (string, error) {
	if r != nil && r.Board != "" {
		return r.Board, nil
	}
	return "", errs.Usage("NO_BOARD", "this command needs a board and none is set").
		WithRemedy("pass --board, set JIRA_BOARD, or select a context that sets one")
}

// CheckWritable reports whether a mutating command may proceed.
//
// Read-only refuses before any network call, so a blocked command costs nothing
// and cannot half-happen. It governs changes to Jira; writing local config is
// still allowed, because a caller that could not create a context would have no
// way to configure itself at all.
func (r *Resolved) CheckWritable(command string) error {
	if r == nil || !r.ReadOnly {
		return nil
	}
	e := errs.Blocked("READ_ONLY", "%s would change Jira, and this is read-only", command)
	if r.Name != "" {
		return e.
			WithDetail("context %q is read-only", r.Name).
			WithRemedy("use a context that permits writes")
	}
	return e.
		WithDetail("read-only was requested by --readonly or %s", EnvReadOnly).
		WithRemedy("drop --readonly, or unset %s", EnvReadOnly)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// truthy reads an environment flag. An unset variable is false; anything else
// that is not an explicit falsehood is true, because `JIRA_READONLY=yes` from a
// caller who meant it must not silently mean read-write.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
