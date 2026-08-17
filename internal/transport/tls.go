package transport

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
)

// TLSOptions is what a site needs beyond the operating system's own trust.
//
// It exists because a bare &http.Client{} inherits http.DefaultTransport, which
// covers more than it looks like it does — HTTPS_PROXY and NO_PROXY are honored
// and the system root store is used — and then stops. What it does not cover is
// most of the Data Center install base: an on-premises Jira behind a
// TLS-intercepting proxy re-signs every connection with an internal root, and if
// that root is not in the OS trust store, which on a container or a CI runner it
// usually is not, every request fails verification with no remedy attached. The
// tool does not have a rough edge there. It does not run.
//
// The environment can almost do this and not quite: SSL_CERT_FILE works by
// accident of Go's behaviour on Linux and not at all on macOS, and it is global
// rather than per-site. This is per-context because §2.3 is one login and many
// contexts, and somebody with a Cloud site and a Data Center site behind an
// internal CA would otherwise have to change their environment between
// invocations.
//
// There is no field here for skipping verification, and there is no flag, env
// var, or context setting for it either. See
// TestNothingCanDisableCertificateVerification in internal/lint.
type TLSOptions struct {
	// CABundle is a PEM file of certificates to trust **in addition to** the
	// system roots.
	//
	// Additive rather than replacing, because "trust one more CA" and "trust
	// only this CA" are different and much sharper tools, and a caller reaching
	// for the first should not silently get the second: replacing the pool
	// makes every public site on the same context fail, including the Atlassian
	// one somebody is about to try next.
	CABundle string
	// ClientCert and ClientKey are a PEM certificate and its key, presented
	// when the server asks for one. Data Center behind mTLS is ordinary in
	// regulated environments.
	ClientCert string
	ClientKey  string
}

// Empty reports whether nothing was configured, which is the common case and
// the one that must leave http.DefaultTransport alone.
func (o TLSOptions) Empty() bool {
	return o.CABundle == "" && o.ClientCert == "" && o.ClientKey == ""
}

// httpClient builds the client for these options, or nil when there is nothing
// to configure.
//
// A nil return is not a failure: it means the default transport is right, and
// keeping the default rather than rebuilding an identical one is what preserves
// its connection pooling and its proxy behaviour for every caller who needs
// nothing special.
func (o TLSOptions) httpClient() (*http.Client, error) {
	if o.Empty() {
		return nil, nil
	}
	cfg, err := o.tlsConfig()
	if err != nil {
		return nil, err
	}

	// Cloned from the default rather than built fresh, so proxy support,
	// timeouts, and HTTP/2 come along. A transport assembled by hand here would
	// be a second copy of the standard library's defaults, silently diverging
	// from them at the next Go release.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errs.Runtime("NO_TLS_SUPPORT",
			"this build's default HTTP transport cannot be configured for TLS")
	}
	t := base.Clone()
	t.TLSClientConfig = cfg
	return &http.Client{Transport: t}, nil
}

// tlsConfig turns the options into a verified configuration, refusing anything
// it cannot read rather than falling back to the system defaults.
//
// Falling back is the failure this whole file exists to prevent: a bundle that
// was named and then ignored produces the same verification error the caller
// was trying to fix, and nothing says the file was never read.
func (o TLSOptions) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if o.CABundle != "" {
		pool, err := o.rootPool()
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if o.ClientCert != "" || o.ClientKey != "" {
		cert, err := o.clientCertificate()
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// rootPool is the system trust plus the caller's bundle.
func (o TLSOptions) rootPool() (*x509.CertPool, error) {
	pem, err := os.ReadFile(o.CABundle) //nolint:gosec // the path is the caller's, and reading it is the point.
	if err != nil {
		return nil, errs.Usage("INVALID_CA_BUNDLE",
			"cannot read the CA bundle").
			WithDetail("%s", o.CABundle).
			WithRemedy("point --ca-bundle at a readable PEM file of certificates").
			Wrap(err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		// Not a fallback to an empty pool. An empty pool trusts nothing but the
		// bundle, which is the "trust only this CA" tool this deliberately does
		// not hand out by accident.
		return nil, errs.Runtime("NO_SYSTEM_TRUST",
			"this system's root certificates cannot be read, so a bundle cannot be added to them").
			WithRemedy("report this: the CA bundle is additive and needs the system roots to add to").
			Wrap(err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		// AppendCertsFromPEM reports only whether *any* certificate was added,
		// so this is "nothing in the file was a certificate" rather than "every
		// certificate was fine". A file of one good and one malformed PEM block
		// is accepted, which is the standard library's behaviour and not
		// something to reimplement here.
		return nil, errs.Usage("INVALID_CA_BUNDLE",
			"the CA bundle holds no certificates this tool can read").
			WithDetail("%s", o.CABundle).
			WithRemedy("check the file is PEM, the -----BEGIN CERTIFICATE----- form, " +
				"rather than DER or a private key")
	}
	return pool, nil
}

// clientCertificate loads the certificate this client presents.
func (o TLSOptions) clientCertificate() (tls.Certificate, error) {
	switch {
	case o.ClientCert == "":
		return tls.Certificate{}, errs.Usage("INCOMPLETE_CLIENT_CERT",
			"a client key was given with no certificate").
			WithRemedy("pass both, or neither")
	case o.ClientKey == "":
		return tls.Certificate{}, errs.Usage("INCOMPLETE_CLIENT_CERT",
			"a client certificate was given with no key").
			WithRemedy("pass both, or neither")
	}

	cert, err := tls.LoadX509KeyPair(o.ClientCert, o.ClientKey)
	if err != nil {
		// The standard library's message says which half is wrong, including
		// the case worth naming on its own: a key that does not match the
		// certificate beside it.
		return tls.Certificate{}, errs.Usage("INVALID_CLIENT_CERT",
			"cannot use the client certificate and key").
			WithDetail("certificate %s, key %s", o.ClientCert, o.ClientKey).
			WithRemedy("check both are PEM and that the key belongs to the certificate").
			Wrap(err)
	}
	return cert, nil
}

// ProxyFor reports the proxy the standard library would use for this site, or
// empty when it would go direct.
//
// It exists because the proxy already worked and nothing said it was happening.
// HTTPS_PROXY and NO_PROXY are honored by http.DefaultTransport, so a request
// can be going somewhere nobody in this tool chose, and from the outside that
// looks like a network fault at the site rather than a hop in between. A
// connection this tool cannot describe is the part of the transport's
// guarantees that nothing here can assert.
//
// The URL is the caller's site. A malformed one reports no proxy rather than an
// error: this is a line in a report about the connection, and failing the
// report because the site is unparseable would replace a useful answer about
// everything else with a complaint the caller has already been given.
func ProxyFor(siteURL string) string {
	if strings.TrimSpace(siteURL) == "" {
		return ""
	}
	u, err := url.Parse(siteURL)
	if err != nil || u.Host == "" {
		return ""
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base.Proxy == nil {
		return ""
	}
	proxy, err := base.Proxy(&http.Request{URL: u, Header: http.Header{}})
	if err != nil || proxy == nil {
		return ""
	}
	// Redacted: a proxy URL can carry userinfo, and this string goes to stdout
	// as data. The same rule the trace applies to a request URL.
	proxy.User = nil
	return proxy.String()
}
