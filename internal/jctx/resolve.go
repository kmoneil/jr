package jctx

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
)

// Environment variables that participate in resolution.
const (
	EnvContext  = "JIRA_CONTEXT"
	EnvSite     = "JIRA_SITE"
	EnvProject  = "JIRA_PROJECT"
	EnvBoard    = "JIRA_BOARD"
	EnvReadOnly = "JIRA_READONLY"
	// EnvAPIVersion forces the REST version for every command in a shell, which
	// is the only bearable way to work with an instance whose probe cannot run.
	EnvAPIVersion = "JIRA_API_VERSION"
	// EnvCABundle is --ca-bundle for a whole shell, which is what a CI job
	// wants: the bundle is a property of the network the job runs on rather
	// than of any one command.
	EnvCABundle = "JIRA_CA_BUNDLE"
	// EnvTZ is --tz for a whole shell: the zone timestamps are written in.
	EnvTZ = "JIRA_TZ"
)

// Overrides are the per-invocation flag values that take precedence over the
// context. A zero value overrides nothing.
type Overrides struct {
	// Context names a one-off context, as --context does.
	Context string
	Site    string
	// Projects is every --project given, in the order given. One is the usual
	// case. Several is a set, which only the commands that answer for a union
	// of projects accept.
	Projects []string
	Board    string
	// ReadOnly forces read-only mode on. It cannot force it off: see Resolved.
	ReadOnly bool
	// APIVersion forces the REST version, skipping the deployment probe.
	APIVersion string
	// CABundle is --ca-bundle, overriding the context's.
	//
	// There is no override for the client certificate: a certificate and its
	// key are two paths that have to agree, and a flag that set one of them
	// while the context supplied the other is a combination nobody meant. They
	// are a context setting only.
	CABundle string
}

// Resolved is the effective configuration for one invocation.
type Resolved struct {
	// Name is the context these values came from, or empty when they came
	// only from flags and the environment.
	Name string
	Site string
	// Project is the project, when the scope is exactly one. It is empty when
	// there is none and when there are several: a caller that needs one asks
	// RequireProject, which refuses a set by name rather than picking a member.
	Project string
	// Projects is the whole project scope: every --project given, or the one
	// the environment or the context supplies, or nothing.
	Projects []string
	Board    string
	Fields   []string

	// ReadOnly is true if any source asked for it.
	//
	// It is a one-way latch on purpose. A context created with --readonly is a
	// statement about what that context is for, and an invocation that simply
	// omits --readonly must not quietly promote itself to read-write. To make
	// a change, use a context that permits it.
	ReadOnly bool

	// ReadOnlySource says which source latched ReadOnly on, or is empty when
	// the invocation is not read-only.
	//
	// ReadOnly is an OR over three sources rather than a precedence chain, so
	// sourceOf cannot answer this. What a caller needs when a write is refused
	// is which of the three to go and change, and only one of them is a file
	// somebody would think to look in.
	ReadOnlySource Source

	// CredentialRef is the key the credential store is asked for.
	CredentialRef string

	// APIVersion is the REST version an operator forced, or zero for the
	// probe's answer. See site.Declare for why a declaration is not a guess.
	APIVersion int

	// CABundle, ClientCert and ClientKey are the TLS settings in effect. See
	// Context for why they are paths and why they are per context.
	CABundle   string
	ClientCert string
	ClientKey  string

	// CABundleSource says where CABundle came from, on the same terms as
	// SiteSource: a chain that fails to verify is the least searchable failure
	// this tool has, and "which bundle, from where" is the first question.
	CABundleSource Source

	// SiteSource says where Site came from, so an error about it can be acted
	// on without a second command.
	//
	// "the site is not reachable" is unresolvable on its own when three things
	// could have supplied it. Precedence is flag, then environment, then
	// context, and which one won is not visible anywhere else.
	SiteSource Source

	// TZ is the zone timestamps are written in when an invocation names none,
	// as the environment or the context asked for it: `utc`, `account`,
	// `local`, or an IANA name. Empty means neither did, which is UTC.
	//
	// There is no override for it here, unlike Site and Board. --tz is not a
	// global: it exists only on a command that writes a timestamp, and is
	// read there, over whatever this resolves to.
	//
	// It is the request and not a location. Whether a name is a zone, and what
	// `account` resolves to, are settled where the zone database and the
	// session are, which is not here: this package reads a file and the
	// environment and reaches nothing else.
	TZ string
	// TZSource says where TZ came from, so a refusal of it can name the place
	// to change. A zone that stopped resolving is far likelier to be a line in
	// a context than something typed on this invocation.
	TZSource Source
}

// Source names where a resolved value came from.
type Source string

// The places a setting can come from, in precedence order.
const (
	FromFlag    Source = "flag"
	FromEnv     Source = "environment"
	FromContext Source = "context"
	FromNowhere Source = ""
)

// SiteOrigin renders where the site came from, for an error message.
//
// It returns the empty string when nothing supplied one, because "no site is
// configured" already says everything there is to say.
func (r *Resolved) SiteOrigin() string {
	if r == nil {
		return ""
	}
	switch r.SiteSource {
	case FromFlag:
		return "the site came from --site"
	case FromEnv:
		return "the site came from " + EnvSite
	case FromContext:
		if r.Name != "" {
			return fmt.Sprintf("the site came from context %q", r.Name)
		}
		return "the site came from the selected context"
	case FromNowhere:
		return ""
	}
	return ""
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

	projects := resolveProjects(over.Projects, getenv(EnvProject), ctx.Project)
	r := &Resolved{
		Name:       name,
		Site:       firstNonEmpty(over.Site, getenv(EnvSite), ctx.Site),
		SiteSource: sourceOf(over.Site, getenv(EnvSite), ctx.Site),
		Project:    onlyProject(projects),
		Projects:   projects,
		Board:      firstNonEmpty(over.Board, getenv(EnvBoard), ctx.Board),
		// Read-only latches on from any source and never off.
		ReadOnly:       over.ReadOnly || truthy(getenv(EnvReadOnly)) || ctx.ReadOnly,
		ReadOnlySource: readOnlySource(over.ReadOnly, getenv(EnvReadOnly), ctx.ReadOnly),
		CredentialRef:  ctx.Credential,

		CABundle:       firstNonEmpty(over.CABundle, getenv(EnvCABundle), ctx.CABundle),
		CABundleSource: sourceOf(over.CABundle, getenv(EnvCABundle), ctx.CABundle),
		ClientCert:     ctx.ClientCert,
		ClientKey:      ctx.ClientKey,

		TZ:       firstNonEmpty(getenv(EnvTZ), ctx.TZ),
		TZSource: sourceOf("", getenv(EnvTZ), ctx.TZ),
	}

	// Fields come from the context and from nowhere else at this layer.
	//
	// There was a precedence rule here — a flag's fields replaced the context's
	// — and nothing could reach it: no persistent --field exists, so
	// Overrides.Fields was never populated by any caller. Only a test
	// constructed one, which is what made the branch look alive.
	//
	// The rule was also the wrong one. `--field` is per-command and additive,
	// so an ad-hoc field adds to the context's set rather than replacing it;
	// the union happens where the request is built, because that is the only
	// layer that knows which command asked. See validateFields in
	// internal/resource/issue.
	r.Fields = slices.Clone(ctx.Fields)

	if v := firstNonEmpty(over.APIVersion, getenv(EnvAPIVersion)); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, errs.Usage("INVALID_API_VERSION",
				"--api-version accepts 2 or 3, not %q", v).
				WithRemedy("omit it and let the deployment probe decide")
		}
		if n != 2 && n != 3 {
			// Checked here rather than by calling into site, which would add a
			// package dependency for a two-value range. site.Declare checks it
			// again for its own callers; the mapping from version to deployment
			// still lives there and only there.
			return nil, errs.Usage("INVALID_API_VERSION",
				"--api-version accepts 2 or 3, not %d", n).
				WithDetail("Cloud serves v3; Data Center serves v2").
				WithRemedy("omit it and let the deployment probe decide")
		}
		r.APIVersion = n
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
//
// The remedy leads with `auth login` because this is the first error a new
// caller sees, and it is the only one of the three that leaves them able to run
// the next command. `context create` answers the error — a context now has a
// site — and not the goal: the credential is still missing, so the very next
// invocation fails with NO_CREDENTIALS. `auth login --site` stores the
// credential and creates the first context in one step.
//
// The two overrides stay, and stay second. Somebody with JIRA_API_TOKEN already
// exported needs a site and nothing else, and telling them to log in again
// would be the same unhelpfulness pointing the other way.
func (r *Resolved) RequireSite() (string, error) {
	if r != nil && r.Site != "" {
		return r.Site, nil
	}
	return "", errs.Usage("NO_SITE", "no Jira site configured").
		WithRemedy("run `jr auth login --site <host>` to store a credential and " +
			"create your first context, or pass --site, or set JIRA_SITE")
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
	if r != nil && len(r.Projects) > 1 {
		return "", ProjectRepeated(len(r.Projects))
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

// ProjectRepeated is the refusal for a command that reads one project and was
// given several.
//
// A set is answered only by the commands whose result is a union over
// projects. Everywhere else the choices were to take the first, take the last,
// or run once per key, and each is a guess about what the caller meant by a
// flag they typed more than once.
func ProjectRepeated(n int) *errs.Error {
	return errs.Usage("PROJECT_REPEATED",
		"--project was given %d times, and this command reads one project", n).
		WithRemedy("pass one --project; `%s schema <command>` marks --project "+
			"repeatable on the commands that take several", buildinfo.App)
}

// resolveProjects is the project scope: every --project given, or else the one
// the environment names, or else the context's.
//
// The flag is the only source that can name several. JIRA_PROJECT and a
// context's project are each one key, because a set there would be a default
// that every later command inherits without anybody having typed it.
//
// A key given twice is one key, compared without regard to case, which is how
// Jira compares them.
func resolveProjects(flags []string, env, ctx string) []string {
	if out := DistinctProjects(flags); len(out) > 0 {
		return out
	}
	if one := firstNonEmpty(env, ctx); one != "" {
		return []string{one}
	}
	return nil
}

// DistinctProjects is what a list of --project values names: each key once, in
// the order first given, compared without regard to case.
//
// Exported for the one caller that has to count them before a session exists,
// which is the command line refusing a repeat on a command that reads one.
func DistinctProjects(flags []string) []string {
	var out []string
	for _, p := range flags {
		p = strings.TrimSpace(p)
		if p == "" || slices.ContainsFunc(out, func(have string) bool {
			return strings.EqualFold(have, p)
		}) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// onlyProject is the project when there is exactly one.
func onlyProject(projects []string) string {
	if len(projects) == 1 {
		return projects[0]
	}
	return ""
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

// readOnlySource names the first source that latched read-only on.
//
// The order is the same as sourceOf's and means something different: there is
// no precedence here, because any source turning it on turns it on. What this
// answers is which one to go and undo first.
func readOnlySource(flag bool, env string, ctx bool) Source {
	switch {
	case flag:
		return FromFlag
	case truthy(env):
		return FromEnv
	case ctx:
		return FromContext
	}
	return FromNowhere
}

// sourceOf reports which of the three precedence slots supplied a value. It
// takes the same arguments as firstNonEmpty, in the same order, so the two
// cannot disagree about which one won.
func sourceOf(flag, env, ctx string) Source {
	switch {
	case strings.TrimSpace(flag) != "":
		return FromFlag
	case strings.TrimSpace(env) != "":
		return FromEnv
	case strings.TrimSpace(ctx) != "":
		return FromContext
	}
	return FromNowhere
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
