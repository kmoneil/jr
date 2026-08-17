package site

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/transport"
)

// TimeLayouts are the timestamp formats Jira serves. The first is what both
// deployments actually send; the rest are there because a proxy or a plugin
// occasionally normalizes them.
//
// Here rather than beside the code that decodes issues, because "what a Jira
// timestamp looks like" is a fact about the server and this package's subject is
// the server. It was in internal/resource/issue, which was fine while one
// package parsed timestamps and stopped being fine the moment a second one had
// to: internal/cli cannot import a resource, so a clock check there would have
// needed a second copy of this list, and a second copy of a list is the defect
// this tree has already paid for twice in internal/adf.
var TimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05.999-0700",
	"2006-01-02T15:04:05-0700",
	time.RFC3339Nano,
	time.RFC3339,
}

// ParseTime reads a timestamp in any form Jira sends it, and reports whether it
// was one.
//
// The caller decides what an unparseable timestamp means, because the answer
// differs: a row in a listing refuses, and a clock check reports that the site
// did not say. Both are better than a zero time nobody can tell from midnight.
func ParseTime(value string) (time.Time, bool) {
	for _, layout := range TimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// Now reads the site's own clock.
//
// Two callers need it and they need it for the same reason. A change feed bounds
// its window with the server's clock, because every timestamp it compares was
// written by the server: using this process's clock makes the bound wrong by the
// skew between the two, and wrong in the direction that loses changes, since a
// client running behind the site claims to have reported through an instant the
// site had not reached. And a diagnostic reports the skew itself, because skew
// breaks JQL date bounds, feed cursors, and any `created >= -1m` in a script,
// and it is invisible to every other command.
//
// It costs one request and reuses the deployment probe's endpoint, which is the
// one route this tool already knows both deployments serve, under a context path
// and behind a proxy. The probe's own cached answer cannot be used: a cached
// clock is not a clock.
func Now(ctx context.Context, client Doer, info Info) (time.Time, error) {
	resp, err := client.Do(ctx, transport.Request{
		Method: transport.MethodGet,
		Path:   ProbePath,
	})
	if err != nil {
		return time.Time{}, err
	}
	if err := transport.Err(resp); err != nil {
		return time.Time{}, err
	}

	var body struct {
		ServerTime string `json:"serverTime"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return time.Time{}, errs.Remote("MALFORMED_SERVER_INFO",
			"%s did not return usable server information", ProbePath).
			WithRequestID(resp.RequestID).
			Wrap(err)
	}
	if body.ServerTime == "" {
		// Both deployments send it, and a site that does not is one this tool
		// cannot bound a feed on. Falling back to the local clock here would be
		// the guess this exists to avoid, and it would be invisible.
		return time.Time{}, errs.Remote("NO_SERVER_TIME",
			"%s did not report the site's clock", ProbePath).
			WithRequestID(resp.RequestID).
			WithDetail("a change feed bounds its window with the server's clock, " +
				"because the timestamps it compares were written by the server").
			WithRemedy("report this: the feed cannot be bounded on this site")
	}

	if t, ok := ParseTime(body.ServerTime); ok {
		return t, nil
	}
	return time.Time{}, errs.Remote("MALFORMED_TIMESTAMP",
		"Jira returned a serverTime this tool cannot parse").
		WithRequestID(resp.RequestID).
		WithDetail("%q", body.ServerTime).
		WithRemedy("report this: the timestamp format changed")
}
