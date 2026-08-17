package transport

import "net/http"

// KeptHeaders names every response header a recording keeps, and what reads it.
//
// Everything else is dropped as the interaction is recorded, so it never
// reaches memory as part of the tape and never reaches a file. That is a
// stronger guarantee than reporting it afterwards, and the reason for it is the
// case a report cannot cover: a header carrying identity in a shape nobody
// anticipated. Data Center answers with `X-AUSERNAME`, whose value is the
// authenticated account name and which matches no pattern in this package —
// not an email, not a UUID, not a host, not a long hex run. A residue check
// would have read it and said nothing.
//
// Recordings used to keep 25 response headers. Twenty of them were CDN and
// trace telemetry — `Atl-Traceid`, `X-Amz-Cf-Id`, `Server-Timing`, `Via`,
// `Report-To` — which no code reads, no test asserts, and which differ between
// two recordings of the same call, so they also made every re-recording churn
// for no reason.
//
// **Trimming a recording is not free** and this list is the price. A fixture is
// evidence of what a server said, and a header dropped here cannot answer a
// question later — "does Cloud send Retry-After on a 429" is not answerable
// from an old cassette if Retry-After was never kept. So the rule is: keep what
// something reads, name the reader, and record fresh when the question changes.
// An entry with no reader is an entry somebody added speculatively, which is
// how this grows back to 25.
var KeptHeaders = map[string]string{
	"Content-Type": "transport/errors.go parses a Jira error body by it and " +
		"detects an HTML error page with it; issue/attachment.go reads it too",
	"Content-Disposition": "issue/attachment.go takes the filename from it, " +
		"which is the only thing that names a downloaded file",
	// Not "waits for exactly as long as it says", which this entry claimed
	// until 2026-08-17 and TestRetryAfterIsCapped has always disproved. The
	// reason to keep the header is unaffected, since retry.go reads it in both
	// of its forms. But the sentence explaining why a fixture keeps evidence is
	// the wrong sentence to be wrong.
	"Retry-After": "transport/retry.go reads it, in either of its two forms, " +
		"and waits for as long as it says up to a 30s cap",
	"X-Authentication-Denied-Reason": "transport/errors.go turns it into the " +
		"detail on a 401, and it is the only place Jira says which of several " +
		"auth failures happened",
	// The tool's own header, echoed. Kept because a recorded response that
	// echoes it is evidence the correlation works end to end.
	"X-Request-Id": "transport/transport.go reads the echo to correlate a " +
		"failure with the request that caused it",
}

// keepHeader reports whether a recording keeps this header.
//
// Canonical form, because http.Header keys are canonicalized on the way in and
// a map keyed on the wire spelling would silently keep nothing.
func keepHeader(name string) bool {
	_, ok := KeptHeaders[http.CanonicalHeaderKey(name)]
	return ok
}

// recordableHeader is RedactHeader plus the drop.
//
// Redaction still runs first and still matters: a kept header could carry a
// credential — that is what Set-Cookie did before it was dropped — and the rule
// is that a credential is redacted where an event is built, never where one is
// formatted. Dropping is the second layer, not a replacement for the first.
func recordableHeader(h http.Header) http.Header {
	redacted := RedactHeader(h)
	out := make(http.Header, len(KeptHeaders))
	for name, values := range redacted {
		if keepHeader(name) {
			out[name] = values
		}
	}
	return out
}
