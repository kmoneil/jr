package cli

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/jctx"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// Output kinds owned by the auth commands.
const (
	kindAuthStatus = "auth.status"
	kindAuthToken  = "auth.token"
	// v2 adds `site-scoped`, which reports whether the credential's provider is
	// keyed by host. An exported JIRA_API_TOKEN is not, so it is sent to
	// whatever site the invocation resolves — correct behaviour that nothing
	// had ever said out loud.
	versionAuthStatus = 2
	versionAuthToken  = 1
)

// contextNotCreatedCode is the warning a login writes when the name it would
// give a site's context already belongs to another site's.
const contextNotCreatedCode = "CONTEXT_NOT_CREATED"

func (a *app) authCommands() []*registry.Command {
	return []*registry.Command{
		a.authLoginCommand(),
		a.authLogoutCommand(),
		a.authStatusCommand(),
		a.authTokenCommand(),
	}
}

func (a *app) authLoginCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"auth", "login"},
		Summary: "Store a credential for a site",
		Description: strings.TrimSpace(`
Writes a credential to the credential store, which lives under the state
directory at mode 0600 and is separate from the config file. The config file is
meant to be hand-edited and kept in a dotfiles repository; a credential in it
would be committed by the first person who tried.

On Cloud, pair --email with an API token. On Data Center, either pair --user
with a password, or supply a personal access token alone and it is used as a
bearer token.

Supply the token with --token-stdin or --token-file, never as a flag value: a
token on the command line lands in the shell history and in the process list,
where anyone on the machine can read it.

**With a terminal on stdin, this asks.** The prompt does not echo, and what you
type at it never reaches the shell history either, which is the property a flag
value cannot have. That is the whole interactive path:

    ` + buildinfo.App + ` auth login --site <host> --email <you>
    API token for <host>:

For a script, pipe it or point at a file instead:

    printf '%s' "$TOKEN" | ` + buildinfo.App + ` auth login --site <host> --token-stdin
    ` + buildinfo.App + ` auth login --site <host> --token-file ~/.secrets/jira

The agent, reader, and ci builds have no prompt compiled in, so there a terminal
on stdin is refused rather than waited on: nobody is there to ask, and a command
that waits with no reader is indistinguishable from a hang. Those builds take
the token by pipe or by file, as every script should:

    printf '%s' "$TOKEN" | ` + buildinfo.App + ` auth login --site <host> --token-stdin

Trailing whitespace is trimmed on every path, so a stray newline from an editor
or an echo is not a wrong token.

You do not have to log in at all: set ` + auth.EnvToken + `, plus ` + auth.EnvEmail + ` on
Cloud, and every command uses it.

The credential is verified against the site before anything is written: the
deployment is probed and the account is fetched, so a wrong host, a wrong
context path, or a bad token is refused here rather than surfacing two commands
later as something that looks unrelated. --no-verify skips the check, for
preparing a configuration offline.

If no context names this site, one is made for it so --context can reach it,
named by --context-name or for the host's first label (jira.corp.com makes
"jira"), and the result's context attribute says which. A context that already
names the site is left as it is. The current context never changes: the first
context ever made becomes current, and after that "` + buildinfo.App + ` context use"
is how one is chosen, because a login that quietly moved every command to
another site would be worse than none.

A derived name that already belongs to another site's context is not replaced
and not suffixed. Nothing is made, and a CONTEXT_NOT_CREATED warning names the
two ways out. A --context-name another site's context holds is refused before
anything is checked or stored, since making it would replace that context.`),
		Example: strings.Join([]string{
			"printf '%s' \"$TOKEN\" | " + buildinfo.App +
				" auth login --site your-site.atlassian.net --email ada@example.com --token-stdin",
			"printf '%s' \"$PAT\" | " + buildinfo.App +
				" auth login --site jira.acme.internal --token-stdin",
			"printf '%s' \"$PAT\" | " + buildinfo.App +
				" auth login --site jira.acme.example/jira --context-name acme --token-stdin",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "site", Type: registry.TypeString, Required: true,
				Usage: "Jira site, e.g. your-site.atlassian.net",
			},
			{Name: "email", Type: registry.TypeString, Usage: "Cloud account email"},
			{Name: "user", Type: registry.TypeString, Usage: "Data Center username"},
			{
				Name: "token-stdin", Type: registry.TypeBool,
				Usage: "read the token from stdin; required unless --token-file is given",
			},
			{
				Name: "token-file", Type: registry.TypeString,
				Usage: "read the token from this file; - means stdin",
			},
			{
				Name: "scheme", Type: registry.TypeEnum, Enum: auth.Schemes(),
				Usage: "authentication scheme; inferred from whether a user was given",
			},
			{
				Name: "no-verify", Type: registry.TypeBool,
				Usage: "store the credential without checking it against the site",
			},
			{
				Name: "context-name", Type: registry.TypeString,
				Usage: "name the context made for this site; defaults to the host's first label",
			},
		},
		LocalState: true,
		Outputs:    []registry.Output{{Kind: kindAuthStatus, Version: versionAuthStatus}},
		ExitCodes: []exitcode.Code{
			exitcode.Auth, exitcode.NotFound, exitcode.Permission, exitcode.Remote,
		},
		Validate: a.validateAuthLogin,
		Run:      a.runAuthLogin,
	}
}

// validateAuthLogin refuses a --context-name the login could not honor, before
// the credential is checked or stored. Config.Set adds or replaces, so a name
// another site's context holds would replace that context, and storing the
// credential while refusing its name would be half a login.
func (a *app) validateAuthLogin(_ context.Context, inv *registry.Invocation) error {
	name := inv.Flags.String("context-name")
	if name == "" {
		return nil
	}
	if err := jctx.ValidateName(name); err != nil {
		return err
	}
	siteURL, err := jctx.NormalizeSite(inv.Flags.String("site"))
	if err != nil {
		// Run refuses it, and decorates the refusal with the site.
		return nil //nolint:nilerr // the site's refusal is Run's to make.
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	if held, ok := cfg.Get(name); ok && !jctx.SameSite(held.Site, siteURL) {
		return errs.Usage("CONTEXT_NAME_TAKEN",
			"context %q already names %s", name, held.Site).
			WithRemedy("pass a --context-name no context holds; `%s context list` shows them",
				buildinfo.App)
	}
	return nil
}

func (a *app) runAuthLogin(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	siteURL, err := a.normalizeSite(inv.Flags.String("site"))
	if err != nil {
		return nil, err
	}

	cred, err := a.credentialFrom(inv)
	if err != nil {
		return nil, err
	}

	// Verify before writing anything. Storing a credential that does not work,
	// and creating a context pointing at a site that does not answer, is how a
	// login reports success and every later command fails for reasons that look
	// unrelated to it.
	var account *site.Account
	if !inv.Flags.Bool("no-verify") {
		verified, err := a.verifyCredential(ctx, siteURL, cred)
		if err != nil {
			return nil, err
		}
		account = &verified
	}

	store, err := a.credentialStore()
	if err != nil {
		return nil, err
	}
	if err := store.Save(siteURL, cred); err != nil {
		return nil, err
	}

	cred.Source = store.Path
	doc := authStatusDoc(siteURL, cred, true, nil)
	if account != nil {
		doc.Record.
			Attr("account", account.ID).
			Attr("display", account.Display)
	}

	// Storing a credential for a site and then having the next command report
	// "no Jira site configured", or answer --context with UNKNOWN_CONTEXT, is
	// the tool accepting input and behaving as though it never heard it.
	created, err := a.ensureContextFor(inv, siteURL)
	if err != nil {
		return nil, err
	}
	if created != "" {
		doc.Record.Attr("context", created)
	}
	return doc, nil
}

// credentialFrom reads the token and works out the scheme to send it under.
func (a *app) credentialFrom(inv *registry.Invocation) (auth.Credential, error) {
	tokenFile := inv.Flags.String("token-file")
	fromStdin := inv.Flags.Bool("token-stdin")
	switch {
	case !fromStdin && tokenFile == "" && (!canPrompt || !isTerminal(a.stdin)):
		// No source named, and no human to ask. A build that *can* ask falls
		// through to readToken, which prompts — that is the plain
		// `auth login --site X --email Y` a person types, and having it refuse
		// with a shell pipeline to copy was the whole complaint.
		//
		// The terminal check matters as much as the tag: with stdin redirected
		// and no flag, reading it anyway would consume input the caller never
		// offered and make --token-stdin mean nothing.
		return auth.Credential{}, errs.Usage("NO_TOKEN_SOURCE", "no token source given").
			WithRemedy("%s", tokenSourceRemedy)
	case fromStdin && tokenFile != "" && tokenFile != "-":
		// Two sources would mean silently picking one, and the caller would
		// have no way to know which credential was stored.
		return auth.Credential{}, errs.Usage("AMBIGUOUS_TOKEN_SOURCE",
			"--token-stdin and --token-file name two different sources").
			WithRemedy("pass one of them, or --token-file - for stdin")
	}

	token, err := a.readToken(tokenFile, inv.Flags.String("site"))
	if err != nil {
		return auth.Credential{}, err
	}
	user := firstNonEmpty(inv.Flags.String("email"), inv.Flags.String("user"))

	scheme := auth.Bearer
	if user != "" {
		scheme = auth.Basic
	}
	if requested := inv.Flags.String("scheme"); requested != "" {
		if scheme, err = auth.ParseScheme(requested); err != nil {
			return auth.Credential{}, err
		}
	}

	cred := auth.Credential{Scheme: scheme, User: user, Secret: token}
	if err := cred.Validate(); err != nil {
		return auth.Credential{}, err
	}
	return cred, nil
}

// ensureContextFor makes a context for a site a credential was just stored
// for, when none names it, and returns the name it made, or empty.
//
// A site no context names is unambiguous whether or not other contexts exist:
// making its context guesses nothing and changes nothing already there. One
// that a context names already reaches the credential, so it is left alone and
// nothing is said. Config.Set makes a context current only when none is, so
// this never moves the commands already pointed somewhere.
//
// --context-name asks for a context of that name on this site, and makes one
// even beside another that names the site; validateAuthLogin has refused it
// if another site's context holds it. A derived name another site's context
// holds is neither replaced nor suffixed: nothing is made, and a warning says
// so and names the ways out.
func (a *app) ensureContextFor(inv *registry.Invocation, site string) (string, error) {
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	requested := inv.Flags.String("context-name")
	if requested == "" && anyContextNames(cfg, site) {
		return "", nil
	}

	name := cmp.Or(requested, contextNameFor(site))
	if held, ok := cfg.Get(name); ok {
		if !jctx.SameSite(held.Site, site) {
			warnContextNotCreated(inv, site, name, held.Site)
		}
		return "", nil
	}
	if err := cfg.Set(name, jctx.Context{Site: site}); err != nil {
		return "", err
	}
	if err := cfg.Save(); err != nil {
		return "", err
	}
	return name, nil
}

// anyContextNames reports whether some context already names site.
func anyContextNames(cfg *jctx.Config, site string) bool {
	return slices.ContainsFunc(cfg.Names(), func(name string) bool {
		ctx, _ := cfg.Get(name)
		return jctx.SameSite(ctx.Site, site)
	})
}

// warnContextNotCreated says that a login stored its credential and made no
// context, because the name it derived is another site's. The credential is
// reachable with --site alone, which nobody would think to try after a login
// that reported success.
func warnContextNotCreated(inv *registry.Invocation, site, name, heldBy string) {
	if inv.Stderr == nil {
		return
	}
	_ = render.WriteWarning(inv.Stderr, contextNotCreatedCode, fmt.Sprintf(
		"no context was made for %s, because %q already names %s; log in again "+
			"with --context-name <name>, or run `%s context create <name> --site %s`",
		site, name, heldBy, buildinfo.App, site), inv.Format)
}

// contextNameFor derives a context name from a site's first label, so
// jira.corp.com becomes "jira". A label that is not a usable name — a bare IP,
// say — falls back to something that always is.
func contextNameFor(site string) string {
	host := jctx.Context{Site: site}.Host()
	host, _, _ = strings.Cut(host, ":")
	label, _, _ := strings.Cut(host, ".")

	if jctx.ValidateName(label) == nil && !isAllDigits(label) {
		return label
	}
	return "default"
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (a *app) authLogoutCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"auth", "logout"},
		Summary: "Remove a stored credential",
		Description: strings.TrimSpace(`
Deletes the credential this tool stored for a site. It cannot remove one that
came from the environment or from .netrc, and says so if that is where the
credential is coming from — otherwise ` + "`auth logout`" + ` would report success
while the site stayed authenticated.`),
		Example: buildinfo.App + " auth logout --site your-site.atlassian.net --yes",
		Flags: []registry.Flag{
			{
				Name: "site", Type: registry.TypeString, Required: true,
				Usage: "Jira site to forget",
			},
			{Name: "yes", Type: registry.TypeBool, Usage: "confirm the removal"},
		},
		LocalState:  true,
		Destructive: true,
		Outputs:     []registry.Output{{Kind: kindAuthStatus, Version: versionAuthStatus}},
		// Auth is the credential store's, not a request's. Reading the store to
		// find what to remove refuses it at STORE_PERMISSIONS, STORE_INVALID, or
		// STORE_UNREADABLE, all exit 4, and this command has to read it before
		// it can delete anything. `auth status` and `auth token` declared that
		// and this did not, which is the same asymmetry three of these commands
		// had over --context: the reachable exit belongs to the layer below and
		// only the ones somebody happened to probe by hand were corrected.
		ExitCodes: []exitcode.Code{exitcode.Auth, exitcode.NotFound, exitcode.Blocked},
		Run:       a.runAuthLogout,
	}
}

func (a *app) runAuthLogout(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	site, err := a.normalizeSite(inv.Flags.String("site"))
	if err != nil {
		return nil, err
	}
	store, err := a.credentialStore()
	if err != nil {
		return nil, err
	}
	removed, err := store.Delete(site)
	if err != nil {
		return nil, err
	}
	if !removed {
		return nil, errs.NotFound("NO_STORED_CREDENTIAL",
			"no stored credential for %s", site).
			WithRemedy("run `%s auth status` to see where its credential comes from",
				buildinfo.App)
	}

	// Report what is left, so a credential still arriving from the environment
	// is visible rather than a surprise on the next command.
	remaining, found, lookupErr := a.chain().Lookup(site)
	if lookupErr != nil {
		return nil, lookupErr
	}
	doc := authStatusDoc(site, remaining, found, nil)
	doc.Record.Attr("removed", "true")
	return doc, nil
}

func (a *app) authStatusCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"auth", "status"},
		Summary: "Report which credential a site would use, and where it comes from",
		Description: strings.TrimSpace(`
Reports the credential that would be used and the source it came from, without
revealing it.

Sources are tried in a fixed order: the environment first, so a CI job can
override what is on disk without editing it; then this tool's credential store;
then .netrc last, because it is shared with every other tool on the machine and
is the least specific statement of intent.

This does not contact Jira. It answers "which credential would be used", not
"does that credential still work".`),
		Example: strings.Join([]string{
			buildinfo.App + " auth status",
			buildinfo.App + " auth status --site your-site.atlassian.net",
		}, "\n"),
		Flags: []registry.Flag{{
			Name: "site", Type: registry.TypeString,
			Usage: "site to check; defaults to the current context's",
		}},
		Outputs: []registry.Output{{Kind: kindAuthStatus, Version: versionAuthStatus}},
		// NotFound because the site defaults to the current context's, so this
		// resolves one: `--context nope` is UNKNOWN_CONTEXT at exit 5, and a
		// command whose job is to explain why authentication is not working is
		// the worst place to answer with an exit it never published.
		ExitCodes: []exitcode.Code{exitcode.Auth, exitcode.NotFound},
		Run:       a.runAuthStatus,
	}
}

func (a *app) runAuthStatus(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	site, err := a.siteFor(inv.Flags.String("site"))
	if err != nil {
		return nil, err
	}

	chain := a.chain()
	cred, found, err := chain.Lookup(site)
	if err != nil {
		return nil, err
	}
	return authStatusDoc(site, cred, found, chain.Sources()), nil
}

func (a *app) authTokenCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"auth", "token"},
		Summary: "Print the credential for a site, for use in another tool",
		Description: strings.TrimSpace(`
Prints the Authorization header value a request to this site would carry.

This deliberately reveals a secret. It exists so a script can hand the
credential to curl or another client without re-implementing the credential
lookup.

--header prints the whole header and nothing else, on one line, so the obvious
capture is the correct one:

    curl -H "$(` + buildinfo.App + ` auth token --header)" ...

Without it the result is a document like every other result, and the value is
one field of it, so take that field:

    header=$(` + buildinfo.App + ` auth token --format tsv | awk -F'\t' '$1=="authorization"{print $2}')
    curl -H "Authorization: $header" ...

Capturing a whole document into a variable and using it as a header does not
work in any format and does not say so: curl reports status 000, which is what
it reports for a network failure. --header exists because that failure looks
like something else.

Everywhere else in this tool a credential is redacted. Here it is the requested
output, and it goes to stdout like any other result — so redirect it
deliberately, and do not pass it through a command that logs its arguments.`),
		Example: strings.Join([]string{
			buildinfo.App + " auth token --site your-site.atlassian.net",
			buildinfo.App + " auth token --header",
		}, "\n"),
		Flags: []registry.Flag{
			{
				Name: "site", Type: registry.TypeString,
				Usage: "site to print the credential for; defaults to the current context's",
			},
			{
				Name: "header", Type: registry.TypeBool,
				Usage: "print `Authorization: <value>` on one line and nothing " +
					"else, for a shell to interpolate whole",
			},
		},
		// Only with --header. Without it this emits its document like anything
		// else, and the declaration says which invocation produces which.
		OwnsStdoutWhen: headerOwnsStdout,
		Outputs: []registry.Output{{
			Kind: kindAuthToken, Version: versionAuthToken,
			When: "--header was not given",
		}},
		// NotFound for the reason `auth status` carries it: --site defaults to
		// the current context's, so an unknown --context is exit 5 here too.
		ExitCodes: []exitcode.Code{exitcode.Auth, exitcode.NotFound},
		Validate:  a.validateAuthToken,
		Run:       a.runAuthToken,
	}
}

// headerOwnsStdout reports whether this invocation writes a bare header line.
func headerOwnsStdout(inv *registry.Invocation) bool {
	return inv.Flags.Bool("header")
}

// validateAuthToken refuses the two ways --header can be asked for something it
// cannot give.
//
// --format names one of four documents and --header is not one of them. Letting
// the two coexist would mean silently ignoring whichever lost, and an input a
// command accepted is never quietly forgotten. JIRA_FORMAT is deliberately not
// grounds for refusal: it is set once for a whole shell, and a per-invocation
// flag has to beat an ambient default rather than collide with it.
func (a *app) validateAuthToken(_ context.Context, inv *registry.Invocation) error {
	if !inv.Flags.Bool("header") {
		return nil
	}
	if a.formatFromFlag {
		return errs.Usage("HEADER_AND_FORMAT",
			"--header and --format name two different outputs").
			WithDetail("--header prints one line that is not a document, and " +
				"--format chooses how a document is encoded").
			WithRemedy("pass one of them: --header for a header, or " +
				"--format tsv and take the authorization field")
	}
	if inv.Stdout == nil {
		// Inside `mcp serve` stdout carries JSON-RPC frames, and a bare line
		// there is a frame the peer cannot parse. The same refusal that
		// `issue attachment download --output -` makes, for the same reason.
		return errs.Usage("NO_STDOUT",
			"this caller has no stdout to write a header to").
			WithRemedy("drop --header and read the authorization field of the result")
	}
	return nil
}

func (a *app) runAuthToken(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
	site, err := a.siteFor(inv.Flags.String("site"))
	if err != nil {
		return nil, err
	}
	cred, err := a.chain().Resolve(site)
	if err != nil {
		return nil, err
	}
	header, err := cred.Header()
	if err != nil {
		return nil, err
	}

	if inv.Flags.Bool("header") {
		// No document: the line is the output. The CLI knows not to render one
		// because OwnsStdoutWhen said so before this ran. The name is written
		// out rather than taken from the map's key, so the line is the same
		// whatever the scheme resolved to.
		if _, err := fmt.Fprintln(inv.Stdout, "Authorization: "+header["Authorization"]); err != nil {
			return nil, errs.Runtime("HEADER_NOT_WRITTEN",
				"the credential could not be written to stdout").Wrap(err)
		}
		return nil, nil //nolint:nilnil // the line is the result.
	}

	n := render.El("token").
		Attr("site", site).
		Attr("scheme", string(cred.Scheme)).
		Attr("source", cred.Source).
		Leaf("authorization", header["Authorization"])
	return render.Record(kindAuthToken, versionAuthToken, n), nil
}

// authStatusDoc renders a credential without revealing it. The value never
// reaches this function: only the scheme, the user, and where it came from.
func authStatusDoc(site string, cred auth.Credential, found bool, sources []string) *render.Doc {
	n := render.El("auth").
		Attr("site", site).
		Attr("authenticated", strconv.FormatBool(found))

	if found {
		n.Attr("scheme", string(cred.Scheme)).
			Attr("source", cred.Source).
			// Whether the credential was looked up *for* this site or merely
			// found. An exported JIRA_API_TOKEN is not keyed by host, so it
			// follows whatever site the invocation resolves — from --site, from
			// JIRA_SITE, or from a config.toml that is 0644 by design and meant
			// to be shared between machines. Nothing there misbehaves; the
			// composition is just worth one attribute in the command somebody
			// already runs to ask where their credential comes from.
			Attr("site-scoped", strconv.FormatBool(cred.SiteScoped))
		if cred.User != "" {
			n.Attr("user", cred.User)
		}
	}

	items := make([]*render.Node, 0, len(sources))
	for _, s := range sources {
		items = append(items, render.El("source").SetText(s))
	}
	n.Child(render.ListEl("sources", "source", items...))
	return render.Record(kindAuthStatus, versionAuthStatus, n)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
