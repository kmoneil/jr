package jctx

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
)

// Environment variables that relocate the three XDG roots, plus the override
// for the config file itself.
const (
	EnvConfigHome = "XDG_CONFIG_HOME"
	EnvStateHome  = "XDG_STATE_HOME"
	EnvCacheHome  = "XDG_CACHE_HOME"
	EnvHome       = "HOME"
	// EnvConfigFile points at one config file, for a caller that keeps several.
	EnvConfigFile = "JIRA_CONFIG_FILE"
)

// Paths locates this tool's three directories.
//
// Config, state, and cache are separate because they have different lifetimes
// and different backup expectations: config is hand-written and worth keeping,
// state is machine-written and worth keeping, cache is machine-written and
// disposable. Putting them in one directory means a user who clears a cache
// loses their contexts.
//
// The layout is $XDG_CONFIG_HOME/jr/config.toml — not a hidden file inside a
// hidden directory inside a namespaced directory inside .config.
type Paths struct {
	Config string
	State  string
	Cache  string
}

// Getenv reads the environment. It is a parameter so tests do not have to
// mutate the process environment, which would make them order-dependent.
type Getenv func(string) string

// DefaultPaths resolves the XDG directories, honoring each variable and falling
// back to the specified default when one is unset or relative.
func DefaultPaths(getenv Getenv) (Paths, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	home := getenv(EnvHome)
	if home == "" {
		// os.UserHomeDir consults the platform's own notion of home, which is
		// what a Windows or a launchd process will have instead of HOME.
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Paths{}, errs.Runtime("NO_HOME",
				"cannot determine a home directory").
				WithRemedy("set HOME, or set XDG_CONFIG_HOME, XDG_STATE_HOME, and XDG_CACHE_HOME").
				Wrap(err)
		}
	}

	return Paths{
		Config: xdgDir(getenv, EnvConfigHome, filepath.Join(home, ".config")),
		State:  xdgDir(getenv, EnvStateHome, filepath.Join(home, ".local", "state")),
		Cache:  xdgDir(getenv, EnvCacheHome, filepath.Join(home, ".cache")),
	}, nil
}

// xdgDir returns $<name>/jr, or fallback/jr.
//
// The spec requires an absolute path for an XDG variable, so a relative one is
// ignored rather than resolved against the working directory — which would put
// a user's contexts somewhere different depending on where they ran the command.
func xdgDir(getenv Getenv, name, fallback string) string {
	dir := strings.TrimSpace(getenv(name))
	if dir == "" || !filepath.IsAbs(dir) {
		dir = fallback
	}
	return filepath.Join(dir, buildinfo.App)
}

// ConfigFile is the path to the contexts file, honoring JIRA_CONFIG_FILE.
func (p Paths) ConfigFile(getenv Getenv) string {
	if getenv != nil {
		if override := strings.TrimSpace(getenv(EnvConfigFile)); override != "" {
			return override
		}
	}
	return filepath.Join(p.Config, "config.toml")
}

// CredentialsFile is where the file-backed credential store lives.
//
// It sits under the state directory rather than next to the config, because
// the config is meant to be hand-edited, shared, and committed to a dotfiles
// repository, and a credential must not be swept along with it.
func (p Paths) CredentialsFile() string {
	return filepath.Join(p.State, "credentials.toml")
}

// SiteCache is the cache directory for one site's metadata: field ids, issue
// types, transitions, and the deployment probe.
func (p Paths) SiteCache(site string) string {
	return filepath.Join(p.Cache, sanitizeSite(site))
}

// sanitizeSite turns a site URL into a single safe path element, so a hostname
// can never escape the cache directory or collide across schemes.
func sanitizeSite(site string) string {
	s := strings.TrimSpace(strings.ToLower(site))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")

	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}
