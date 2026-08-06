package transport

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Scrubber rewrites the identifiers in a recorded cassette.
//
// A recording is only useful if it can be committed, and a real conversation
// carries the name of a person, their email, their account id, their host, and
// their project keys. None of that is what a fixture is for: the value is the
// *shape* — which endpoint, which parameters, which fields come back — and that
// survives renaming intact.
//
// Credentials are not handled here. They are redacted as the interaction is
// recorded, so a token never reaches a file even if recording is interrupted;
// this runs afterwards on data that is already credential-free.
type Scrubber struct {
	// Replace maps a literal string to what should stand in its place. Longest
	// match first, so a display name is not half-rewritten by a shorter one.
	Replace map[string]string
	// Patterns rewrite anything matched, for identifiers that are shaped rather
	// than known — an account id, an avatar URL with a hash in it.
	Patterns []PatternRule
}

// PatternRule is one shaped identifier and what replaces it.
type PatternRule struct {
	Match *regexp.Regexp
	With  string
}

// CloudAccountID matches an Atlassian account id in any of its forms, which
// appear in every user object a Cloud instance returns.
//
// The prefixed form is <prefix>:<uuid> and the prefix is not a fixed width — an
// earlier version required six or more characters and let a real five-digit one
// through. The separator may also be percent-encoded, because the same id
// appears in a query string as well as in a body, and matching only the literal
// colon left one `%3A` form untouched in exactly one place.
var CloudAccountID = regexp.MustCompile(
	`\b[0-9a-zA-Z]+(?::|%3[Aa])` + uuidPattern + `\b|\b[0-9a-f]{24}\b`,
)

// uuidPattern is the shape at the heart of an account id, and the one part that
// survives however the rest of it is encoded.
const uuidPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

// BareUUID matches a UUID with no account prefix. Jira hangs several on a
// project — entityId, uuid — and they are instance-specific without being
// personal. They are scrubbed anyway: they differ between two recordings of the
// same call, so leaving them makes a fixture churn for no reason, and leaving
// them also means the residue report is never empty, which is how a report
// stops being read.
var BareUUID = regexp.MustCompile(uuidPattern)

// AvatarURL matches the avatar links Jira attaches to every user and project.
// They carry an account id and a hash, and nothing is asserted about them.
var AvatarURL = regexp.MustCompile(`https?://[^"\\ ]*(avatar|secure/useravatar|secure/projectavatar)[^"\\ ]*`)

// Scrub rewrites every interaction in place.
func (s Scrubber) Scrub(c *Cassette) {
	if c == nil {
		return
	}
	for i := range c.Interactions {
		in := &c.Interactions[i]
		in.Request.Path = s.text(in.Request.Path)
		in.Request.Query = s.text(in.Request.Query)
		in.Request.Body = s.text(in.Request.Body)
		in.Response.Body = s.text(in.Response.Body)
		for name, values := range in.Response.Header {
			for j, v := range values {
				in.Response.Header[name][j] = s.text(v)
			}
		}
	}
}

// text applies every rule to one string.
//
// Literals go first and longest-first, so "Ada Lovelace" is not left as
// "Ada Placeholder" by a rule for "Lovelace". Patterns run afterwards, on what
// the literals did not already name.
func (s Scrubber) text(in string) string {
	if in == "" {
		return in
	}
	out := in
	for _, from := range longestFirst(s.Replace) {
		out = strings.ReplaceAll(out, from, s.Replace[from])
	}
	for _, rule := range s.Patterns {
		out = rule.Match.ReplaceAllString(out, rule.With)
	}
	return out
}

// longestFirst orders the literals so a longer one is applied before any
// shorter one it contains.
func longestFirst(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Residue reports identifiers that survived scrubbing, so a cassette is never
// committed on the assumption that the rules were complete.
//
// It looks for the shapes that carry identity rather than for particular
// values: an email address, an account id, a hostname that is not a reserved
// test domain. A hit is not proof of a leak, but every hit is worth reading
// before the file is committed.
//
// **It deliberately does not reuse the scrubber's patterns.** The first version
// did, and missed a real account id for exactly that reason: the pattern was
// too narrow, so the scrubber left it and the check that was supposed to catch
// the miss was blind in the same place. A guard that shares a definition with
// the thing it guards cannot catch that definition being wrong, so these are
// looser on purpose and expected to produce the occasional false positive.
func (c *Cassette) Residue() []string {
	var out []string
	seen := map[string]bool{}

	add := func(kind, value string) {
		if isPlaceholder(value) {
			return
		}
		key := kind + ":" + value
		if !seen[key] {
			seen[key] = true
			out = append(out, kind+" "+value)
		}
	}

	for _, in := range c.Interactions {
		for _, text := range []string{
			in.Request.Path, in.Request.Query, in.Request.Body, in.Response.Body,
		} {
			for _, m := range EmailAddress.FindAllString(text, -1) {
				add("email", m)
			}
			// identifierish, deliberately not CloudAccountID — see above.
			for _, m := range identifierish.FindAllString(text, -1) {
				add("possible identifier", m)
			}
			for _, m := range hostPattern.FindAllString(text, -1) {
				if !reservedHost(m) {
					add("host", m)
				}
			}
		}
	}
	return out
}

// EmailAddress matches an address anywhere in a body. Jira attaches one to
// every user object a Data Center instance returns, and to a Cloud one whenever
// the privacy setting allows.
var EmailAddress = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)

var hostPattern = regexp.MustCompile(`https?://[\w.-]+`)

// identifierish is the loose counterpart to CloudAccountID: any colon-joined
// token that looks like an opaque handle, any bare UUID, and any long hex run.
// It will flag things that are not identifiers, which is the intended direction
// to be wrong in — the cost of a false positive is reading one line.
//
// The bare UUID is the important part. It is what remains of an account id
// however the rest of it was encoded, so it catches the forms the scrubber's
// own pattern is shaped too tightly to see.
var identifierish = regexp.MustCompile(
	`\b[0-9a-zA-Z_-]{2,}(?::|%3[Aa])[0-9a-fA-F][0-9a-fA-F-]{7,}\b` +
		`|` + uuidPattern + `|\b[0-9a-f]{16,}\b`,
)

// placeholders are what the scrubber itself writes. Reporting them back as
// residue is noise, and noise trains a reader to skim the list — which is the
// one thing this must not become.
var placeholders = []string{
	"00000000-0000-0000-0000-000000000000",
	"ada@example.invalid",
}

func isPlaceholder(v string) bool {
	for _, p := range placeholders {
		if strings.Contains(v, p) {
			return true
		}
	}
	return false
}

// reservedHost reports whether a host is one of the domains reserved for
// documentation and testing, which is the only kind a fixture may name.
func reservedHost(u string) bool {
	host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	for _, suffix := range []string{
		".invalid", ".test", ".example", ".example.com", ".example.org",
		".example.net", "localhost", "127.0.0.1",
	} {
		if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(suffix, ".") {
			return true
		}
	}
	return false
}

// LoadAndScrub reads a recording, rewrites it, and reports what survived.
func LoadAndScrub(path string, s Scrubber) (*Cassette, []string, error) {
	c, err := LoadCassette(path)
	if err != nil {
		return nil, nil, err
	}
	s.Scrub(c)
	return c, c.Residue(), nil
}

// PrettyBody re-encodes a JSON body so a committed fixture is readable in a
// diff. A body that is not JSON is left exactly as it arrived.
func PrettyBody(body string) string {
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return string(out)
}
