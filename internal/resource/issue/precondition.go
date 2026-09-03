package issue

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
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
	// Updated is the issue's last-modified time, canonical to the precision
	// below.
	Updated string `json:"u"`
	// Precision is how sharp this baseline is, and it is here because the two
	// endpoints a baseline can come from do not agree.
	//
	// Measured on Jira Data Center 10.4.0, for one issue at one moment:
	//
	//	GET /rest/api/2/search?jql=key=ENG-7&fields=updated
	//	  2026-09-03T21:57:13.000+0000
	//	GET /rest/api/2/issue/ENG-7?fields=updated
	//	  2026-09-03T21:57:13.679+0000
	//
	// The search answers from the index and the issue from the store, and on
	// Data Center the index keeps the second. Cloud returns the millisecond
	// from both. Without this field every baseline a listing minted was
	// compared at a precision it never had, so it read `.000` against `.679`
	// and refused every write on that deployment: `issue list --precondition`
	// handed out tokens nothing accepted, and every row of every plan failed
	// STALE_WRITE.
	//
	// So the token carries the precision it was minted at and the comparison
	// uses it. A baseline from a listing is second-precision, on both
	// deployments, because that is what the weaker of its two sources supports
	// and one rule is easier to reason about than a table of them. A baseline
	// from `issue get` keeps the millisecond.
	//
	// Empty means millisecond, which is what every token minted before this
	// field existed was formatted as.
	Precision string `json:"p,omitempty"`
}

// The precisions a baseline can be minted at.
const (
	// PrecisionMillisecond is what the issue endpoint serves, on both
	// deployments.
	PrecisionMillisecond = "ms"
	// PrecisionSecond is what a search-sourced baseline is held to. It is a
	// weaker guarantee and the token says so rather than implying the stronger
	// one: two edits inside one second are indistinguishable to it, and that
	// window is the server's, not this tool's.
	PrecisionSecond = "s"
)

// preconditionSecondLayout is preconditionLayout without the milliseconds.
const preconditionSecondLayout = "2006-01-02T15:04:05Z07:00"

// layoutFor returns the spelling a precision is written in.
func layoutFor(precision string) string {
	if precision == PrecisionSecond {
		return preconditionSecondLayout
	}
	return preconditionLayout
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
func EncodePrecondition(
	info site.Info, key, rawUpdated, precision string,
) (string, error) {
	if rawUpdated == "" || key == "" {
		return "", nil
	}
	stamp, err := preconditionStamp(rawUpdated, precision)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(Precondition{
		Deployment: info.Kind,
		Key:        key,
		Updated:    stamp,
		Precision:  precision,
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
func preconditionStamp(rawUpdated, precision string) (string, error) {
	t, ok := site.ParseTime(rawUpdated)
	if !ok {
		return "", errs.Remote("MALFORMED_TIMESTAMP",
			"Jira returned an updated timestamp this tool cannot parse").
			WithDetail("%q", rawUpdated).
			WithRemedy("report this: the timestamp format changed")
	}

	stamp := t.Format(layoutFor(precision))
	// Parseable is not the same as representable, and the minter has to hold
	// itself to the spelling the parser accepts or it can hand out a token its
	// own write verbs refuse.
	//
	// Found by fuzzing the round trip, not by anything a server did: an instant
	// before year 1 UTC formats a negative year, `preconditionLayout` cannot
	// read one back, and ParsePrecondition then reports INVALID_PRECONDITION,
	// which tells the caller they typed something wrong about a token this tool
	// minted for them. No Jira serves such a date. Refusing here costs nothing
	// and keeps "minted implies accepted" true rather than true-in-practice.
	if !isCanonicalVersion(stamp, precision) {
		return "", errs.Remote("MALFORMED_TIMESTAMP",
			"Jira returned an updated timestamp this tool cannot use as a version").
			WithDetail("%q canonicalizes to %q, which is not a version this "+
				"tool can record", rawUpdated, stamp).
			WithRemedy("report this: an issue carries a date outside the range " +
				"this tool can compare")
	}
	return stamp, nil
}

// isCanonicalVersion reports whether a version is spelled the way
// EncodePrecondition spells one: UTC, to the millisecond, and nothing else.
//
// Both the minter and the parser ask this, which is deliberate: they have to
// agree, and the way to make two things agree is to give them one definition.
// The fuzz targets compare against a definition of their own for the case where
// this one is itself wrong.
func isCanonicalVersion(version, precision string) bool {
	layout := layoutFor(precision)
	t, err := time.Parse(layout, version)
	if err != nil {
		return false
	}
	return t.UTC().Format(layout) == version
}

// preconditionFlagName asks a listing to mint a baseline for every row.
//
// Off by default, for the reason --url and --age are: §2.4 says no field
// appears unless it was requested or is in the documented default set, and a
// token is sixty-odd bytes on every row of every listing to serve the caller
// who is about to write. Most callers are not.
//
// It exists because the alternative is worse arithmetic, not because the
// default was wrong. "List the blocked issues, edit the three that matter"
// otherwise costs one request for the listing and one `issue get` per row to
// obtain a baseline the listing already held: the row carries Jira's own
// `updated`, DefaultFields has asked for it since the first version of this
// command, and EncodePrecondition needs nothing else. Paying a request per row
// for a value already in hand is the kind of cost a caller cannot see and
// cannot avoid.
//
// A row whose issue has no `updated` gets no token, exactly as on `issue get`:
// absence says "no baseline available", which is a fact a caller can act on,
// where a token matching anything is not.
const preconditionFlagName = "precondition"

// preconditionFlag is declared once because `issue list` is not the only
// command that could want it. `issue get` mints unconditionally and takes no
// flag: a record is one issue, the token is one attribute, and the byte
// argument that justifies the flag on a listing does not survive being divided
// by one.
func preconditionFlag() registry.Flag {
	return registry.Flag{
		Name: preconditionFlagName, Type: registry.TypeBool,
		Usage: "include a precondition token per row, to pass to " +
			"--if-unchanged on a later write; the same value " +
			buildinfo.App + " issue get always reports",
	}
}

// preconditionColumn is the TSV column --precondition appends.
//
// Appended rather than inserted, on the same terms as --age: turning a flag on
// must not move a column somebody already parses. The path is a bare attribute
// reference because the token is an attribute of the row itself.
func preconditionColumn() render.Column {
	return render.Column{Header: preconditionFlagName, Path: "@" + preconditionFlagName}
}

// stampPreconditions mints a baseline for each issue on a page.
//
// It fails the page rather than the row when a timestamp will not parse. A
// listing that silently dropped the token for one row would hand the caller a
// set where "no baseline" and "baseline you did not get" are spelled the same,
// and the whole point of the token is to tell a caller which issue they are
// holding.
func stampPreconditions(issues []Issue, info site.Info) error {
	for i := range issues {
		token, err := EncodePrecondition(
			info, issues[i].Key, issues[i].updatedRaw, PrecisionSecond)
		if err != nil {
			return err
		}
		issues[i].Precondition = token
	}
	return nil
}
