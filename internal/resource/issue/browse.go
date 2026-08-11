package issue

import (
	"context"
	"net/url"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// browsePath is the segment Jira serves an issue to a human at, on both
// deployments.
const browsePath = "/browse/"

// BrowseURL is where a person opens an issue.
//
// It is built from the deployment's own baseUrl rather than from the site the
// caller configured. The two are usually the same string and are allowed to
// differ: an instance reached through an internal hostname, a reverse proxy, or
// a context path answers on one URL and tells the world to use another.
// baseUrl is the one Jira puts in its own notification emails, so it is the one
// a link is expected to match.
//
// Jira's own `self` on an issue is not this. It is the REST endpoint, which
// returns JSON — a link that opens a document rather than an issue.
func BrowseURL(info site.Info, key string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(info.BaseURL), "/")
	if base == "" {
		return "", errs.Runtime("NO_BASE_URL",
			"this site did not report a base URL, so an issue link cannot be built").
			WithDetail("the base URL comes from %s, which every Jira serves; "+
				"an empty one usually means something between here and Jira "+
				"rewrote the response", site.ProbePath).
			WithRemedy("drop --url, or fix what is mangling serverInfo")
	}

	// Parsed, then escaped. ParseKey is what guarantees a key cannot carry a
	// path segment of its own — it is the reason "../../admin-1" is not a
	// project — and PathEscape is the second layer, because a key here comes
	// from a server response rather than from something this tool parsed on the
	// way in. Escaping alone would leave `..` intact and a link that walks up
	// out of /browse/.
	parsed, ok := ParseKey(key)
	if !ok {
		return "", errs.Runtime("INVALID_KEY",
			"%q is not an issue key, so no link can be built for it", key).
			WithDetail("this key came from Jira, not from the command line").
			WithRemedy("report it: " + buildinfo.App + " returned a row it cannot address")
	}
	return base + browsePath + url.PathEscape(parsed.String()), nil
}

// urlFlagName adds the browse link to the output.
//
// It is off by default and always will be. Every row would otherwise carry
// forty-odd bytes of a string most callers throw away, and §2.4 says no field
// appears unless it was requested or is in the documented default set.
const urlFlagName = "url"

// urlFlag is declared once because `issue list` and `issue get` need the same
// question answered the same way.
func urlFlag() registry.Flag {
	return registry.Flag{
		Name: urlFlagName, Type: registry.TypeBool,
		Usage: "include the browse URL, built from the site's own base URL; " +
			"a bare URL, which most terminals make clickable",
	}
}

// urlColumn is the TSV column --url appends.
//
// Appended rather than inserted, so adding it cannot shift the position of a
// column somebody already parses. A bare URL is what goes in it: most terminals
// linkify one, so it is clickable without this tool writing an escape sequence
// into a data column — which is the one thing a column may never contain.
func urlColumn() render.Column {
	return render.Column{Header: "url", Path: "url"}
}

// stampURLs fills in the browse link on each issue of a page.
//
// It runs in the command rather than in the client because a URL is a
// presentation of an issue and not part of one: nothing this tool sends or
// decodes depends on it, and a library caller reading issues through Client
// should not pay for a string they did not ask for.
//
// The only error it can still raise here is a key this tool cannot parse, which
// is bad data from the server rather than a bad flag, and is not knowable until
// the row arrives. Everything that *is* knowable up front is validateBrowseURL's
// job, below.
func stampURLs(issues []Issue, info site.Info) error {
	for i := range issues {
		link, err := BrowseURL(info, issues[i].Key)
		if err != nil {
			return err
		}
		issues[i].URL = link
	}
	return nil
}

// validateBrowseURL refuses --url against a site that cannot supply a base.
//
// In Validate, not in the body, and for the reason every other check here is:
// `issue list` streams, so its header is on stdout before the first page
// arrives. A refusal raised while stamping row one would arrive after the
// column names — including a `url` column that is never going to be filled.
//
// It costs nothing extra. Connect is what the body is about to call anyway, and
// the deployment probe behind it is cached.
func validateBrowseURL(ctx context.Context, inv *registry.Invocation) error {
	if !inv.Flags.Bool(urlFlagName) {
		return nil
	}
	if inv.Jira == nil {
		return errs.Runtime("NO_SESSION",
			"--url cannot be built without a connection to Jira")
	}
	_, info, err := inv.Jira.Connect(ctx)
	if err != nil {
		return err
	}
	// The key is this tool's own and is known to parse; what is being checked
	// is the base.
	_, err = BrowseURL(info, "AAA-1")
	return err
}
