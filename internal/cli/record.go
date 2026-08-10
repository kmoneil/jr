package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

// Environment variables that drive recording. There are no flags for this: a
// flag would join the command surface, appear in `jr schema`, and need
// declaring on sixty commands, for something no caller of this tool should
// reach for.
const (
	// EnvRecord names a file to write this invocation's HTTP conversation to.
	//
	// It exists because every fixture in this repository was written by hand,
	// and three turned out to encode an assumption rather than the API — a
	// removed endpoint, a parameter of the wrong type, an expand nobody
	// documents as necessary. Each passed its tests happily. A cassette proves
	// a request is unchanged; only a recorded one proves it was ever right.
	EnvRecord = "JIRA_RECORD"

	// EnvRecordScrub renames identifiers on the way out, as comma-separated
	// from=to pairs — a display name, a project key, anything this cannot know
	// to look for.
	EnvRecordScrub = "JIRA_RECORD_SCRUB"
)

// recordedHost is what every recorded site becomes. It is the host the existing
// fixtures already use, and a reserved TLD, so a test that somehow dialled it
// could not reach anything.
const recordedHost = "recorded.invalid"

// recorder returns the round tripper this invocation should use, and the
// function that writes what it captured.
//
// Recording is off unless EnvRecord names a path, so the ordinary path pays
// nothing and cannot accidentally write one.
func (a *app) recorder(siteURL string) (*transport.Recorder, func()) {
	path := strings.TrimSpace(a.getenv(EnvRecord))
	if path == "" {
		return nil, func() {}
	}

	rec := transport.NewRecorder(nil, transport.DataCenter)
	return rec, func() {
		cassette := rec.Cassette()

		// Scrubbed as it is written, not as a later step somebody has to
		// remember. The same reasoning as credential redaction: a file that
		// only becomes safe if a second command is run is a file that will be
		// committed before it is.
		a.scrubber(siteURL).Scrub(cassette)

		if err := cassette.Save(path); err != nil {
			// A recording that cannot be written is not worth failing the
			// command for: the work happened, and the cost is doing it again.
			warnf(a, "could not write the recording: %s", err)
			return
		}

		// Everything the caller asked to rename is, by their own account, an
		// identifier — so it is also exactly what to hunt for inside an encoded
		// value, where neither the replacement nor the text scan can reach. A
		// Cloud page token is a base64 protobuf holding the JQL that produced
		// it, and a project key rode out inside one before this existed.
		targets := a.scrubTargets()
		residue := cassette.Residue()
		residue = append(residue, cassette.EncodedResidue(targets)...)
		// The forms of a declared identifier the caller did not declare. A
		// short project key cannot be listed bare without rewriting GET and
		// Set-Cookie, so the caller lists `ET-` and `"ET"` and whichever one
		// they miss is invisible to both of the checks above.
		residue = append(residue, cassette.StemResidue(targets)...)

		// Reported rather than refused. These are shapes that *carry* identity,
		// not proof of a leak — a summary can legitimately contain an address —
		// and a recording nobody looks at is the failure this guards against.
		if len(residue) > 0 {
			warnf(a, "%s still names %d identifier(s); read it before committing:",
				path, len(residue))
			for _, r := range residue {
				warnf(a, "  %s", r)
			}
		}
	}
}

// scrubber builds the rules for one recording: the site it was made against,
// whatever the caller named, and the shapes that carry identity everywhere.
func (a *app) scrubber(siteURL string) transport.Scrubber {
	replace := map[string]string{}

	// The host reaches every self link in every response.
	if host := hostOnly(siteURL); host != "" {
		replace[host] = recordedHost
	}
	for _, pair := range strings.Split(a.getenv(EnvRecordScrub), ",") {
		from, to, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(from) != "" {
			replace[strings.TrimSpace(from)] = strings.TrimSpace(to)
		}
	}

	return transport.Scrubber{
		Replace: replace,
		Patterns: []transport.PatternRule{
			// An avatar URL carries an account id and a hash, and nothing is
			// ever asserted about one.
			{Match: transport.AvatarURL, With: "https://" + recordedHost + "/avatar"},
			// Before the unencoded rule, and with an encoded replacement: a
			// query carries the separator as %3A, and rewriting it to a literal
			// colon yields a recording that no request this tool builds can
			// match.
			{
				Match: transport.CloudAccountIDEncoded,
				With:  "000000%3A00000000-0000-0000-0000-000000000000",
			},
			{Match: transport.CloudAccountID, With: "000000:00000000-0000-0000-0000-000000000000"},
			// After the account ids, so a prefixed one is not reduced to a bare
			// UUID first and then left with its prefix intact.
			{Match: transport.BareUUID, With: "00000000-0000-0000-0000-000000000000"},
			{Match: transport.EmailAddress, With: "ada@example.invalid"},
		},
	}
}

// scrubTargets lists the literals the caller named, which are the values worth
// looking for inside an encoded field. The host is deliberately not among them:
// it appears in a self link in every response and would report on every
// recording, and a report that always fires is one nobody reads.
func (a *app) scrubTargets() []string {
	var out []string
	for _, pair := range strings.Split(a.getenv(EnvRecordScrub), ",") {
		from, _, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if from = strings.TrimSpace(from); ok && from != "" {
			out = append(out, from)
		}
	}
	return out
}

// hostOnly reduces a site URL to its host, which is the form that appears
// inside a response body.
func hostOnly(siteURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(siteURL, "https://"), "http://")
	host, _, _ := strings.Cut(s, "/")
	return host
}

// warnf writes a line to stderr. Recording is a development affordance and its
// diagnostics are plain text: they are not part of any contract, and dressing
// them as one would imply they were.
func warnf(a *app, format string, args ...any) {
	w := a.stderr
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

// recordedDeployment maps what the probe found onto what a cassette records.
func recordedDeployment(info site.Info) transport.Deployment {
	if info.Kind == site.Cloud {
		return transport.Cloud
	}
	return transport.DataCenter
}
