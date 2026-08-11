// Package auth resolves credentials for a site.
//
// It covers the credential providers — environment, netrc, and a file store —
// behind one interface, and turns a credential into the headers the transport
// applies. A credential never leaves this package as a loggable value: Secret
// deliberately does not stringify, so a struct carrying one cannot be printed
// by accident.
package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
)

// Scheme is how a credential authenticates.
type Scheme string

// The supported schemes.
const (
	// Basic is an email plus API token on Cloud, or a username plus password
	// on Data Center.
	Basic Scheme = "basic"
	// Bearer is a personal access token, which Data Center issues and Cloud
	// OAuth returns.
	Bearer Scheme = "bearer"
)

// Schemes returns every supported scheme, for flag enums and schema output.
func Schemes() []string { return []string{string(Basic), string(Bearer)} }

// ParseScheme resolves a --scheme value.
func ParseScheme(s string) (Scheme, error) {
	switch Scheme(strings.ToLower(strings.TrimSpace(s))) {
	case Basic:
		return Basic, nil
	case Bearer:
		return Bearer, nil
	}
	return "", errs.Usage("INVALID_SCHEME", "unknown auth scheme %q", s).
		WithDetail("valid schemes: %s", strings.Join(Schemes(), ", ")).
		WithRemedy("pass --scheme with one of the listed values")
}

// Secret holds a credential value.
//
// It is a named type with String and Format methods that hide the value, so a
// %v, a %s, or a careless log line prints a marker rather than the token. The
// value comes out only through Reveal, which is greppable: every place a
// credential can escape is one search away.
type Secret string

// Redacted is what a Secret prints as.
const Redacted = "REDACTED"

// String implements fmt.Stringer without revealing the value.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return Redacted
}

// Format implements fmt.Formatter, so %s, %q, and %v are all safe. Without it,
// %q would quote the underlying string and print the token.
func (s Secret) Format(f fmt.State, verb rune) {
	switch {
	case s == "":
		_, _ = f.Write(nil)
	case verb == 'q':
		_, _ = fmt.Fprintf(f, "%q", Redacted)
	default:
		_, _ = fmt.Fprint(f, Redacted)
	}
}

// Reveal returns the credential value. Call it only where the value is about to
// be used, never where it might be stored or logged.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s == "" }

// Credential is one stored credential for one site.
type Credential struct {
	Scheme Scheme
	// User is the email on Cloud, or the username on Data Center. It is empty
	// for a bearer token.
	User   string
	Secret Secret
	// Source names where this credential came from, for `jr auth status`. It
	// is never part of the credential itself.
	Source string
	// SiteScoped reports whether the provider that produced this credential
	// was keyed by host — whether it was looked up *for* the site it is about
	// to be sent to, or merely found.
	//
	// FileStore and NetrcProvider are keyed by host and set it. EnvProvider is
	// not and deliberately leaves it false: an exported JIRA_API_TOKEN is used
	// for whatever site the invocation resolves, which is the behaviour a CI
	// job wants and is documented on EnvProvider.Lookup.
	//
	// The zero value is the cautious one. A credential built somewhere that
	// never considered the question reports "not site-scoped", which
	// overstates the case in `jr auth status` and never understates it.
	SiteScoped bool
}

// Validate reports whether the credential can actually authenticate.
func (c Credential) Validate() error {
	if c.Secret.IsZero() {
		return errs.Auth("INCOMPLETE_CREDENTIAL", "credential has no token")
	}
	switch c.Scheme {
	case Basic:
		if c.User == "" {
			return errs.Auth("INCOMPLETE_CREDENTIAL",
				"basic auth needs a user as well as a token").
				WithRemedy("pass --email on Cloud, or --user on Data Center")
		}
	case Bearer:
	default:
		return errs.Auth("INVALID_SCHEME", "unknown auth scheme %q", string(c.Scheme))
	}
	return nil
}

// Header returns the credential as request headers.
func (c Credential) Header() (map[string]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Scheme {
	case Bearer:
		return map[string]string{"Authorization": "Bearer " + c.Secret.Reveal()}, nil
	default:
		pair := c.User + ":" + c.Secret.Reveal()
		return map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(pair)),
		}, nil
	}
}

// Authorizer adapts a credential to what the transport asks for.
//
// It returns headers rather than touching a request, which is what keeps this
// package free of net/http and means supplying a credential cannot alter the
// request in any other way.
type Authorizer struct{ Credential Credential }

// Authorize implements the transport's Authorizer interface.
func (a Authorizer) Authorize(_ context.Context, _ RequestInfo) (map[string]string, error) {
	return a.Credential.Header()
}

// RequestInfo mirrors the transport's type. It is redeclared rather than
// imported so this package does not depend on the transport, and the two are
// matched structurally where they meet.
type RequestInfo struct {
	Method string
	URL    string
}

// Provider looks up a credential for a site.
type Provider interface {
	// Name identifies the provider in `jr auth status` and in errors.
	Name() string
	// Lookup returns the credential for a site, or ok=false if it has none.
	Lookup(site string) (Credential, bool, error)
}

// Chain tries providers in order and returns the first credential found.
//
// Order is precedence, and it is deliberate: the environment wins so a CI job
// can override whatever is on disk without editing it, and netrc comes last
// because it is shared with other tools and is the least specific statement of
// intent.
type Chain []Provider

// Lookup returns the first credential any provider has for the site.
func (c Chain) Lookup(site string) (Credential, bool, error) {
	for _, p := range c {
		cred, ok, err := p.Lookup(site)
		if err != nil {
			return Credential{}, false, err
		}
		if !ok {
			continue
		}
		if cred.Source == "" {
			cred.Source = p.Name()
		}
		return cred, true, nil
	}
	return Credential{}, false, nil
}

// Sources returns the provider names in precedence order.
func (c Chain) Sources() []string {
	out := make([]string, 0, len(c))
	for _, p := range c {
		out = append(out, p.Name())
	}
	return out
}

// Resolve returns the credential for a site, or an auth error explaining every
// place that was looked.
//
// The error names the sources rather than saying "not found", because "which of
// the four places was I supposed to put it" is the actual question a caller has
// at that moment.
func (c Chain) Resolve(site string) (Credential, error) {
	cred, ok, err := c.Lookup(site)
	if err != nil {
		return Credential{}, err
	}
	if !ok {
		return Credential{}, errs.Auth("NO_CREDENTIALS",
			"no credentials for %s", site).
			WithDetail("looked in: %s", strings.Join(c.Sources(), ", ")).
			WithRemedy("run `jr auth login --site %s`, or set %s and %s",
				site, EnvToken, EnvEmail)
	}
	if err := cred.Validate(); err != nil {
		return Credential{}, err
	}
	return cred, nil
}

// hosts normalizes a site to the host key credentials are stored under.
func hostOf(site string) string {
	s := strings.TrimSpace(strings.ToLower(site))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	host, _, _ := strings.Cut(s, "/")
	return host
}

// sortedKeys returns a map's keys in a stable order, so output does not vary
// between runs.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
