package transport_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// The certificates here are generated per-run rather than committed, because a
// committed one expires and then fails a suite that has nothing to do with what
// broke. Nothing resolves and nothing dials off the loopback, so the hosts lint
// is satisfied by construction.

// ca is a generated authority and the files a caller would point the tool at.
type ca struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	bundle  string // Path to the PEM bundle.
	dir     string
}

func newCA(t *testing.T) *ca {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "jr test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	dir := t.TempDir()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(bundle, certPEM, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return &ca{cert: cert, key: key, certPEM: certPEM, bundle: bundle, dir: dir}
}

// issue signs a certificate for the given hosts, and writes the pair.
func (c *ca) issue(t *testing.T, name string, hosts ...string) (certPath, keyPath string, chain [][]byte, key *ecdsa.PrivateKey) {
	t.Helper()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &leafKey.PublicKey, c.key)
	if err != nil {
		t.Fatalf("sign %s: %v", name, err)
	}

	certPath = filepath.Join(c.dir, name+".pem")
	keyPath = filepath.Join(c.dir, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(
		&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath, [][]byte{der}, leafKey
}

// TestASiteBehindAPrivateCAIsReachedWithABundle is the case that made this
// feature: an on-premises Jira behind a TLS-intercepting proxy, whose internal
// root is in no operating system's trust store.
func TestASiteBehindAPrivateCAIsReachedWithABundle(t *testing.T) {
	authority := newCA(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	certPath, keyPath, _, _ := authority.issue(t, "server", "127.0.0.1", "localhost")
	pair, err := loadPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	srv.TLS = serverTLS(pair)
	srv.StartTLS()
	defer srv.Close()

	// Without the bundle: the failure the card describes, and it has to be a
	// failure rather than a silent success.
	plain, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: -1})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := plain.Do(context.Background(), transport.Request{
		Method: transport.MethodGet, Path: "/",
	}); err == nil {
		t.Fatal("a private CA verified against the system roots, so this test proves nothing")
	}

	// With it.
	withCA, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: -1,
		TLS: transport.TLSOptions{CABundle: authority.bundle},
	})
	if err != nil {
		t.Fatalf("new with bundle: %v", err)
	}
	resp, err := withCA.Do(context.Background(), transport.Request{
		Method: transport.MethodGet, Path: "/",
	})
	if err != nil {
		t.Fatalf("a site behind the bundle's CA is still unreachable: %v", err)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("body = %q", resp.Body)
	}
}

// TestABundleIsAddedToTheSystemRootsAndDoesNotReplaceThem holds the choice the
// card called out: "trust one more CA" and "trust only this CA" are different
// tools, and reaching for the first must not silently hand over the second.
func TestABundleIsAddedToTheSystemRootsAndDoesNotReplaceThem(t *testing.T) {
	authority := newCA(t)
	system, err := x509.SystemCertPool()
	if err != nil {
		t.Skipf("no system trust store to compare against: %v", err)
	}

	client, err := transport.New(transport.Options{
		BaseURL: "https://jira.invalid", Retries: -1,
		TLS: transport.TLSOptions{CABundle: authority.bundle},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// The pool this built, against the pool a replacing implementation would
	// have built. Equal means the system roots were dropped, which turns every
	// public site on the same context into a verification failure.
	bundleOnly := x509.NewCertPool()
	if !bundleOnly.AppendCertsFromPEM(authority.certPEM) {
		t.Fatal("the generated CA is not a PEM this test can read")
	}
	if system.Equal(bundleOnly) {
		t.Skip("this machine's system trust store is empty, so the two pools " +
			"are the same and the comparison says nothing")
	}
	if transport.RootPool(client).Equal(bundleOnly) {
		t.Error("the bundle replaced the system roots rather than being added " +
			"to them: `--ca-bundle` is \"trust one more CA\", and \"trust only " +
			"this CA\" is a different and much sharper tool")
	}
}

// TestAClientCertificateIsPresentedWhenTheServerAsks covers mTLS, which is
// ordinary for Data Center in a regulated environment.
func TestAClientCertificateIsPresentedWhenTheServerAsks(t *testing.T) {
	authority := newCA(t)
	serverCert, serverKey, _, _ := authority.issue(t, "server", "127.0.0.1", "localhost")
	clientCert, clientKey, _, _ := authority.issue(t, "client")

	var seen string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				seen = r.TLS.PeerCertificates[0].Subject.CommonName
			}
			_, _ = w.Write([]byte(`{}`))
		}))
	pair, err := loadPair(serverCert, serverKey)
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	cfg := serverTLS(pair)
	cfg.ClientAuth = requireAndVerify()
	cfg.ClientCAs = poolOf(authority.certPEM)
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()

	client, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: -1,
		TLS: transport.TLSOptions{
			CABundle:   authority.bundle,
			ClientCert: clientCert,
			ClientKey:  clientKey,
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := client.Do(context.Background(), transport.Request{
		Method: transport.MethodGet, Path: "/",
	}); err != nil {
		t.Fatalf("mTLS handshake failed: %v", err)
	}
	if seen != "client" {
		t.Errorf("the server saw peer %q, want the client certificate", seen)
	}
}

// TestABundleThatCannotBeReadIsRefusedRatherThanIgnored. Falling back to the
// system roots would produce the same verification error the caller was trying
// to fix, with nothing saying the file was never read.
func TestABundleThatCannotBeReadIsRefusedRatherThanIgnored(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("this is not a certificate\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for name, bundle := range map[string]string{
		"missing":   filepath.Join(dir, "nothing-here.pem"),
		"not a PEM": notPEM,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := transport.New(transport.Options{
				BaseURL: "https://jira.invalid", Retries: -1,
				TLS: transport.TLSOptions{CABundle: bundle},
			})
			structured := requireCode(t, err, "INVALID_CA_BUNDLE")
			if !strings.Contains(structured.Detail, bundle) {
				t.Errorf("the refusal does not name the file: %q", structured.Detail)
			}
			if structured.Remedy == "" {
				t.Error("the refusal carries no remedy")
			}
		})
	}
}

// TestHalfAClientCertificateIsRefused. A certificate with no key cannot be
// presented, and a key with no certificate names nothing; either way the
// connection would fall back to no client certificate at all and fail at the
// handshake with a message about the server.
func TestHalfAClientCertificateIsRefused(t *testing.T) {
	authority := newCA(t)
	certPath, keyPath, _, _ := authority.issue(t, "client")

	for name, opt := range map[string]transport.TLSOptions{
		"certificate with no key": {ClientCert: certPath},
		"key with no certificate": {ClientKey: keyPath},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := transport.New(transport.Options{
				BaseURL: "https://jira.invalid", Retries: -1, TLS: opt,
			})
			_ = requireCode(t, err, "INCOMPLETE_CLIENT_CERT")
		})
	}
}

// TestAKeyThatDoesNotMatchItsCertificateIsRefused, and refused here rather than
// at the handshake, where the error names the server and not the file.
func TestAKeyThatDoesNotMatchItsCertificateIsRefused(t *testing.T) {
	authority := newCA(t)
	certPath, _, _, _ := authority.issue(t, "client")
	_, otherKey, _, _ := authority.issue(t, "other")

	_, err := transport.New(transport.Options{
		BaseURL: "https://jira.invalid", Retries: -1,
		TLS: transport.TLSOptions{ClientCert: certPath, ClientKey: otherKey},
	})
	structured := requireCode(t, err, "INVALID_CLIENT_CERT")
	if !strings.Contains(structured.Detail, certPath) {
		t.Errorf("the refusal does not name the certificate: %q", structured.Detail)
	}
}

// TestNoTLSSettingsLeavesTheDefaultTransportAlone. The common case is a Cloud
// site with nothing configured, and rebuilding an identical transport there
// would trade the standard library's connection pooling and proxy handling for
// nothing at all.
func TestNoTLSSettingsLeavesTheDefaultTransportAlone(t *testing.T) {
	client, err := transport.New(transport.Options{
		BaseURL: "https://jira.invalid", Retries: -1,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if transport.HasCustomTransport(client) {
		t.Error("a client with no TLS settings built its own transport")
	}
}

// TestARecordingRunStillGoesThroughTheConfiguredChain covers the combination
// that would otherwise drop the bundle without a word.
//
// The recorder is built before the site's TLS settings are known, so it takes
// the default transport. A run that is both recording and talking to a site
// behind a private CA would then send every request through the default chain,
// fail verification, and leave nothing saying the bundle was never used — the
// same "named it and ignored it" failure the bundle refusals exist to prevent.
func TestARecordingRunStillGoesThroughTheConfiguredChain(t *testing.T) {
	authority := newCA(t)
	rec := transport.NewRecorder(nil, transport.Cloud)
	if transport.RecorderNext(rec) != http.DefaultTransport {
		t.Fatal("a recorder built with no transport does not start on the default one")
	}

	if _, err := transport.New(transport.Options{
		BaseURL: "https://jira.invalid", Retries: -1,
		RoundTripper: rec,
		TLS:          transport.TLSOptions{CABundle: authority.bundle},
	}); err != nil {
		t.Fatalf("new: %v", err)
	}
	if transport.RecorderNext(rec) == http.DefaultTransport {
		t.Error("the recorder still dials through the default transport, so a " +
			"recording run against a site behind a private CA would fail " +
			"verification with the bundle configured and unused")
	}
}

// TestARecorderBuiltOnANamedTransportKeepsIt. Only the default is replaced: a
// caller that named a transport meant that one, and the replay path builds
// exactly that.
func TestARecorderBuiltOnANamedTransportKeepsIt(t *testing.T) {
	authority := newCA(t)
	named := &countingTripper{}
	rec := transport.NewRecorder(named, transport.Cloud)

	if _, err := transport.New(transport.Options{
		BaseURL: "https://jira.invalid", Retries: -1,
		RoundTripper: rec,
		TLS:          transport.TLSOptions{CABundle: authority.bundle},
	}); err != nil {
		t.Fatalf("new: %v", err)
	}
	if transport.RecorderNext(rec) != http.RoundTripper(named) {
		t.Error("a recorder built on a named transport had it replaced")
	}
}

// countingTripper is a transport that is not the default one.
type countingTripper struct{ calls int }

func (c *countingTripper) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errs.Runtime("NOT_DIALLED", "this test never sends anything")
}

// serverTLS is a server configuration presenting this pair.
func serverTLS(pair tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}

// loadPair reads a certificate and key from disk, as the server side of these
// tests needs them.
func loadPair(certPath, keyPath string) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// poolOf builds a pool from PEM, for the server's own client-certificate
// verification.
func poolOf(pemBytes []byte) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)
	return pool
}

// requireAndVerify is the server asking for a client certificate and checking
// it, which is what mTLS means.
func requireAndVerify() tls.ClientAuthType { return tls.RequireAndVerifyClientCert }

// requireCode fails unless err is this tool's structured error with this code.
func requireCode(t *testing.T, err error, code string) *errs.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	structured, ok := errs.AsError(err)
	if !ok {
		t.Fatalf("error is not structured: %v", err)
	}
	if structured.Code != code {
		t.Fatalf("code = %q, want %s (%v)", structured.Code, code, err)
	}
	return structured
}
