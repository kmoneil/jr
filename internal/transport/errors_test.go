package transport_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// TestStatusMapping pins every status to an exit code. A caller declares the
// exit codes it handles, so a status must always produce the same one.
func TestStatusMapping(t *testing.T) {
	cases := []struct {
		status    int
		header    http.Header
		exit      exitcode.Code
		code      string
		retryable bool
	}{
		{400, nil, exitcode.Usage, "BAD_REQUEST", false},
		{401, nil, exitcode.Auth, "UNAUTHORIZED", false},
		{403, nil, exitcode.Permission, "FORBIDDEN", false},
		{
			403,
			http.Header{"X-Authentication-Denied-Reason": {"CAPTCHA_CHALLENGE"}},
			exitcode.Auth, "AUTHENTICATION_DENIED", false,
		},
		{404, nil, exitcode.NotFound, "NOT_FOUND", false},
		{409, nil, exitcode.Conflict, "CONFLICT", false},
		{412, nil, exitcode.Conflict, "CONFLICT", false},
		{413, nil, exitcode.Usage, "TOO_LARGE", false},
		{422, nil, exitcode.Usage, "BAD_REQUEST", false},
		{429, nil, exitcode.RateLimit, "RATE_LIMITED", true},
		{500, nil, exitcode.Remote, "UPSTREAM_ERROR", true},
		{502, nil, exitcode.Remote, "UPSTREAM_ERROR", true},
		{503, nil, exitcode.Remote, "UPSTREAM_ERROR", true},
		{504, nil, exitcode.Remote, "UPSTREAM_ERROR", true},
		{302, nil, exitcode.Remote, "UNEXPECTED_STATUS", true},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.status)+"/"+tc.code, func(t *testing.T) {
			header := tc.header
			if header == nil {
				header = http.Header{}
			}
			err := transport.Err(&transport.Response{
				Status: tc.status, Header: header, RequestID: "req-1",
			})
			if err == nil {
				t.Fatalf("status %d reported success", tc.status)
			}
			e := errs.Coerce(err)
			if e.Code != tc.code {
				t.Errorf("code = %q, want %q", e.Code, tc.code)
			}
			if e.Exit != tc.exit {
				t.Errorf("exit = %v, want %v", e.Exit, tc.exit)
			}
			if e.Retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", e.Retryable, tc.retryable)
			}
			if e.RequestID != "req-1" {
				t.Errorf("the request id was dropped: %q", e.RequestID)
			}
		})
	}
}

// TestJiraErrorBodyReachesTheDetail is what makes a 400 actionable. An opaque
// rejection with the server's explanation swallowed is the least useful error
// there is — and it is exactly what the incumbent produces.
func TestJiraErrorBodyReachesTheDetail(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		contentType string
		want        []string
	}{
		{
			"error messages",
			`{"errorMessages":["Field 'foo' does not exist"],"errors":{}}`,
			"application/json",
			[]string{"Field 'foo' does not exist"},
		},
		{
			"field errors are sorted",
			`{"errorMessages":[],"errors":{"summary":"is required","priority":"invalid"}}`,
			"application/json",
			[]string{"priority: invalid", "summary: is required"},
		},
		{
			"both shapes",
			`{"errorMessages":["top level"],"errors":{"summary":"is required"}}`,
			"application/json",
			[]string{"top level", "summary: is required"},
		},
		{
			"message field",
			`{"message":"Issue does not exist"}`,
			"application/json",
			[]string{"Issue does not exist"},
		},
		{
			"an HTML error page still says something",
			"<html><body><h1>502 Bad Gateway</h1></body></html>",
			"text/html",
			[]string{"502 Bad Gateway"},
		},
		{
			"malformed JSON degrades rather than disappearing",
			`{"errorMessages": [ truncated`,
			"application/json",
			[]string{"truncated"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			header.Set("Content-Type", tc.contentType)
			err := transport.Err(&transport.Response{
				Status: http.StatusBadRequest, Header: header, Body: []byte(tc.body),
			})
			detail := errs.Coerce(err).Detail
			for _, want := range tc.want {
				if !strings.Contains(detail, want) {
					t.Errorf("detail %q does not carry %q", detail, want)
				}
			}
		})
	}
}

// TestFieldErrorOrderIsStable stops the same failure reporting differently run
// to run, which would make a golden file or a log diff useless.
func TestFieldErrorOrderIsStable(t *testing.T) {
	body := []byte(`{"errors":{"z":"1","a":"2","m":"3","b":"4"}}`)
	header := http.Header{}
	header.Set("Content-Type", "application/json")

	var first string
	for i := range 20 {
		err := transport.Err(&transport.Response{
			Status: http.StatusBadRequest, Header: header, Body: body,
		})
		got := errs.Coerce(err).Detail
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("detail varies between runs:\n%q\n%q", first, got)
		}
	}
	want := "a: 2; b: 4; m: 3; z: 1"
	if first != want {
		t.Errorf("detail = %q, want %q", first, want)
	}
}

func TestOversizedBodyIsTruncatedInTheDetail(t *testing.T) {
	header := http.Header{}
	header.Set("Content-Type", "text/plain")
	err := transport.Err(&transport.Response{
		Status: http.StatusInternalServerError,
		Header: header,
		Body:   []byte(strings.Repeat("x", 4000)),
	})
	detail := errs.Coerce(err).Detail
	if len(detail) > 600 {
		t.Errorf("detail is %d bytes; an error message should not carry a whole page", len(detail))
	}
	if !strings.HasSuffix(detail, "…") {
		t.Error("a truncated detail does not say it was truncated")
	}
}

func TestEmptyBodyStillProducesAUsefulError(t *testing.T) {
	err := transport.Err(&transport.Response{Status: http.StatusNotFound, Header: http.Header{}})
	e := errs.Coerce(err)
	if e.Message == "" {
		t.Error("a 404 with no body produced no message")
	}
	if e.Remedy == "" {
		t.Error("a 404 with no body produced no remedy")
	}
}

// TestNotFoundRemedyMentionsPermissions matters because Jira answers 404 for a
// resource you simply cannot see, and "it does not exist" sends people looking
// for the wrong problem.
func TestNotFoundRemedyMentionsPermissions(t *testing.T) {
	err := transport.Err(&transport.Response{Status: http.StatusNotFound, Header: http.Header{}})
	if !strings.Contains(errs.Coerce(err).Remedy, "cannot see") {
		t.Errorf("remedy does not mention visibility: %q", errs.Coerce(err).Remedy)
	}
}

// TestErrorNamesTheEndpoint is what makes a 404 diagnosable. A failure that does
// not say what it asked for leaves the caller guessing at which of several
// requests a command made.
func TestErrorNamesTheEndpoint(t *testing.T) {
	err := transport.Err(&transport.Response{
		Status: http.StatusNotFound,
		Header: http.Header{},
		Method: "GET",
		URL:    "https://jira.corp.com/rest/api/2/search?jql=project+%3D+ENG",
	})
	detail := errs.Coerce(err).Detail
	if !strings.Contains(detail, "GET https://jira.corp.com/rest/api/2/search") {
		t.Errorf("the detail does not name the endpoint: %q", detail)
	}
}

// TestHTMLErrorPageIsRecognized covers the Tomcat 404 a Data Center instance
// serves when the request never reached the API at all. Telling the caller to
// check an issue key would send them after the wrong problem.
func TestHTMLErrorPageIsRecognized(t *testing.T) {
	tomcat := `<!doctype html><html lang="en"><head><title>HTTP Status 404 – Not Found` +
		`</title><style type="text/css">body {font-family:Tahoma;} h1 {color:white;}` +
		`</style></head><body><h1>HTTP Status 404 – Not Found</h1><hr class="line" />` +
		`<p><b>Type</b> Status Report</p></body></html>`

	header := http.Header{}
	header.Set("Content-Type", "text/html;charset=utf-8")
	err := transport.Err(&transport.Response{
		Status: http.StatusNotFound, Header: header,
		Body: []byte(tomcat), Method: "GET",
		URL: "https://jira.corp.com/rest/api/2/search",
	})

	e := errs.Coerce(err)
	if e.Code != "NO_SUCH_ENDPOINT" {
		t.Errorf("code = %q, want NO_SUCH_ENDPOINT", e.Code)
	}
	// The headline, not a page of inline CSS truncated mid-selector.
	if !strings.Contains(e.Detail, "HTTP Status 404") {
		t.Errorf("the detail does not carry the page's headline: %q", e.Detail)
	}
	if strings.Contains(e.Detail, "font-family") {
		t.Errorf("the detail carries the page's CSS: %q", e.Detail)
	}
	// A context path is the usual cause on Data Center, so the remedy says so.
	if !strings.Contains(e.Remedy, "context path") {
		t.Errorf("the remedy does not mention a context path: %q", e.Remedy)
	}
}

// TestHTMLDetectedWithoutAContentType covers a proxy that serves a page with no
// or a wrong content type.
func TestHTMLDetectedWithoutAContentType(t *testing.T) {
	err := transport.Err(&transport.Response{
		Status: http.StatusNotFound,
		Header: http.Header{},
		Body:   []byte("<html><head><title>Sign in</title></head><body>...</body></html>"),
	})
	if got := errs.Coerce(err).Code; got != "NO_SUCH_ENDPOINT" {
		t.Errorf("code = %q, want NO_SUCH_ENDPOINT", got)
	}
}

// TestJSON404StillReportsAMissingResource is the converse: Jira's own 404 means
// the resource really is missing or invisible.
func TestJSON404StillReportsAMissingResource(t *testing.T) {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	err := transport.Err(&transport.Response{
		Status: http.StatusNotFound, Header: header,
		Body: []byte(`{"errorMessages":["Issue does not exist"],"errors":{}}`),
	})
	e := errs.Coerce(err)
	if e.Code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", e.Code)
	}
	if !strings.Contains(e.Detail, "Issue does not exist") {
		t.Errorf("Jira's explanation was dropped: %q", e.Detail)
	}
}
