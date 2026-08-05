// Package jctx implements named contexts: one credential store, many sites.
//
// A context is {site, credential ref, default project, default board, default
// fields}. Project is never mandatory — it defaults from the context and can
// always be overridden or omitted. A command that genuinely needs a project and
// has none fails with exit 2 and names the flag.
//
// The package is jctx rather than context so that resource packages can import
// it alongside the standard library's context without aliasing either.
package jctx

import (
	"regexp"
	"slices"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Context is one named site/project pairing.
//
// It holds a reference to a credential, never a credential. The config file is
// meant to be hand-edited and kept in a dotfiles repository; a secret in it
// would be committed by the first person who tried.
type Context struct {
	Site string `toml:"site"`
	// Credential names an entry in the credential store. Empty means "the
	// credential stored for this site", which is the common case.
	Credential string `toml:"credential,omitempty"`

	Project string   `toml:"project,omitempty"`
	Board   string   `toml:"board,omitempty"`
	Fields  []string `toml:"fields,omitempty"`

	// ReadOnly bakes read-only mode into the context. It is a one-way latch:
	// see Resolved.ReadOnly.
	ReadOnly bool `toml:"readonly,omitempty"`
}

// nameFormat is what a context may be called. It is deliberately narrow: a
// context name appears in a flag value, in output, and in a file, and a name
// needing quotes in any of those is a name worth refusing.
var nameFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidateName reports whether name is usable as a context name.
func ValidateName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errs.Usage("INVALID_CONTEXT_NAME", "a context name cannot be empty")
	case len(name) > 64:
		return errs.Usage("INVALID_CONTEXT_NAME", "context name is longer than 64 characters").
			WithDetail("%q", name)
	case !nameFormat.MatchString(name):
		return errs.Usage("INVALID_CONTEXT_NAME",
			"%q is not a valid context name", name).
			WithDetail("names are lowercase and may contain letters, digits, dot, dash, "+
				"and underscore, starting with a letter or digit").
			WithRemedy("try %q", suggestName(name))
	}
	return nil
}

// suggestName offers the nearest legal spelling of an illegal name.
func suggestName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" || !nameFormat.MatchString(out) {
		return "work"
	}
	return out
}

// Validate checks a context is usable.
func (c Context) Validate() error {
	if strings.TrimSpace(c.Site) == "" {
		return errs.Usage("NO_SITE", "context has no site").
			WithRemedy("pass --site, e.g. --site acme.atlassian.net")
	}
	if _, err := NormalizeSite(c.Site); err != nil {
		return err
	}
	return nil
}

// siteFormat is a hostname, optionally with a port and a path prefix. A Data
// Center instance is often served under a path, e.g. jira.acme.internal/jira.
var siteFormat = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:\d+)?(/[^\s]*)?$`)

// NormalizeSite turns what a user typed into a canonical base URL.
//
// A bare hostname gets https, because a Jira reachable over plain http is rare
// enough that defaulting to it would be a security downgrade nobody asked for.
// An explicit http:// is honored, since a local test instance is a real case.
func NormalizeSite(site string) (string, error) {
	s := strings.TrimSpace(site)
	if s == "" {
		return "", errs.Usage("NO_SITE", "no site given").
			WithRemedy("pass --site, e.g. --site acme.atlassian.net")
	}

	scheme := "https://"
	switch {
	case strings.HasPrefix(strings.ToLower(s), "https://"):
		scheme, s = "https://", s[len("https://"):]
	case strings.HasPrefix(strings.ToLower(s), "http://"):
		scheme, s = "http://", s[len("http://"):]
	case strings.Contains(s, "://"):
		got, _, _ := strings.Cut(s, "://")
		return "", errs.Usage("INVALID_SITE",
			"site URL scheme %q is not http or https", got).
			WithDetail("%s", site)
	}

	s = strings.TrimRight(s, "/")
	if s == "" || !siteFormat.MatchString(s) {
		return "", errs.Usage("INVALID_SITE", "%q is not a valid site", site).
			WithRemedy("use a hostname, e.g. acme.atlassian.net, " +
				"optionally with a port and a path prefix")
	}
	return scheme + s, nil
}

// Host returns the site's hostname, which is what a credential is keyed on.
// Two contexts pointing at the same host share one credential.
func (c Context) Host() string {
	normalized, err := NormalizeSite(c.Site)
	if err != nil {
		return c.Site
	}
	normalized = strings.TrimPrefix(normalized, "https://")
	normalized = strings.TrimPrefix(normalized, "http://")
	host, _, _ := strings.Cut(normalized, "/")
	return host
}

// CredentialRef returns the key this context's credential is stored under.
func (c Context) CredentialRef() string {
	if c.Credential != "" {
		return c.Credential
	}
	return c.Host()
}

// clone returns a deep copy, so a caller mutating a returned context cannot
// reach into the loaded config.
func (c Context) clone() Context {
	c.Fields = slices.Clone(c.Fields)
	return c
}
