package transport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
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
// through.
//
// This matches the literal-colon form only. The percent-encoded form is
// CloudAccountIDEncoded, and the split is not cosmetic: one pattern with one
// replacement cannot preserve the encoding of what it matched, and a rule that
// matched both wrote a literal colon over a `%3A` — which then re-matched this
// pattern and undid the encoded rule that ran before it.
var CloudAccountID = regexp.MustCompile(
	`\b[0-9a-zA-Z]+:` + uuidPattern + `\b|\b[0-9a-f]{24}\b`,
)

// CloudAccountIDEncoded matches only the percent-encoded form, so it can be
// replaced with a percent-encoded stand-in.
//
// A rule that rewrote `70121%3A5faf...` to `000000:00000000-...` produced a
// recording no request could ever match: the tool escapes the separator when it
// builds the query, so the replayer looked for `%3A` and the cassette held a
// literal colon. Scrubbing must not change how a value is encoded — only what
// it says.
var CloudAccountIDEncoded = regexp.MustCompile(
	`\b[0-9a-zA-Z]+%3[Aa]` + uuidPattern + `\b`,
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
	// Last, because it re-encodes: anything the plain rules were going to
	// rewrite has already been rewritten in the visible text.
	return s.scrubEncoded(out)
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

// base64Run matches a token long enough to be worth decoding, in either
// alphabet, and *including* any percent-encoding around it.
//
// The percent-encoding is not an embellishment. The same page token appears
// twice in a recording — once in a response body as plain base64, once in the
// next request's query where `+`, `/`, and `=` are escaped. Matching only the
// unescaped characters stopped the run before its `%3D%3D` padding, so the two
// copies were re-encoded to different strings and the replayer, which matches a
// request by its query, could no longer pair them.
var base64Run = regexp.MustCompile(
	`(?:[A-Za-z0-9+/_-]|%2[BbFf]){24,}(?:=|%3[Dd]){0,2}`,
)

// percentEscaped reports whether a matched run was written in escaped form, and
// therefore has to be written back that way.
var percentEscaped = regexp.MustCompile(`%(?:2[BbFf]|3[Dd])`)

// decodeBase64 returns the decoded bytes of a run that is base64 and carries
// readable content, and reports whether it was one.
//
// "Readable" is the test that matters. A hex id or a random string will often
// decode without error into bytes that mean nothing, and treating those as text
// would produce noise in every recording. A Jira page token decodes to a
// protobuf with the JQL sitting in it as plain ASCII, which is exactly the case
// worth catching.
func decodeBase64(run string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		out, err := enc.DecodeString(run)
		if err != nil || len(out) == 0 {
			continue
		}
		if printableRun(out) >= 8 {
			return out, true
		}
	}
	return nil, false
}

// printableRun returns the length of the longest run of printable ASCII.
func printableRun(b []byte) int {
	best, run := 0, 0
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			run++
			if run > best {
				best = run
			}
		} else {
			run = 0
		}
	}
	return best
}

// scrubEncoded rewrites identifiers hidden inside base64.
//
// A Cloud page token is a protobuf carrying the JQL that produced it, so the
// project key a caller searched for is inside every one — invisible to a text
// replacement and to a guard that scans text. This decodes each run, applies
// the literal rules, and re-encodes only when something changed.
//
// Re-encoding produces a token no server would accept, which does not matter:
// inside a fixture a token only has to match the one recorded beside it, and
// the replayer compares it as an opaque string.
func (s Scrubber) scrubEncoded(in string) string {
	return base64Run.ReplaceAllStringFunc(in, func(run string) string {
		escaped := percentEscaped.MatchString(run)

		unescaped := run
		if escaped {
			u, err := url.QueryUnescape(run)
			if err != nil {
				return run
			}
			unescaped = u
		}

		decoded, ok := decodeBase64(unescaped)
		if !ok {
			return run
		}
		rewritten := decoded
		for _, from := range longestFirst(s.Replace) {
			rewritten = bytes.ReplaceAll(rewritten, []byte(from), []byte(s.Replace[from]))
		}
		if bytes.Equal(rewritten, decoded) {
			return run
		}

		// Re-encoded, then escaped again if that is how it was found. A
		// replacement changes the payload length, so the padding is whatever the
		// new bytes need rather than whatever was there before.
		out := base64.StdEncoding.EncodeToString(rewritten)
		if escaped {
			out = url.QueryEscape(out)
		}
		return out
	})
}

// EncodedResidue reports identifiers hiding inside base64 in a cassette.
//
// It is separate from Residue for the same reason the residue patterns are
// separate from the scrubber's: this looks for a thing the other one cannot
// see, and folding them together would let one blind spot cover both.
func (c *Cassette) EncodedResidue(suspect []string) []string {
	var out []string
	seen := map[string]bool{}

	for _, in := range c.Interactions {
		for _, text := range []string{
			in.Request.Path, in.Request.Query, in.Request.Body, in.Response.Body,
		} {
			for _, run := range base64Run.FindAllString(text, -1) {
				if u, err := url.QueryUnescape(run); err == nil {
					run = u
				}
				decoded, ok := decodeBase64(run)
				if !ok {
					continue
				}
				for _, want := range suspect {
					if want == "" || !bytes.Contains(decoded, []byte(want)) {
						continue
					}
					if !seen[want] {
						seen[want] = true
						out = append(out, "encoded "+want)
					}
				}
			}
		}
	}
	return out
}
