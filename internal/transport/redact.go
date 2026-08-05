package transport

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Redacted is what replaces a secret.
//
// It is a fixed string rather than a length hint or a value prefix, because
// either would leak information about the credential. It carries no punctuation
// so that it survives URL escaping unchanged: a marker that becomes
// %5BREDACTED%5D in a query string and [REDACTED] in a header is one a reader —
// and a test asserting the redaction happened — has to know two spellings of.
const Redacted = "REDACTED"

// sensitiveHeaders are always redacted, whatever their value.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"www-authenticate":    true,
	"proxy-authenticate":  true,
	"x-atlassian-token":   true,
}

// sensitiveSubstrings catch headers this project has not heard of yet. A header
// named X-Acme-Api-Token should be redacted the first time a proxy adds it, not
// the first time someone notices it in a bug report.
var sensitiveSubstrings = []string{
	"auth", "token", "secret", "credential", "password", "passwd", "session",
	"cookie", "api-key", "apikey", "private",
}

// IsSensitiveHeader reports whether a header name carries a credential.
func IsSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	if sensitiveHeaders[lower] {
		return true
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// RedactHeader returns a copy of h with every credential replaced.
//
// The copy matters: redacting in place would mutate the request that is about
// to be sent, and a bug there would strip the caller's own authentication.
func RedactHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if IsSensitiveHeader(name) {
			out[name] = []string{Redacted}
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}

// sensitiveQueryParams are credentials that show up in a URL.
var sensitiveQueryParams = []string{
	"token", "access_token", "api_key", "apikey", "key", "secret",
	"password", "jwt", "signature", "sig",
}

// RedactURL returns u as a string with userinfo and any credential-bearing
// query parameter replaced.
//
// A URL is not obviously a secret, which is exactly why it gets missed:
// https://user:pass@site/rest/api/3/search?token=... carries two.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clone := *u

	if clone.User != nil {
		// The password is the secret; the username is kept because the only
		// question debug output exists to answer is which account made the
		// call. Replacing a missing password with the marker would be worse
		// than useless — it would claim a credential was present when none
		// was.
		if _, hasPassword := clone.User.Password(); hasPassword {
			clone.User = url.UserPassword(clone.User.Username(), Redacted)
		}
	}

	if clone.RawQuery != "" {
		q := clone.Query()
		for key := range q {
			if isSensitiveParam(key) {
				q.Set(key, Redacted)
			}
		}
		clone.RawQuery = q.Encode()
	}
	return clone.String()
}

func isSensitiveParam(key string) bool {
	lower := strings.ToLower(key)
	if slices.Contains(sensitiveQueryParams, lower) {
		return true
	}
	// Reuse the header heuristic so a new credential-shaped parameter is
	// covered without a second list to keep in sync.
	for _, s := range sensitiveSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
