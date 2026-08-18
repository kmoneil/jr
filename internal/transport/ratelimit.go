package transport

// Limits is what a response's headers disclose about throttling.
//
// Verbatim rather than parsed, because the four are two spellings of the same
// draft standard plus one Atlassian extension, and a diagnostic that reports
// what the server said is worth more than one that reports what this tool made
// of it. Measured against Cloud on 2026-08-17, on an ordinary 200:
//
//	Ratelimit: "jira-burst-based";r=348;t=1
//	Ratelimit-Policy: "jira-burst-based";q=100;w=1
//	X-Ratelimit-Limit: 350
//	X-Ratelimit-Remaining: 348
//
// A default Data Center sends none of them, measured the same day against
// 10.4.0: no Retry-After, no Ratelimit, no X-Ratelimit-*. That is why every
// field is optional and why Disclosed exists: "this site advertises no policy"
// is an answer, and it is not the same answer as "nobody asked".
//
// It lives here rather than in internal/site because a response header is this
// package's business: everything else reads a Response, and only this package
// imports net/http. See TestTransportOwnsHTTP.
type Limits struct {
	// Policy is the quota, e.g. `"jira-burst-based";q=100;w=1` for 100 per
	// one-second window.
	Policy string
	// State is what is left of it right now, e.g.
	// `"jira-burst-based";r=348;t=1`.
	State string
	// Limit and Remaining are Atlassian's own pair, which on Cloud reads as a
	// token bucket sitting over the policy above.
	Limit     string
	Remaining string
}

// Disclosed reports whether the site said anything at all about throttling.
func (l Limits) Disclosed() bool {
	return l.Policy != "" || l.State != "" || l.Limit != "" || l.Remaining != ""
}

// LimitsFrom reads what one response disclosed, and nothing from a response
// that never arrived.
//
// http.Header.Get canonicalizes, so the wire spelling does not matter here.
// What does matter is KeptHeaders: a recording drops every header nothing
// reads, so these four became recordable on the day this function started
// reading them, and no cassette taken before that can show one.
func LimitsFrom(resp *Response) Limits {
	if resp == nil {
		return Limits{}
	}
	return Limits{
		Policy:    resp.Header.Get("Ratelimit-Policy"),
		State:     resp.Header.Get("Ratelimit"),
		Limit:     resp.Header.Get("X-Ratelimit-Limit"),
		Remaining: resp.Header.Get("X-Ratelimit-Remaining"),
	}
}
