package auth

import (
	"strings"

	"github.com/kmoneil/jr/internal/errs"
)

// Environment variables the env provider reads.
const (
	EnvToken = "JIRA_API_TOKEN"
	EnvEmail = "JIRA_EMAIL"
	EnvUser  = "JIRA_USER"
	// EnvAuthScheme forces a scheme. Without it, the scheme is inferred from
	// whether a user was supplied.
	EnvAuthScheme = "JIRA_AUTH_SCHEME"
)

// Getenv reads the environment. It is a parameter so tests need not mutate the
// process environment, which would make them order-dependent.
type Getenv func(string) string

// EnvProvider reads a credential from the environment.
//
// It is first in the chain so a CI job can supply a credential without writing
// anything to disk, and can override whatever a developer image happens to
// have baked in.
type EnvProvider struct{ Getenv Getenv }

// Name implements Provider.
func (EnvProvider) Name() string { return "environment" }

// Lookup implements Provider. The environment is not site-scoped: a caller who
// exports a token has one site in mind, and refusing to use it because the
// variable does not name a host would be pedantry.
//
// That is a decision and not an oversight, and it was re-examined deliberately,
// because the composition reads worse than either half: config.toml is 0644 by
// design and meant to be committed to a dotfiles repository, so the site an
// exported token is sent to can come from a file somebody else wrote. Every
// layer then behaves exactly as documented — transport.New validates the URL,
// resolve refuses an absolute path, Relative refuses an off-site redirect — and
// the credential goes to the host the configuration named.
//
// Scoping this provider to a host is the obvious fix and is the wrong one. It
// breaks the case the provider exists for and turns the ordinary CI setup into
// a three-variable one, and "somebody might commit a hostile config.toml" is
// not the argument it looks like: a person who can write your config can write
// your shell profile. What was missing was not a refusal but a sentence, so
// `jr auth status` reports site-scoped="false" against this credential and the
// fact is visible where somebody is already asking where their credential comes
// from.
//
// TestAnEnvironmentCredentialFollowsWhateverSiteItIsPointedAt pins this. If
// host-scoping is being added here, that test is the argument to answer first —
// it exists because tightening this breaks nothing visible and reads as
// security.
//
// If project-access-policy ever lands, revisit: a mechanism that constrains
// which projects a configuration may touch has an obvious sibling in which
// sites a credential may reach, and at that point this is a hole in a mechanism
// rather than a documented convenience.
func (p EnvProvider) Lookup(string) (Credential, bool, error) {
	getenv := p.Getenv
	if getenv == nil {
		return Credential{}, false, nil
	}

	token := strings.TrimSpace(getenv(EnvToken))
	if token == "" {
		return Credential{}, false, nil
	}
	user := strings.TrimSpace(firstNonEmpty(getenv(EnvEmail), getenv(EnvUser)))

	scheme := inferScheme(user)
	if forced := strings.TrimSpace(getenv(EnvAuthScheme)); forced != "" {
		parsed, err := ParseScheme(forced)
		if err != nil {
			return Credential{}, false, errs.Auth("INVALID_SCHEME",
				"%s is not a valid auth scheme", EnvAuthScheme).
				WithDetail("%s", err.Error())
		}
		scheme = parsed
	}

	cred := Credential{
		Scheme: scheme,
		User:   user,
		Secret: Secret(token),
		Source: EnvToken,
		// Written out rather than left to the zero value, so that a grep for
		// SiteScoped finds all three providers and this one's answer is a
		// statement instead of an absence.
		SiteScoped: false,
	}
	if err := cred.Validate(); err != nil {
		// A half-configured environment is worth reporting loudly. Silently
		// falling through to the next provider would produce a confusing
		// "no credentials" from a caller who plainly supplied one.
		return Credential{}, false, errs.Auth("INCOMPLETE_CREDENTIAL",
			"%s is set but the credential is incomplete", EnvToken).
			WithDetail("%s", err.Error()).
			WithRemedy("set %s for Cloud, or set %s=bearer for a personal access token",
				EnvEmail, EnvAuthScheme)
	}
	return cred, true, nil
}

// inferScheme picks basic when a user was supplied and bearer otherwise, which
// matches how the two deployments issue credentials: Cloud pairs an email with
// an API token, Data Center issues a standalone personal access token.
func inferScheme(user string) Scheme {
	if user == "" {
		return Bearer
	}
	return Basic
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
