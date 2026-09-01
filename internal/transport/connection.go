package transport

import (
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"slices"
)

// Connection is what the transport observed about the connection its most
// recent response arrived on.
//
// It exists for one reader, `jr doctor`, and for a distinction configuration
// cannot make. A CA bundle that was named and read is still a bundle that may
// never be consulted: the site's chain verifies against the system roots
// without it, and the configuration is doing nothing. A negotiated TLS 1.2
// against a site that speaks 1.3 is a middlebox nobody mentioned. Both are
// properties of a connection that was actually made, which is why this is
// observed off a response rather than derived from the options.
//
// What it deliberately does not carry is the issuer. A root's subject names an
// employer and sometimes a department, and doctor's document is what somebody
// pastes into a bug report, which is why the account's email address is kept
// out of it too. "Verified against the bundle" answers the question the
// issuer would have been used to answer, without naming anybody.
type Connection struct {
	// TLS reports whether the connection was encrypted. False is a plain
	// http:// site that answered, and never the absence of an observation:
	// see LastConnection.
	TLS bool
	// Version is the negotiated protocol as crypto/tls names it, "TLS 1.3".
	// Empty when TLS is false.
	Version string
	// VerifiedAgainst is what the site's chain was verified against, one of
	// VerifiedAgainstSystem and VerifiedAgainstBundle. Empty when TLS is
	// false.
	VerifiedAgainst string
}

// The two things a chain can verify against.
const (
	// VerifiedAgainstSystem means the chain verified through the system trust
	// store, whether or not a bundle was configured. With one configured this
	// is the finding the type exists for: the bundle was not needed.
	VerifiedAgainstSystem = "system"
	// VerifiedAgainstBundle means no chain verified without a certificate
	// from the configured bundle, so the bundle is what made the site
	// reachable.
	VerifiedAgainstBundle = "bundle"
)

// VerifiedAgainstValues is the closed set, for a schema to publish.
var VerifiedAgainstValues = []string{VerifiedAgainstSystem, VerifiedAgainstBundle}

// LastConnection reports the connection the most recent response arrived on,
// and false when none has been observed.
//
// False is not "plain HTTP". A replayed fixture builds its responses out of a
// file and makes no connection, so a response for an https site that arrived
// with no TLS state is that case, and it is left unrecorded rather than
// recorded as unencrypted. A plain http site that answered is observed, as
// TLS false.
func (c *Client) LastConnection() (Connection, bool) {
	if p := c.connection.Load(); p != nil {
		return *p, true
	}
	return Connection{}, false
}

// observe records what the connection a response arrived on looked like.
func (c *Client) observe(target *url.URL, state *tls.ConnectionState) {
	switch {
	case state != nil:
		c.connection.Store(&Connection{
			TLS:             true,
			Version:         tls.VersionName(state.Version),
			VerifiedAgainst: verifiedAgainst(state.VerifiedChains, c.bundle),
		})
	case target.Scheme == "http":
		c.connection.Store(&Connection{})
	}
	// An https target answered by something that made no TLS connection is a
	// replayed or synthesised response. Nothing was observed, so nothing is
	// recorded, and a state from an earlier real response is left in place.
}

// verifiedAgainst decides whether the bundle was needed.
//
// The verifier reports every chain it could build to a trusted root. The site
// could have verified without the bundle if any one of those chains holds no
// certificate from it, so the bundle was needed only when every chain used
// it. A chain is compared certificate by certificate rather than at its root
// alone, because a bundle may carry an intermediate the site does not send,
// and that is a bundle doing its job.
func verifiedAgainst(chains [][]*x509.Certificate, bundle []*x509.Certificate) string {
	if len(bundle) == 0 || len(chains) == 0 {
		return VerifiedAgainstSystem
	}
	for _, chain := range chains {
		if !chainUses(chain, bundle) {
			return VerifiedAgainstSystem
		}
	}
	return VerifiedAgainstBundle
}

func chainUses(chain, bundle []*x509.Certificate) bool {
	for _, cert := range chain {
		if slices.ContainsFunc(bundle, cert.Equal) {
			return true
		}
	}
	return false
}
