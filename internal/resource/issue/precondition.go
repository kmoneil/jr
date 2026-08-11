package issue

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/site"
)

// Precondition names the version of an issue a caller was looking at when they
// decided to write, so a write built on a stale read can be refused instead of
// applied.
//
// §6.3 asks for compare-and-swap "where Jira supports it". Jira supports it
// nowhere: neither deployment offers an ETag or a Last-Modified on an issue —
// the recorded Cloud responses in testdata carry `Cache-Control: no-cache,
// no-store` and no validator at all — and `PUT /issue/{key}` honours no
// `If-Match`. So there is no conditional request to send, and the check has to
// be a read followed by a comparison followed by the write. That has a window
// the width of one round trip, and the result document says which mechanism ran
// rather than letting the spec's word imply the stronger one.
//
// It is opt-in, and that is a property of the tool rather than a preference.
// `jr` is one-shot and holds no prior read: a default-on check would fetch the
// issue and compare it against itself microseconds earlier, which detects
// nothing and costs a request. The baseline has to come from the caller, so
// there is a flag. Keeping it in local state instead would make the guarantee
// depend on which machine and which command did the reading — an agent that
// read through `issue list`, through MCP, or from anywhere else would have no
// record, and "no record" would have to mean "go ahead" exactly when it matters.
//
// It is opaque for the same reason PageToken is. What it carries is the
// millisecond timestamp Jira served, and the `updated` element this tool
// publishes is RFC 3339 to the second — see normalizeTime — so conditioning on
// the visible value would leave a whole second in which somebody else's edit is
// invisible. Wrapping the precise value keeps the published shape as it is and
// the check as sharp as the server's own clock.
type Precondition struct {
	// Deployment is the kind of site this was minted against. A token carries
	// it for the reason a page token does: replaying one site's value against
	// another is a question with no useful answer, and refusing it by name
	// beats comparing two unrelated timestamps and calling the issue stale.
	Deployment site.Kind `json:"d"`
	// Key is the issue this describes. The write verbs take a key of their own,
	// and a token from a different issue would otherwise compare one issue's
	// timestamp against another's and refuse every write for the wrong reason.
	Key string `json:"k"`
	// Updated is the issue's last-modified time, canonical to the millisecond.
	Updated string `json:"u"`
}

// preconditionLayout is RFC 3339 with milliseconds, which is the precision both
// deployments serve.
//
// Fixed rather than time.RFC3339Nano, which drops trailing zeros: a timestamp
// on a whole second would render three characters shorter than one beside it,
// and two spellings of one instant is a comparison waiting to go wrong.
const preconditionLayout = "2006-01-02T15:04:05.000Z07:00"

// EncodePrecondition mints the token for one issue.
//
// An issue with no `updated` gets no token, rather than one asserting a version
// of nothing. The attribute is optional in the schema for exactly this case: a
// caller who cannot be given a baseline is told so by its absence, which is
// better than a token that compares equal to everything.
func EncodePrecondition(info site.Info, key, rawUpdated string) (string, error) {
	if rawUpdated == "" || key == "" {
		return "", nil
	}
	stamp, err := preconditionStamp(rawUpdated)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(Precondition{
		Deployment: info.Kind,
		Key:        key,
		Updated:    stamp,
	})
	if err != nil {
		return "", errs.Runtime("ENCODE_FAILED",
			"cannot encode the precondition for %s", key).Wrap(err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// preconditionStamp puts a timestamp Jira served into the one spelling this
// token compares.
//
// Both sides of the comparison go through here, so the check is on an instant
// and not on a string the server happened to format one way today: a proxy that
// normalized `+0000` to `Z` between two reads would otherwise report an
// untouched issue as changed, and a false stale write is as wrong as a missed
// one.
func preconditionStamp(rawUpdated string) (string, error) {
	for _, layout := range jiraTimeLayouts {
		if t, err := time.Parse(layout, rawUpdated); err == nil {
			return t.UTC().Format(preconditionLayout), nil
		}
	}
	return "", errs.Remote("MALFORMED_TIMESTAMP",
		"Jira returned an updated timestamp this tool cannot parse").
		WithDetail("%q", rawUpdated).
		WithRemedy("report this: the timestamp format changed")
}
