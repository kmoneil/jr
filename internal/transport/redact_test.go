package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/transport"
)

// theToken is the credential every test in this file hunts for. If it appears
// anywhere in debug output, the redaction is broken.
const theToken = "ATATT3xFfGF0-super-secret-token-value-9c4e1a"

func TestIsSensitiveHeader(t *testing.T) {
	sensitive := []string{
		"Authorization", "authorization", "AUTHORIZATION",
		"Proxy-Authorization", "Cookie", "Set-Cookie",
		"WWW-Authenticate", "X-Atlassian-Token",
		// Covered by the substring heuristic, so a header nobody has thought
		// of yet is redacted the first time a proxy adds it.
		"X-Acme-Api-Token", "X-Session-Id", "X-Client-Secret",
		"X-My-Password", "X-Private-Thing", "X-Api-Key", "X-Credential-Ref",
	}
	for _, name := range sensitive {
		if !transport.IsSensitiveHeader(name) {
			t.Errorf("IsSensitiveHeader(%q) = false", name)
		}
	}

	safe := []string{
		"Content-Type", "Accept", "User-Agent", "X-Request-Id",
		"Retry-After", "Content-Length", "X-Atlassian-Request-Id",
	}
	for _, name := range safe {
		if transport.IsSensitiveHeader(name) {
			t.Errorf("IsSensitiveHeader(%q) = true, which would hide useful debug output", name)
		}
	}
}

func TestRedactHeaderDoesNotMutateTheOriginal(t *testing.T) {
	original := http.Header{}
	original.Set("Authorization", "Bearer "+theToken)
	original.Set("Content-Type", "application/json")

	redacted := transport.RedactHeader(original)

	if got := redacted.Get("Authorization"); got != transport.Redacted {
		t.Errorf("Authorization = %q, want %q", got, transport.Redacted)
	}
	if got := redacted.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want it left alone", got)
	}
	// Redacting in place would strip the caller's own authentication from the
	// request that is about to be sent.
	if got := original.Get("Authorization"); got != "Bearer "+theToken {
		t.Errorf("the original header was mutated: %q", got)
	}
}

// TestRedactedMarkerSurvivesEscaping is why the marker carries no punctuation:
// it has to be findable by the same string in a header, a URL, and an error
// message, including by the tests that assert redaction happened at all.
func TestRedactedMarkerSurvivesEscaping(t *testing.T) {
	if got := url.QueryEscape(transport.Redacted); got != transport.Redacted {
		t.Errorf("the marker changes under query escaping: %q became %q",
			transport.Redacted, got)
	}
	if got := url.PathEscape(transport.Redacted); got != transport.Redacted {
		t.Errorf("the marker changes under path escaping: %q became %q",
			transport.Redacted, got)
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://acme.atlassian.net/rest/api/3/myself",
			"https://acme.atlassian.net/rest/api/3/myself",
		},
		{
			"https://ada:hunter2@acme.atlassian.net/rest/api/3/myself",
			"https://ada:" + transport.Redacted + "@acme.atlassian.net/rest/api/3/myself",
		},
		{
			"https://acme.atlassian.net/x?token=" + theToken,
			"https://acme.atlassian.net/x?token=" + transport.Redacted,
		},
		{
			"https://acme.atlassian.net/x?jql=project+%3D+ENG&maxResults=50",
			"https://acme.atlassian.net/x?jql=project+%3D+ENG&maxResults=50",
		},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if got := transport.RedactURL(u); got != tc.want {
			t.Errorf("RedactURL(%q)\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}

	if got := transport.RedactURL(nil); got != "" {
		t.Errorf("RedactURL(nil) = %q", got)
	}
}

// TestTokenNeverReachesDebugOutput is the test the spec calls for by name.
//
// It exercises the whole path — an authorizer setting a real header, a server
// echoing credentials back, a redirect-shaped URL carrying one in the query —
// and then asserts the literal token string appears nowhere in the trace.
func TestTokenNeverReachesDebugOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A server that reflects credentials back is not hypothetical: some
		// proxies echo headers, and WWW-Authenticate legitimately carries one.
		w.Header().Set("Set-Cookie", "JSESSIONID="+theToken+"; Path=/")
		w.Header().Set("WWW-Authenticate", `Bearer realm="jira", token="`+theToken+`"`)
		w.Header().Set("X-Downstream-Api-Token", theToken)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorMessages":["Client must be authenticated"]}`))
	}))
	defer srv.Close()

	var debug strings.Builder
	c, err := transport.New(transport.Options{
		BaseURL: srv.URL,
		Auth: transport.AuthorizerFunc(
			func(context.Context, transport.RequestInfo) (map[string]string, error) {
				return map[string]string{
					"Authorization": "Bearer " + theToken,
					"Cookie":        "JSESSIONID=" + theToken,
				}, nil
			},
		),
		Retries: -1,
		Tracer:  transport.NewTextTracer(&debug),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	resp, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet,
		Path:   "/rest/api/3/myself",
		Query:  url.Values{"access_token": {theToken}},
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.Status)
	}

	trace := debug.String()
	if trace == "" {
		t.Fatal("the tracer received nothing, so this test proves nothing")
	}
	if strings.Contains(trace, theToken) {
		t.Fatalf("the token reached debug output:\n%s", trace)
	}
	// And prove the trace is real rather than empty of everything.
	for _, want := range []string{"[http] request", "[http] response", "status=401"} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace is missing %q:\n%s", want, trace)
		}
	}
	if !strings.Contains(trace, transport.Redacted) {
		t.Errorf("nothing was redacted, so the credential may not have been sent:\n%s", trace)
	}
}

// TestTokenNeverReachesAnError covers the other way a credential escapes: a
// *url.Error stringifies to include the URL it failed on.
func TestTokenNeverReachesAnError(t *testing.T) {
	c, err := transport.New(transport.Options{
		// A port nothing listens on, so the transport fails at connect time
		// and the resulting *url.Error carries the full URL.
		BaseURL: "http://127.0.0.1:1",
		Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = c.Do(t.Context(), transport.Request{
		Method: http.MethodGet,
		Path:   "/rest/api/3/myself",
		Query:  url.Values{"token": {theToken}},
	})
	if err == nil {
		t.Fatal("connecting to a closed port succeeded")
	}
	if strings.Contains(err.Error(), theToken) {
		t.Fatalf("the token reached the error message: %s", err.Error())
	}
	if !strings.Contains(err.Error(), transport.Redacted) {
		t.Errorf("the failing URL was not redacted, so nothing proves it was scrubbed: %s",
			err.Error())
	}
}

// TestRecorderRedactsAsItRecords asserts a fixture cannot capture a credential,
// even if recording is interrupted before the file is written.
func TestRecorderRedactsAsItRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "JSESSIONID="+theToken)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rec := transport.NewRecorder(http.DefaultTransport, transport.Cloud)
	c, err := transport.New(transport.Options{
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Transport: rec},
		Auth: transport.AuthorizerFunc(
			func(context.Context, transport.RequestInfo) (map[string]string, error) {
				return map[string]string{"Authorization": "Bearer " + theToken}, nil
			},
		),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Do(t.Context(), transport.Request{
		Method: http.MethodGet, Path: "/rest/api/3/myself",
	}); err != nil {
		t.Fatalf("do: %v", err)
	}

	path := t.TempDir() + "/cassette.json"
	if err := rec.Cassette().Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(data, theToken) {
		t.Fatalf("the token was recorded into a fixture:\n%s", data)
	}
}
