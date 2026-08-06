// Package site describes the Jira instance a context points at.
//
// Deployment kind and API version are detected, never declared. The incumbent
// freezes them into config at init time, and they go stale the moment the
// server is upgraded — which surfaces much later as an endpoint that used to
// work returning 404.
package site

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Kind is which flavour of Jira a site runs.
type Kind string

// The deployments this project supports.
const (
	Cloud      Kind = "cloud"
	DataCenter Kind = "datacenter"
)

// ProbePath is the endpoint that reports what a site is. It is on API v2
// because that is the one version both deployments have always served.
const ProbePath = "/rest/api/2/serverInfo"

// DefaultTTL is how long a probe result is trusted. A day is long enough that
// the probe costs nothing in practice, and short enough that an upgrade is
// picked up the next day without anyone clearing a cache.
const DefaultTTL = 24 * time.Hour

// Info is what a probe learned about a site.
type Info struct {
	Kind    Kind   `json:"kind"`
	Version string `json:"version"`
	// VersionNumbers is the parsed version, for comparisons that need it.
	VersionNumbers []int  `json:"versionNumbers,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"`
	// ProbedAt is when this was learned, which is what the TTL is measured
	// from.
	ProbedAt time.Time `json:"probedAt"`
	// Cached reports whether this came from disk rather than the network.
	Cached bool `json:"-"`
}

// APIBase is the REST prefix for this deployment.
//
// Cloud has moved to v3 for most endpoints; Data Center is still v2. Choosing
// between them is exactly why the probe exists.
func (i Info) APIBase() string {
	if i.Kind == Cloud {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

// AgileBase is the prefix for the Agile API, which boards, sprints, and epics
// live behind.
//
// It is not a third API version — it is a different API. Jira Software ships
// `/rest/agile/1.0` on both deployments and has never versioned it alongside
// the platform REST API, so a Cloud site serving v3 platform endpoints still
// serves agile 1.0. Sending an agile request to APIBase is a 404 that reads
// like a missing board.
//
// This is a method rather than a constant because it is the honest place to
// put a split if one ever appears, and because a caller reaching for a package
// constant would be one edit away from hardcoding the path at every call site.
func (i Info) AgileBase() string { return "/rest/agile/1.0" }

// CursorPaginated reports whether this deployment pages by opaque cursor.
//
// Cloud does. Data Center still pages by offset, which is why the page token
// this tool hands out is opaque: it wraps whichever the server actually uses,
// so a caller never has to know and never gets an offset that cannot be
// honored.
func (i Info) CursorPaginated() bool { return i.Kind == Cloud }

// serverInfo is the response shape, which both deployments share.
type serverInfo struct {
	BaseURL        string `json:"baseUrl"`
	Version        string `json:"version"`
	VersionNumbers []int  `json:"versionNumbers"`
	DeploymentType string `json:"deploymentType"`
}

// Doer is the part of the transport this package needs. Narrow so a test can
// supply a response without a server.
type Doer interface {
	Do(ctx context.Context, r transport.Request) (*transport.Response, error)
}

// Probe asks a site what it is.
func Probe(ctx context.Context, client Doer, now time.Time) (Info, error) {
	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   ProbePath,
	})
	if err != nil {
		return Info{}, err
	}
	if err := transport.Err(resp); err != nil {
		return Info{}, err
	}

	var raw serverInfo
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return Info{}, errs.Remote("MALFORMED_SERVER_INFO",
			"%s did not return usable server information", ProbePath).
			WithRequestID(resp.RequestID).
			WithRemedy("check that the site URL points at a Jira instance, " +
				"not a proxy or a login page").
			Wrap(err)
	}

	kind, typeErr := parseDeploymentType(raw.DeploymentType)
	if typeErr != nil {
		return Info{}, typeErr.WithRequestID(resp.RequestID)
	}

	return Info{
		Kind:           kind,
		Version:        raw.Version,
		VersionNumbers: raw.VersionNumbers,
		BaseURL:        raw.BaseURL,
		ProbedAt:       now,
	}, nil
}

// parseDeploymentType maps what the server calls itself onto what this tool
// needs to decide.
//
// An unrecognized value is refused rather than defaulted. Guessing Cloud would
// send v3 requests to a v2 server and produce a 404 that looks like a missing
// issue; guessing Data Center would silently use offset pagination against a
// cursor API, which is the incumbent's exact bug.
func parseDeploymentType(s string) (Kind, *errs.Error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "cloud":
		return Cloud, nil
	case "server", "datacenter", "data center":
		return DataCenter, nil
	case "":
		return "", errs.Remote("UNKNOWN_DEPLOYMENT",
			"%s reported no deployment type", ProbePath).
			WithRemedy("check that the site URL points at a Jira instance")
	default:
		return "", errs.Remote("UNKNOWN_DEPLOYMENT",
			"%s reported an unrecognized deployment type", ProbePath).
			WithDetail("%q", s).
			WithRemedy("report this: the tool cannot choose an API version for it")
	}
}

// Resolver probes a site and remembers the answer.
type Resolver struct {
	Client Doer
	Cache  *Cache
	// TTL overrides DefaultTTL.
	TTL time.Duration
	// Now is the time source, so a test can expire an entry without waiting.
	Now func() time.Time
	// Refresh forces a probe even when a valid cache entry exists.
	Refresh bool
}

// cacheKey is the cache entry the deployment probe is stored under.
const cacheKey = "deployment"

// Resolve returns what a site is, from cache when it is still fresh.
//
// Caching is a feature rather than an optimization: without it every single
// invocation pays a round trip before it can decide which endpoint to call.
func (r *Resolver) Resolve(ctx context.Context) (Info, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	ttl := r.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	if r.Cache != nil {
		// Share the resolver's clock, so an entry this resolver writes is one
		// this resolver will accept back.
		r.Cache.Now = now
	}

	if r.Cache != nil && !r.Refresh {
		var cached Info
		if ok, err := r.Cache.Get(cacheKey, ttl, &cached); err != nil {
			return Info{}, err
		} else if ok {
			cached.Cached = true
			return cached, nil
		}
	}

	info, err := Probe(ctx, r.Client, now())
	if err != nil {
		return Info{}, err
	}
	if r.Cache != nil {
		// A cache that cannot be written is not worth failing the command for:
		// the probe succeeded, and the only cost is doing it again next time.
		_ = r.Cache.Put(cacheKey, info)
	}
	return info, nil
}
