package cli

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/kmoneil/jira-cli/internal/auth"
	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/jctx"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// session implements registry.Session.
//
// It exists so a resource asks for a connection and gets one, without learning
// where a site, a credential, or a context comes from. Everything is lazy: a
// command that never calls Connect never resolves a credential and never probes
// the deployment, which is why `jr version` costs no round trip.
type session struct {
	app      *app
	resolved *jctx.Resolved

	once   sync.Once
	client *transport.Client
	info   site.Info
	err    error
}

// newSession builds the session for this invocation.
func (a *app) newSession() (*session, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	resolved, err := a.resolve(cfg)
	if err != nil {
		return nil, err
	}
	return &session{app: a, resolved: resolved}, nil
}

// Project implements registry.Session.
func (s *session) Project() string { return s.resolved.Project }

// Board implements registry.Session.
func (s *session) Board() string { return s.resolved.Board }

// RequireProject implements registry.Session.
func (s *session) RequireProject() (string, error) { return s.resolved.RequireProject() }

// CheckWritable implements registry.Session.
func (s *session) CheckWritable(command string) error {
	return s.resolved.CheckWritable(command)
}

// Connect implements registry.Session. The connection and the deployment probe
// are built once and reused, so paging does not re-probe on every page.
func (s *session) Connect(ctx context.Context) (*transport.Client, site.Info, error) {
	s.once.Do(func() { s.client, s.info, s.err = s.connect(ctx) })
	return s.client, s.info, s.err
}

func (s *session) connect(ctx context.Context) (*transport.Client, site.Info, error) {
	siteURL, err := s.resolved.RequireSite()
	if err != nil {
		return nil, site.Info{}, s.app.explainMissingSite(err)
	}

	cred, err := s.app.chain().Resolve(siteURL)
	if err != nil {
		return nil, site.Info{}, err
	}

	client, err := transport.New(transport.Options{
		BaseURL:     siteURL,
		Auth:        authorizerFor(cred),
		Retries:     s.app.retries,
		MaxRequests: s.app.maxRequests,
		UserAgent:   userAgent(),
		Tracer:      s.app.tracer(),
	})
	if err != nil {
		return nil, site.Info{}, err
	}

	info, err := s.probe(ctx, client, siteURL)
	if err != nil {
		return nil, site.Info{}, err
	}
	return client, info, nil
}

// probe resolves what the site is, from cache when it is still fresh.
//
// The deployment is detected rather than declared, because a value frozen into
// config goes stale the moment the server is upgraded — and the failure then
// looks like an endpoint that used to work returning 404.
func (s *session) probe(
	ctx context.Context, client *transport.Client, siteURL string,
) (site.Info, error) {
	resolver := &site.Resolver{Client: client, Refresh: s.app.refresh}

	if paths, err := s.app.resolvePaths(); err == nil {
		resolver.Cache = &site.Cache{Dir: paths.SiteCache(siteURL)}
	}
	return resolver.Resolve(ctx)
}

// authorizerFor adapts a credential to what the transport asks for.
//
// The two RequestInfo types are structurally identical but are distinct named
// types, so this three-line bridge is what keeps internal/auth free of any
// dependency on the transport — and the transport free of any knowledge of
// where a credential comes from.
func authorizerFor(cred auth.Credential) transport.Authorizer {
	inner := auth.Authorizer{Credential: cred}
	return transport.AuthorizerFunc(
		func(ctx context.Context, req transport.RequestInfo) (map[string]string, error) {
			return inner.Authorize(ctx, auth.RequestInfo{Method: req.Method, URL: req.URL})
		},
	)
}

// explainMissingSite adds what the credential store knows to a "no site"
// error.
//
// A caller who has stored a credential has already told this tool which site
// they mean, and repeating a generic "pass --site" back at them is unhelpful
// when the answer is sitting on disk.
func (a *app) explainMissingSite(err error) error {
	e := errs.Coerce(err)
	if e.Code != "NO_SITE" {
		return err
	}

	store, storeErr := a.credentialStore()
	if storeErr != nil {
		return err
	}
	hosts, hostsErr := store.Hosts()
	if hostsErr != nil || len(hosts) == 0 {
		return err
	}

	e = e.WithDetail("credentials are stored for: %s", strings.Join(hosts, ", "))
	if len(hosts) == 1 {
		return e.WithRemedy("run `%s context create %s --site %s`, or pass --site %s",
			buildinfo.App, contextNameFor(hosts[0]), hosts[0], hosts[0])
	}
	return e.WithRemedy("run `%s context create <name> --site <one of the above>`",
		buildinfo.App)
}

// userAgent identifies this build to the server, including the profile, so a
// site administrator can tell an agent build from a human one.
func userAgent() string {
	return buildinfo.App + "/" + buildinfo.Release + " (" + buildinfo.Profile() + ")"
}

// tracer returns the debug tracer, or nil when --debug was not passed.
//
// Debug output goes to stderr. It is redacted inside the transport, so nothing
// here has to remember to scrub it.
func (a *app) tracer() transport.Tracer {
	if !a.debug {
		return nil
	}
	return transport.NewTextTracer(a.stderr)
}

// jiraSession builds the session a command's invocation carries, or nil when
// this build has no way to reach Jira.
func (a *app) jiraSession() (registry.Session, error) {
	s, err := a.newSession()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// verifyCredential proves a credential works against a site before it is
// stored.
//
// Two questions, in order: is this really a Jira API — which catches a wrong
// host or a missing context path — and does this credential authenticate. The
// deployment probe answers anonymously on most instances and so proves nothing
// about the token, which is why the account fetch follows it.
//
// The probe is not cached here. Caching a result gathered with a credential that
// might be about to be rejected would leave a wrong answer on disk for a day.
func (a *app) verifyCredential(
	ctx context.Context, siteURL string, cred auth.Credential,
) (site.Account, error) {
	client, err := transport.New(transport.Options{
		BaseURL:   siteURL,
		Auth:      authorizerFor(cred),
		Retries:   a.retries,
		UserAgent: userAgent(),
		Tracer:    a.tracer(),
	})
	if err != nil {
		return site.Account{}, err
	}

	info, err := site.Probe(ctx, client, time.Now())
	if err != nil {
		return site.Account{}, err
	}
	return site.Whoami(ctx, client, info)
}
