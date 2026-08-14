package cli

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/jctx"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
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

	metaOnce sync.Once
	meta     *site.Metadata

	ledgerOnce sync.Once
	ledger     *idem.Ledger
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

// Site implements registry.Session.
//
// The error is dropped on purpose. A session with no resolvable site is one
// whose first Connect will fail with `NO_SITE` and a remedy; saying so twice,
// once as an empty envelope attribute, helps nobody. Absent means "this answer
// names no Jira", which is the honest reading either way.
func (s *session) Site() string {
	siteURL, err := s.resolved.RequireSite()
	if err != nil {
		return ""
	}
	return siteURL
}

// Project implements registry.Session.
func (s *session) Project() string { return s.resolved.Project }

// Board implements registry.Session.
func (s *session) Board() string { return s.resolved.Board }

// Fields implements registry.Session.
func (s *session) Fields() []string { return slices.Clone(s.resolved.Fields) }

// RequireProject implements registry.Session.
func (s *session) RequireProject() (string, error) { return s.resolved.RequireProject() }

// RequireBoard implements registry.Session.
func (s *session) RequireBoard() (string, error) { return s.resolved.RequireBoard() }

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

	// The recorder wraps the transport rather than sitting inside it, so the
	// probe is captured too — it is the first request any run makes and the
	// one whose answer decides every path taken afterwards.
	rec, save := s.app.recorder(siteURL)

	opts := transport.Options{
		BaseURL:     siteURL,
		Auth:        authorizerFor(cred),
		Retries:     s.app.retries,
		MaxRequests: s.app.maxRequests,
		UserAgent:   userAgent(),
		Tracer:      s.app.tracer(),
	}
	if rec != nil {
		opts.RoundTripper = rec
		// Registered before anything is sent, not after the probe answers.
		//
		// It used to be registered on the way out, once the deployment was
		// known, which meant a run that *failed* wrote no cassette at all —
		// and a failure is the conversation most worth recording. A Data
		// Center 11 refusing basic authentication answers the probe with a 403
		// and nothing else happens, so the exchange this tool most needed a
		// fixture of was the one it could not capture.
		rec.Cassette().Source = transport.Recorded
		s.app.cleanup = append(s.app.cleanup, save)
	}
	client, err := transport.New(opts)
	if err != nil {
		return nil, site.Info{}, err
	}

	info, err := s.probe(ctx, client, siteURL)
	if err != nil {
		return nil, site.Info{}, err
	}
	if rec != nil {
		// The deployment is a label on the cassette and is not known until the
		// probe has answered. A cassette saved before that keeps the
		// placeholder, which is the honest answer: nothing told us.
		rec.Cassette().Deployment = recordedDeployment(info)
	}
	return client, info, nil
}

// Metadata implements registry.Session.
//
// The accessor is built once per invocation and memoizes each catalogue it
// fetches, so a command that resolves a field name while validating its flags
// and again while running costs one request rather than two.
func (s *session) Metadata(ctx context.Context) (*site.Metadata, error) {
	client, info, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	s.metaOnce.Do(func() {
		s.meta = &site.Metadata{Client: client, Info: info, Refresh: s.app.refresh}
		if paths, err := s.app.resolvePaths(); err == nil {
			s.meta.Cache = &site.Cache{Dir: paths.SiteCache(s.resolved.Site)}
		}
	})
	return s.meta, nil
}

// Idempotency implements registry.Session.
//
// A ledger that cannot be located is nil rather than an error: the commands
// that use it say so, and failing here would stop a mutation that has nothing
// to do with idempotency.
func (s *session) Idempotency() *idem.Ledger {
	s.ledgerOnce.Do(func() {
		paths, err := s.app.resolvePaths()
		if err != nil {
			return
		}
		s.ledger = &idem.Ledger{Path: paths.IdempotencyFile()}
	})
	return s.ledger
}

// probe resolves what the site is, from cache when it is still fresh.
//
// The deployment is detected rather than declared, because a value frozen into
// config goes stale the moment the server is upgraded — and the failure then
// looks like an endpoint that used to work returning 404.
func (s *session) probe(
	ctx context.Context, client *transport.Client, siteURL string,
) (site.Info, error) {
	// A declared version skips the probe rather than overriding its answer.
	// The point of the escape hatch is a site whose serverInfo cannot be read
	// at all, so asking it anyway would fail before the override could help.
	if v := s.resolved.APIVersion; v != 0 {
		return site.Declare(v)
	}

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

// userAgent identifies this build to the server, e.g. `jr/1.2.0 (reader)`.
//
// The profile stays in it, which was an open question. It names the compile-out
// guarantee, and an administrator reading their access log to work out which
// client touched an issue learns from `(reader)` that this one could not have —
// the mutating verbs are not in that binary. That is exonerating information
// they can get nowhere else, and a capability class rather than a secret.
//
// The release is always a semantic version; scripts/version.sh guarantees it.
// It used to be whatever `git describe --always` produced, which on an untagged
// tree is a bare commit hash — `jr/786d271`, which tells an administrator
// nothing, does not sort, and does not announce that it is not a version.
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
	account, err := site.Whoami(ctx, client, info)
	if err != nil {
		return site.Account{}, explainScheme(err, info, cred)
	}
	return account, nil
}

// explainScheme names the likeliest cause of a rejected credential when the
// deployment and the scheme disagree.
//
// A bare 401 says "Jira rejected the credentials" and offers "check that the
// token has not expired", which is the wrong advice for the commonest way to
// get this wrong: pairing --email with a Data Center personal access token. The
// token is fine. It was sent as Basic because an email was given, and Data
// Center wants it as a bearer token.
//
// By this point both halves are known — the probe has already said which
// deployment this is, and the scheme was chosen here — so the guess costs
// nothing and is not a guess. It only ever *adds* to the detail: the underlying
// refusal is still what it was.
func explainScheme(err error, info site.Info, cred auth.Credential) error {
	e := errs.Coerce(err)
	if e.Code != "UNAUTHORIZED" || info.Kind == site.Cloud || cred.Scheme != auth.Basic {
		return err
	}
	return e.
		WithDetail("%s", joinScheme(e.Detail,
			"this is a "+string(info.Kind)+" instance and the credential was "+
				"sent as Basic, because a user or an email was given")).
		WithRemedy("a Data Center personal access token is a bearer token: " +
			"run the same command without --email or --user. Pair --user with " +
			"a password only on an instance that still accepts Basic")
}

// joinScheme appends the explanation to whatever detail the failure already
// carried, so the endpoint that refused is not lost.
func joinScheme(detail, added string) string {
	if detail == "" {
		return added
	}
	return detail + "; " + added
}
