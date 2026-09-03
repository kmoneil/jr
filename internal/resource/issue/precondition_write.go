//go:build write

package issue

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// ifUnchangedFlag carries the token `issue get` minted.
const ifUnchangedFlag = "if-unchanged"

// preconditionMethod names how the check was performed.
//
// It is published rather than assumed because the two mechanisms are not the
// same promise. A conditional request the server evaluates is atomic; a read
// followed by a comparison followed by a write has a window one round trip
// wide, and a caller entitled to know they did not get the strong one should
// not have to infer it from the absence of a claim. Jira offers no validator on
// an issue today, so this is the only value; the attribute exists so that the
// day one appears, the difference is expressible rather than silent.
const preconditionMethod = "read-compare"

// ifUnchangedFlagDecl is declared once because edit, move, and assign all ask
// the same question and must answer it identically.
func ifUnchangedFlagDecl() registry.Flag {
	return registry.Flag{
		Name: ifUnchangedFlag, Type: registry.TypeString,
		// No backquotes. cobra reads the first backquoted run in a flag's usage
		// as the name of its argument, so "which `jr issue get` reports"
		// rendered the flag as `--if-unchanged jr issue get`. Args are not
		// affected, which is why every other backquoted usage in the tree is
		// fine and this one was not.
		Usage: "refuse the write if the issue changed since this precondition, " +
			"which " + buildinfo.App + " issue get reports",
	}
}

// preconditionChild is the record of the check, on every verb that can run one.
func preconditionChild() render.Child {
	return render.Child{Schema: &render.Schema{
		Element: "precondition",
		Attrs: []render.Field{{
			Name: "method", Type: render.TypeString,
			Enum: []string{preconditionMethod},
		}},
	}, Optional: true}
}

// ParsePrecondition reads a --if-unchanged value's own shape, and says nothing
// about which site or which issue it belongs to.
//
// Split from the site comparison for the reason ParsePageToken is: everything
// here is arithmetic on a string the caller typed, so a garbled token can be
// refused before a session exists. The deployment probe behind Connect is a
// request, and a typo answered with NETWORK at exit 9 tells a caller their
// mistake is worth retrying.
func ParsePrecondition(encoded string) (Precondition, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Precondition{}, invalidPrecondition().Wrap(err)
	}

	var p Precondition
	if err := json.Unmarshal(data, &p); err != nil {
		return Precondition{}, invalidPrecondition().Wrap(err)
	}

	// Every token this tool mints stamps all three, so one missing any of them
	// is not ours whatever site is on the other end — `{"not":"valid"}` is
	// legal base64 and legal JSON and decodes to a precondition about nothing.
	// Saying it was never issued here beats reporting it as a mismatch against
	// values it does not have.
	if p.Deployment == "" || p.Key == "" || p.Updated == "" {
		return Precondition{}, invalidPrecondition().
			WithDetail("it names no issue, deployment, or version, " +
				"and every precondition this tool mints names all three")
	}
	if _, ok := ParseKey(p.Key); !ok {
		return Precondition{}, invalidPrecondition().
			WithDetail("it names %q, which is not an issue key", p.Key)
	}
	// The version has to be in the one spelling this tool mints, and refusing
	// anything else here is what keeps a bad token from being reported as a
	// stale write. checkUnchanged compares against a canonical stamp, so a
	// token carrying `2026-08-04T11:32:07+0100` — or any other legal spelling
	// of an instant — would never match and every write would come back
	// STALE_WRITE at exit 7, telling the caller the issue changed when what
	// actually happened is that they passed something this tool did not issue.
	// Two causes with different remedies are two errors.
	if !isCanonicalVersion(p.Updated) {
		return Precondition{}, invalidPrecondition().
			WithDetail("it carries %q, which is not a version this tool records",
				p.Updated)
	}
	return p, nil
}

func invalidPrecondition() *errs.Error {
	return errs.Usage("INVALID_PRECONDITION",
		"--"+ifUnchangedFlag+" is not a precondition this tool issued").
		WithRemedy("pass the precondition attribute from `" + buildinfo.App +
			" issue get`, or omit the flag to write unconditionally")
}

// validatePrecondition is the half of the check that needs no server.
//
// In Command.Validate, with the rest of the local refusals, so a bad token
// costs no round trip and cannot arrive after a write has gone out.
func validatePrecondition(inv *registry.Invocation) error {
	encoded := inv.Flags.String(ifUnchangedFlag)
	if encoded == "" {
		// An empty value and an absent flag are the same string at this layer
		// and are opposite requests: one asked for a guarantee and has nothing
		// to check it against, the other asked for no guarantee at all. Writing
		// unconditionally for both is the worse half of that in every case,
		// because the caller who typed the flag believes a check ran.
		//
		// It became reachable without typing it when `issue list
		// --precondition` shipped. A row for an issue Jira reports no `updated`
		// for carries no token, so the cell is empty, and a loop reading that
		// column hands the empty cell straight to this flag. That loop is in
		// docs/recipes.md; the refusal is what makes the example honest.
		if inv.Flags.WasSet(ifUnchangedFlag) {
			return errs.Usage("INVALID_PRECONDITION",
				"--"+ifUnchangedFlag+" was given an empty value").
				WithDetail("an empty precondition is not a baseline, and this " +
					"write would otherwise be sent with no check at all").
				WithRemedy("a row with no `precondition` had no version to " +
					"mint one from: read it with `" + buildinfo.App +
					" issue get`, or omit the flag to write unconditionally")
		}
		return nil
	}
	p, err := ParsePrecondition(encoded)
	if err != nil {
		return err
	}

	// Both through ParseKey rather than compared as text, so ENG-1 and eng-1
	// are the one issue they are.
	want, ok := ParseKey(inv.Args[0])
	if !ok {
		// The verb's own key check reports this better than a precondition
		// error would; leave it to say so.
		return nil
	}
	got, _ := ParseKey(p.Key)
	if got.String() != want.String() {
		return invalidPrecondition().
			WithDetail("it describes %s, and this command is writing to %s",
				got, want).
			WithRemedy("read the issue you are about to write: `%s issue get %s`",
				buildinfo.App, want)
	}
	return nil
}

// checkUnchanged reads the issue and refuses the write if it has moved.
//
// Returns whether a check ran, so the acknowledgement can record it rather than
// leaving a caller to infer from a bare success that their flag did anything.
//
// The read is one request and only when the flag is passed. It asks for the one
// field it compares, so it is the cheapest response Jira will serve for an
// issue, and it goes through Client.Get so that a key Jira does not have is the
// same NOT_FOUND here as it is everywhere else.
func checkUnchanged(
	ctx context.Context, inv *registry.Invocation, c *Client, key string,
) (bool, error) {
	encoded := inv.Flags.String(ifUnchangedFlag)
	if encoded == "" {
		return false, nil
	}
	p, err := ParsePrecondition(encoded)
	if err != nil {
		return false, err
	}
	if p.Deployment != c.Site.Kind {
		// Two sites' timestamps have nothing to say to each other, and
		// comparing them would refuse the write with "the issue changed",
		// which is a statement about this issue that nobody checked.
		return false, invalidPrecondition().
			WithDetail("it was issued for a %s site and this is %s",
				deploymentName(p.Deployment), deploymentName(c.Site.Kind))
	}

	current, err := c.Get(ctx, key, []string{"updated"})
	if err != nil {
		return false, err
	}
	stamp, err := preconditionStamp(current.updatedRaw)
	if err != nil {
		return false, err
	}
	if stamp != p.Updated {
		return false, errs.Conflict("STALE_WRITE",
			"%s changed after the precondition was taken, so this write was not sent", key).
			WithDetail("the precondition describes %s and the issue now reads %s",
				p.Updated, stamp).
			WithRemedy("re-read it with `%s issue get %s`, decide again against "+
				"what it says now, and retry with the precondition from that read",
				buildinfo.App, key)
	}
	return true, nil
}

// stampPrecondition records that a check ran, on the document a verb returns.
//
// Nothing is added when no check ran: an element saying a precondition was not
// checked would appear on every write in the tree to describe the absence of a
// flag.
func stampPrecondition(doc *render.Doc, checked bool) *render.Doc {
	if checked && doc != nil && doc.Record != nil {
		doc.Record.Child(render.El("precondition").
			Attr("method", preconditionMethod))
	}
	return doc
}
