package cli

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/kmoneil/jira-cli/internal/auth"
	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/jctx"
)

// maxTokenBytes bounds what --token-stdin will read. A token is short; anything
// larger is a redirected file the caller did not mean to pipe in.
const maxTokenBytes = 64 << 10

// environment is the app's view of the outside world: the config file, the
// credential store, and stdin. It is grouped here so a test can replace all of
// it at once, and so no command reaches for os.Getenv or os.Stdin directly.
type environment struct {
	paths     jctx.Paths
	pathsOnce sync.Once
	pathsErr  error

	cfg     *jctx.Config
	cfgOnce sync.Once
	cfgErr  error
}

// resolvePaths locates the XDG directories once per invocation.
func (a *app) resolvePaths() (jctx.Paths, error) {
	a.env.pathsOnce.Do(func() {
		a.env.paths, a.env.pathsErr = jctx.DefaultPaths(a.getenv)
	})
	return a.env.paths, a.env.pathsErr
}

// config loads the config file once per invocation.
func (a *app) config() (*jctx.Config, error) {
	a.env.cfgOnce.Do(func() {
		paths, err := a.resolvePaths()
		if err != nil {
			a.env.cfgErr = err
			return
		}
		a.env.cfg, a.env.cfgErr = jctx.Load(paths.ConfigFile(a.getenv))
	})
	return a.env.cfg, a.env.cfgErr
}

// resolve computes the effective settings from flags, environment, and context.
func (a *app) resolve(cfg *jctx.Config) (*jctx.Resolved, error) {
	return jctx.Resolve(cfg, jctx.Overrides{
		Context:  a.contextName,
		Site:     a.site,
		Project:  a.project,
		Board:    a.board,
		ReadOnly: a.readOnly,
	}, a.getenv)
}

// credentialStore returns the file-backed store.
func (a *app) credentialStore() (auth.FileStore, error) {
	paths, err := a.resolvePaths()
	if err != nil {
		return auth.FileStore{}, err
	}
	return auth.FileStore{Path: paths.CredentialsFile()}, nil
}

// chain returns the credential providers in precedence order.
func (a *app) chain() auth.Chain {
	store, err := a.credentialStore()
	if err != nil {
		// Without a state directory there is no file store, but the
		// environment still works, and reporting "no home directory" from
		// `auth status` would be less useful than reporting what is available.
		return auth.Chain{
			auth.EnvProvider{Getenv: auth.Getenv(a.getenv)},
			auth.NetrcProvider{Getenv: auth.Getenv(a.getenv)},
		}
	}
	return auth.DefaultChain(auth.Getenv(a.getenv), store.Path)
}

// normalizeSite canonicalizes a --site value.
func (a *app) normalizeSite(site string) (string, error) {
	return jctx.NormalizeSite(site)
}

// siteFor resolves the site a command should act on: the flag if given, else
// whatever the context and environment settle on.
func (a *app) siteFor(flagValue string) (string, error) {
	if strings.TrimSpace(flagValue) != "" {
		return jctx.NormalizeSite(flagValue)
	}
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	resolved, err := a.resolve(cfg)
	if err != nil {
		return "", err
	}
	return resolved.RequireSite()
}

// isTerminal reports whether v is an interactive terminal rather than a pipe or
// a file. It takes any so the same check covers stdin and stderr.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// tokenSourceRemedy lists every way to supply a credential, because "it did
// nothing" is the least actionable failure there is.
const tokenSourceRemedy = "pipe it in: printf '%s' \"$TOKEN\" | " +
	buildinfo.App + " auth login --site <host> --token-stdin; " +
	"or read a file: --token-file <path>; " +
	"or skip login entirely and set " + auth.EnvToken + " (with " +
	auth.EnvEmail + " on Cloud)"

// readToken reads a credential from stdin or from a file.
//
// Never from a flag: a token passed as an argument is visible in the shell
// history and in the process list, where every other user on the machine can
// read it. Trailing whitespace is trimmed, since `echo` adds a newline and a
// token carrying one fails authentication in a way that looks like a wrong
// token.
func (a *app) readToken(path string) (auth.Secret, error) {
	source := a.stdin
	name := "stdin"

	if path != "" && path != "-" {
		f, err := os.Open(path) //nolint:gosec // the path is the caller's own token file.
		if err != nil {
			return "", errs.Usage("TOKEN_FILE_UNREADABLE",
				"cannot read the token file").
				WithDetail("%s", err.Error()).
				WithRemedy("check the path and its permissions")
		}
		defer func() { _ = f.Close() }()
		source, name = f, path
	}

	if source == nil {
		return "", errs.Usage("NO_TOKEN_SOURCE", "no input to read the token from").
			WithRemedy("%s", tokenSourceRemedy)
	}

	// Reading from a terminal would block until the caller pressed Ctrl-D,
	// with no prompt and no output — which reads as the command hanging. A
	// headless-first tool never waits on a human, so this refuses instead and
	// says what to do.
	if isTerminal(source) {
		return "", errs.Usage("NO_TOKEN_SOURCE",
			"--token-stdin was given but stdin is a terminal, not a pipe or a file").
			WithDetail("this command never prompts: it would have waited forever").
			WithRemedy("%s", tokenSourceRemedy)
	}

	data, err := io.ReadAll(io.LimitReader(source, maxTokenBytes+1))
	if err != nil {
		return "", errs.Usage("TOKEN_UNREADABLE", "cannot read the token from %s", name).Wrap(err)
	}
	if len(data) > maxTokenBytes {
		return "", errs.Usage("TOKEN_TOO_LARGE",
			"the token from %s is larger than %d bytes", name, maxTokenBytes).
			WithRemedy("check that the wrong file was not redirected in by mistake")
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errs.Usage("EMPTY_TOKEN", "%s was empty", name).
			WithRemedy("%s", tokenSourceRemedy)
	}
	return auth.Secret(token), nil
}
