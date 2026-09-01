package transport_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kmoneil/jr/internal/transport"
)

// TestTheConnectionSaysWhatTheChainVerifiedAgainst is the distinction
// configuration cannot make.
//
// `jr doctor` reported the bundle that was configured, and a configured bundle
// is still one the site may never have needed: the chain verifies against the
// system roots and the setting does nothing. The only thing that can tell the
// two apart is the chain the verifier actually built, compared against what
// the bundle holds.
func TestTheConnectionSaysWhatTheChainVerifiedAgainst(t *testing.T) {
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

	get := transport.Request{Method: transport.MethodGet, Path: "/"}

	// Through the bundle: the site is reachable only because of it.
	withBundle, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: -1,
		TLS: transport.TLSOptions{CABundle: authority.bundle},
	})
	if err != nil {
		t.Fatalf("new with bundle: %v", err)
	}
	if _, err := withBundle.Do(context.Background(), get); err != nil {
		t.Fatalf("do with bundle: %v", err)
	}
	conn, ok := withBundle.LastConnection()
	if !ok {
		t.Fatal("a response arrived over TLS and no connection was observed")
	}
	if !conn.TLS || conn.Version != "TLS 1.3" {
		t.Errorf("observed %+v, want TLS 1.3", conn)
	}
	if conn.VerifiedAgainst != transport.VerifiedAgainstBundle {
		t.Errorf("verified against %q, want %q: the only root that can verify "+
			"this chain is the one the bundle supplied", conn.VerifiedAgainst,
			transport.VerifiedAgainstBundle)
	}

	// Through roots the bundle did not supply. A caller-built client whose
	// trust already holds the authority is what a system trust store looks
	// like from here: the chain verifies, and nothing in it came from a
	// bundle this client was configured with, because it was configured with
	// none.
	trusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: poolOf(authority.certPEM), MinVersion: tls.VersionTLS12,
	}}}
	withSystem, err := transport.New(transport.Options{
		BaseURL: srv.URL, Retries: -1, HTTPClient: trusted,
	})
	if err != nil {
		t.Fatalf("new with a trusting client: %v", err)
	}
	if _, err := withSystem.Do(context.Background(), get); err != nil {
		t.Fatalf("do with a trusting client: %v", err)
	}
	conn, ok = withSystem.LastConnection()
	if !ok || !conn.TLS {
		t.Fatalf("observed %+v, %v; want a TLS connection", conn, ok)
	}
	if conn.VerifiedAgainst != transport.VerifiedAgainstSystem {
		t.Errorf("verified against %q, want %q: no bundle was configured, so "+
			"nothing but the trust store could have verified it",
			conn.VerifiedAgainst, transport.VerifiedAgainstSystem)
	}
}

// TestABundleThatWasNotNeededIsReportedAsSuchAndSoIsOneThatWas drives the
// classifier over the case no loopback server can produce: a bundle that was
// configured and read, and a chain that verified without it.
func TestABundleThatWasNotNeededIsReportedAsSuchAndSoIsOneThatWas(t *testing.T) {
	authority := newCA(t)
	other := newCA(t)
	_, _, leafDER, _ := authority.issue(t, "leaf", "localhost")
	leaf, err := x509.ParseCertificate(leafDER[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	viaAuthority := []*x509.Certificate{leaf, authority.cert}

	for _, tc := range []struct {
		name   string
		chains [][]*x509.Certificate
		bundle []*x509.Certificate
		want   string
	}{
		{
			"no bundle configured",
			[][]*x509.Certificate{viaAuthority},
			nil,
			transport.VerifiedAgainstSystem,
		},
		{
			"the bundle holds the root the chain ends in",
			[][]*x509.Certificate{viaAuthority},
			[]*x509.Certificate{authority.cert},
			transport.VerifiedAgainstBundle,
		},
		// The finding this exists for: a bundle was named, read, and added,
		// and the chain never touched it.
		{
			"the bundle holds a root the chain does not use",
			[][]*x509.Certificate{viaAuthority},
			[]*x509.Certificate{other.cert},
			transport.VerifiedAgainstSystem,
		},
		// Two chains, one of which needs nothing from the bundle: the site
		// would have verified without it, so it was not needed.
		{
			"one chain uses the bundle and another does not",
			[][]*x509.Certificate{viaAuthority, {leaf, other.cert}},
			[]*x509.Certificate{authority.cert},
			transport.VerifiedAgainstSystem,
		},
		// A bundle carrying an intermediate the site does not send is a
		// bundle doing its job, and the root the chain ends in is not the
		// certificate that says so.
		{
			"the bundle holds an intermediate rather than the root",
			[][]*x509.Certificate{{leaf, authority.cert, other.cert}},
			[]*x509.Certificate{authority.cert},
			transport.VerifiedAgainstBundle,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transport.VerifiedAgainstForTest(tc.chains, tc.bundle); got != tc.want {
				t.Errorf("verified against %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAPlainSiteIsObservedAsUnencrypted keeps "no TLS" and "nothing observed"
// apart: a plain http:// site that answered is a connection, and it says so.
func TestAPlainSiteIsObservedAsUnencrypted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	defer srv.Close()

	client, err := transport.New(transport.Options{BaseURL: srv.URL, Retries: -1})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, ok := client.LastConnection(); ok {
		t.Fatal("a connection was observed before any request was made")
	}
	if _, err := client.Do(context.Background(), transport.Request{
		Method: transport.MethodGet, Path: "/",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	conn, ok := client.LastConnection()
	if !ok {
		t.Fatal("a plain site answered and no connection was observed")
	}
	if conn.TLS || conn.Version != "" || conn.VerifiedAgainst != "" {
		t.Errorf("observed %+v, want an unencrypted connection with nothing negotiated", conn)
	}
}

// TestAReplayedResponseObservesNoConnection is the case the card warned about.
//
// A cassette keeps a status, five headers, and a body, and no connection state
// at all, so a replayed run has no answer about the chain. A response for an
// https site that arrived with no TLS state is that case, and the honest
// report is that nothing was observed, never that the site was unencrypted.
func TestAReplayedResponseObservesNoConnection(t *testing.T) {
	cassette, err := transport.LoadCassette(
		filepath.Join("testdata", "basic-refused.datacenter.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	replayer := transport.NewReplayer(cassette)
	client, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := client.Do(context.Background(), transport.Request{
		Method: transport.MethodGet, Path: "/rest/api/2/serverInfo",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if conn, ok := client.LastConnection(); ok {
		t.Errorf("a replayed response was observed as %+v; it came from a file, "+
			"and reporting it as a connection would report a chain nobody verified", conn)
	}
}
