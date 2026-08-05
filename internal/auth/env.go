package auth

import (
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
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
