package cli

import (
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jctx"
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
		Context:    a.contextName,
		Site:       a.site,
		Project:    a.project,
		Board:      a.board,
		APIVersion: a.apiVersion,
		ReadOnly:   a.readOnly,
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
	// A command that names its own site is the authority on where the request
	// went, and the error decoration has to agree with it.
	//
	// `--site` is declared twice: once as a persistent flag, and once on each
	// auth command, where it is required. Cobra binds the command-local one, so
	// the value lands in the invocation's flags and never in a.site — and
	// explainSite, reading a.site, saw nothing, fell through to the context,
	// and told a caller who had typed `--site` that their site "came from
	// context X". Against a context naming a different host entirely, which is
	// a diagnostic pointing at the wrong file.
	if trimmed := strings.TrimSpace(site); trimmed != "" {
		a.site = trimmed
	}
	return jctx.NormalizeSite(site)
}

// siteFor resolves the site a command should act on: the flag if given, else
// whatever the context and environment settle on.
func (a *app) siteFor(flagValue string) (string, error) {
	if strings.TrimSpace(flagValue) != "" {
		return a.normalizeSite(flagValue)
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
//
// It asks the terminal driver rather than reading the file mode. `ModeCharDevice`
// was the old test and it is true of `/dev/null`, which is a character device
// and is nobody: `auth login --token-stdin < /dev/null` was refused with "stdin
// is a terminal", which is both wrong and unactionable, and the same false
// positive would now send a human build off to prompt on something that cannot
// answer. Measured before adding the dependency to every profile: the agent
// binary is the same size with it as without.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// promptForToken asks the person at the terminal, in a build that has one.
//
// The site is named in the prompt because a credential is per-site and getting
// them crossed is a failure that surfaces two commands later as something that
// looks unrelated.
func (a *app) promptForToken(site string) (auth.Secret, error) {
	label := "API token"
	if site != "" {
		label += " for " + site
	}
	secret, err := promptSecret(label)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(secret))
	if trimmed == "" {
		return "", errs.Usage("EMPTY_TOKEN", "no token was typed").
			WithRemedy("%s", tokenSourceRemedy)
	}
	return auth.Secret(trimmed), nil
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
func (a *app) readToken(path, site string) (auth.Secret, error) {
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

	// A terminal on stdin is a person, and what to do about that depends on
	// whether this build has one to talk to.
	//
	// Reading it directly would block until they pressed Ctrl-D, with no prompt
	// and no output, which is indistinguishable from a hang. That is the whole
	// reason for the rule, so a build that can *ask* satisfies it by asking:
	// the wait is visible, it is bounded by a keystroke, and the token still
	// never reaches the process list or the shell history. A build without the
	// prompt tag has nobody there and keeps refusing.
	if isTerminal(source) {
		if !canPrompt {
			return "", errs.Usage("NO_TOKEN_SOURCE",
				"stdin is a terminal, not a pipe or a file, and this build cannot prompt").
				WithDetail("the agent, reader, and ci builds have no interactive "+
					"prompt compiled in, so there is nobody to ask").
				WithRemedy("%s", tokenSourceRemedy)
		}
		return a.promptForToken(site)
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
