package transport

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// jiraError is the error body Jira returns. Both shapes appear in the wild:
// a list of messages, and a field-keyed map.
type jiraError struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
	// Some endpoints return these instead.
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

// summarize flattens a Jira error body into one line, with field errors sorted
// so the same failure always reports the same way.
func (e jiraError) summarize() string {
	var parts []string
	parts = append(parts, e.ErrorMessages...)

	if len(e.Errors) > 0 {
		fields := make([]string, 0, len(e.Errors))
		for field := range e.Errors {
			fields = append(fields, field)
		}
		sortStrings(fields)
		for _, field := range fields {
			parts = append(parts, field+": "+e.Errors[field])
		}
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	return strings.Join(parts, "; ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// parseJiraError extracts a human-readable summary from a response body. A body
// that is not JSON, or is JSON of an unexpected shape, degrades to a readable
// summary rather than being dropped: an opaque 400 with the body swallowed is
// the least actionable error there is.
func parseJiraError(body []byte, contentType string) string {
	if len(body) == 0 {
		return ""
	}
	if strings.Contains(strings.ToLower(contentType), "json") {
		var je jiraError
		if err := json.Unmarshal(body, &je); err == nil {
			if summary := je.summarize(); summary != "" {
				return summary
			}
		}
	}
	if isHTML(body, contentType) {
		return htmlSummary(body)
	}
	return snippet(body)
}

// isHTML reports whether a body is a web page rather than an API response.
//
// It matters because a JSON API answering with HTML means the request reached a
// web server rather than the API — which is a different problem from anything
// the status code alone suggests.
func isHTML(body []byte, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 64)])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

var (
	titleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	h1Tag    = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	anyTag   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// htmlSummary reduces an error page to its headline.
//
// A page of Tomcat's inline CSS tells a reader nothing, and truncating it at
// some byte count usually cuts before the one line that would have.
func htmlSummary(body []byte) string {
	for _, re := range []*regexp.Regexp{titleTag, h1Tag} {
		if m := re.FindSubmatch(body); len(m) == 2 {
			if text := strings.TrimSpace(anyTag.ReplaceAllString(string(m[1]), "")); text != "" {
				return text + " (an HTML page, not a Jira API response)"
			}
		}
	}
	return snippet(body)
}

// maxSnippet bounds how much of an unparseable body reaches an error message.
const maxSnippet = 512

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxSnippet {
		return s[:maxSnippet] + "…"
	}
	return s
}

// statusError maps an HTTP response to a structured error.
//
// The mapping is fixed: a caller pins the exit codes it handles, so a status
// must always produce the same one.
func statusError(resp *Response) *errs.Error {
	detail := parseJiraError(resp.Body, resp.Header.Get("Content-Type"))

	var e *errs.Error
	switch {
	case resp.Status == http.StatusUnauthorized:
		e = errs.Auth("UNAUTHORIZED", "Jira rejected the credentials").
			WithRemedy("run `jr auth login`, or check that the token has not expired")

	case resp.Status == http.StatusForbidden:
		// Jira answers 403 both for "you may not do this" and for an auth
		// failure that tripped a CAPTCHA. The header is the only way to tell,
		// and they need different remedies.
		if reason := resp.Header.Get("X-Authentication-Denied-Reason"); reason != "" {
			e = errs.Auth("AUTHENTICATION_DENIED", "Jira denied authentication").
				WithRemedy("log in through the web UI to clear the challenge, then retry")
			detail = joinDetail(reason, detail)
			break
		}
		e = errs.Permission("FORBIDDEN", "authenticated, but not permitted to do this").
			WithRemedy("check the project permissions for this account")

	case resp.Status == http.StatusNotFound:
		// An HTML 404 did not come from Jira's API at all. Telling the caller
		// to check an issue key would send them looking for the wrong problem
		// entirely: the request never reached the API.
		if isHTML(resp.Body, resp.Header.Get("Content-Type")) {
			e = errs.NotFound("NO_SUCH_ENDPOINT",
				"the server returned a web page, not a Jira API response").
				WithRemedy("check the site URL, including any context path: a Data Center " +
					"instance is often served under one, e.g. https://jira.example.com/jira")
			break
		}
		e = errs.NotFound("NOT_FOUND", "Jira has no such resource").
			WithRemedy("check the key or id; a resource you cannot see also reports as missing")

	case resp.Status == http.StatusConflict,
		resp.Status == http.StatusPreconditionFailed:
		e = errs.Conflict("CONFLICT", "the request conflicts with the current state").
			WithRemedy("re-read the resource and retry against the current version")

	case resp.Status == http.StatusTooManyRequests:
		e = errs.RateLimit("RATE_LIMITED", "throttled by Jira after the retry budget was spent").
			WithRemedy("raise --retries, or slow down the caller")

	case resp.Status == http.StatusRequestEntityTooLarge:
		e = errs.Usage("TOO_LARGE", "the request body is larger than Jira accepts")

	case resp.Status >= 500:
		e = errs.Remote("UPSTREAM_ERROR", "Jira returned %d", resp.Status).
			WithRemedy("this is usually transient; retry, or check the Atlassian status page")

	case resp.Status >= 400:
		e = errs.Usage("BAD_REQUEST", "Jira rejected the request").
			WithRemedy("the detail below comes from Jira and names the offending field")

	default:
		// A 3xx that reached here means redirect handling is off and the
		// response is not something a caller can use.
		e = errs.Remote("UNEXPECTED_STATUS", "Jira returned an unexpected status %d", resp.Status)
	}

	// Name the endpoint. A failure that does not say what it asked for leaves
	// the caller guessing at which of several requests a command made.
	if endpoint := resp.Endpoint(); endpoint != "" {
		detail = joinDetail(endpoint, detail)
	}
	if detail != "" {
		e = e.WithDetail("%s", detail)
	}
	return e.WithRequestID(resp.RequestID)
}

func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}
